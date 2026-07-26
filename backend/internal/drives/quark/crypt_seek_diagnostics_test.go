package quark

import (
	"testing"
	"time"
)

func TestQuarkCryptSeekTraceSnapshot(t *testing.T) {
	trace := newQuarkCryptSeekTrace("file-1")
	trace.notePlainRange(1024, -1)
	trace.notePlainRange(2048, 4096)
	trace.noteSizeProbe()
	trace.noteDownloadLink(12 * time.Millisecond)
	trace.noteUpstreamResponse(34 * time.Millisecond)
	trace.noteUpstreamResponse(56 * time.Millisecond)
	trace.noteCacheHit()
	trace.noteCacheMiss()
	trace.noteActiveCiphertext(2)
	trace.notePrefetchStarted()
	trace.notePrefetchStarted()
	trace.notePrefetchCompleted(10 * 1024 * 1024)
	trace.notePrefetchCanceled()
	trace.notePrefetchFailed()
	trace.notePrefetchGate(true, true, true, true)
	trace.notePrefetchGate(true, true, false, true)
	trace.noteOpenReady()
	trace.noteFirstByte()
	if !trace.notePlayback(120*time.Millisecond, 340*time.Millisecond, 4) {
		t.Fatal("first playback report was rejected")
	}
	if trace.notePlayback(time.Second, time.Second, 3) {
		t.Fatal("second playback report was accepted")
	}

	snapshot, ok := trace.finish("first-byte")
	if !ok {
		t.Fatal("finish returned false")
	}
	if snapshot.fileID != "file-1" || snapshot.reason != "first-byte" {
		t.Fatalf("unexpected identity: %+v", snapshot)
	}
	if snapshot.plainRanges != 2 || snapshot.sizeProbes != 1 {
		t.Fatalf("unexpected request counts: %+v", snapshot)
	}
	if got := formatQuarkCryptPlainRangeSamples(snapshot.plainRangeSamples); got != "1024+EOF,2048+4096" {
		t.Fatalf("unexpected plain range samples: %s", got)
	}
	if snapshot.linkRequests != 1 || snapshot.linkWait != 12*time.Millisecond {
		t.Fatalf("unexpected link metrics: %+v", snapshot)
	}
	if snapshot.upstreamRequests != 2 || snapshot.upstreamWait != 90*time.Millisecond || snapshot.upstreamWaitMax != 56*time.Millisecond {
		t.Fatalf("unexpected upstream metrics: %+v", snapshot)
	}
	if snapshot.cacheHits != 1 || snapshot.cacheMisses != 1 || snapshot.maxActive != 2 {
		t.Fatalf("unexpected cache or concurrency metrics: %+v", snapshot)
	}
	if snapshot.prefetchStarted != 2 || snapshot.prefetchCompleted != 1 || snapshot.prefetchCanceled != 1 || snapshot.prefetchFailed != 1 || snapshot.prefetchCachedByte != 10*1024*1024 {
		t.Fatalf("unexpected prefetch metrics: %+v", snapshot)
	}
	if snapshot.prefetchGateChecks != 2 || snapshot.prefetchGatePasses != 1 || !snapshot.prefetchRangeKnown || snapshot.prefetchRangeOK || !snapshot.prefetchSizeKnown {
		t.Fatalf("unexpected prefetch gate metrics: %+v", snapshot)
	}
	if !snapshot.playbackReported || snapshot.playbackWait != 120*time.Millisecond || snapshot.playbackBuffered != 340*time.Millisecond || snapshot.playbackReadyState != 4 {
		t.Fatalf("unexpected playback metrics: %+v", snapshot)
	}
	if _, ok := trace.finish("again"); ok {
		t.Fatal("second finish returned true")
	}
}
