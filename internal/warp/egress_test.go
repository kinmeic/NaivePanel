package warp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This SOCKS server forwards only to a local TLS test server. It records the
// domain supplied by the client, proving that DNS resolution stays at the proxy.
func traceTestClient(t *testing.T, handler http.Handler) (*http.Client, <-chan string) {
	t.Helper()
	upstream := httptest.NewTLSServer(handler)
	t.Cleanup(upstream.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	destination := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		header := make([]byte, 2)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		methods := make([]byte, int(header[1]))
		if _, err := io.ReadFull(conn, methods); err != nil {
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return
		}
		request := make([]byte, 5)
		if _, err := io.ReadFull(conn, request); err != nil {
			return
		}
		if request[0] != 5 || request[1] != 1 || request[3] != 3 {
			destination <- "expected SOCKS domain address"
			return
		}
		domain := make([]byte, int(request[4]))
		if _, err := io.ReadFull(conn, domain); err != nil {
			return
		}
		port := make([]byte, 2)
		if _, err := io.ReadFull(conn, port); err != nil {
			return
		}
		destination <- fmt.Sprintf("%s:%d", domain, binary.BigEndian.Uint16(port))
		target, err := net.Dial("tcp", upstream.Listener.Addr().String())
		if err != nil {
			return
		}
		defer target.Close()
		if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
			return
		}
		go func() { _, _ = io.Copy(target, conn) }()
		_, _ = io.Copy(conn, target)
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	client := traceClient(port)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: upstream.Certificate().DNSNames[0]}
	t.Cleanup(client.CloseIdleConnections)
	return client, destination
}

func TestTraceUsesSOCKSRemoteDNSAndActualResponse(t *testing.T) {
	// Environment proxy settings cannot override the explicitly selected SOCKS proxy.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	for _, flag := range []string{"on", "plus", "off"} {
		t.Run(flag, func(t *testing.T) {
			client, destination := traceTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/cdn-cgi/trace" || r.Host != "www.cloudflare.com" {
					t.Errorf("unexpected trace request: %s %s", r.Host, r.URL)
				}
				fmt.Fprintf(w, "ip=203.0.113.7\nloc=SG\ncolo=SIN\nwarp=%s\n", flag)
			}))
			result, err := requestTrace(context.Background(), client, "127.0.0.1:12345")
			if err != nil {
				t.Fatal(err)
			}
			if result.Verified != (flag != "off") || result.IP != "203.0.113.7" || result.Location != "SG" || result.Colo != "SIN" || result.Proxy != "127.0.0.1:12345" {
				t.Fatalf("unexpected result: %#v", result)
			}
			select {
			case got := <-destination:
				if got != "www.cloudflare.com:443" {
					t.Fatalf("SOCKS target: %s", got)
				}
			case <-time.After(time.Second):
				t.Fatal("no SOCKS connection")
			}
		})
	}
}

func TestTraceRejectsBadResponsesAndRedirects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server error", 503, "unavailable"},
		{"redirect", 302, ""},
		{"html", 200, "<html>not trace</html>"},
		{"invalid warp", 200, "ip=203.0.113.1\nwarp=unknown"},
		{"duplicate warp", 200, "ip=203.0.113.1\nwarp=off\nwarp=on"},
		{"oversized", 200, strings.Repeat("x", maxTraceSize+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := traceTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.invalid/must-not-follow")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			if _, err := requestTrace(context.Background(), client, "127.0.0.1:12345"); err == nil {
				t.Fatal("accepted bad trace response")
			}
		})
	}
}

func TestTraceDoesNotFallbackWhenProxyUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	client := traceClient(port)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = requestTrace(ctx, client, "127.0.0.1:"+strconv.Itoa(port))
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("expected local proxy failure: %v", err)
	}
}

func TestVerifyEgressUsesLivePortWithoutChangingConnection(t *testing.T) {
	m := fakeManager(t, func(args []string) (string, error) {
		if strings.Join(args, " ") != "settings" {
			t.Errorf("verification must not change service: %v", args)
		}
		return "Mode: WarpProxy on port 45678", nil
	})
	m.trace = func(ctx context.Context, port int) (EgressResult, error) {
		if port != 45678 {
			t.Errorf("used stale/default port: %d", port)
		}
		return EgressResult{Verified: true, IP: "2001:db8::1", WARP: "on"}, nil
	}
	result, err := m.VerifyEgress(context.Background())
	if err != nil || !result.Verified {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	m.mu.Lock()
	_, err = m.VerifyEgress(context.Background())
	m.mu.Unlock()
	if err == nil {
		t.Fatal("verification should reject an ongoing WARP operation")
	}
}
