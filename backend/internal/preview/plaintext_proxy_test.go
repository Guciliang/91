package preview

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/video-site/backend/internal/drives"
)

type plaintextPreviewSource struct {
	data []byte
}

func (s *plaintextPreviewSource) PlaintextSize(context.Context, string) (int64, error) {
	return int64(len(s.data)), nil
}

func (s *plaintextPreviewSource) OpenPlaintextRange(_ context.Context, _ string, offset, limit int64) (io.ReadCloser, error) {
	end := int(offset + limit)
	return io.NopCloser(bytes.NewReader(s.data[offset:end])), nil
}

func (s *plaintextPreviewSource) PlaintextContentType(string) string {
	return "video/mp4"
}

var _ drives.PlaintextRangeProvider = (*plaintextPreviewSource)(nil)

func TestPrepareFFmpegLinkServesPlaintextRangeThroughLoopback(t *testing.T) {
	source := &plaintextPreviewSource{data: []byte("0123456789")}
	link, cleanup, err := prepareFFmpegLink(context.Background(), &drives.StreamLink{
		PlaintextSource: source,
		PlaintextFileID: "crypt-file",
	})
	if err != nil {
		t.Fatalf("prepareFFmpegLink: %v", err)
	}
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, link.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=3-6")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusPartialContent || string(got) != "3456" {
		t.Fatalf("response status=%d body=%q", resp.StatusCode, got)
	}
	if contentRange := resp.Header.Get("Content-Range"); contentRange != "bytes 3-6/10" {
		t.Fatalf("Content-Range = %q", contentRange)
	}
}

func TestShouldProxyFFmpegLinkForPrivateDriveClient(t *testing.T) {
	if !shouldProxyFFmpegLink(&drives.StreamLink{
		URL:        "https://cdn.example.test/video.mp4",
		HTTPClient: &http.Client{},
	}) {
		t.Fatal("private drive HTTP client must be served through the loopback proxy")
	}
}
