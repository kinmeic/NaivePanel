package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinmeic/NaivePanel/internal/fail2ban"
	"github.com/kinmeic/NaivePanel/internal/sites"
)

type fakeFail2ban struct {
	state      fail2ban.Snapshot
	rules      string
	applyError error
	install    func() error
	action     func(string) error
}

func (f *fakeFail2ban) Installed() bool             { return f.state.Installed }
func (f *fakeFail2ban) Snapshot() fail2ban.Snapshot { return f.state }
func (f *fakeFail2ban) Install() error {
	if f.install != nil {
		return f.install()
	}
	return nil
}
func (f *fakeFail2ban) Action(verb string) error {
	if f.action != nil {
		return f.action(verb)
	}
	return nil
}
func (f *fakeFail2ban) ReadRules() (string, error)    { return f.rules, nil }
func (f *fakeFail2ban) ApplyRules(rules string) error { f.rules = rules; return f.applyError }

func authenticatedRequest(t *testing.T, s *Server, method, path string, form url.Values) *http.Request {
	t.Helper()
	token, session, err := s.Sessions.Create("admin", false)
	if err != nil {
		t.Fatal(err)
	}
	if form == nil {
		form = url.Values{}
	}
	form.Set("_csrf", session.CSRF)
	r := httptest.NewRequest(method, s.Cfg.BasePath+path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set(sites.ProxyTokenHeader, s.Cfg.ProxyToken)
	r.AddCookie(&http.Cookie{Name: "np_session", Value: token})
	return r
}

func TestNewSecurityRoutesRequireSessionAndCSRF(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	s.Fail2ban = &fakeFail2ban{}
	for _, path := range []string{"/security/fail2ban/install", "/security/fail2ban/service", "/security/fail2ban/rules", "/settings/server-alias"} {
		r := httptest.NewRequest(http.MethodPost, s.Cfg.BasePath+path, nil)
		r.Header.Set(sites.ProxyTokenHeader, s.Cfg.ProxyToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther || !strings.HasSuffix(w.Header().Get("Location"), "/login") {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
		r = authenticatedRequest(t, s, http.MethodPost, path, url.Values{"action": {"stop"}})
		r.Body = http.NoBody
		r.ContentLength = 0
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("missing CSRF %s: %d", path, w.Code)
		}
	}
}

func TestFail2banPageAndInvalidRulesPreserveInput(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)
	f := &fakeFail2ban{state: fail2ban.Snapshot{Installed: true, Active: true, Enabled: true, CurrentCount: 1, Jails: []string{"sshd"}, Records: []fail2ban.BanRecord{
		{IP: "192.0.2.1", Jail: "sshd", Current: true, State: "封禁中", BannedAt: now},
		{IP: "2001:db8::1", Jail: "sshd", State: "已解禁", BannedAt: now, UnbannedAt: now.Add(time.Hour)},
	}}, rules: fail2ban.DefaultRules, applyError: errors.New("invalid rule")}
	s.Fail2ban = f
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodGet, "/security", nil))
	for _, want := range []string{"安装状态", "value=\"start\"", "value=\"stop\"", "value=\"restart\"", "value=\"disable\"", "规则内容", "192.0.2.1", "2001:db8::1", "2026-11-02 09:00:00", "2026-11-02 10:00:00", "尚未解禁"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("page missing %q: %s", want, w.Body.String())
		}
	}
	input := "[sshd]\nfilter = <bad>\n"
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodPost, "/security/fail2ban/rules", url.Values{"rules": {input}}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid rule") || !strings.Contains(w.Body.String(), "filter = &lt;bad&gt;") {
		t.Fatalf("failed editor input lost: %d %s", w.Code, w.Body.String())
	}
	f.state.Installed = false
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodGet, "/security", nil))
	if !strings.Contains(w.Body.String(), "/security/fail2ban/install") || strings.Contains(w.Body.String(), "id=\"fail2ban-rules\"") {
		t.Fatalf("bad uninstalled page: %s", w.Body.String())
	}
}

func TestFail2banInstallIsAsyncAndDeduplicated(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.Fail2ban = &fakeFail2ban{install: func() error { calls.Add(1); close(started); <-release; return nil }}
	post := func() serviceOperationResponse {
		r := authenticatedRequest(t, s, http.MethodPost, "/security/fail2ban/install", nil)
		r.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusAccepted {
			t.Fatalf("install response: %d %s", w.Code, w.Body.String())
		}
		var response serviceOperationResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := post()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("install not started")
	}
	second := post()
	if second.ID != first.ID || calls.Load() != 1 {
		t.Fatal("duplicate install was not coalesced")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		op, _ := s.serviceOperationSnapshot(first.ID)
		if op.Done {
			if !op.Success {
				t.Fatalf("failed operation: %#v", op)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("install operation did not complete")
}
