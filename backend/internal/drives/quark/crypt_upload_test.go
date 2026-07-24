package quark

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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

func writeQuarkTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
