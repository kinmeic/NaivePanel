package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kinmeic/NaivePanel/internal/auth"
	"github.com/kinmeic/NaivePanel/internal/sites"
	"github.com/kinmeic/NaivePanel/internal/warp"
)

type fakeWARP struct {
	state  warp.State
	run    func() error
	egress func(context.Context) (warp.EgressResult, error)
}

func (f *fakeWARP) VerifyEgress(ctx context.Context) (warp.EgressResult, error) { return f.egress(ctx) }

func (f *fakeWARP) State() warp.State        { return f.state }
func (f *fakeWARP) Install() error           { return f.run() }
func (f *fakeWARP) Register() error          { return f.run() }
func (f *fakeWARP) Connect() error           { return f.run() }
func (f *fakeWARP) Disconnect() error        { return f.run() }
func (f *fakeWARP) SetPort(int) error        { return f.run() }
func (f *fakeWARP) BindLicense(string) error { return f.run() }

func warpRequest(t *testing.T, s *Server, path string, form url.Values, authenticated, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	method := http.MethodGet
	if form != nil {
		method = http.MethodPost
	}
	var token string
	if authenticated {
		tok, sess, err := s.Sessions.Create("admin", false)
		if err != nil {
			t.Fatal(err)
		}
		token = tok
		if form != nil && csrf {
			form.Set("_csrf", sess.CSRF)
		}
	}
	r := httptest.NewRequest(method, s.Cfg.BasePath+path, strings.NewReader(form.Encode()))
	r.Header.Set(sites.ProxyTokenHeader, s.Cfg.ProxyToken)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("X-NaivePanel-Async", "1")
	if token != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestWARPRoutesRequireAuthenticationAndCSRF(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	s.WARP = &fakeWARP{run: func() error { t.Error("unauthorized mutation"); return nil }, egress: func(context.Context) (warp.EgressResult, error) {
		t.Error("unauthorized verification")
		return warp.EgressResult{}, nil
	}}
	for _, path := range []string{"/warp/service", "/warp/settings", "/warp/license", "/warp/egress"} {
		w := warpRequest(t, s, path, url.Values{"action": {"connect"}}, false, false)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("%s unauthenticated=%d", path, w.Code)
		}
		w = warpRequest(t, s, path, url.Values{"action": {"connect"}}, true, false)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s missing csrf=%d", path, w.Code)
		}
	}
	if w := warpRequest(t, s, "/warp", nil, false, false); w.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated GET=%d", w.Code)
	}
}

func TestWARPRejectsInvalidInputBeforeMutation(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	s.WARP = &fakeWARP{run: func() error { t.Error("invalid input reached service"); return nil }}
	for _, tc := range []struct {
		path string
		form url.Values
	}{
		{"/warp/service", url.Values{"action": {"arbitrary"}}},
		{"/warp/settings", url.Values{"port": {"0"}}},
		{"/warp/settings", url.Values{"port": {"65536"}}},
		{"/warp/settings", url.Values{"port": {"1080;touch /tmp/x"}}},
		{"/warp/license", url.Values{"license": {"invalid-secret"}}},
	} {
		w := warpRequest(t, s, tc.path, tc.form, true, true)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d: %s", tc.path, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "invalid-secret") {
			t.Fatal("echoed license input")
		}
	}
}

func TestWARPOperationRunsInBackgroundAndRejectsConflicts(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	s.WARP = &fakeWARP{run: func() error { close(started); <-release; return nil }}
	defer close(release)
	w := warpRequest(t, s, "/warp/service", url.Values{"action": {"install"}}, true, true)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d: %s", w.Code, w.Body)
	}
	var response serviceOperationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("operation never started")
	}
	if response.StatusURL != s.Cfg.BasePath+"/service-operations/"+response.ID {
		t.Fatalf("bad status URL: %q", response.StatusURL)
	}
	w = warpRequest(t, s, "/warp/service", url.Values{"action": {"connect"}}, true, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d", w.Code)
	}
	w = warpRequest(t, s, "/warp", nil, true, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "data-operation-url=") {
		t.Fatalf("active operation cannot resume: %s", w.Body)
	}
}

func TestWARPPageRendersStatesAndMenuOrder(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []warp.State{
		{ConnectionLabel: "未安装", ConnectionClass: "muted"},
		{Installed: true, Active: true, RegistrationKnown: true, ConnectionLabel: "未连接", ConnectionClass: "muted"},
		{Installed: true, Active: true, Enabled: true, Registered: true, RegistrationKnown: true, AccountType: "Unlimited", DeviceID: "device-123", Mode: "proxy", Port: 40000, Connection: "connected", ConnectionLabel: "已连接", ConnectionClass: "ok"},
		{Installed: true, Active: true, ConnectionLabel: "未知", ConnectionClass: "warn", Errors: []string{"注册状态读取失败"}},
	} {
		s.WARP = &fakeWARP{state: state}
		w := warpRequest(t, s, "/warp", nil, true, false)
		if w.Code != http.StatusOK {
			t.Fatalf("render status=%d: %s", w.Code, w.Body)
		}
		html := w.Body.String()
		for _, expected := range []string{"/warp/service", "/warp/settings", "/warp/license", "/warp/egress", "出口验证", `value="proxy" readonly`, `name="_csrf"`, state.ConnectionLabel} {
			if !strings.Contains(html, expected) {
				t.Fatalf("missing %q", expected)
			}
		}
		bypassAt, warpAt, geoAt := strings.Index(html, `href="/manage-test/bypasscore"`), strings.Index(html, `href="/manage-test/warp"`), strings.Index(html, `href="/manage-test/geo"`)
		if bypassAt < 0 || warpAt < bypassAt || warpAt > geoAt {
			t.Fatal("WARP menu must follow BypassCore")
		}
		if state.Registered && !strings.Contains(html, "账号类型：Unlimited") {
			t.Fatal("missing account")
		}
		if len(state.Errors) > 0 && !strings.Contains(html, state.Errors[0]) {
			t.Fatal("missing query error")
		}
	}
}

func TestWARPEgressReturnsResultAndFailures(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, verified := range []bool{true, false} {
		s.WARP = &fakeWARP{egress: func(context.Context) (warp.EgressResult, error) {
			flag := "off"
			if verified {
				flag = "on"
			}
			return warp.EgressResult{Verified: verified, IP: "203.0.113.7", WARP: flag, Raw: "ip=203.0.113.7\nwarp=" + flag}, nil
		}}
		// No port or arbitrary URL supplied in the form can change the verification target.
		w := warpRequest(t, s, "/warp/egress", url.Values{"port": {"1"}, "url": {"https://example.invalid"}}, true, true)
		if w.Code != 200 {
			t.Fatalf("status=%d: %s", w.Code, w.Body)
		}
		var result warp.EgressResult
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Verified != verified || result.IP != "203.0.113.7" {
			t.Fatalf("unexpected result: %#v", result)
		}
	}
	s.WARP = &fakeWARP{egress: func(context.Context) (warp.EgressResult, error) {
		return warp.EgressResult{}, errors.New("代理连接失败")
	}}
	w := warpRequest(t, s, "/warp/egress", url.Values{}, true, true)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "代理连接失败") {
		t.Fatalf("unexpected failure: %d %s", w.Code, w.Body)
	}
}
