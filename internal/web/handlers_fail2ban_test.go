package web

import (
	"encoding/json"
	"errors"
	"fmt"
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
	fileSaved  bool
	readError  error
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
func (f *fakeFail2ban) ReadRuleFile() (fail2ban.RuleFile, error) {
	return fail2ban.RuleFile{Content: f.rules, Path: fail2ban.RulesPath, Saved: f.fileSaved}, f.readError
}
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
	for _, path := range []string{"/fail2ban/install", "/fail2ban/service", "/fail2ban/config", "/security/fail2ban/install", "/security/fail2ban/service", "/security/fail2ban/rules", "/settings/server-alias"} {
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
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodGet, "/fail2ban", nil))
	for _, want := range []string{"安装状态", "value=\"start\"", "value=\"stop\"", "value=\"restart\"", "value=\"disable\"", "编辑配置", "192.0.2.1", "2001:db8::1", "2026-11-02 09:00:00", "2026-11-02 10:00:00", "尚未解禁"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("page missing %q: %s", want, w.Body.String())
		}
	}
	if strings.Index(w.Body.String(), "编辑配置</a>") > strings.Index(w.Body.String(), "刷新</a>") {
		t.Fatal("config button must precede refresh")
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodGet, "/fail2ban/config", nil))
	for _, want := range []string{"未保存的示例", "Raw INI", "全局配置", "jails", "当前运行的 SSH 防护", "已启用"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("editor missing %q", want)
		}
	}
	input := "[sshd]\nfilter = <bad>\n"
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodPost, "/fail2ban/config", url.Values{"rules": {input}, "editor_tab": {"raw"}}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid rule") || !strings.Contains(w.Body.String(), "filter = &lt;bad&gt;") {
		t.Fatalf("failed editor input lost: %d %s", w.Code, w.Body.String())
	}
	f.state.Installed = false
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodGet, "/fail2ban", nil))
	if !strings.Contains(w.Body.String(), "/fail2ban/install") || strings.Contains(w.Body.String(), "fail2ban-config-raw") {
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
		r := authenticatedRequest(t, s, http.MethodPost, "/fail2ban/install", nil)
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

func TestFail2banHistoryPaginationAndMenu(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFail2ban{state: fail2ban.Snapshot{Installed: true}}
	for i := 1; i <= 243; i++ {
		f.state.Records = append(f.state.Records, fail2ban.BanRecord{IP: fmt.Sprintf("192.0.2.%d", i), Jail: "sshd", State: "已解禁"})
	}
	s.Fail2ban = f
	for _, tc := range []struct {
		query                    string
		page, first, last, count int
	}{
		{"", 1, 1, 20, 20}, {"?page=2", 2, 21, 40, 20}, {"?page=13", 13, 241, 243, 3},
		{"?page=999999999", 13, 241, 243, 3}, {"?page=-1", 1, 1, 20, 20}, {"?page=garbage", 1, 1, 20, 20},
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodGet, "/fail2ban"+tc.query, nil))
		html := w.Body.String()
		if w.Code != 200 {
			t.Fatalf("response %d: %s", w.Code, html)
		}
		start := strings.Index(html, `<table class="fail2ban-table">`)
		end := strings.Index(html[start:], "</table>") + start
		table := html[start:end]
		if strings.Count(table, "<td><code>") != tc.count || !strings.Contains(table, fmt.Sprintf(">192.0.2.%d<", tc.first)) || !strings.Contains(table, fmt.Sprintf(">192.0.2.%d<", tc.last)) {
			t.Fatalf("incorrect pagination %s: %s", tc.query, table)
		}
		if !strings.Contains(html, fmt.Sprintf("第 %d / 13 页", tc.page)) {
			t.Fatalf("bad page count %s", tc.query)
		}
		warp := strings.Index(html, `href="/manage-test/warp"`)
		fail2ban := strings.Index(html, `href="/manage-test/fail2ban"`)
		terminal := strings.Index(html, `href="/manage-test/terminal"`)
		if warp < 0 || fail2ban <= warp || terminal <= fail2ban {
			t.Fatal("Fail2ban menu must be below WARP")
		}
	}
	f.state.Records = nil
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodGet, "/fail2ban?page=2", nil))
	if !strings.Contains(w.Body.String(), "暂无封禁记录") || !strings.Contains(w.Body.String(), "第 1 / 1 页") {
		t.Fatal("bad empty history")
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodGet, "/security", nil))
	if strings.Contains(w.Body.String(), "fail2ban-progress") || strings.Contains(w.Body.String(), "最近封禁的 IP") {
		t.Fatal("Fail2ban controls remain on security page")
	}
}

func TestFail2banConfigSavedStateAndSuccessfulSave(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFail2ban{state: fail2ban.Snapshot{Installed: true}, fileSaved: true, rules: "[sshd]\nenabled = false\n"}
	s.Fail2ban = f
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodGet, "/fail2ban/config", nil))
	if !strings.Contains(w.Body.String(), "正在编辑已保存的面板配置") || strings.Contains(w.Body.String(), "当前是未保存的示例") {
		t.Fatal("saved file reported as draft")
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodPost, "/fail2ban/config", url.Values{"rules": {"[sshd]\nenabled = true\n"}}))
	if w.Code != 303 || w.Header().Get("Location") != "/manage-test/fail2ban" || !strings.Contains(f.rules, "enabled = true") {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	f.readError = errors.New("permission denied")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodGet, "/fail2ban/config", nil))
	if !strings.Contains(w.Body.String(), "配置文件无法读取，编辑已禁用") || !strings.Contains(w.Body.String(), `class="fail2ban-editor-fields" disabled`) {
		t.Fatal("unreadable config was presented as editable")
	}
}

func TestFail2banSSHDStateDoesNotConfuseUnknownWithDisabled(t *testing.T) {
	for _, tc := range []struct {
		state fail2ban.Snapshot
		want  string
	}{
		{fail2ban.Snapshot{}, "未安装"},
		{fail2ban.Snapshot{Installed: true}, "服务未运行"},
		{fail2ban.Snapshot{Installed: true, Active: true, StatusError: "query failed"}, "无法读取"},
		{fail2ban.Snapshot{Installed: true, Active: true}, "未启用"},
		{fail2ban.Snapshot{Installed: true, Active: true, Jails: []string{"sshd"}}, "已启用"},
	} {
		if got := fail2banSSHDState(tc.state); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
