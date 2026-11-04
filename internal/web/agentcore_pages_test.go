package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
		{"GET", "/agentcore/api/conversations"},
		{"GET", "/agentcore/api/conversations/test-id"},
		{"GET", "/agentcore/api/conversations/test-id/logs"},
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
	for _, path := range []string{"/agentcore/api/tools", "/agentcore/api/conversations/id/extra"} {
		r := httptest.NewRequest("GET", s.Cfg.BasePath+path, nil)
		_, pattern := s.mux.Handler(r)
		if strings.Contains(pattern, "/agentcore/api/") {
			t.Errorf("unexpected route %q", pattern)
		}
	}
}

func TestAgentCoreAPIRouteCSRF(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-CSRF-Token") != "" {
			t.Error("CSRF token leaked upstream")
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
	for _, path := range []string{"/chat", "/chat/stream", "/chat/test-id/stop"} {
		for _, csrf := range []string{"", "wrong", session.CSRF} {
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
		}
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
