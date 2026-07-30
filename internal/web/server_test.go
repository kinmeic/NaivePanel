package web

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/sites"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `domain: panel.example.com
base_path: /manage-test
admin_user: admin
admin_pass_hash: "$2a$10$123456789012345678901u7vD8w6jZ73QGZsOlRVl8Qz3r9mK"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestUpdatedPagesRenderExpectedControls(t *testing.T) {
	server, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/manage-test/", nil)

	recorder := httptest.NewRecorder()
	server.render(recorder, request, "bypass_config", "BypassCore 配置", map[string]any{
		"Config": `{"log":{"level":"info"}}`,
	})
	bypassHTML := recorder.Body.String()
	if !strings.Contains(bypassHTML, `id="json-format-btn"`) ||
		strings.Contains(bypassHTML, "结构化编辑") {
		t.Fatalf("unexpected BypassCore editor:\n%s", bypassHTML)
	}
	formatAt := strings.Index(bypassHTML, `id="json-format-btn"`)
	applyAt := strings.Index(bypassHTML, "校验并生效")
	if formatAt < 0 || applyAt < 0 || formatAt > applyAt {
		t.Fatalf("JSON formatter must be immediately available before apply:\n%s", bypassHTML)
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "caddy_config", "Caddy 配置", caddyConfigPageData{
		Config: "{\n\trespond ok\n}\n",
	})
	caddyConfigHTML := recorder.Body.String()
	last := -1
	for _, control := range []string{`value="format"`, `value="validate"`, `value="save"`, `class="btn" href="/manage-test/caddy"`} {
		at := strings.Index(caddyConfigHTML, control)
		if at < 0 || at < last {
			t.Fatalf("Caddy editor controls are missing or out of order (%s):\n%s", control, caddyConfigHTML)
		}
		last = at
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "caddy", "Caddy", caddyPageData{Active: true, Enabled: true})
	caddyHTML := recorder.Body.String()
	if strings.Contains(caddyHTML, "配置查看") || strings.Contains(caddyHTML, ">站点<") ||
		!strings.Contains(caddyHTML, `/manage-test/caddy/config`) ||
		!strings.Contains(caddyHTML, `class="service-status-line"`) {
		t.Fatalf("unexpected Caddy service page:\n%s", caddyHTML)
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "bypass", "BypassCore", map[string]any{
		"Installed": true, "Active": true, "Enabled": true, "SocksPort": 1080,
		"StatusInfo": "test",
	})
	bypassPageHTML := recorder.Body.String()
	if strings.Contains(bypassPageHTML, "安装状态") || strings.Contains(bypassPageHTML, "<h3>配置</h3>") {
		t.Fatalf("legacy BypassCore cards remain:\n%s", bypassPageHTML)
	}
	if !strings.Contains(bypassPageHTML, `class="service-status-line"`) {
		t.Fatalf("BypassCore service statuses are not on one responsive line:\n%s", bypassPageHTML)
	}
	editAt := strings.Index(bypassPageHTML, `/manage-test/bypasscore/config`)
	logAt := strings.Index(bypassPageHTML, `/manage-test/logs?service=bypasscore`)
	if editAt < 0 || logAt < editAt {
		t.Fatalf("BypassCore log control must follow config control:\n%s", bypassPageHTML)
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "settings", "设置", map[string]any{
		"Version": "test",
		"Bypass": map[string]any{
			"Installed": false, "BinPath": "/usr/local/bin/bypasscore",
			"ConfigPath": "/etc/bypasscore/config.json", "SocksPort": 1080,
		},
	})
	settingsHTML := recorder.Body.String()
	panelInfoAt := strings.Index(settingsHTML, "面板信息")
	bypassInfoAt := strings.Index(settingsHTML, "BypassCore 信息")
	passwordAt := strings.Index(settingsHTML, "修改密码")
	if panelInfoAt < 0 || bypassInfoAt < panelInfoAt || passwordAt < bypassInfoAt {
		t.Fatalf("BypassCore information card is not beside panel information:\n%s", settingsHTML)
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "cron", "计划任务", map[string]any{
		"Installed": true, "Active": true, "Enabled": true,
		"CronFile": "/etc/cron.d/naivepanel", "LogFile": "/var/log/naivepanel-cron.log",
	})
	cronHTML := recorder.Body.String()
	if !strings.Contains(cronHTML, `class="service-status-line"`) {
		t.Fatalf("Cron service statuses are not on one responsive line:\n%s", cronHTML)
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "dashboard", "仪表盘", map[string]any{})
	if strings.Contains(recorder.Body.String(), ">站点<") ||
		strings.Contains(recorder.Body.String(), "/caddy/sites") {
		t.Fatalf("dashboard still contains the sites card:\n%s", recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "logs", "日志", map[string]any{
		"Service": "operations", "Lines": 200, "Content": "（暂无操作日志）",
	})
	logsHTML := recorder.Body.String()
	operationsAt := strings.Index(logsHTML, "操作日志")
	caddyAt := strings.Index(logsHTML, ">Caddy</a>")
	if !strings.Contains(logsHTML, "<h1>日志</h1>") ||
		operationsAt < 0 || caddyAt < operationsAt {
		t.Fatalf("operation log tab is missing or misplaced:\n%s", logsHTML)
	}

	recorder = httptest.NewRecorder()
	site := sites.Site{
		Domain: "example.com", RouteExplicit: true,
		Web: sites.Web{Type: sites.WebReverseProxy, ProxyTo: "127.0.0.1:3000"},
	}
	server.render(recorder, request, "site_form", "编辑站点 example.com", siteForm{
		Site: site, Preview: "example.com {\n\treverse_proxy 127.0.0.1:3000\n}\n",
	})
	siteHTML := recorder.Body.String()
	if !strings.Contains(siteHTML, `id="caddy-handlers-block" class="caddy-directives-block"`) ||
		!strings.Contains(siteHTML, `caddy-route-brace hidden`) {
		t.Fatalf("route-off editor state is not represented correctly:\n%s", siteHTML)
	}

	recorder = httptest.NewRecorder()
	server.renderFrag(recorder, request, "sites_list", map[string]any{
		"Rows": []siteRow{{Site: site}},
	})
	listHTML := recorder.Body.String()
	if !strings.Contains(listHTML, `class="site-action-buttons"`) {
		t.Fatalf("site action alignment wrapper missing:\n%s", listHTML)
	}
}

func TestNewParsesAllTemplates(t *testing.T) {
	server, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"dashboard", "caddy", "caddy_config", "bypass", "bypass_config", "cron", "cron_form", "logs"} {
		if server.pages[page] == nil {
			t.Errorf("template %q was not parsed", page)
		}
	}
}

func TestFilterOperationLogs(t *testing.T) {
	journal := "startup\n" +
		"one " + operationLogMarker + " status=200\n" +
		"noise\n" +
		"two " + operationLogMarker + " status=303\n" +
		"three " + operationLogMarker + " status=403\n"
	got := filterOperationLogs(journal, 2)
	if strings.Contains(got, "one ") || strings.Contains(got, "noise") ||
		!strings.Contains(got, "two ") || !strings.Contains(got, "three ") {
		t.Fatalf("unexpected filtered operation log:\n%s", got)
	}
	if got := filterOperationLogs("ordinary journal line\n", 100); got != "（暂无操作日志）" {
		t.Fatalf("unexpected empty-state text: %q", got)
	}
}

func TestProtectedPostWritesSafeOperationLog(t *testing.T) {
	server, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := server.Sessions.Create("admin", false)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"_csrf":    {session.CSRF},
		"password": {"must-not-appear"},
	}
	request := httptest.NewRequest(http.MethodPost,
		"/manage-test/settings/password?token=must-not-appear",
		strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "np_session", Value: token})
	recorder := httptest.NewRecorder()

	var output bytes.Buffer
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	}()

	server.protect(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})(recorder, request)

	got := output.String()
	for _, want := range []string{
		operationLogMarker, `user="admin"`, `method="POST"`,
		`path="/settings/password"`, "status=204",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("operation log missing %q: %s", want, got)
		}
	}
	for _, secret := range []string{"must-not-appear", session.CSRF, "/manage-test"} {
		if strings.Contains(got, secret) {
			t.Fatalf("operation log leaked %q: %s", secret, got)
		}
	}
}
