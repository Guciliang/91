package drives

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/video-site/backend/internal/scopedproxy"
)

func TestHTTPClientForProxyAllowsScopedOverride(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "origin")
	}))
	t.Cleanup(origin.Close)

	defaultProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "default")
	}))
	t.Cleanup(defaultProxy.Close)

	scopedProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "scoped")
	}))
	t.Cleanup(scopedProxy.Close)

	client, err := NewHTTPClientForProxy(defaultProxy.URL, 0, nil)
	if err != nil {
		t.Fatalf("NewHTTPClientForProxy: %v", err)
	}

	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("default proxy request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if got := string(body); got != "default" {
		t.Fatalf("default proxy body = %q, want default", got)
	}

	ctx, err := scopedproxy.WithURL(context.Background(), scopedProxy.URL)
	if err != nil {
		t.Fatalf("WithURL: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL, nil)
	if err != nil {
		t.Fatalf("new scoped request: %v", err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("scoped proxy request: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if got := string(body); got != "scoped" {
		t.Fatalf("scoped proxy body = %q, want scoped", got)
	}
}
