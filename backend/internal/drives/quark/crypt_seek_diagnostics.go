package quark

import (
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	quarkCryptSeekTraceTimeout                = time.Minute
	quarkCryptSeekPlaybackObservationDuration = 15 * time.Second
	quarkCryptSeekPlainRangeSamples           = 8
)

type quarkCryptPlainRangeSample struct {
	offset int64
	limit  int64
}

// quarkCryptSeekTrace captures just the critical path after a browser seek.
// It deliberately emits one summary instead of adding one log line per Range.
type quarkCryptSeekTrace struct {
	fileID  string
	started time.Time

	mu sync.Mutex

	finished         bool
	firstByteLogged  bool
	playbackReported bool

	plainRanges       int
	plainRangeSamples []quarkCryptPlainRangeSample
	sizeProbes        int

	linkRequests int
	linkWait     time.Duration

	upstreamRequests int
	upstreamWait     time.Duration
	upstreamWaitMax  time.Duration

	cacheHits   int
	cacheMisses int
	maxActive   int

	prefetchStarted    int
	prefetchCompleted  int
	prefetchCanceled   int
	prefetchFailed     int
	prefetchCachedByte int64
	prefetchGateChecks int
	prefetchGatePasses int
	prefetchRangeKnown bool
	prefetchRangeOK    bool
	prefetchSizeKnown  bool

	openReady time.Duration
	firstByte time.Duration

	playbackWait       time.Duration
	playbackBuffered   time.Duration
	playbackReadyState int
}

type quarkCryptSeekTraceSnapshot struct {
	fileID string
	reason string

	plainRanges       int
	plainRangeSamples []quarkCryptPlainRangeSample
	sizeProbes        int

	linkRequests int
	linkWait     time.Duration

	upstreamRequests int
	upstreamWait     time.Duration
	upstreamWaitMax  time.Duration

	cacheHits   int
	cacheMisses int
	maxActive   int

	prefetchStarted    int
	prefetchCompleted  int
	prefetchCanceled   int
	prefetchFailed     int
	prefetchCachedByte int64
	prefetchGateChecks int
	prefetchGatePasses int
	prefetchRangeKnown bool
	prefetchRangeOK    bool
	prefetchSizeKnown  bool

	openReady time.Duration
	firstByte time.Duration

	playbackReported   bool
	playbackWait       time.Duration
	playbackBuffered   time.Duration
	playbackReadyState int
}

func newQuarkCryptSeekTrace(fileID string) *quarkCryptSeekTrace {
	return &quarkCryptSeekTrace{fileID: fileID, started: time.Now()}
}

func (d *CryptDriver) beginSeekTrace(fileID string) {
	trace := newQuarkCryptSeekTrace(fileID)
	d.seekMu.Lock()
	previous := d.seekTraces[fileID]
	if d.seekTraces == nil {
		d.seekTraces = make(map[string]*quarkCryptSeekTrace)
	}
	d.seekTraces[fileID] = trace
	d.seekMu.Unlock()

	if previous != nil {
		d.finishSeekTrace(previous, "superseded")
	}
	go func() {
		timer := time.NewTimer(quarkCryptSeekTraceTimeout)
		defer timer.Stop()
		<-timer.C
		d.finishSeekTrace(trace, "timeout")
	}()
}

func (d *CryptDriver) activeSeekTrace(fileID string) *quarkCryptSeekTrace {
	d.seekMu.RLock()
	trace := d.seekTraces[fileID]
	d.seekMu.RUnlock()
	return trace
}

func (d *CryptDriver) finishSeekTrace(trace *quarkCryptSeekTrace, reason string) {
	snapshot, ok := d.completeSeekTrace(trace, reason)
	if !ok {
		return
	}
	logQuarkCryptSeek(snapshot)
}

func (d *CryptDriver) completeSeekTrace(trace *quarkCryptSeekTrace, reason string) (quarkCryptSeekTraceSnapshot, bool) {
	if trace == nil {
		return quarkCryptSeekTraceSnapshot{}, false
	}
	snapshot, ok := trace.finish(reason)
	if !ok {
		return quarkCryptSeekTraceSnapshot{}, false
	}
	d.seekMu.Lock()
	if d.seekTraces[trace.fileID] == trace {
		delete(d.seekTraces, trace.fileID)
	}
	d.seekMu.Unlock()
	return snapshot, true
}

func logQuarkCryptSeek(snapshot quarkCryptSeekTraceSnapshot) {
	log.Printf("[quark crypt seek] file=%s outcome=%s first_byte=%s open_ready=%s plain_ranges=%d plain_samples=%s size_probes=%d link=%s/%d cdn_ttfb=%s/%d max_cdn_ttfb=%s cache=hit:%d miss:%d max_active=%d playback_reported=%t playback_wait=%s playback_buffered=%s playback_ready_state=%d prefetch=gate:%d/%d range:%t/%t size:%t started:%d completed:%d canceled:%d failed:%d bytes:%d",
		snapshot.fileID,
		snapshot.reason,
		formatQuarkCryptSeekDuration(snapshot.firstByte),
		formatQuarkCryptSeekDuration(snapshot.openReady),
		snapshot.plainRanges,
		formatQuarkCryptPlainRangeSamples(snapshot.plainRangeSamples),
		snapshot.sizeProbes,
		formatQuarkCryptSeekDuration(snapshot.linkWait),
		snapshot.linkRequests,
		formatQuarkCryptSeekDuration(snapshot.upstreamWait),
		snapshot.upstreamRequests,
		formatQuarkCryptSeekDuration(snapshot.upstreamWaitMax),
		snapshot.cacheHits,
		snapshot.cacheMisses,
		snapshot.maxActive,
		snapshot.playbackReported,
		formatQuarkCryptSeekDuration(snapshot.playbackWait),
		formatQuarkCryptSeekDuration(snapshot.playbackBuffered),
		snapshot.playbackReadyState,
		snapshot.prefetchGatePasses,
		snapshot.prefetchGateChecks,
		snapshot.prefetchRangeKnown,
		snapshot.prefetchRangeOK,
		snapshot.prefetchSizeKnown,
		snapshot.prefetchStarted,
		snapshot.prefetchCompleted,
		snapshot.prefetchCanceled,
		snapshot.prefetchFailed,
		snapshot.prefetchCachedByte,
	)
}

func formatQuarkCryptSeekDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return d.Round(time.Millisecond).String()
}

func formatQuarkCryptPlainRangeSamples(samples []quarkCryptPlainRangeSample) string {
	if len(samples) == 0 {
		return "-"
	}
	var builder strings.Builder
	for index, sample := range samples {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatInt(sample.offset, 10))
		builder.WriteByte('+')
		if sample.limit < 0 {
			builder.WriteString("EOF")
			continue
		}
		builder.WriteString(strconv.FormatInt(sample.limit, 10))
	}
	return builder.String()
}

// ReportPlaintextSeekPlayback records the browser-visible outcome and leaves
// a short observation window for queued lookaheads to settle. A seek can emit
// playing before a canceled stale CDN read has released its prefetch slot.
func (d *CryptDriver) ReportPlaintextSeekPlayback(fileID string, wait, buffered time.Duration, readyState int) {
	if fileID == "" {
		return
	}
	trace := d.activeSeekTrace(fileID)
	if trace == nil || !trace.notePlayback(wait, buffered, readyState) {
		log.Printf("[quark crypt seek playback] file=%s wait=%s buffered=%s ready_state=%d trace=unavailable",
			fileID,
			formatQuarkCryptSeekDuration(wait),
			formatQuarkCryptSeekDuration(buffered),
			readyState,
		)
		return
	}
	go func() {
		timer := time.NewTimer(quarkCryptSeekPlaybackObservationDuration)
		defer timer.Stop()
		<-timer.C
		d.finishSeekTrace(trace, "playback-observation")
	}()
}

func (d *CryptDriver) beginCiphertextRange(fileID string) int {
	d.ciphertextMu.Lock()
	if d.activeCiphertext == nil {
		d.activeCiphertext = make(map[string]int)
	}
	d.activeCiphertext[fileID]++
	active := d.activeCiphertext[fileID]
	d.ciphertextMu.Unlock()
	return active
}

func (d *CryptDriver) endCiphertextRange(fileID string) {
	d.ciphertextMu.Lock()
	if active := d.activeCiphertext[fileID]; active <= 1 {
		delete(d.activeCiphertext, fileID)
	} else {
		d.activeCiphertext[fileID] = active - 1
	}
	d.ciphertextMu.Unlock()
}

func (t *quarkCryptSeekTrace) notePlainRange(offset, limit int64) {
	t.mu.Lock()
	if !t.finished {
		t.plainRanges++
		if len(t.plainRangeSamples) < quarkCryptSeekPlainRangeSamples {
			t.plainRangeSamples = append(t.plainRangeSamples, quarkCryptPlainRangeSample{offset: offset, limit: limit})
		}
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) noteSizeProbe() {
	t.mu.Lock()
	if !t.finished {
		t.sizeProbes++
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) noteDownloadLink(wait time.Duration) {
	t.mu.Lock()
	if !t.finished {
		t.linkRequests++
		t.linkWait += wait
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) noteUpstreamResponse(wait time.Duration) {
	t.mu.Lock()
	if !t.finished {
		t.upstreamRequests++
		t.upstreamWait += wait
		if wait > t.upstreamWaitMax {
			t.upstreamWaitMax = wait
		}
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) noteCacheHit() {
	t.mu.Lock()
	if !t.finished {
		t.cacheHits++
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) noteCacheMiss() {
	t.mu.Lock()
	if !t.finished {
		t.cacheMisses++
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) noteActiveCiphertext(active int) {
	t.mu.Lock()
	if !t.finished && active > t.maxActive {
		t.maxActive = active
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) notePrefetchStarted() {
	t.mu.Lock()
	if !t.finished {
		t.prefetchStarted++
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) notePrefetchCompleted(bytes int64) {
	t.mu.Lock()
	if !t.finished {
		t.prefetchCompleted++
		t.prefetchCachedByte += bytes
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) notePrefetchCanceled() {
	t.mu.Lock()
	if !t.finished {
		t.prefetchCanceled++
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) notePrefetchFailed() {
	t.mu.Lock()
	if !t.finished {
		t.prefetchFailed++
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) notePrefetchGate(playback, rangeKnown, rangeSupported, sizeKnown bool) {
	t.mu.Lock()
	if !t.finished {
		t.prefetchGateChecks++
		if playback && rangeKnown && rangeSupported && sizeKnown {
			t.prefetchGatePasses++
		}
		t.prefetchRangeKnown = rangeKnown
		t.prefetchRangeOK = rangeSupported
		t.prefetchSizeKnown = sizeKnown
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) noteOpenReady() {
	t.noteEarliest(&t.openReady)
}

func (t *quarkCryptSeekTrace) noteFirstByte() {
	t.noteEarliest(&t.firstByte)
}

func (t *quarkCryptSeekTrace) notePlayback(wait, buffered time.Duration, readyState int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished || t.playbackReported {
		return false
	}
	t.playbackReported = true
	t.playbackWait = wait
	t.playbackBuffered = buffered
	t.playbackReadyState = readyState
	return true
}

func (t *quarkCryptSeekTrace) firstByteSnapshot() (quarkCryptSeekTraceSnapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished || t.firstByteLogged {
		return quarkCryptSeekTraceSnapshot{}, false
	}
	t.firstByteLogged = true
	return t.snapshotLocked("first-byte"), true
}

func (t *quarkCryptSeekTrace) noteEarliest(target *time.Duration) {
	elapsed := time.Since(t.started)
	t.mu.Lock()
	if !t.finished && (*target == 0 || elapsed < *target) {
		*target = elapsed
	}
	t.mu.Unlock()
}

func (t *quarkCryptSeekTrace) finish(reason string) (quarkCryptSeekTraceSnapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return quarkCryptSeekTraceSnapshot{}, false
	}
	t.finished = true
	return t.snapshotLocked(reason), true
}

func (t *quarkCryptSeekTrace) snapshotLocked(reason string) quarkCryptSeekTraceSnapshot {
	return quarkCryptSeekTraceSnapshot{
		fileID:             t.fileID,
		reason:             reason,
		plainRanges:        t.plainRanges,
		plainRangeSamples:  append([]quarkCryptPlainRangeSample(nil), t.plainRangeSamples...),
		sizeProbes:         t.sizeProbes,
		linkRequests:       t.linkRequests,
		linkWait:           t.linkWait,
		upstreamRequests:   t.upstreamRequests,
		upstreamWait:       t.upstreamWait,
		upstreamWaitMax:    t.upstreamWaitMax,
		cacheHits:          t.cacheHits,
		cacheMisses:        t.cacheMisses,
		maxActive:          t.maxActive,
		prefetchStarted:    t.prefetchStarted,
		prefetchCompleted:  t.prefetchCompleted,
		prefetchCanceled:   t.prefetchCanceled,
		prefetchFailed:     t.prefetchFailed,
		prefetchCachedByte: t.prefetchCachedByte,
		prefetchGateChecks: t.prefetchGateChecks,
		prefetchGatePasses: t.prefetchGatePasses,
		prefetchRangeKnown: t.prefetchRangeKnown,
		prefetchRangeOK:    t.prefetchRangeOK,
		prefetchSizeKnown:  t.prefetchSizeKnown,
		openReady:          t.openReady,
		firstByte:          t.firstByte,
		playbackReported:   t.playbackReported,
		playbackWait:       t.playbackWait,
		playbackBuffered:   t.playbackBuffered,
		playbackReadyState: t.playbackReadyState,
	}
}

type quarkCryptFirstByteReader struct {
	io.ReadCloser
	driver *CryptDriver
	trace  *quarkCryptSeekTrace
	once   sync.Once
}

// quarkCryptCiphertextReadCloser keeps the active-range count accurate until
// the caller has released the upstream response body.
type quarkCryptCiphertextReadCloser struct {
	io.ReadCloser
	done func()
	once sync.Once
}

func (r *quarkCryptCiphertextReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.done)
	return err
}

func (r *quarkCryptFirstByteReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.once.Do(func() {
			r.trace.noteFirstByte()
			if snapshot, ok := r.trace.firstByteSnapshot(); ok {
				logQuarkCryptSeek(snapshot)
			}
		})
	}
	return n, err
}
