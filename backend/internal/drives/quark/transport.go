package quark

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

// transportForProxy makes a transport private to one Quark drive. A private
// clone prevents the proxy setting from leaking into other cloud providers.
func transportForProxy(raw string) (*http.Transport, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" {
		return nil, errors.New("quark proxy configuration is invalid")
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
			return nil, errors.New("quark proxy configuration is invalid")
		}
		transport.Proxy = nil
		transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
			return dialer.Dial(network, address)
		}
	default:
		return nil, errors.New("quark proxy configuration is invalid")
	}
	return transport, nil
}

// configureStreamTransport keeps enough idle CDN connections for Crypt's
// three-part reader and a browser's overlapping byte-range requests. The
// standard library default of two idle connections per host is too small for
// this access pattern and repeatedly pays the TLS/connection setup cost.
func configureStreamTransport(transport *http.Transport) {
	if transport == nil {
		return
	}
	transport.MaxIdleConns = 64
	transport.MaxIdleConnsPerHost = 32
	transport.IdleConnTimeout = 2 * time.Minute
}
