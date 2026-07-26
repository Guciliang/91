package quark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/video-site/backend/internal/drives"
)

func TestCryptDriverDecryptsNonZeroPlaintextRange(t *testing.T) {
	plain := []byte("0123456789-crypt-range-test")
	base := New(Config{ID: "quark-crypt", Cookie: "cookie"})
	cryptDrive, err := NewCrypt(base, CryptConfig{
		Password:                "test password",
		Salt:                    "test salt",
		FilenameEncryption:      "standard",
		DirectoryNameEncryption: true,
		FilenameEncoding:        "base64",
		Suffix:                  ".bin",
	})
	if err != nil {
		t.Fatalf("NewCrypt: %v", err)
	}
	encryptedReader, err := cryptDrive.cipher.EncryptData(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("EncryptData: %v", err)
	}
	encrypted, err := io.ReadAll(encryptedReader)
	if err != nil {
		t.Fatalf("read encrypted data: %v", err)
	}

	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/download":
			writeQuarkTestJSON(w, map[string]any{
				"code": 0,
				"data": []map[string]string{{"download_url": upstream.URL + "/content"}},
			})
		case "/content":
			http.ServeContent(w, r, "encrypted.bin", time.Time{}, bytes.NewReader(encrypted))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base.apiBase = upstream.URL

	size, err := cryptDrive.PlaintextSize(context.Background(), "file-1")
	if err != nil {
		t.Fatalf("PlaintextSize: %v", err)
	}
	if size != int64(len(plain)) {
		t.Fatalf("PlaintextSize = %d, want %d", size, len(plain))
	}
	body, err := cryptDrive.OpenPlaintextRange(context.Background(), "file-1", 6, 9)
	if err != nil {
		t.Fatalf("OpenPlaintextRange: %v", err)
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close plaintext range: read=%v close=%v", readErr, closeErr)
	}
	if string(got) != string(plain[6:15]) {
		t.Fatalf("plaintext range = %q, want %q", got, plain[6:15])
	}
}

func TestCryptDriverFallsBackWhenDownloadIgnoresRanges(t *testing.T) {
	plain := bytes.Repeat([]byte("crypt-range-fallback-"), 256*1024)
	base := New(Config{ID: "quark-crypt", Cookie: "cookie"})
	cryptDrive, err := NewCrypt(base, CryptConfig{
		Password:                "test password",
		Salt:                    "test salt",
		FilenameEncryption:      "standard",
		DirectoryNameEncryption: true,
		FilenameEncoding:        "base64",
		Suffix:                  ".bin",
	})
	if err != nil {
		t.Fatalf("NewCrypt: %v", err)
	}
	encryptedReader, err := cryptDrive.cipher.EncryptData(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("EncryptData: %v", err)
	}
	encrypted, err := io.ReadAll(encryptedReader)
	if err != nil {
		t.Fatalf("read encrypted data: %v", err)
	}

	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/download":
			writeQuarkTestJSON(w, map[string]any{
				"code": 0,
				"data": []map[string]string{{"download_url": upstream.URL + "/content"}},
			})
		case "/content":
			// Deliberately ignore Range, as affected Quark CDN nodes do.
			_, _ = w.Write(encrypted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base.apiBase = upstream.URL

	const offset = 3*1024*1024 + 17
	const length = 12345
	body, err := cryptDrive.OpenPlaintextRange(context.Background(), "file-1", offset, length)
	if err != nil {
		t.Fatalf("OpenPlaintextRange: %v", err)
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close plaintext range: read=%v close=%v", readErr, closeErr)
	}
	if string(got) != string(plain[offset:offset+length]) {
		t.Fatalf("plaintext range did not match requested bytes")
	}
}

func TestCryptDriverRefreshesDownloadLinkForEachPlaintextRange(t *testing.T) {
	plain := bytes.Repeat([]byte("crypt-header-cache-"), 16*1024)
	base := New(Config{ID: "quark-crypt", Cookie: "cookie"})
	cryptDrive, err := NewCrypt(base, CryptConfig{
		Password:                "test password",
		Salt:                    "test salt",
		FilenameEncryption:      "standard",
		DirectoryNameEncryption: true,
		FilenameEncoding:        "base64",
		Suffix:                  ".bin",
	})
	if err != nil {
		t.Fatalf("NewCrypt: %v", err)
	}
	encryptedReader, err := cryptDrive.cipher.EncryptData(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("EncryptData: %v", err)
	}
	encrypted, err := io.ReadAll(encryptedReader)
	if err != nil {
		t.Fatalf("read encrypted data: %v", err)
	}

	contentRequests := 0
	downloadRequests := 0
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/download":
			downloadRequests++
			writeQuarkTestJSON(w, map[string]any{
				"code": 0,
				"data": []map[string]string{{"download_url": upstream.URL + "/content"}},
			})
		case "/content":
			contentRequests++
			http.ServeContent(w, r, "encrypted.bin", time.Time{}, bytes.NewReader(encrypted))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base.apiBase = upstream.URL

	if _, err := cryptDrive.PlaintextSize(context.Background(), "file-1"); err != nil {
		t.Fatalf("PlaintextSize: %v", err)
	}
	const offset = 100 * 1024
	const length = 1024
	body, err := cryptDrive.OpenPlaintextRange(context.Background(), "file-1", offset, length)
	if err != nil {
		t.Fatalf("OpenPlaintextRange: %v", err)
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close plaintext range: read=%v close=%v", readErr, closeErr)
	}
	if string(got) != string(plain[offset:offset+length]) {
		t.Fatalf("plaintext range did not match requested bytes")
	}
	if contentRequests != 2 {
		t.Fatalf("content requests = %d, want 2 (size probe and first requested range)", contentRequests)
	}
	if downloadRequests != 2 {
		t.Fatalf("download-link requests = %d, want 2 (size probe and first browser range)", downloadRequests)
	}

	body, err = cryptDrive.OpenPlaintextRange(context.Background(), "file-1", offset+length, length)
	if err != nil {
		t.Fatalf("second OpenPlaintextRange: %v", err)
	}
	got, readErr = io.ReadAll(body)
	closeErr = body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close second plaintext range: read=%v close=%v", readErr, closeErr)
	}
	if string(got) != string(plain[offset+length:offset+2*length]) {
		t.Fatalf("second plaintext range did not match requested bytes")
	}
	if downloadRequests != 3 {
		t.Fatalf("download-link requests = %d, want a fresh link for each browser range", downloadRequests)
	}
}

func TestCryptDriverSniffsPlaintextContentType(t *testing.T) {
	plain := append([]byte{
		0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
		0, 0, 0, 1, 'i', 's', 'o', 'm', 'm', 'p', '4', '2',
	}, bytes.Repeat([]byte{0}, 1024)...)
	base := New(Config{ID: "quark-crypt", Cookie: "cookie"})
	cryptDrive, err := NewCrypt(base, CryptConfig{Password: "test password", Salt: "test salt"})
	if err != nil {
		t.Fatalf("NewCrypt: %v", err)
	}
	encryptedReader, err := cryptDrive.cipher.EncryptData(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("EncryptData: %v", err)
	}
	encrypted, err := io.ReadAll(encryptedReader)
	if err != nil {
		t.Fatalf("read encrypted data: %v", err)
	}

	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/download":
			writeQuarkTestJSON(w, map[string]any{
				"code": 0,
				"data": []map[string]string{{"download_url": upstream.URL + "/content"}},
			})
		case "/content":
			http.ServeContent(w, r, "encrypted.bin", time.Time{}, bytes.NewReader(encrypted))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base.apiBase = upstream.URL

	body, err := cryptDrive.OpenPlaintextRange(context.Background(), "file-1", 0, int64(len(plain)))
	if err != nil {
		t.Fatalf("OpenPlaintextRange: %v", err)
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close plaintext range: read=%v close=%v", readErr, closeErr)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("plaintext content did not match")
	}
	if gotType := cryptDrive.PlaintextContentType("file-1"); gotType != "video/mp4" {
		t.Fatalf("PlaintextContentType = %q, want video/mp4", gotType)
	}
}

func TestCryptDriverPrefetchesLargeEncryptedRange(t *testing.T) {
	plain := bytes.Repeat([]byte("crypt-prefetch-range-"), 1280*1024)
	base := New(Config{ID: "quark-crypt", Cookie: "cookie"})
	cryptDrive, err := NewCrypt(base, CryptConfig{Password: "test password", Salt: "test salt"})
	if err != nil {
		t.Fatalf("NewCrypt: %v", err)
	}
	encryptedReader, err := cryptDrive.cipher.EncryptData(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("EncryptData: %v", err)
	}
	encrypted, err := io.ReadAll(encryptedReader)
	if err != nil {
		t.Fatalf("read encrypted data: %v", err)
	}
	cryptDrive.rememberFile("file-1", cryptFile{
		plaintextSize: int64(len(plain)),
		encryptedSize: int64(len(encrypted)),
		name:          "movie.mp4",
	})

	started := make(chan struct{}, quarkCryptPartConcurrency)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/download":
			writeQuarkTestJSON(w, map[string]any{
				"code": 0,
				"data": []map[string]string{{"download_url": upstream.URL + "/content"}},
			})
		case "/content":
			parts := strings.Split(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), "-")
			if len(parts) != 2 {
				t.Fatalf("missing range header: %q", r.Header.Get("Range"))
			}
			start, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				t.Fatalf("parse range start: %v", err)
			}
			end, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				t.Fatalf("parse range end: %v", err)
			}
			if start < 0 || end < start || end >= int64(len(encrypted)) {
				t.Fatalf("unexpected range %d-%d for %d bytes", start, end, len(encrypted))
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(encrypted)))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.WriteHeader(http.StatusPartialContent)
			part := encrypted[start : end+1]
			if len(part) <= cryptFileHeaderSize {
				_, _ = w.Write(part)
				return
			}
			_, _ = w.Write(part[:cryptFileHeaderSize])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(part[cryptFileHeaderSize:])
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base.apiBase = upstream.URL

	if _, err := cryptDrive.PlaintextSize(context.Background(), "file-1"); err != nil {
		t.Fatalf("PlaintextSize: %v", err)
	}
	body, err := cryptDrive.OpenPlaintextRange(context.Background(), "file-1", 0, int64(len(plain)))
	if err != nil {
		t.Fatalf("OpenPlaintextRange: %v", err)
	}
	defer body.Close()

	for i := 0; i < quarkCryptPartConcurrency; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d/%d encrypted range requests started", i, quarkCryptPartConcurrency)
		}
	}
	close(release)
	released = true
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read plaintext range: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("prefetched plaintext did not match source")
	}
}

func TestEncryptedRangePrefetchStreamsPartBeforeFullDownload(t *testing.T) {
	prefix := bytes.Repeat([]byte("b"), 64*1024)
	total := quarkCryptPartSize + 128*1024
	layout := quarkCryptPartLayout(total)
	if len(layout) != 2 {
		t.Fatalf("layout parts = %d, want 2", len(layout))
	}
	first := bytes.Repeat([]byte("a"), int(layout[0]))
	tail := bytes.Repeat([]byte("c"), int(layout[1])-len(prefix))
	second := append(append([]byte{}, prefix...), tail...)
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})

	open := func(ctx context.Context, offset, length int64) (io.ReadCloser, error) {
		switch offset {
		case 0:
			if length != int64(len(first)) {
				return nil, fmt.Errorf("first length = %d, want %d", length, len(first))
			}
			return io.NopCloser(bytes.NewReader(first)), nil
		case int64(len(first)):
			if length != int64(len(second)) {
				return nil, fmt.Errorf("second length = %d, want %d", length, len(second))
			}
			reader, writer := io.Pipe()
			go func() {
				defer writer.Close()
				if _, err := writer.Write(prefix); err != nil {
					return
				}
				close(secondStarted)
				select {
				case <-releaseSecond:
				case <-ctx.Done():
					return
				}
				_, _ = writer.Write(tail)
			}()
			return reader, nil
		default:
			return nil, fmt.Errorf("unexpected encrypted offset %d", offset)
		}
	}

	body, err := newEncryptedRangePrefetchReader(context.Background(), 0, total, open, nil, quarkCryptPartConcurrency-1)
	if err != nil {
		t.Fatalf("newEncryptedRangePrefetchReader: %v", err)
	}
	defer body.Close()

	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second encrypted range did not begin downloading")
	}
	gotFirst := make([]byte, len(first))
	if _, err := io.ReadFull(body, gotFirst); err != nil {
		t.Fatalf("read first part: %v", err)
	}
	if !bytes.Equal(gotFirst, first) {
		t.Fatal("first part did not match source")
	}

	readPrefix := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data := make([]byte, len(prefix))
		_, err := io.ReadFull(body, data)
		readPrefix <- struct {
			data []byte
			err  error
		}{data: data, err: err}
	}()
	select {
	case result := <-readPrefix:
		if result.err != nil {
			t.Fatalf("read already-downloaded second-part bytes: %v", result.err)
		}
		if !bytes.Equal(result.data, prefix) {
			t.Fatal("streamed second-part prefix did not match source")
		}
	case <-time.After(time.Second):
		t.Fatal("reader waited for the complete second part instead of streaming its prefix")
	}

	close(releaseSecond)
	gotTail, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read second-part tail: %v", err)
	}
	if !bytes.Equal(gotTail, tail) {
		t.Fatal("second-part tail did not match source")
	}
}

func TestEncryptedRangePrefetchReadsSequentiallyWithoutBackgroundParts(t *testing.T) {
	total := quarkCryptPartSize + 128*1024
	layout := quarkCryptPartLayout(total)
	first := bytes.Repeat([]byte("a"), int(layout[0]))
	second := bytes.Repeat([]byte("b"), int(layout[1]))
	var offsets []int64

	open := func(_ context.Context, offset, length int64) (io.ReadCloser, error) {
		offsets = append(offsets, offset)
		switch offset {
		case 0:
			if length != int64(len(first)) {
				return nil, fmt.Errorf("first length = %d, want %d", length, len(first))
			}
			return io.NopCloser(bytes.NewReader(first)), nil
		case int64(len(first)):
			if length != int64(len(second)) {
				return nil, fmt.Errorf("second length = %d, want %d", length, len(second))
			}
			return io.NopCloser(bytes.NewReader(second)), nil
		default:
			return nil, fmt.Errorf("unexpected encrypted offset %d", offset)
		}
	}

	body, err := newEncryptedRangePrefetchReader(context.Background(), 0, total, open, nil, 0)
	if err != nil {
		t.Fatalf("newEncryptedRangePrefetchReader: %v", err)
	}
	defer body.Close()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read sequential range: %v", err)
	}
	if !bytes.Equal(got, append(first, second...)) {
		t.Fatal("sequential range did not match source")
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != int64(len(first)) {
		t.Fatalf("opened offsets = %v, want [0 %d]", offsets, len(first))
	}
}

func TestEncryptedRangePrefetchBuffersFirstPartBeforeClientReads(t *testing.T) {
	total := quarkCryptPartSize + 128*1024
	layout := quarkCryptPartLayout(total)
	firstArrived := make(chan struct{})
	releaseFirst := make(chan struct{})

	open := func(ctx context.Context, offset, length int64) (io.ReadCloser, error) {
		switch offset {
		case 0:
			if length != layout[0] {
				return nil, fmt.Errorf("first length = %d, want %d", length, layout[0])
			}
			reader, writer := io.Pipe()
			go func() {
				defer writer.Close()
				if _, err := writer.Write(bytes.Repeat([]byte("a"), 64*1024)); err != nil {
					return
				}
				close(firstArrived)
				select {
				case <-releaseFirst:
				case <-ctx.Done():
					return
				}
				_, _ = writer.Write(bytes.Repeat([]byte("a"), int(length)-64*1024))
			}()
			return reader, nil
		case layout[0]:
			return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("b"), int(length)))), nil
		default:
			return nil, fmt.Errorf("unexpected encrypted offset %d", offset)
		}
	}

	body, err := newEncryptedRangePrefetchReader(context.Background(), 0, total, open, nil, quarkCryptPartConcurrency-1)
	if err != nil {
		t.Fatalf("newEncryptedRangePrefetchReader: %v", err)
	}
	defer body.Close()

	select {
	case <-firstArrived:
		// The producer pulled data into the first part without a client Read.
	case <-time.After(time.Second):
		t.Fatal("first encrypted part waited for the client instead of filling its buffer")
	}
	close(releaseFirst)
}

func TestCryptDriverClampsFinalEncryptedRange(t *testing.T) {
	plain := append(bytes.Repeat([]byte("crypt-final-range-"), 16*1024), "tail"...)
	base := New(Config{ID: "quark-crypt", Cookie: "cookie"})
	cryptDrive, err := NewCrypt(base, CryptConfig{
		Password:                "test password",
		Salt:                    "test salt",
		FilenameEncryption:      "standard",
		DirectoryNameEncryption: true,
		FilenameEncoding:        "base64",
		Suffix:                  ".bin",
	})
	if err != nil {
		t.Fatalf("NewCrypt: %v", err)
	}
	encryptedReader, err := cryptDrive.cipher.EncryptData(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("EncryptData: %v", err)
	}
	encrypted, err := io.ReadAll(encryptedReader)
	if err != nil {
		t.Fatalf("read encrypted data: %v", err)
	}

	var lastRangeEnd int64 = -1
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/download":
			writeQuarkTestJSON(w, map[string]any{
				"code": 0,
				"data": []map[string]string{{"download_url": upstream.URL + "/content"}},
			})
		case "/content":
			parts := strings.Split(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), "-")
			if len(parts) != 2 {
				t.Fatalf("missing range header: %q", r.Header.Get("Range"))
			}
			start, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				t.Fatalf("parse range start: %v", err)
			}
			end, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				t.Fatalf("parse range end: %v", err)
			}
			lastRangeEnd = end
			if end >= int64(len(encrypted)) {
				// Simulate strict Quark CDN nodes that ignore an overlong Range.
				_, _ = w.Write(encrypted)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(encrypted)))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(encrypted[start : end+1])
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base.apiBase = upstream.URL

	if _, err := cryptDrive.PlaintextSize(context.Background(), "file-1"); err != nil {
		t.Fatalf("PlaintextSize: %v", err)
	}
	const length = 1024
	offset := int64(len(plain) - length)
	body, err := cryptDrive.OpenPlaintextRange(context.Background(), "file-1", offset, length)
	if err != nil {
		t.Fatalf("OpenPlaintextRange: %v", err)
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close plaintext range: read=%v close=%v", readErr, closeErr)
	}
	if string(got) != string(plain[offset:]) {
		t.Fatalf("plaintext range did not match requested bytes")
	}
	if lastRangeEnd != int64(len(encrypted))-1 {
		t.Fatalf("final encrypted range end = %d, want %d", lastRangeEnd, len(encrypted)-1)
	}
}

func TestCryptDriverListSkipsUndecryptableEntries(t *testing.T) {
	base := New(Config{ID: "quark-crypt", Cookie: "cookie"})
	cryptDrive, err := NewCrypt(base, CryptConfig{
		Password:                "test password",
		Salt:                    "test salt",
		FilenameEncryption:      "standard",
		DirectoryNameEncryption: true,
		FilenameEncoding:        "base64",
		Suffix:                  ".bin",
	})
	if err != nil {
		t.Fatalf("NewCrypt: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/sort" {
			http.NotFound(w, r)
			return
		}
		writeQuarkTestJSON(w, map[string]any{
			"code": 0,
			"data": map[string]any{"list": []map[string]any{
				{"fid": "valid", "file_name": cryptDrive.cipher.EncryptFileName("movie.mp4"), "size": cryptDrive.cipher.EncryptedSize(12), "file": true},
				{"fid": "invalid", "file_name": "not-a-crypt-name", "size": 12, "file": true},
			}},
			"metadata": map[string]any{"_total": 2},
		})
	}))
	defer upstream.Close()
	base.apiBase = upstream.URL

	entries, err := cryptDrive.List(context.Background(), "0")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "valid" || entries[0].Name != "movie.mp4" || entries[0].Size != 12 {
		t.Fatalf("entries = %#v, want decrypted valid entry only", entries)
	}
}

func TestUploadUsesQuarkPreHashMultipartCommitAndFinish(t *testing.T) {
	var calls []string
	var uploaded bytes.Buffer
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			calls = append(calls, "pre")
			writeQuarkTestJSON(w, map[string]any{
				"code": 0,
				"data": map[string]any{
					"task_id": "task-1", "upload_id": "upload-1", "obj_key": "object",
					"upload_url": upstream.URL, "fid": "file-1", "bucket": "bucket",
					"callback": map[string]string{"callbackUrl": "https://callback.example.test"}, "auth_info": "auth-info",
				},
				"metadata": map[string]any{"part_size": 3},
			})
		case "/file/update/hash":
			calls = append(calls, "hash")
			writeQuarkTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"finish": false}})
		case "/file/upload/auth":
			calls = append(calls, "auth")
			writeQuarkTestJSON(w, map[string]any{"code": 0, "data": map[string]string{"auth_key": "oss-auth"}})
		case "/object":
			if r.Method == http.MethodPut {
				calls = append(calls, "part")
				_, _ = io.Copy(&uploaded, r.Body)
				w.Header().Set("Etag", `"part-etag"`)
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method == http.MethodPost {
				calls = append(calls, "commit")
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), "CompleteMultipartUpload") {
					t.Errorf("commit body = %s", body)
				}
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Error(w, "unexpected object method", http.StatusMethodNotAllowed)
		case "/file/upload/finish":
			calls = append(calls, "finish")
			writeQuarkTestJSON(w, map[string]any{"code": 0, "data": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	baseURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyTransport := http.DefaultTransport.(*http.Transport).Clone()
	proxyTransport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr == nil && strings.HasPrefix(host, "bucket.") {
			return (&net.Dialer{}).DialContext(ctx, network, baseURL.Host)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	d := New(Config{ID: "quark-upload", Cookie: "cookie", UploadTempDir: t.TempDir()})
	d.apiBase = upstream.URL
	d.streamHTTPClient = &http.Client{Transport: proxyTransport}

	fileID, err := d.Upload(context.Background(), "parent-1", "video.mp4", strings.NewReader("hello world"), int64(len("hello world")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if fileID != "file-1" {
		t.Fatalf("file ID = %q", fileID)
	}
	if uploaded.String() != "hello world" {
		t.Fatalf("uploaded = %q", uploaded.String())
	}
	if got, want := strings.Join(calls, ","), "pre,hash,auth,part,auth,part,auth,part,auth,part,auth,commit,finish"; got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}

func TestCryptDriverWrapsAnyCloudDriveForDirectoriesAndUploads(t *testing.T) {
	base := &genericCryptTestDrive{}
	cryptDrive, err := NewCrypt(base, CryptConfig{
		Password:                "generic password",
		Salt:                    "generic salt",
		FilenameEncryption:      "standard",
		DirectoryNameEncryption: true,
		FilenameEncoding:        "base64",
		Suffix:                  ".bin",
	})
	if err != nil {
		t.Fatalf("NewCrypt: %v", err)
	}

	dirID, err := cryptDrive.EnsureDir(context.Background(), "Library/Films")
	if err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if dirID != "encrypted-dir" {
		t.Fatalf("EnsureDir ID = %q", dirID)
	}
	wantPath := cryptDrive.cipher.EncryptDirName("Library") + "/" + cryptDrive.cipher.EncryptDirName("Films")
	if base.ensuredPath != wantPath {
		t.Fatalf("encrypted path = %q, want %q", base.ensuredPath, wantPath)
	}

	plain := []byte("generic crypt upload")
	fileID, err := cryptDrive.Upload(context.Background(), dirID, "movie.mkv", bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if fileID != "encrypted-file" {
		t.Fatalf("Upload file ID = %q", fileID)
	}
	if base.uploadedName != cryptDrive.cipher.EncryptFileName("movie.mkv") {
		t.Fatalf("encrypted name = %q", base.uploadedName)
	}
	if base.uploadedSize != cryptDrive.cipher.EncryptedSize(int64(len(plain))) {
		t.Fatalf("encrypted size = %d", base.uploadedSize)
	}
	decrypted, err := cryptDrive.cipher.DecryptData(io.NopCloser(bytes.NewReader(base.uploadedData)))
	if err != nil {
		t.Fatalf("DecryptData: %v", err)
	}
	got, readErr := io.ReadAll(decrypted)
	closeErr := decrypted.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read decrypted upload: read=%v close=%v", readErr, closeErr)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypted upload = %q, want %q", got, plain)
	}
}

type genericCryptTestDrive struct {
	ensuredPath  string
	uploadedName string
	uploadedData []byte
	uploadedSize int64
}

func (d *genericCryptTestDrive) Kind() string               { return "p123" }
func (d *genericCryptTestDrive) ID() string                 { return "generic-crypt" }
func (d *genericCryptTestDrive) RootID() string             { return "root" }
func (d *genericCryptTestDrive) Init(context.Context) error { return nil }
func (d *genericCryptTestDrive) List(context.Context, string) ([]drives.Entry, error) {
	return nil, nil
}
func (d *genericCryptTestDrive) Stat(context.Context, string) (*drives.Entry, error) {
	return nil, drives.ErrNotSupported
}
func (d *genericCryptTestDrive) StreamURL(context.Context, string) (*drives.StreamLink, error) {
	return nil, fmt.Errorf("not used")
}
func (d *genericCryptTestDrive) EnsureDir(_ context.Context, path string) (string, error) {
	d.ensuredPath = path
	return "encrypted-dir", nil
}
func (d *genericCryptTestDrive) Upload(_ context.Context, _ string, name string, r io.Reader, size int64) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	d.uploadedName = name
	d.uploadedData = data
	d.uploadedSize = size
	return "encrypted-file", nil
}

func writeQuarkTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
