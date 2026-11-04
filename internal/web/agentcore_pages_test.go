package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
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
			assertAgentCoreLogsLayout(t, body)
			for _, want := range []string{"AgentCore 日志", `class="logbar"`, `href="/manage-test/agentcore"`, fmt.Sprintf(`class="btn active" href="/manage-test/agentcore/logs?lines=%d"`, lines), "刷新", "返回 AgentCore", "journal &lt;script&gt;", "error &lt;script&gt;"} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, value := range []string{"100", "200", "500", "1000"} {
				if !strings.Contains(body, `href="/manage-test/agentcore/logs?lines=`+value+`"`) {
					t.Errorf("missing option %s", value)
				}
			}
			if w.Code != http.StatusOK || strings.Contains(body, "<script>alert(1)</script>") {
				t.Fatalf("unsafe or failed render: %d", w.Code)
			}
		})
	}
}

func assertAgentCoreLogsLayout(t *testing.T, body string) {
	t.Helper()
	main := regexp.MustCompile(`(?s)<main>(.*?)</main>`).FindStringSubmatch(body)
	if main == nil {
		t.Fatal("missing rendered main content")
	}
	content := strings.TrimSpace(main[1])
	if strings.Count(content, "<h1>") != 1 || !strings.Contains(content, "<h1>AgentCore 日志</h1>") {
		t.Error("logs must have exactly one complete AgentCore 日志 heading")
	}
	header := regexp.MustCompile(`(?s)^<div class="editor-title"><a class="btn icon-btn" href="/manage-test/agentcore" title="返回 AgentCore" aria-label="返回 AgentCore">(.*?)</a><div><h1>AgentCore 日志</h1></div></div>`).FindStringSubmatch(content)
	if header == nil {
		t.Error("logs back icon must precede the heading in the left editor-title header")
	} else {
		assertNonemptySVG(t, header[1])
		if !strings.Contains(header[1], iconSVGs["arrow-left"]) {
			t.Error("logs back link must render arrow-left")
		}
	}
	if strings.Count(content, `href="/manage-test/agentcore"`) != 1 || regexp.MustCompile(`>\s*返回(?: AgentCore)?\s*<`).MatchString(content) {
		t.Error("logs must not have a duplicate or bottom text back button")
	}
	bar := strings.Index(content, `<div class="logbar">`)
	card := strings.Index(content, `<div class="card">`)
	if bar < 0 || card < 0 || bar >= card || strings.Contains(content[card:], `<div class="logbar">`) || strings.Contains(content, "<select") {
		t.Error("logs filters must use the operation-log button bar outside the details card")
	}
	// Check the rendered fragment without browser HTML repair hiding broken tags.
	var stack []string
	for _, tag := range regexp.MustCompile(`</?([a-z][a-z0-9-]*)\b[^>]*>`).FindAllStringSubmatch(content, -1) {
		name := tag[1]
		if strings.HasPrefix(tag[0], "</") {
			if len(stack) == 0 || stack[len(stack)-1] != name {
				t.Fatalf("unbalanced logs tag %s; open tags: %v", tag[0], stack)
			}
			stack = stack[:len(stack)-1]
		} else if !strings.HasSuffix(tag[0], "/>") && !strings.Contains(" area base br col embed hr img input link meta param source track wbr ", " "+name+" ") {
			stack = append(stack, name)
		}
	}
	if len(stack) != 0 {
		t.Errorf("unclosed logs tags: %v", stack)
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
				if strings.HasPrefix(route.path, "/agentcore/api/") {
					if w.Code != http.StatusUnauthorized || w.Header().Get("Location") != "" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Panel-Session-Invalid") != "1" {
						t.Fatalf("API status %d location %q cache %q", w.Code, w.Header().Get("Location"), w.Header().Get("Cache-Control"))
					}
				} else if w.Code != http.StatusSeeOther || w.Header().Get("Location") != login {
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

func TestAgentCoreSendAfterLogoutRejected(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	s.Agent = agentCoreProxyServer(t, upstream.URL).Agent
	token, session, err := s.Sessions.Create("admin", false)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, s.Cfg.BasePath+path, strings.NewReader(`{"message":"hello"}`))
		r.AddCookie(&http.Cookie{Name: "np_session", Value: token})
		r.Header.Set("X-CSRF-Token", session.CSRF)
		return r
	}
	before := httptest.NewRecorder()
	s.mux.ServeHTTP(before, request("/agentcore/api/chat"))
	if before.Code != http.StatusAccepted || calls.Load() != 1 {
		t.Fatalf("before logout: status %d, upstream calls %d", before.Code, calls.Load())
	}
	logout := httptest.NewRequest(http.MethodPost, s.Cfg.BasePath+"/logout", strings.NewReader("_csrf="+session.CSRF))
	logout.AddCookie(&http.Cookie{Name: "np_session", Value: token})
	logout.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loggedOut := httptest.NewRecorder()
	s.mux.ServeHTTP(loggedOut, logout)
	if loggedOut.Code != http.StatusSeeOther || s.Sessions.Get(token) != nil {
		t.Fatalf("logout: status %d, session still active: %v", loggedOut.Code, s.Sessions.Get(token) != nil)
	}
	for _, path := range []string{"/agentcore/api/chat", "/agentcore/api/chat/stream"} {
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, request(path))
		if w.Code != http.StatusUnauthorized || w.Header().Get("X-Panel-Session-Invalid") != "1" || calls.Load() != 1 {
			t.Fatalf("after logout %s: status %d, upstream calls %d", path, w.Code, calls.Load())
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
			t.Error("deletion must not preflight runs")
		}
		if r.URL.Path == "/v1/conversations/test-id" {
			if r.Method != http.MethodDelete || r.URL.RawQuery != "permanent=true" {
				t.Errorf("unexpected deletion: %s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, `{"status":"deleted"}`)
			return
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
				if strings.HasSuffix(path, "/delete") {
					want = http.StatusOK
				}
				if calls.Load() != before+1 {
					t.Error("valid CSRF must make exactly one upstream request")
				}
			}
			if w.Code != want {
				t.Errorf("%s csrf=%q: status %d, want %d: %s", path, csrf, w.Code, want, w.Body)
			}
			if csrf != session.CSRF && calls.Load() != before {
				t.Error("CSRF rejection contacted upstream")
			}
			if csrf != session.CSRF && w.Header().Get("X-Panel-Session-Invalid") != "1" {
				t.Error("CSRF rejection must mark the stale panel session")
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
		if !strings.Contains(w.Body.String(), fmt.Sprintf(`class="btn active" href="/manage-test/agentcore/logs?lines=%d"`, logLines(r))) {
			t.Errorf("wrong line selection for %s", query)
		}
	}
}
