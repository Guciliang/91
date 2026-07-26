package quark

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Quark's OpenList driver uses three 10 MiB download parts. Keeping the same
// shape avoids a single CDN connection becoming the playback bottleneck while
// bounding each plaintext stream to three buffered ciphertext parts.
const (
	quarkCryptPartSize        int64 = 10 * 1024 * 1024
	quarkCryptPartConcurrency       = 3
)

type encryptedRangeOpener func(context.Context, int64, int64) (io.ReadCloser, error)

type encryptedPartResult struct {
	index int
	part  *streamingEncryptedPart
	err   error
}

// encryptedRangePrefetchReader starts the current encrypted range plus the
// next two ranges. Each part streams to the current reader while retaining its
// completed bytes for a brief cross-request cache. Waiting for a full 10 MiB
// part here causes video playback to underflow when a CDN connection slows
// down near a part boundary.
type encryptedRangePrefetchReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	open   encryptedRangeOpener
	// onPartComplete receives fully downloaded ciphertext before the browser
	// can discard this reader. It feeds the bounded cross-request cache.
	onPartComplete func(int64, []byte)

	partStarts  []int64
	partLengths []int64
	partCount   int
	prefetchMax int

	currentIndex int
	current      io.ReadCloser
	nextIndex    int
	inflight     int
	ready        map[int]encryptedPartResult
	results      chan encryptedPartResult

	partsMu sync.Mutex
	parts   map[*streamingEncryptedPart]struct{}
	closed  bool

	closeOnce sync.Once
}

func newEncryptedRangePrefetchReader(ctx context.Context, start, length int64, open encryptedRangeOpener, onPartComplete func(int64, []byte), prefetchMax int) (io.ReadCloser, error) {
	if length <= quarkCryptPartSize {
		return open(ctx, start, length)
	}
	if open == nil {
		return nil, fmt.Errorf("quark crypt: missing encrypted range opener")
	}
	if prefetchMax < 0 {
		prefetchMax = 0
	}
	if prefetchMax > quarkCryptPartConcurrency-1 {
		prefetchMax = quarkCryptPartConcurrency - 1
	}

	readCtx, cancel := context.WithCancel(ctx)
	partLengths := quarkCryptPartLayout(length)
	partStarts := make([]int64, len(partLengths))
	for index := 1; index < len(partStarts); index++ {
		partStarts[index] = partStarts[index-1] + partLengths[index-1]
	}
	for index := range partStarts {
		partStarts[index] += start
	}
	first, err := open(readCtx, partStarts[0], partLengths[0])
	if err != nil {
		cancel()
		return nil, err
	}

	r := &encryptedRangePrefetchReader{
		ctx:         readCtx,
		cancel:      cancel,
		open:        open,
		partStarts:  partStarts,
		partLengths: partLengths,
		partCount:   len(partLengths),
		prefetchMax: prefetchMax,
		nextIndex:   1,
		ready:       make(map[int]encryptedPartResult),
		results:     make(chan encryptedPartResult, max(1, prefetchMax)),
		parts:       make(map[*streamingEncryptedPart]struct{}),
	}
	// The first range must be buffered too. Leaving it directly connected to
	// the browser lets a client's small TCP receive window throttle all
	// upstream prefetch before the later parts can help playback.
	firstPart := newStreamingEncryptedPart(readCtx, first, partLengths[0], func(data []byte) {
		if onPartComplete != nil {
			onPartComplete(partStarts[0], data)
		}
	})
	if !r.trackPart(firstPart) {
		_ = firstPart.Close()
		cancel()
		return nil, context.Canceled
	}
	r.current = firstPart
	r.onPartComplete = onPartComplete
	r.fillPrefetch()
	return r, nil
}

func (r *encryptedRangePrefetchReader) Read(p []byte) (int, error) {
	for {
		n, err := r.current.Read(p)
		if n > 0 {
			if err == io.EOF {
				return n, nil
			}
			return n, err
		}
		if err == nil {
			continue
		}
		if err != io.EOF {
			return 0, err
		}
		if err := r.advance(); err != nil {
			return 0, err
		}
	}
}

func (r *encryptedRangePrefetchReader) advance() error {
	r.closeCurrent()
	if r.currentIndex+1 >= r.partCount {
		return io.EOF
	}
	r.currentIndex++
	if r.prefetchMax == 0 {
		partIndex := r.currentIndex
		partStart := r.partStarts[partIndex]
		partLength := r.partLengths[partIndex]
		body, err := r.open(r.ctx, partStart, partLength)
		if err != nil {
			return err
		}
		part := newStreamingEncryptedPart(r.ctx, body, partLength, func(data []byte) {
			if r.onPartComplete != nil {
				r.onPartComplete(partStart, data)
			}
		})
		if !r.trackPart(part) {
			_ = part.Close()
			return context.Canceled
		}
		r.current = part
		return nil
	}
	part, err := r.waitForPart(r.currentIndex)
	if err != nil {
		return err
	}
	r.current = part.part
	r.inflight--
	r.fillPrefetch()
	return nil
}

func (r *encryptedRangePrefetchReader) waitForPart(index int) (encryptedPartResult, error) {
	if part, ok := r.ready[index]; ok {
		delete(r.ready, index)
		if part.err != nil {
			return encryptedPartResult{}, part.err
		}
		return part, nil
	}
	for {
		select {
		case <-r.ctx.Done():
			return encryptedPartResult{}, r.ctx.Err()
		case part := <-r.results:
			if part.index == index {
				if part.err != nil {
					return encryptedPartResult{}, part.err
				}
				return part, nil
			}
			r.ready[part.index] = part
		}
	}
}

func (r *encryptedRangePrefetchReader) fillPrefetch() {
	for r.ctx.Err() == nil && r.inflight < r.prefetchMax && r.nextIndex < r.partCount {
		index := r.nextIndex
		r.nextIndex++
		r.inflight++
		go r.fetchPart(index)
	}
}

func (r *encryptedRangePrefetchReader) fetchPart(index int) {
	length := r.partLengths[index]
	offset := r.partStarts[index]
	body, err := r.open(r.ctx, offset, length)
	if err != nil {
		r.sendPart(encryptedPartResult{index: index, err: err})
		return
	}

	part := newStreamingEncryptedPart(r.ctx, body, length, func(data []byte) {
		if r.onPartComplete != nil {
			r.onPartComplete(offset, data)
		}
	})
	if !r.trackPart(part) {
		_ = part.Close()
		return
	}
	r.sendPart(encryptedPartResult{index: index, part: part})
}

func (r *encryptedRangePrefetchReader) sendPart(part encryptedPartResult) {
	select {
	case r.results <- part:
	case <-r.ctx.Done():
		if part.part != nil {
			_ = part.part.Close()
			r.untrackPart(part.part)
		}
	}
}

func (r *encryptedRangePrefetchReader) trackPart(part *streamingEncryptedPart) bool {
	r.partsMu.Lock()
	defer r.partsMu.Unlock()
	if r.closed {
		return false
	}
	r.parts[part] = struct{}{}
	return true
}

func (r *encryptedRangePrefetchReader) untrackPart(part *streamingEncryptedPart) {
	r.partsMu.Lock()
	delete(r.parts, part)
	r.partsMu.Unlock()
}

func (r *encryptedRangePrefetchReader) closeCurrent() {
	current := r.current
	if current == nil {
		return
	}
	_ = current.Close()
	if part, ok := current.(*streamingEncryptedPart); ok {
		r.untrackPart(part)
	}
	r.current = nil
}

func (r *encryptedRangePrefetchReader) Close() error {
	var closeErr error
	r.closeOnce.Do(func() {
		r.cancel()
		r.closeCurrent()

		r.partsMu.Lock()
		r.closed = true
		parts := make([]*streamingEncryptedPart, 0, len(r.parts))
		for part := range r.parts {
			parts = append(parts, part)
		}
		clear(r.parts)
		r.partsMu.Unlock()

		for _, part := range parts {
			if err := part.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

// streamingEncryptedPart drains one upstream range into a fixed part buffer.
// Readers can consume bytes before the download completes, and a completed
// buffer remains intact long enough to seed the cross-request cache.
type streamingEncryptedPart struct {
	body     io.ReadCloser
	expected int64

	mu       sync.Mutex
	changed  *sync.Cond
	buffer   []byte
	written  int
	read     int
	terminal error
	closed   bool
	onDone   func([]byte)
	done     chan struct{}

	closeOnce sync.Once
	doneOnce  sync.Once
	stop      func() bool
}

func newStreamingEncryptedPart(ctx context.Context, body io.ReadCloser, expected int64, onDone func([]byte)) *streamingEncryptedPart {
	part := &streamingEncryptedPart{
		body:     body,
		expected: expected,
		buffer:   make([]byte, expected),
		onDone:   onDone,
		done:     make(chan struct{}),
	}
	part.changed = sync.NewCond(&part.mu)
	part.stop = context.AfterFunc(ctx, func() { _ = part.Close() })
	go part.pump()
	return part
}

// NewReader creates an independent view into a part that is still being
// filled. It lets later browser ranges reuse a detached lookahead connection
// instead of starting another CDN request for the same bytes.
func (p *streamingEncryptedPart) NewReader(offset int64) io.ReadCloser {
	if offset < 0 {
		offset = 0
	}
	return &streamingEncryptedPartReader{part: p, read: offset}
}

type streamingEncryptedPartReader struct {
	part   *streamingEncryptedPart
	read   int64
	closed bool
}

func (r *streamingEncryptedPartReader) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if r.closed || r.part == nil {
		return 0, io.ErrClosedPipe
	}
	p := r.part
	p.mu.Lock()
	defer p.mu.Unlock()
	for r.read >= int64(p.written) && p.terminal == nil && !p.closed {
		p.changed.Wait()
	}
	if r.read >= int64(p.written) {
		if p.terminal != nil {
			return 0, p.terminal
		}
		return 0, context.Canceled
	}
	available := int64(p.written) - r.read
	toRead := int64(len(dst))
	if toRead > available {
		toRead = available
	}
	n := copy(dst, p.buffer[r.read:r.read+toRead])
	r.read += int64(n)
	return n, nil
}

func (r *streamingEncryptedPartReader) Close() error {
	r.closed = true
	return nil
}

func (p *streamingEncryptedPart) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.read == p.written && p.terminal == nil && !p.closed {
		p.changed.Wait()
	}
	if p.read == p.written {
		if p.terminal != nil {
			return 0, p.terminal
		}
		return 0, context.Canceled
	}

	toRead := len(dst)
	if available := p.written - p.read; toRead > available {
		toRead = available
	}
	n := copy(dst[:toRead], p.buffer[p.read:p.read+toRead])
	p.read += n
	return n, nil
}

func (p *streamingEncryptedPart) Close() error {
	var closeErr error
	p.closeOnce.Do(func() {
		if p.stop != nil {
			p.stop()
		}
		closeErr = p.body.Close()
		p.mu.Lock()
		p.closed = true
		p.buffer = nil
		p.written = 0
		p.read = 0
		if p.terminal == nil {
			p.terminal = context.Canceled
		}
		p.changed.Broadcast()
		p.mu.Unlock()
		p.signalDone()
	})
	return closeErr
}

func (p *streamingEncryptedPart) wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminal == io.EOF && p.written == len(p.buffer) {
		return nil
	}
	if p.terminal != nil {
		return p.terminal
	}
	return context.Canceled
}

func (p *streamingEncryptedPart) completedData() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminal != io.EOF || p.written != len(p.buffer) {
		return nil
	}
	return append([]byte(nil), p.buffer...)
}

func (p *streamingEncryptedPart) signalDone() {
	p.doneOnce.Do(func() { close(p.done) })
}

func (p *streamingEncryptedPart) pump() {
	defer p.body.Close()
	remaining := p.expected
	scratch := make([]byte, 64*1024)
	for remaining > 0 {
		readSize := int64(len(scratch))
		if remaining < readSize {
			readSize = remaining
		}
		n, err := p.body.Read(scratch[:readSize])
		if n > 0 {
			if writeErr := p.enqueue(scratch[:n]); writeErr != nil {
				p.finish(writeErr)
				return
			}
			remaining -= int64(n)
		}
		if err != nil {
			if err == io.EOF && remaining == 0 {
				p.finish(nil)
			} else if err == io.EOF {
				p.finish(fmt.Errorf("quark crypt: encrypted part ended with %d bytes remaining", remaining))
			} else {
				p.finish(err)
			}
			return
		}
	}
	p.finish(nil)
}

func (p *streamingEncryptedPart) enqueue(src []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(src) > 0 {
		if p.closed {
			return context.Canceled
		}
		free := len(p.buffer) - p.written
		if free == 0 {
			return io.ErrShortWrite
		}
		n := len(src)
		if n > free {
			n = free
		}
		copy(p.buffer[p.written:p.written+n], src[:n])
		p.written += n
		src = src[n:]
		p.changed.Broadcast()
	}
	return nil
}

func (p *streamingEncryptedPart) finish(err error) {
	var completed []byte
	p.mu.Lock()
	if p.closed || p.terminal != nil {
		p.mu.Unlock()
		return
	}
	if err == nil {
		p.terminal = io.EOF
		if p.written == len(p.buffer) {
			completed = p.buffer
		}
	} else {
		p.terminal = err
	}
	p.changed.Broadcast()
	p.mu.Unlock()
	p.signalDone()
	if completed != nil && p.onDone != nil {
		p.onDone(completed)
	}
}

// quarkCryptPartLayout front-loads a shorter first segment when the final
// segment would otherwise be small. It lets the first producer finish and
// fill the following two buffers sooner, while keeping the steady-state
// segment size at 10 MiB.
func quarkCryptPartLayout(total int64) []int64 {
	if total <= 0 {
		return nil
	}
	remaining := total
	remainder := total % quarkCryptPartSize
	minimumFirst := quarkCryptPartSize / 2
	lengths := make([]int64, 0, int((total+quarkCryptPartSize-1)/quarkCryptPartSize))
	for index := 0; remaining > 0; index++ {
		length := quarkCryptPartSize
		switch index {
		case 0:
			if remainder > 0 {
				length = remainder
				if length < minimumFirst {
					length = minimumFirst
				}
			}
		case 1:
			if remainder > 0 && remainder < minimumFirst {
				length += remainder - minimumFirst
			}
		}
		if length > remaining {
			length = remaining
		}
		lengths = append(lengths, length)
		remaining -= length
	}
	return lengths
}

func quarkRangeResponseAt(resp *http.Response, offset int64) bool {
	if resp == nil {
		return false
	}
	if resp.StatusCode == http.StatusPartialContent {
		return true
	}
	contentRange := strings.TrimSpace(resp.Header.Get("Content-Range"))
	if !strings.HasPrefix(contentRange, "bytes ") {
		return false
	}
	span := strings.TrimPrefix(contentRange, "bytes ")
	start, _, ok := strings.Cut(span, "-")
	parsed, err := strconv.ParseInt(start, 10, 64)
	return ok && err == nil && parsed == offset
}
