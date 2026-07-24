package quark

import "testing"

func TestTransportForProxyAcceptsSupportedURLs(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:7890",
		"https://proxy.example.test:443",
		"socks5://127.0.0.1:1080",
		"socks5h://user:pass@127.0.0.1:1080",
	} {
		transport, err := transportForProxy(raw)
		if err != nil {
			t.Fatalf("transportForProxy(%q): %v", raw, err)
		}
		if transport == nil {
			t.Fatalf("transportForProxy(%q) returned nil", raw)
		}
	}
}

func TestTransportForProxyRejectsInvalidURLsWithoutEchoingCredentials(t *testing.T) {
	for _, raw := range []string{"ftp://127.0.0.1", "http://", "127.0.0.1:7890"} {
		_, err := transportForProxy(raw)
		if err == nil || err.Error() != "quark proxy configuration is invalid" {
			t.Fatalf("transportForProxy(%q) error = %v", raw, err)
		}
	}
}
