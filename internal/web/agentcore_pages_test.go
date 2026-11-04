package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAgentCoreLogsTemplate(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, lines := range []int{100, 200, 500, 1000} {
		t.Run(fmt.Sprint(lines), func(t *testing.T) {
			w := httptest.NewRecorder()
			s.render(w, httptest.NewRequest("GET", s.Cfg.BasePath+"/agentcore/logs", nil), "agentcore_logs", "AgentCore 日志", map[string]any{"LogLines": lines, "LogContent": "journal <script>alert(1)</script>", "LogError": "error <script>"})
			body := w.Body.String()
			for _, want := range []string{"AgentCore 日志", `action="/manage-test/agentcore/logs"`, `href="/manage-test/agentcore"`, fmt.Sprintf(`value="%d" selected`, lines), "刷新", "返回 AgentCore", "journal &lt;script&gt;", "error &lt;script&gt;"} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, value := range []string{"100", "200", "500", "1000"} {
				if !strings.Contains(body, `value="`+value+`"`) {
					t.Errorf("missing option %s", value)
				}
			}
			if w.Code != http.StatusOK || strings.Contains(body, "<script>alert(1)</script>") {
				t.Fatalf("unsafe or failed render: %d", w.Code)
			}
		})
	}
}

func TestAgentCorePageRoutesProtected(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	routes := []struct{ method, path string }{
		{"GET", "/agentcore"}, {"GET", "/agentcore/logs"},
		{"GET", "/agentcore/api/options"},
		{"GET", "/agentcore/api/runs/run-1/events/stream?after=42"},
		{"GET", "/agentcore/api/approvals"},
		{"POST", "/agentcore/api/approvals/request-id"},
		{"POST", "/agentcore/api/chat/test-id/clarify"},
		{"GET", "/agentcore/api/conversations"},
		{"GET", "/agentcore/api/conversations/test-id"},
		{"GET", "/agentcore/api/conversations/test-id/logs"},
		{"GET", "/agentcore/api/conversations/test-id/runs"},
		{"POST", "/agentcore/api/conversations/test-id/delete"},
		{"POST", "/agentcore/api/chat"}, {"POST", "/agentcore/api/chat/stream"},
		{"POST", "/agentcore/api/chat/test-id/stop"},
	}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			for _, pending := range []bool{false, true} {
				r := httptest.NewRequest(route.method, s.Cfg.BasePath+route.path, nil)
				login := s.Cfg.BasePath + "/login"
				if pending {
					token, _, err := s.Sessions.Create("admin", true)
					if err != nil {
						t.Fatal(err)
					}
					r.AddCookie(&http.Cookie{Name: "np_session", Value: token})
					login += "/totp"
				}
				w := httptest.NewRecorder()
				s.mux.ServeHTTP(w, r)
				if w.Code != http.StatusSeeOther || w.Header().Get("Location") != login {
					t.Fatalf("status %d location %q", w.Code, w.Header().Get("Location"))
				}
			}
		})
	}
	// Unknown API endpoints must not be covered by a catch-all proxy route.
	for _, path := range []string{"/agentcore/api/tools", "/agentcore/api/conversations/id/extra", "/agentcore/api/runs/run-1/events/stream/extra", "/agentcore/api/runs/run-1/events/stream/"} {
		r := httptest.NewRequest("GET", s.Cfg.BasePath+path, nil)
		_, pattern := s.mux.Handler(r)
		if strings.Contains(pattern, "/agentcore/api/") {
			t.Errorf("unexpected route %q", pattern)
		}
	}
}

func TestAgentCoreAPIRouteCSRF(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-CSRF-Token") != "" {
			t.Error("CSRF token leaked upstream")
		}
		if strings.HasSuffix(r.URL.Path, "/runs") {
			fmt.Fprint(w, `[]`)
			return
		}
		if r.URL.Path == "/v1/conversations/test-id" && r.Method != http.MethodDelete {
			t.Errorf("delete method: %s", r.Method)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	proxy := agentCoreProxyServer(t, upstream.URL)
	s.Agent = proxy.Agent
	token, session, err := s.Sessions.Create("admin", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/chat", "/chat/stream", "/chat/test-id/stop", "/chat/test-id/clarify", "/approvals/request-id", "/conversations/test-id/delete"} {
		for _, csrf := range []string{"", "wrong", session.CSRF} {
			before := calls.Load()
			r := httptest.NewRequest("POST", s.Cfg.BasePath+"/agentcore/api"+path, strings.NewReader(`{"message":"hello"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-CSRF-Token", csrf)
			r.AddCookie(&http.Cookie{Name: "np_session", Value: token})
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, r)
			want := http.StatusForbidden
			if csrf == session.CSRF {
				want = http.StatusAccepted
			}
			if w.Code != want {
				t.Errorf("%s csrf=%q: status %d, want %d: %s", path, csrf, w.Code, want, w.Body)
			}
			if csrf != session.CSRF && calls.Load() != before {
				t.Error("CSRF rejection contacted upstream")
			}
		}
	}
}

func TestAgentCoreRunsRouteAuthenticated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/v1/conversations/test-id/runs?limit=4" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, `[{"id":"run-1","status":"waiting_approval"}]`)
	}))
	defer upstream.Close()
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	s.Agent = agentCoreProxyServer(t, upstream.URL).Agent
	r := authenticatedRequest(t, s, "GET", "/agentcore/api/conversations/test-id/runs?limit=4&secret=ignored", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != `[{"id":"run-1","status":"waiting_approval"}]` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response %d %s", w.Code, w.Body)
	}
	for _, method := range []string{"GET", "DELETE"} {
		r := authenticatedRequest(t, s, method, "/agentcore/api/conversations/test-id/delete", nil)
		_, pattern := s.mux.Handler(r)
		if strings.Contains(pattern, "/agentcore/api/") {
			t.Errorf("unsafe deletion route: %s %s", method, pattern)
		}
	}
}

func TestAgentCoreResumeRouteAuthenticated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/v1/runs/run-1/events/stream?after=42" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-CSRF-Token") != "" || r.Header.Get("Authorization") != "Bearer config-secret" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("unsafe or missing upstream headers: %v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "id: 43\ndata: resumed\n\n")
	}))
	defer upstream.Close()
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	proxy := agentCoreProxyServer(t, upstream.URL)
	s.Agent = proxy.Agent
	// A completed panel session may resume with GET without a CSRF token.
	r := authenticatedRequest(t, s, "GET", "/agentcore/api/runs/run-1/events/stream?after=42&secret=browser-secret", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "id: 43\ndata: resumed\n\n" || w.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("response %d %v %s", w.Code, w.Header(), w.Body)
	}
}

func TestAgentCoreLogsRoute(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?lines=100", "?lines=200", "?lines=500", "?lines=1000", "?lines=invalid", "?lines=999999"} {
		r := authenticatedRequest(t, s, "GET", "/agentcore/logs"+query, nil)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "AgentCore 日志") {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), fmt.Sprintf(`value="%d" selected`, logLines(r))) {
			t.Errorf("wrong line selection for %s", query)
		}
	}
}
