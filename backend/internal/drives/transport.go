package drives

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// NewHTTPTransportForProxy returns a private transport for one drive. The
// transport is never shared with another configured drive, so a proxy setting
// cannot leak into unrelated API, upload, or playback requests.
func NewHTTPTransportForProxy(raw string) (*http.Transport, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" {
		return nil, errors.New("proxy configuration is invalid")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &xproxy.Auth{User: u.User.Username(), Password: password}
		}
		dialer, err := xproxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second})
		if err != nil {
			return nil, errors.New("proxy configuration is invalid")
		}
		transport.Proxy = nil
		transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
			return dialer.Dial(network, address)
		}
	default:
		return nil, errors.New("proxy configuration is invalid")
	}
	return transport, nil
}

// NewHTTPClientForProxy constructs a private client even when no proxy is
// configured. That makes later per-drive client changes safe and predictable.
func NewHTTPClientForProxy(raw string, timeout time.Duration, checkRedirect func(*http.Request, []*http.Request) error) (*http.Client, error) {
	transport, err := NewHTTPTransportForProxy(raw)
	if err != nil {
		return nil, err
	}
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: checkRedirect}, nil
}

// ConfigureStreamTransport keeps enough idle CDN connections for transformed
// playback and overlapping browser range requests.
func ConfigureStreamTransport(transport *http.Transport) {
	if transport == nil {
		return
	}
	transport.MaxIdleConns = 64
	transport.MaxIdleConnsPerHost = 32
	transport.IdleConnTimeout = 2 * time.Minute
}
