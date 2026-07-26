package quark

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"
)

// Browsers commonly open overlapping ranges that run to EOF, read only a
// small prefix, then cancel them. Retain completed ciphertext parts in a
// bounded LRU so those overlapping readers do not download the same Quark
// bytes again.
const quarkCryptCacheBytes = 96 * 1024 * 1024

type quarkCryptCacheEntry struct {
	fileID string
	start  int64
	data   []byte
	usedAt time.Time
}

type quarkCryptRangeCache struct {
	mu          sync.Mutex
	entries     []quarkCryptCacheEntry
	liveEntries []quarkCryptLiveCacheEntry
	bytes       int
}

type quarkCryptLiveCacheEntry struct {
	fileID string
	start  int64
	length int64
	part   *streamingEncryptedPart
}

func newQuarkCryptRangeCache() *quarkCryptRangeCache {
	return &quarkCryptRangeCache{}
}

func (c *quarkCryptRangeCache) AddLive(fileID string, start, length int64, part *streamingEncryptedPart) {
	if c == nil || fileID == "" || length <= 0 || part == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := len(c.liveEntries) - 1; index >= 0; index-- {
		entry := c.liveEntries[index]
		if entry.fileID == fileID && entry.start == start && entry.length == length {
			c.liveEntries[index] = quarkCryptLiveCacheEntry{fileID: fileID, start: start, length: length, part: part}
			return
		}
	}
	c.liveEntries = append(c.liveEntries, quarkCryptLiveCacheEntry{fileID: fileID, start: start, length: length, part: part})
}

func (c *quarkCryptRangeCache) RemoveLive(fileID string, start, length int64, part *streamingEncryptedPart) {
	if c == nil || part == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := len(c.liveEntries) - 1; index >= 0; index-- {
		entry := c.liveEntries[index]
		if entry.fileID == fileID && entry.start == start && entry.length == length && entry.part == part {
			c.liveEntries = append(c.liveEntries[:index], c.liveEntries[index+1:]...)
		}
	}
}

func (c *quarkCryptRangeCache) Get(fileID string, start, length int64) []byte {
	if c == nil || length <= 0 {
		return nil
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	end := start + length
	for index := len(c.entries) - 1; index >= 0; index-- {
		entry := &c.entries[index]
		entryEnd := entry.start + int64(len(entry.data))
		if entry.fileID == fileID && start >= entry.start && end <= entryEnd {
			entry.usedAt = now
			return entry.data[start-entry.start : end-entry.start]
		}
	}
	return nil
}

// Open returns a reader assembled from cached bytes and the smallest missing
// upstream ranges. Browser media stacks frequently advance a Range start by a
// few KiB while keeping most of the preceding request, so requiring a cache
// entry to contain the full request would still redownload almost everything.
// The bool reports whether at least one cached span was used.
func (c *quarkCryptRangeCache) Open(
	ctx context.Context,
	fileID string,
	start, length int64,
	open encryptedRangeOpener,
) (io.ReadCloser, bool, error) {
	if c == nil || length <= 0 || open == nil {
		return nil, false, nil
	}

	end := start + length
	if end <= start {
		return nil, false, nil
	}
	spans := c.spans(fileID, start, end)
	if len(spans) == 0 {
		return nil, false, nil
	}

	parts := make([]cachedRangeReaderPart, 0, len(spans)*2+1)
	position := start
	for _, span := range spans {
		if position < span.start {
			parts = append(parts, cachedRangeReaderPart{start: position, length: span.start - position, open: open})
		}
		if span.live != nil {
			parts = append(parts, cachedRangeReaderPart{live: span.live, liveOffset: span.liveOffset, length: span.end - span.start})
		} else {
			parts = append(parts, cachedRangeReaderPart{data: span.data, length: int64(len(span.data))})
		}
		position = span.end
	}
	if position < end {
		parts = append(parts, cachedRangeReaderPart{start: position, length: end - position, open: open})
	}
	return &cachedRangeReader{ctx: ctx, parts: parts}, true, nil
}

// ContiguousEnd returns the first byte after the cached prefix beginning at
// start. It is used to put live Quark downloads immediately after that prefix.
func (c *quarkCryptRangeCache) ContiguousEnd(fileID string, start, end int64) int64 {
	if c == nil || start >= end {
		return start
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	position := start
	for position < end {
		bestIndex := -1
		bestEnd := position
		for index := range c.entries {
			entry := &c.entries[index]
			entryEnd := entry.start + int64(len(entry.data))
			if entry.fileID == fileID && entry.start <= position && entryEnd > bestEnd {
				bestIndex = index
				bestEnd = entryEnd
			}
		}
		if bestIndex < 0 {
			break
		}
		c.entries[bestIndex].usedAt = now
		position = bestEnd
	}
	if position > end {
		return end
	}
	return position
}

type cachedRangeSpan struct {
	start      int64
	end        int64
	data       []byte
	live       *streamingEncryptedPart
	liveOffset int64
}

// spans returns non-overlapping cached spans that cover as much of the range
// as possible. Cache entry data is immutable after Put, so returned slices
// stay valid even if a later LRU eviction removes their entry.
func (c *quarkCryptRangeCache) spans(fileID string, start, end int64) []cachedRangeSpan {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	position := start
	spans := make([]cachedRangeSpan, 0)
	for position < end {
		bestIndex := -1
		bestLiveIndex := -1
		bestEnd := position
		nextStart := end
		for index := range c.entries {
			entry := &c.entries[index]
			if entry.fileID != fileID {
				continue
			}
			entryEnd := entry.start + int64(len(entry.data))
			if entry.start <= position && entryEnd > bestEnd {
				bestIndex = index
				bestEnd = entryEnd
			}
			if entry.start > position && entry.start < nextStart {
				nextStart = entry.start
			}
		}
		for index := range c.liveEntries {
			entry := &c.liveEntries[index]
			if entry.fileID != fileID || entry.part == nil {
				continue
			}
			entryEnd := entry.start + entry.length
			if entry.start <= position && entryEnd > bestEnd {
				bestIndex = -1
				bestLiveIndex = index
				bestEnd = entryEnd
			}
			if entry.start > position && entry.start < nextStart {
				nextStart = entry.start
			}
		}
		if bestIndex < 0 && bestLiveIndex < 0 {
			position = nextStart
			continue
		}

		spanEnd := bestEnd
		if spanEnd > end {
			spanEnd = end
		}
		if bestIndex >= 0 {
			entry := &c.entries[bestIndex]
			entry.usedAt = now
			spans = append(spans, cachedRangeSpan{
				start: position,
				end:   spanEnd,
				data:  entry.data[position-entry.start : spanEnd-entry.start],
			})
		} else {
			entry := &c.liveEntries[bestLiveIndex]
			spans = append(spans, cachedRangeSpan{
				start:      position,
				end:        spanEnd,
				live:       entry.part,
				liveOffset: position - entry.start,
			})
		}
		position = spanEnd
	}
	return spans
}

type cachedRangeReaderPart struct {
	start      int64
	length     int64
	data       []byte
	live       *streamingEncryptedPart
	liveOffset int64
	open       encryptedRangeOpener
}

type cachedRangeReader struct {
	ctx     context.Context
	parts   []cachedRangeReaderPart
	index   int
	current io.ReadCloser
	read    int64
	closed  bool
}

func (r *cachedRangeReader) Read(dst []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	for r.index < len(r.parts) {
		if r.current == nil {
			part := r.parts[r.index]
			if part.live != nil {
				liveReader := part.live.NewReader(part.liveOffset)
				r.current = &limitedReadCloser{Reader: io.LimitReader(liveReader, part.length), Closer: liveReader}
			} else if part.data != nil {
				r.current = io.NopCloser(bytes.NewReader(part.data))
			} else {
				reader, err := part.open(r.ctx, part.start, part.length)
				if err != nil {
					return 0, err
				}
				r.current = reader
			}
			r.read = 0
		}

		n, err := r.current.Read(dst)
		r.read += int64(n)
		if n > 0 {
			// Keep the sequence alive when an upstream reader returns its last
			// bytes together with EOF. The next Read validates the part length
			// and advances to the following cached or upstream span.
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
		if r.read != r.parts[r.index].length {
			return 0, io.ErrUnexpectedEOF
		}
		if err := r.current.Close(); err != nil {
			return 0, err
		}
		r.current = nil
		r.index++
	}
	return 0, io.EOF
}

func (r *cachedRangeReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}

func (c *quarkCryptRangeCache) Put(fileID string, start int64, data []byte) {
	if c == nil || fileID == "" || len(data) == 0 || len(data) > quarkCryptCacheBytes {
		return
	}
	copyData := append([]byte(nil), data...)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	for index := len(c.entries) - 1; index >= 0; index-- {
		entry := c.entries[index]
		if entry.fileID == fileID && entry.start == start && len(entry.data) == len(copyData) {
			c.entries[index].usedAt = now
			return
		}
	}
	c.entries = append(c.entries, quarkCryptCacheEntry{fileID: fileID, start: start, data: copyData, usedAt: now})
	c.bytes += len(copyData)
	for c.bytes > quarkCryptCacheBytes && len(c.entries) > 0 {
		oldest := 0
		for index := 1; index < len(c.entries); index++ {
			if c.entries[index].usedAt.Before(c.entries[oldest].usedAt) {
				oldest = index
			}
		}
		c.bytes -= len(c.entries[oldest].data)
		c.entries = append(c.entries[:oldest], c.entries[oldest+1:]...)
	}
}
