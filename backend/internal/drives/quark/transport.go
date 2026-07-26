package quark

import (
	"errors"
	"net/http"

	"github.com/video-site/backend/internal/drives"
)

// transportForProxy makes a transport private to one Quark drive. A private
// clone prevents the proxy setting from leaking into other cloud providers.
func transportForProxy(raw string) (*http.Transport, error) {
	transport, err := drives.NewHTTPTransportForProxy(raw)
	if err != nil {
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
	drives.ConfigureStreamTransport(transport)
}
