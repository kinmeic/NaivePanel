package web

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kinmeic/NaivePanel/internal/agentcore"
	"github.com/kinmeic/NaivePanel/internal/config"
)

func agentCoreProxyServer(t *testing.T, upstream string) *Server {
	t.Helper()
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{BasePath: "/panel"}
	cfg.AgentCore.ConfigPath = filepath.Join(t.TempDir(), "agent-core.yaml")
	data := fmt.Sprintf("server:\n  host: %q\n  port: %s\n  auth_token: config-secret\n", u.Hostname(), u.Port())
	if err := os.WriteFile(cfg.AgentCore.ConfigPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return &Server{Cfg: cfg, Agent: agentcore.New(cfg)}
}

func TestAgentCoreTarget(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.0.0.2", "::1", "localhost"} {
		target, token, err := agentCoreTarget([]byte(fmt.Sprintf("server: {host: %q, port: 3030, auth_token: secret}", host)))
		if err != nil || target == nil || token != "secret" {
			t.Fatalf("%s: %v", host, err)
		}
	}
	for _, data := range []string{
		"server: {host: example.com, port: 80}",
		"server: {host: 0.0.0.0, port: 3030}",
		"server: {host: '::', port: 3030}",
		"server: {host: 169.254.169.254, port: 80}",
		"server: {host: 192.168.1.1, port: 80}",
		"server: {host: 'http://127.0.0.1', port: 80}",
		"server: {host: 127.0.0.1, port: 0}",
		"server: {host: 127.0.0.1, port: 65536}",
		"server: {host: 127.0.0.1, port: 3030, auth_token: '${AUTH_TOKEN}'}",
		"server: {host: 127.0.0.1, port: 3030, auth_token: \"bad\\nheader\"}",
		"{}", "server: [",
	} {
		if _, _, err := agentCoreTarget([]byte(data)); err == nil {
			t.Errorf("accepted %s", data)
		}
	}
}

func TestAgentCoreProxyWhitelist(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/tools"}, {"DELETE", "/conversations/abc"},
		{"PUT", "/conversations/abc"}, {"GET", "/chat"},
		{"GET", "/conversations/abc/logs/extra"}, {"POST", "/chat/abc/clarify/extra"},
		{"GET", "/conversations/../tools"}, {"GET", "/conversations/a%2Fb"},
		{"GET", "/conversations/%61"}, {"GET", "/conversations/."},
		{"GET", "/conversations/"}, {"HEAD", "/conversations"},
	} {
		w := httptest.NewRecorder()
		s.handleAgentCoreAPI(w, httptest.NewRequest(tc.method, "/agentcore/api"+tc.path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}

func TestAgentCoreProxyForwarding(t *testing.T) {
	for _, tc := range []struct{ method, path, upstream string }{
		{"GET", "/conversations?source=web&limit=10&offset=2&token=evil", "/v1/conversations?limit=10&offset=2&source=web"},
		{"GET", "/conversations/abc-123?foo=bar", "/v1/conversations/abc-123"},
		{"GET", "/conversations/abc-123/logs", "/v1/conversations/abc-123/logs"},
		{"GET", "/approvals", "/v1/approvals"},
		{"POST", "/approvals/request-123", "/v1/approvals/request-123"},
		{"POST", "/chat/abc-123/clarify", "/v1/chat/abc-123/clarify"},
		{"POST", "/chat", "/v1/chat"},
		{"POST", "/chat/abc-123/stop", "/v1/chat/abc-123/stop"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.RequestURI() != tc.upstream {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer config-secret" {
					t.Error("wrong token")
				}
				for _, h := range []string{"Cookie", "Origin", "Forwarded", "X-Forwarded-For", "X-Other", "X-CSRF-Token"} {
					if r.Header.Get(h) != "" {
						t.Errorf("leaked %s", h)
					}
				}
				if tc.method == "POST" {
					b, _ := io.ReadAll(r.Body)
					if string(b) != `{"message":"hello"}` {
						t.Errorf("body: %s", b)
					}
				}
				w.Header().Set("Set-Cookie", "upstream=secret")
				w.Header().Set("Location", "http://example.com")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, `{"error":"busy"}`)
			}))
			defer upstream.Close()
			s := agentCoreProxyServer(t, upstream.URL)
			req := httptest.NewRequest(tc.method, "/panel/agentcore/api"+tc.path, strings.NewReader(`{"message":"hello"}`))
			for _, h := range []string{"Cookie", "Authorization", "Origin", "Forwarded", "X-Forwarded-For", "X-Other", "X-CSRF-Token"} {
				req.Header.Set(h, "browser-secret")
			}
			w := httptest.NewRecorder()
			s.handleAgentCoreAPI(w, req)
			if w.Code != 409 || w.Body.String() != `{"error":"busy"}` {
				t.Fatalf("response %d %s", w.Code, w.Body)
			}
			if w.Header().Get("Set-Cookie") != "" || w.Header().Get("Location") != "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("unsafe response headers")
			}
		})
	}
}

func TestAgentCoreProxyStreamAndCancellation(t *testing.T) {
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/stream" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Run-ID", "run-1")
		fmt.Fprint(w, "event: delta\ndata: {\"content\":\"hello\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer upstream.Close()
	s := agentCoreProxyServer(t, upstream.URL)
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleAgentCoreAPI(&statusRecorder{ResponseWriter: w}, r)
	}))
	defer panel.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", panel.URL+"/panel/agentcore/api/chat/stream", strings.NewReader(`{"message":"hello"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Run-ID") != "run-1" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("missing stream headers")
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "event: delta\n" {
		t.Fatalf("stream did not flush: %q %v", line, err)
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not canceled")
	}
}

func TestAgentCoreProxyRejectLargeBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("oversized body reached upstream") }))
	defer upstream.Close()
	s := agentCoreProxyServer(t, upstream.URL)
	w := httptest.NewRecorder()
	s.handleAgentCoreAPI(w, httptest.NewRequest("POST", "/panel/agentcore/api/chat", strings.NewReader(strings.Repeat("x", (1<<20)+1))))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", w.Code)
	}
}

func TestAgentCoreProxyDeadline(t *testing.T) {
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(canceled)
	}))
	defer upstream.Close()
	s := agentCoreProxyServer(t, upstream.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/panel/agentcore/api/conversations", nil).WithContext(ctx)
	s.handleAgentCoreAPI(w, r)
	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("status %d", w.Code)
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not canceled on deadline")
	}
}

func TestAgentCoreProxyDoesNotFollowRedirect(t *testing.T) {
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed") }))
	defer trap.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, trap.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	s := agentCoreProxyServer(t, upstream.URL)
	w := httptest.NewRecorder()
	s.handleAgentCoreAPI(w, httptest.NewRequest("GET", "/panel/agentcore/api/conversations", nil))
	if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != "" {
		t.Fatalf("unsafe redirect: %d %v", w.Code, w.Header())
	}
}

func TestAgentCoreProxyConfigAndUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s := agentCoreProxyServer(t, upstream.URL)
	upstream.Close()
	w := httptest.NewRecorder()
	s.handleAgentCoreAPI(w, httptest.NewRequest("GET", "/panel/agentcore/api/conversations", nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d", w.Code)
	}
	if err := os.Remove(s.Cfg.AgentCore.ConfigPath); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.handleAgentCoreAPI(w, httptest.NewRequest("GET", "/panel/agentcore/api/conversations", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
}
