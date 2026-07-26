package quark

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestConfigureStreamTransportKeepsConnectionsForRangePlayback(t *testing.T) {
	transport, err := transportForProxy("http://127.0.0.1:7890")
	if err != nil {
		t.Fatal(err)
	}
	configureStreamTransport(transport)
	if transport.MaxIdleConns != 64 || transport.MaxIdleConnsPerHost != 32 {
		t.Fatalf("idle connection limits = %d/%d, want 64/32", transport.MaxIdleConns, transport.MaxIdleConnsPerHost)
	}
}

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

func TestTransportForProxyRoutesHTTPRequests(t *testing.T) {
	requests := make(chan *http.Request, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		_, _ = w.Write([]byte("via http proxy"))
	}))
	defer proxyServer.Close()

	transport, err := transportForProxy(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()

	response, err := (&http.Client{Transport: transport, Timeout: time.Second}).Get("http://origin.example.test/video.mp4")
	if err != nil {
		t.Fatalf("request through HTTP proxy: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "via http proxy" {
		t.Fatalf("response body = %q", body)
	}

	select {
	case request := <-requests:
		if request.URL.Scheme != "http" || request.URL.Host != "origin.example.test" || request.URL.Path != "/video.mp4" {
			t.Fatalf("proxy request URL = %s", request.URL)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP proxy did not receive a request")
	}
}

func TestTransportForProxyRoutesSOCKS5Requests(t *testing.T) {
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("via socks5 proxy"))
	}))
	defer targetServer.Close()

	proxyAddress, destinations, closeProxy := startSOCKS5Proxy(t)
	defer closeProxy()
	transport, err := transportForProxy("socks5h://" + proxyAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()

	response, err := (&http.Client{Transport: transport, Timeout: time.Second}).Get(targetServer.URL + "/video.mp4")
	if err != nil {
		t.Fatalf("request through SOCKS5 proxy: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "via socks5 proxy" {
		t.Fatalf("response body = %q", body)
	}

	select {
	case destination := <-destinations:
		if destination != targetServer.Listener.Addr().String() {
			t.Fatalf("SOCKS5 destination = %q, want %q", destination, targetServer.Listener.Addr())
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS5 proxy did not receive a CONNECT request")
	}
}

func startSOCKS5Proxy(t *testing.T) (string, <-chan string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	destinations := make(chan string, 1)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go relaySOCKS5Connection(connection, destinations)
		}
	}()
	return listener.Addr().String(), destinations, func() { _ = listener.Close() }
}

func relaySOCKS5Connection(connection net.Conn, destinations chan<- string) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	if !readSOCKS5Greeting(reader, connection) {
		return
	}
	destination, ok := readSOCKS5Connect(reader, connection)
	if !ok {
		return
	}
	select {
	case destinations <- destination:
	default:
	}
	upstream, err := net.Dial("tcp", destination)
	if err != nil {
		_, _ = connection.Write([]byte{0x05, 0x05, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
		return
	}
	defer upstream.Close()
	_, _ = connection.Write([]byte{0x05, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	go func() { _, _ = io.Copy(upstream, reader) }()
	_, _ = io.Copy(connection, upstream)
}

func readSOCKS5Greeting(reader *bufio.Reader, connection net.Conn) bool {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 0x05 {
		return false
	}
	methods := make([]byte, header[1])
	if _, err := io.ReadFull(reader, methods); err != nil {
		return false
	}
	_, err := connection.Write([]byte{0x05, 0x00})
	return err == nil
}

func readSOCKS5Connect(reader *bufio.Reader, connection net.Conn) (string, bool) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 0x05 || header[1] != 0x01 {
		return "", false
	}
	var host string
	switch header[3] {
	case 0x01:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", false
		}
		host = net.IP(address).String()
	case 0x03:
		length, err := reader.ReadByte()
		if err != nil {
			return "", false
		}
		address := make([]byte, length)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", false
		}
		host = string(address)
	case 0x04:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", false
		}
		host = net.IP(address).String()
	default:
		_, _ = connection.Write([]byte{0x05, 0x08, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
		return "", false
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return "", false
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))), true
}
