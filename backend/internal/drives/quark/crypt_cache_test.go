package quark

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/video-site/backend/internal/drives"
)

func TestQuarkCryptRangeCacheServesContainedRange(t *testing.T) {
	cache := newQuarkCryptRangeCache()
	cache.Put("file-1", 100, []byte("0123456789"))

	got := cache.Get("file-1", 103, 4)
	if string(got) != "3456" {
		t.Fatalf("cache range = %q, want 3456", got)
	}
	if got := cache.Get("file-1", 108, 3); got != nil {
		t.Fatalf("out-of-range cache result = %q, want nil", got)
	}
	if got := cache.Get("other-file", 103, 4); got != nil {
		t.Fatalf("other-file cache result = %q, want nil", got)
	}
}

func TestQuarkCryptRangeCacheSharesLivePrefetch(t *testing.T) {
	const (
		fileID = "file-1"
		start  = int64(100)
	)
	cache := newQuarkCryptRangeCache()
	pipeReader, pipeWriter := io.Pipe()
	part := newStreamingEncryptedPart(context.Background(), io.NopCloser(pipeReader), 8, nil)
	defer part.Close()
	cache.AddLive(fileID, start, 8, part)
	defer cache.RemoveLive(fileID, start, 8, part)

	openedUpstream := false
	reader, hit, err := cache.Open(context.Background(), fileID, start+2, 4, func(context.Context, int64, int64) (io.ReadCloser, error) {
		openedUpstream = true
		return nil, io.ErrUnexpectedEOF
	})
	if err != nil {
		t.Fatalf("open live cache range: %v", err)
	}
	defer reader.Close()
	if !hit {
		t.Fatal("live prefetch was not treated as a cache hit")
	}

	go func() {
		_, _ = io.WriteString(pipeWriter, "01234567")
		_ = pipeWriter.Close()
	}()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read live cache range: %v", err)
	}
	if string(got) != "2345" {
		t.Fatalf("live cache range = %q, want 2345", got)
	}
	if openedUpstream {
		t.Fatal("live cache range opened a duplicate upstream request")
	}
}

func TestQuarkCryptRangeCacheDoesNotExpireLivePlaybackData(t *testing.T) {
	cache := newQuarkCryptRangeCache()
	cache.Put("file-1", 100, []byte("0123456789"))

	cache.mu.Lock()
	cache.entries[0].usedAt = time.Now().Add(-time.Hour)
	cache.mu.Unlock()

	if got := cache.Get("file-1", 103, 4); string(got) != "3456" {
		t.Fatalf("old cache range = %q, want 3456", got)
	}
}

func TestQuarkCryptRangeCacheContiguousEnd(t *testing.T) {
	cache := newQuarkCryptRangeCache()
	cache.Put("file-1", 100, []byte("0123456789"))
	cache.Put("file-1", 110, []byte("abcdefghij"))
	cache.Put("file-1", 130, []byte("klmnopqrst"))

	if got := cache.ContiguousEnd("file-1", 103, 140); got != 120 {
		t.Fatalf("ContiguousEnd = %d, want 120", got)
	}
	if got := cache.ContiguousEnd("file-1", 130, 135); got != 135 {
		t.Fatalf("bounded ContiguousEnd = %d, want 135", got)
	}
}

func TestCryptDriverDefersDownloadLinkForCachedEncryptedRange(t *testing.T) {
	const fileID = "file-1"
	driver := &CryptDriver{
		files:          map[string]cryptFile{fileID: {encryptedSize: 16}},
		encryptedCache: newQuarkCryptRangeCache(),
	}
	driver.encryptedCache.Put(fileID, 4, []byte("cached"))

	linkRequests := 0
	reader, err := driver.openEncryptedRangeWithLinkOpener(context.Background(), fileID, func(context.Context) (*drives.StreamLink, error) {
		linkRequests++
		return nil, nil
	}, 5, 3, nil, false)
	if err != nil {
		t.Fatalf("open cached encrypted range: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read cached encrypted range: %v", err)
	}
	if string(data) != "ach" {
		t.Fatalf("cached encrypted range = %q, want ach", data)
	}
	if linkRequests != 0 {
		t.Fatalf("download link requests = %d, want 0 for a cache hit", linkRequests)
	}
}

func TestCryptDriverPrefetchesAfterCachedPrefix(t *testing.T) {
	const fileID = "file-1"
	first := bytes.Repeat([]byte("a"), int(quarkCryptPartSize))
	next := bytes.Repeat([]byte("b"), int(quarkCryptPartSize))
	rangeSeen := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeSeen <- r.Header.Get("Range")
		w.Header().Set("Content-Range", "bytes 10485760-20971519/20971520")
		w.Header().Set("Content-Length", strconv.Itoa(len(next)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(next)
	}))
	defer upstream.Close()

	driver := &CryptDriver{
		files:          make(map[string]cryptFile),
		encryptedCache: newQuarkCryptRangeCache(),
		prefetching:    make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch),
	}
	driver.encryptedCache.Put(fileID, 0, first)
	driver.prefetchBeyondCachedRange(fileID, &drives.StreamLink{URL: upstream.URL}, 0, 2*quarkCryptPartSize)

	select {
	case got := <-rangeSeen:
		if got != "bytes=10485760-20971519" {
			t.Fatalf("lookahead range = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("lookahead request did not start")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := driver.encryptedCache.Get(fileID, quarkCryptPartSize, quarkCryptPartSize); bytes.Equal(got, next) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("lookahead bytes were not cached")
}

func TestCryptDriverPrioritizePlaintextSeekCancelsBackgroundLookahead(t *testing.T) {
	const fileID = "file-1"
	started := make(chan struct{}, 1)
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		close(canceled)
	}))
	defer upstream.Close()

	driver := &CryptDriver{
		files:          make(map[string]cryptFile),
		encryptedCache: newQuarkCryptRangeCache(),
		prefetching:    make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch),
	}
	driver.encryptedCache.Put(fileID, 0, []byte("cached"))
	driver.startEncryptedLookahead(fileID, &drives.StreamLink{URL: upstream.URL}, 10, 1, nil)

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("lookahead request did not start")
	}

	driver.PrioritizePlaintextSeek(fileID)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("seek priority did not cancel lookahead")
	}
	if got := driver.encryptedCache.Get(fileID, 0, int64(len("cached"))); string(got) != "cached" {
		t.Fatalf("cached bytes = %q, want cached", got)
	}
}

func TestCryptDriverQueuesLookaheadUntilCanceledSlotsRelease(t *testing.T) {
	const fileID = "file-1"
	const length = int64(1)
	rangeSeen := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeSeen <- r.Header.Get("Range")
		w.Header().Set("Content-Range", "bytes 100-100/101")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("x"))
	}))
	defer upstream.Close()

	driver := &CryptDriver{
		files:            make(map[string]cryptFile),
		encryptedCache:   newQuarkCryptRangeCache(),
		prefetching:      make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch),
		queuedPrefetches: make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch),
	}
	for index := 0; index < quarkCryptLookaheadParts; index++ {
		driver.prefetching[quarkCryptPrefetchKey{fileID: "busy", offset: int64(index), length: 1}] = &quarkCryptPrefetch{}
	}

	driver.startEncryptedLookahead(fileID, &drives.StreamLink{URL: upstream.URL}, 100, length, nil)
	driver.prefetchMu.Lock()
	queued := len(driver.queuedPrefetches)
	driver.prefetchMu.Unlock()
	if queued != 1 {
		t.Fatalf("queued lookaheads = %d, want 1", queued)
	}

	driver.prefetchMu.Lock()
	driver.prefetching = make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch)
	driver.prefetchMu.Unlock()

	select {
	case got := <-rangeSeen:
		if got != "bytes=100-100" {
			t.Fatalf("queued lookahead range = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("queued lookahead did not start after a slot was released")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := driver.encryptedCache.Get(fileID, 100, length); string(got) == "x" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("queued lookahead bytes were not cached")
}

func TestCryptDriverPrefetchesAlignedWindowsForSmallRanges(t *testing.T) {
	const fileID = "file-1"
	ranges := make(chan string, quarkCryptSmallRangeLookaheadParts)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges <- r.Header.Get("Range")
		w.Header().Set("Content-Range", "bytes 0-10485759/41943040")
		w.Header().Set("Content-Length", "10485760")
		w.WriteHeader(http.StatusPartialContent)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	driver := &CryptDriver{
		files:          make(map[string]cryptFile),
		encryptedCache: newQuarkCryptRangeCache(),
		prefetching:    make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch),
	}
	driver.prefetchPlaybackWindow(fileID, &drives.StreamLink{URL: upstream.URL}, 2*1024*1024, 4*quarkCryptPartSize, quarkCryptSmallRangeLookaheadParts, nil)

	want := map[string]bool{
		"bytes=0-4194303":       false,
		"bytes=4194304-8388607": false,
	}
	for range want {
		select {
		case got := <-ranges:
			if _, ok := want[got]; !ok {
				t.Fatalf("unexpected prefetch range %q", got)
			}
			want[got] = true
		case <-time.After(time.Second):
			t.Fatal("small Range lookahead requests did not start")
		}
	}
	for request, seen := range want {
		if !seen {
			t.Fatalf("missing prefetch range %q", request)
		}
	}

	driver.PrioritizePlaintextSeek(fileID)
}

func TestCryptDriverPlaybackPrefetchContinuesPastCachedWindow(t *testing.T) {
	const fileID = "file-1"
	ranges := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges <- r.Header.Get("Range")
		w.Header().Set("Content-Range", "bytes 10485760-20971519/41943040")
		w.Header().Set("Content-Length", "10485760")
		w.WriteHeader(http.StatusPartialContent)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	driver := &CryptDriver{
		files:          make(map[string]cryptFile),
		encryptedCache: newQuarkCryptRangeCache(),
		prefetching:    make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch),
	}
	driver.encryptedCache.Put(fileID, 0, bytes.Repeat([]byte("x"), int(quarkCryptPartSize)))
	driver.prefetchPlaybackWindow(fileID, &drives.StreamLink{URL: upstream.URL}, 2*1024*1024, 4*quarkCryptPartSize, 1, nil)

	select {
	case got := <-ranges:
		if got != "bytes=10485760-14680063" {
			t.Fatalf("continuing prefetch range = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("continuing playback prefetch did not start")
	}

	driver.PrioritizePlaintextSeek(fileID)
}

func TestCryptDriverPrefetchesSmallPlaybackRangeWithUnmarkedCallbackContext(t *testing.T) {
	const fileID = "file-1"
	const primaryOffset = int64(2 * 1024 * 1024)
	ranges := make(chan string, 3)
	part := bytes.Repeat([]byte("x"), int(quarkCryptPartSize))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		ranges <- rangeHeader
		switch rangeHeader {
		case "bytes=2097152-2097152":
			w.Header().Set("Content-Range", "bytes 2097152-2097152/31457280")
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("p"))
		default:
			w.Header().Set("Content-Range", "bytes 0-10485759/31457280")
			w.Header().Set("Content-Length", strconv.Itoa(len(part)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(part)
		}
	}))
	defer upstream.Close()

	driver := &CryptDriver{
		files: map[string]cryptFile{fileID: {
			encryptedSize:  3 * quarkCryptPartSize,
			rangeChecked:   true,
			rangeSupported: true,
		}},
		encryptedCache:   newQuarkCryptRangeCache(),
		prefetching:      make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch),
		queuedPrefetches: make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch),
	}

	// rclone invokes its encrypted-range callback with an internal context. The
	// callback context itself need not carry the browser-playback marker.
	reader, err := driver.openEncryptedRange(context.Background(), fileID, &drives.StreamLink{URL: upstream.URL}, primaryOffset, 1, nil, true)
	if err != nil {
		t.Fatalf("open encrypted playback range: %v", err)
	}
	if _, err := io.ReadAll(reader); err != nil {
		_ = reader.Close()
		t.Fatalf("read primary range: %v", err)
	}
	_ = reader.Close()

	want := map[string]bool{
		"bytes=2097152-2097152": false,
		"bytes=0-4194303":       false,
		"bytes=4194304-8388607": false,
	}
	for range want {
		select {
		case got := <-ranges:
			if _, ok := want[got]; !ok {
				t.Fatalf("unexpected request range %q", got)
			}
			want[got] = true
		case <-time.After(time.Second):
			t.Fatal("playback range did not start aligned lookaheads")
		}
	}
	for request, seen := range want {
		if !seen {
			t.Fatalf("missing request range %q", request)
		}
	}
}

func TestQuarkCryptRangeCacheReusesOverlapAndFetchesOnlyGap(t *testing.T) {
	cache := newQuarkCryptRangeCache()
	cache.Put("file-1", 100, []byte("0123456789"))

	var calls int
	var offset, length int64
	reader, hit, err := cache.Open(context.Background(), "file-1", 105, 10, func(_ context.Context, gotOffset, gotLength int64) (io.ReadCloser, error) {
		calls++
		offset, length = gotOffset, gotLength
		return io.NopCloser(bytes.NewReader([]byte("abcde"))), nil
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !hit {
		t.Fatal("Open did not report cache hit")
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "56789abcde" {
		t.Fatalf("combined range = %q, want 56789abcde", got)
	}
	if calls != 1 || offset != 110 || length != 5 {
		t.Fatalf("upstream calls = %d at %d length %d, want one gap 110 length 5", calls, offset, length)
	}
}

func TestQuarkCryptRangeCacheContinuesAfterReaderReturnsDataAndEOF(t *testing.T) {
	cache := newQuarkCryptRangeCache()
	cache.Put("file-1", 100, []byte("abc"))

	reader, hit, err := cache.Open(context.Background(), "file-1", 100, 6, func(_ context.Context, _, _ int64) (io.ReadCloser, error) {
		return &readCloser{Reader: &dataAndEOFReader{data: []byte("def")}}, nil
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !hit {
		t.Fatal("Open did not report cache hit")
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "abcdef" {
		t.Fatalf("combined range = %q, want abcdef", got)
	}
}

func TestStreamingEncryptedPartCachesCompletedBytes(t *testing.T) {
	done := make(chan []byte, 1)
	part := newStreamingEncryptedPart(
		context.Background(),
		io.NopCloser(bytes.NewReader([]byte("data"))),
		4,
		func(data []byte) { done <- append([]byte(nil), data...) },
	)
	defer part.Close()

	got, err := io.ReadAll(part)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("part data = %q, want data", got)
	}
	select {
	case cached := <-done:
		if string(cached) != "data" {
			t.Fatalf("completed data = %q, want data", cached)
		}
	case <-time.After(time.Second):
		t.Fatal("completed part was not cached")
	}
}

type dataAndEOFReader struct {
	data []byte
	done bool
}

func (r *dataAndEOFReader) Read(dst []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(dst, r.data)
	return n, io.EOF
}

type readCloser struct{ io.Reader }

func (readCloser) Close() error { return nil }

func TestQuarkCryptRangeCacheEvictsLeastRecentlyUsedEntry(t *testing.T) {
	cache := newQuarkCryptRangeCache()
	part := bytes.Repeat([]byte("a"), quarkCryptCacheBytes/2+1)
	cache.Put("file-1", 0, part)
	// Distinct timestamps make the eviction order deterministic.
	time.Sleep(time.Millisecond)
	cache.Put("file-1", int64(len(part)), bytes.Repeat([]byte("b"), len(part)))

	if got := cache.Get("file-1", 0, 1); got != nil {
		t.Fatal("oldest entry should have been evicted")
	}
	if got := cache.Get("file-1", int64(len(part)), 1); string(got) != "b" {
		t.Fatalf("newest entry = %q, want b", got)
	}
}
