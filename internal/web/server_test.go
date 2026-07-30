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
	"time"

	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/sites"
	"github.com/kinmeic/NaivePanel/internal/systemstats"
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
		strings.Contains(bypassHTML, "结构化编辑") ||
		!strings.Contains(bypassHTML, "支持热重载的修改无需手动重启") {
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
	server.render(recorder, request, "caddy", "Caddy", caddyPageData{
		Active: true, Enabled: true, LogLines: 200, LogContent: "caddy-log-line",
	})
	caddyHTML := recorder.Body.String()
	if strings.Contains(caddyHTML, "配置查看") || strings.Contains(caddyHTML, ">站点<") ||
		!strings.Contains(caddyHTML, `/manage-test/caddy/config`) ||
		!strings.Contains(caddyHTML, `class="service-status-line"`) ||
		!strings.Contains(caddyHTML, `data-async-restart data-service-label="Caddy"`) ||
		!strings.Contains(caddyHTML, `id="caddy-restart-progress"`) ||
		!strings.Contains(caddyHTML, "Caddy 日志") ||
		!strings.Contains(caddyHTML, "caddy-log-line") ||
		strings.Contains(caddyHTML, `/logs?service=caddy`) {
		t.Fatalf("unexpected Caddy service page:\n%s", caddyHTML)
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "bypass", "BypassCore", map[string]any{
		"Installed": true, "Active": true, "Enabled": true, "SocksPort": 1080,
		"StatusInfo": "test", "LogLines": 200, "LogContent": "bypass-log-line",
	})
	bypassPageHTML := recorder.Body.String()
	if strings.Contains(bypassPageHTML, "安装状态") || strings.Contains(bypassPageHTML, "<h3>配置</h3>") {
		t.Fatalf("legacy BypassCore cards remain:\n%s", bypassPageHTML)
	}
	if !strings.Contains(bypassPageHTML, `class="service-status-line"`) {
		t.Fatalf("BypassCore service statuses are not on one responsive line:\n%s", bypassPageHTML)
	}
	if !strings.Contains(bypassPageHTML, `data-async-restart data-service-label="BypassCore"`) ||
		!strings.Contains(bypassPageHTML, `id="bypass-restart-progress"`) {
		t.Fatalf("BypassCore restart progress controls are missing:\n%s", bypassPageHTML)
	}
	if !strings.Contains(bypassPageHTML, `/manage-test/bypasscore/config`) ||
		!strings.Contains(bypassPageHTML, "BypassCore 日志") ||
		!strings.Contains(bypassPageHTML, "bypass-log-line") ||
		strings.Contains(bypassPageHTML, `/logs?service=bypasscore`) {
		t.Fatalf("BypassCore logs were not moved onto the service page:\n%s", bypassPageHTML)
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "settings", "应用管理", map[string]any{
		"Version": "test",
		"Bypass": map[string]any{
			"Installed": true, "Version": "v1.2.3", "BinPath": "/usr/local/bin/bypasscore",
			"ConfigPath": "/etc/bypasscore/config.json",
		},
	})
	settingsHTML := recorder.Body.String()
	panelInfoAt := strings.Index(settingsHTML, "面板信息")
	bypassInfoAt := strings.Index(settingsHTML, "BypassCore 信息")
	if panelInfoAt < 0 || bypassInfoAt < panelInfoAt {
		t.Fatalf("BypassCore information card is not beside panel information:\n%s", settingsHTML)
	}
	for _, want := range []string{
		"<h1>应用管理</h1>", "版本：<code>v1.2.3</code>",
		`/manage-test/bypasscore/update/check`, "检查更新", "立即更新",
	} {
		if !strings.Contains(settingsHTML, want) {
			t.Fatalf("application management is missing %q:\n%s", want, settingsHTML)
		}
	}
	for _, removed := range []string{"SOCKS5 端口", "修改密码", "两步验证", "面板寄宿站点", "settings/hostsite"} {
		if strings.Contains(settingsHTML, removed) {
			t.Fatalf("application management still contains %q:\n%s", removed, settingsHTML)
		}
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "security", "账号安全", map[string]any{
		"TOTPEnabled": false,
	})
	securityHTML := recorder.Body.String()
	for _, want := range []string{
		"<h1>账号安全</h1>", "修改密码", "两步验证",
		`/manage-test/security/password`, `/manage-test/security/totp/setup`,
	} {
		if !strings.Contains(securityHTML, want) {
			t.Fatalf("account security is missing %q:\n%s", want, securityHTML)
		}
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
	server.render(recorder, request, "dashboard", "概览", map[string]any{
		"BypassInstall": true,
		"BypassActive":  true,
		"BypassVersion": "bypasscore v9.9.9 (commit=should-not-render)",
		"PanelActive":   true,
		"CaddyActive":   true,
		"Host": systemstats.HostInfo{
			Hostname: "example-host", Distribution: "Example Linux",
			KernelVersion: "6.1.0", SystemType: "linux / amd64",
			Addresses: []string{"192.0.2.10"}, BootTime: time.Unix(1_700_000_000, 0),
		},
		"Uptime": "1 天 2 小时 3 分钟",
	})
	dashboardHTML := recorder.Body.String()
	for _, want := range []string{
		"<h1>概览</h1>", ">概览</span>", "CPU", "内存", "负载", "磁盘",
		`id="traffic-chart"`, "系统信息", "example-host", "Example Linux",
		"192.0.2.10", "服务状态",
	} {
		if !strings.Contains(dashboardHTML, want) {
			t.Fatalf("overview is missing %q:\n%s", want, dashboardHTML)
		}
	}
	if strings.Contains(dashboardHTML, ">站点<") ||
		strings.Contains(dashboardHTML, "/caddy/sites") {
		t.Fatalf("dashboard still contains the sites card:\n%s", dashboardHTML)
	}
	if strings.Count(dashboardHTML, "Geo 数据") != 1 ||
		strings.Contains(dashboardHTML, "MFA：") ||
		strings.Contains(dashboardHTML, "内部监听：") {
		t.Fatalf("overview still contains a removed dashboard card:\n%s", dashboardHTML)
	}
	if strings.Contains(dashboardHTML, "v9.9.9") || strings.Contains(dashboardHTML, "should-not-render") {
		t.Fatalf("dashboard still renders the BypassCore version:\n%s", dashboardHTML)
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "logs", "日志", map[string]any{
		"Service": "operations", "Lines": 200, "Content": "（暂无操作日志）",
	})
	logsHTML := recorder.Body.String()
	if !strings.Contains(logsHTML, "<h1>日志</h1>") ||
		!strings.Contains(logsHTML, "面板内已认证操作的审计记录") ||
		strings.Contains(logsHTML, ">Caddy</a>") ||
		strings.Contains(logsHTML, ">BypassCore</a>") {
		t.Fatalf("logs page should contain operation logs only:\n%s", logsHTML)
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
	for _, page := range []string{"dashboard", "caddy", "caddy_config", "bypass", "bypass_config", "cron", "cron_form", "logs", "settings", "security"} {
		if server.pages[page] == nil {
			t.Errorf("template %q was not parsed", page)
		}
	}
}

func TestFormatUptime(t *testing.T) {
	for _, test := range []struct {
		value time.Duration
		want  string
	}{
		{30 * time.Second, "不到 1 分钟"},
		{45 * time.Minute, "45 分钟"},
		{2*time.Hour + 5*time.Minute, "2 小时 5 分钟"},
		{3*24*time.Hour + 4*time.Hour + 6*time.Minute, "3 天 4 小时 6 分钟"},
	} {
		if got := formatUptime(test.value); got != test.want {
			t.Errorf("formatUptime(%v)=%q, want %q", test.value, got, test.want)
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

func TestLegacyServiceLogLinksRedirectToServicePages(t *testing.T) {
	server, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		service string
		want    string
	}{
		{"caddy", "/manage-test/caddy?lines=500"},
		{"bypasscore", "/manage-test/bypasscore?lines=500"},
	} {
		request := httptest.NewRequest(http.MethodGet,
			"/manage-test/logs?service="+test.service+"&lines=500", nil)
		recorder := httptest.NewRecorder()
		server.handleLogs(recorder, request)
		if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != test.want {
			t.Fatalf("%s log redirect: code=%d location=%q, want %q",
				test.service, recorder.Code, recorder.Header().Get("Location"), test.want)
		}
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
		"/manage-test/security/password?token=must-not-appear",
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
		`path="/security/password"`, "status=204",
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
