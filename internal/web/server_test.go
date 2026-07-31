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
	"github.com/kinmeic/NaivePanel/internal/geo"
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
		"Config": `{
  "control": {"enabled": true, "socket": "/run/bypasscore/control.sock", "mode": "0660"},
  "inbounds": [{"tag": "caddy-in", "type": "socks", "listen": "127.0.0.1", "network": "tcp", "port": 1080}],
  "outbounds": [{"tag": "direct", "mode": "freedom"}],
  "routing": {"domainStrategy": "IpIfNonMatch", "finalOutboundTag": "direct", "rules": []},
  "dns": {"queryStrategy": "UseIP", "servers": []}
}`,
	})
	bypassHTML := recorder.Body.String()
	if !strings.Contains(bypassHTML, `id="json-format-btn"`) ||
		!strings.Contains(bypassHTML, `id="bypass-item-dialog"`) ||
		!strings.Contains(bypassHTML, `<select id="bypass-routing-domain-strategy">`) ||
		!strings.Contains(bypassHTML, `<select id="bypass-dns-query-strategy">`) ||
		!strings.Contains(bypassHTML, `<option value="UseSystem">UseSystem</option>`) ||
		!strings.Contains(bypassHTML, `data-bypass-add="inbounds"`) ||
		!strings.Contains(bypassHTML, `data-bypass-add="outbounds"`) ||
		!strings.Contains(bypassHTML, `data-bypass-add="routing.rules"`) ||
		!strings.Contains(bypassHTML, `data-bypass-add="dns.servers"`) ||
		!strings.Contains(bypassHTML, `/static/bypass-config.js`) ||
		!strings.Contains(bypassHTML, "热重载流程") {
		t.Fatalf("unexpected BypassCore editor:\n%s", bypassHTML)
	}
	lastTab := -1
	for _, tab := range []string{"control", "inbounds", "outbounds", "routing", "dns", "raw"} {
		at := strings.Index(bypassHTML, `data-bypass-tab="`+tab+`"`)
		if at < 0 || at < lastTab {
			t.Fatalf("BypassCore config tabs are missing or out of order (%s):\n%s", tab, bypassHTML)
		}
		lastTab = at
	}
	titleAt := strings.Index(bypassHTML, "<h1>BypassCore 配置编辑</h1>")
	backAt := strings.Index(bypassHTML, `aria-label="返回 BypassCore"`)
	if backAt < 0 || titleAt < 0 || backAt > titleAt {
		t.Fatalf("return button must be left of the BypassCore editor title:\n%s", bypassHTML)
	}
	rawAt := strings.Index(bypassHTML, `id="bypass-panel-raw"`)
	if rawAt < 0 {
		t.Fatalf("Raw tab panel is missing:\n%s", bypassHTML)
	}
	formatAt := strings.Index(bypassHTML[rawAt:], `id="json-format-btn"`)
	applyAt := strings.Index(bypassHTML[rawAt:], "校验并生效")
	if formatAt < 0 || applyAt < 0 || formatAt > applyAt {
		t.Fatalf("Raw tab controls are missing or out of order:\n%s", bypassHTML)
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "caddy_config", "Caddy 配置", caddyConfigPageData{
		Config: "{\n\trespond ok\n}\n",
	})
	caddyConfigHTML := recorder.Body.String()
	for _, expected := range []string{
		`id="caddy-base-block"`, `id="caddy-sites-rows"`, `id="caddy-site-dialog"`,
		`id="caddy-site-add"`, `id="caddy-panel-raw"`, `/static/caddy-config.js`,
	} {
		if !strings.Contains(caddyConfigHTML, expected) {
			t.Fatalf("Caddy structured editor is missing %s:\n%s", expected, caddyConfigHTML)
		}
	}
	lastTab = -1
	for _, tab := range []string{"base", "sites", "raw"} {
		at := strings.Index(caddyConfigHTML, `data-caddy-tab="`+tab+`"`)
		if at < 0 || at < lastTab {
			t.Fatalf("Caddy config tabs are missing or out of order (%s):\n%s", tab, caddyConfigHTML)
		}
		lastTab = at
	}
	caddyTitleAt := strings.Index(caddyConfigHTML, "<h1>Caddy 配置编辑</h1>")
	caddyBackAt := strings.Index(caddyConfigHTML, `aria-label="返回 Caddy"`)
	if caddyBackAt < 0 || caddyTitleAt < 0 || caddyBackAt > caddyTitleAt {
		t.Fatalf("return button must be left of the Caddy editor title:\n%s", caddyConfigHTML)
	}
	caddyRawAt := strings.Index(caddyConfigHTML, `id="caddy-panel-raw"`)
	lastControl := -1
	for _, control := range []string{`value="format"`, `value="validate"`, `value="save"`} {
		at := strings.Index(caddyConfigHTML[caddyRawAt:], control)
		if at < 0 || at < lastControl {
			t.Fatalf("Caddy Raw controls are missing or out of order (%s):\n%s", control, caddyConfigHTML)
		}
		lastControl = at
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "caddy", "Caddy", caddyPageData{
		Active: true, Enabled: true, ForwardProxyInstalled: true,
		LogLines: 200, LogContent: "caddy-log-line",
	})
	caddyHTML := recorder.Body.String()
	if strings.Contains(caddyHTML, "配置查看") || strings.Contains(caddyHTML, ">站点<") ||
		!strings.Contains(caddyHTML, `/manage-test/caddy/config`) ||
		!strings.Contains(caddyHTML, `class="service-status-line"`) ||
		!strings.Contains(caddyHTML, `data-async-restart data-service-label="Caddy"`) ||
		!strings.Contains(caddyHTML, `id="caddy-restart-progress"`) ||
		!strings.Contains(caddyHTML, "Forwardproxy 插件：") ||
		!strings.Contains(caddyHTML, `<span class="ok">已安装</span>`) ||
		!strings.Contains(caddyHTML, "Caddy 日志") ||
		!strings.Contains(caddyHTML, "caddy-log-line") ||
		strings.Contains(caddyHTML, `/logs?service=caddy`) {
		t.Fatalf("unexpected Caddy service page:\n%s", caddyHTML)
	}
	autostartAt := strings.Index(caddyHTML, "开机自启：")
	forwardProxyAt := strings.Index(caddyHTML, "Forwardproxy 插件：")
	if autostartAt < 0 || forwardProxyAt < autostartAt {
		t.Fatalf("Forwardproxy status must follow autostart status:\n%s", caddyHTML)
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
		"Geo": config.Geo{
			Mirror: "https://mirror.example.com",
		},
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
		">更新设置</h3>", `/manage-test/settings/update`,
		`value="https://mirror.example.com"`,
		"此通用镜像设置用于 NaivePanel、BypassCore 和 Geo 数据",
	} {
		if !strings.Contains(settingsHTML, want) {
			t.Fatalf("application management is missing %q:\n%s", want, settingsHTML)
		}
	}
	for _, removed := range []string{
		"SOCKS5 端口", "修改密码", "两步验证", "面板寄宿站点",
		"settings/hostsite", "每周自动更新", `name="auto"`,
	} {
		if strings.Contains(settingsHTML, removed) {
			t.Fatalf("application management still contains %q:\n%s", removed, settingsHTML)
		}
	}
	if strings.Contains(settingsHTML, "可在 Geo 数据页配置镜像源") {
		t.Fatalf("application management still points mirror settings to the Geo page:\n%s", settingsHTML)
	}

	recorder = httptest.NewRecorder()
	server.render(recorder, request, "geo", "Geo 数据文件", map[string]any{
		"Dir": "/etc/bypasscore",
		"Comparison": []geo.Comparison{{
			Name: "geoip.dat", LocalExists: true, LocalSize: 100,
			RemoteSize: 120, RemoteSizeKnown: true,
			Status: "大小不同", StatusClass: "warn",
		}},
	})
	geoHTML := recorder.Body.String()
	if !strings.Contains(geoHTML, `/manage-test/geo/check`) ||
		!strings.Contains(geoHTML, `检查更新`) ||
		!strings.Contains(geoHTML, `/manage-test/geo/update`) ||
		!strings.Contains(geoHTML, `本地修改时间`) ||
		!strings.Contains(geoHTML, `远端修改时间`) ||
		!strings.Contains(geoHTML, `大小不同`) ||
		strings.Contains(geoHTML, "更新设置") ||
		strings.Contains(geoHTML, `/manage-test/geo/settings`) {
		t.Fatalf("Geo page should show metadata checks and update controls:\n%s", geoHTML)
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
		"192.0.2.10", "服务状态", `<small class="brand-version">test</small>`,
	} {
		if !strings.Contains(dashboardHTML, want) {
			t.Fatalf("overview is missing %q:\n%s", want, dashboardHTML)
		}
	}
	geoMenuAt := strings.Index(dashboardHTML, `href="/manage-test/geo"`)
	cronMenuAt := strings.Index(dashboardHTML, `href="/manage-test/cron"`)
	logMenuAt := strings.Index(dashboardHTML, `href="/manage-test/logs"`)
	if geoMenuAt < 0 || cronMenuAt < geoMenuAt || logMenuAt < cronMenuAt ||
		!strings.Contains(dashboardHTML, "<span>操作日志</span>") ||
		strings.Contains(dashboardHTML, "<span>日志</span>") {
		t.Fatalf("sidebar navigation order or operation-log label is incorrect:\n%s", dashboardHTML)
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
	server.render(recorder, request, "logs", "操作日志", logsPageData{
		Lines: 200,
		Entries: []operationLogEntry{{
			Time: "2026-07-31 10:00:00", User: "admin", Method: "POST",
			Path: "/caddy/config", Status: "303", StatusClass: "ok", Duration: "12 ms",
		}},
	})
	logsHTML := recorder.Body.String()
	if !strings.Contains(logsHTML, "<h1>操作日志</h1>") ||
		!strings.Contains(logsHTML, "面板内已认证操作的审计记录") ||
		!strings.Contains(logsHTML, "<th>时间</th>") ||
		!strings.Contains(logsHTML, "<th>用户</th>") ||
		!strings.Contains(logsHTML, "<th>方法</th>") ||
		!strings.Contains(logsHTML, "<th>路径</th>") ||
		!strings.Contains(logsHTML, "<th>状态</th>") ||
		!strings.Contains(logsHTML, "<th>耗时</th>") ||
		!strings.Contains(logsHTML, "/caddy/config") ||
		strings.Contains(logsHTML, `<pre class="preview logs">`) ||
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

func TestBypassConfigDialogsUseValidatedSelectOptions(t *testing.T) {
	script, err := uiFS.ReadFile("ui/static/bypass-config.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		`{ value: "redirect", label: "redirect（透明代理 TCP）" }`,
		`{ value: "tproxy", label: "tproxy（透明代理 TCP/UDP）" }`,
		`{ value: "socks", label: "socks（SOCKS5）" }`,
		`{ value: "dns", label: "dns" }`,
		`{ value: "dot", label: "dot（DNS over TLS）" }`,
		`{ value: "doh", label: "doh（DNS over HTTPS）" }`,
		`{ value: "tcp,udp", label: "tcp,udp" }`,
		`{ value: "freedom", label: "freedom（直连）" }`,
		`{ value: "blackhole", label: "blackhole（阻断）" }`,
		`{ value: "proxy", label: "proxy（上游代理）" }`,
		`{ value: "wireguard", label: "wireguard" }`,
		`{ value: "https", label: "https（HTTP CONNECT over TLS）" }`,
		`{ path: "bind.interface", label: "bind.interface", placeholder: "例如 eth0", modes: ["freedom"] }`,
		`path: "upstream.settings.udpMaxPacketBytes"`,
		`path: "upstream.settings.enableHTTP2"`,
		`path: "wireguard.secretKey"`,
		`path: "wireguard.peers"`,
		`{ path: "inboundTag", label: "inboundTag", type: "multi-select", optionsFrom: "inbounds" }`,
		`{ path: "network", label: "network", type: "multi-select", options: "routingNetworks" }`,
		`{ path: "outboundTag", label: "outboundTag", type: "select", optionsFrom: "outbounds" }`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("BypassCore dialog select definition missing %q", required)
		}
	}
	if strings.Count(source, `optionsFrom: "outbounds"`) != 2 {
		t.Fatalf("routing and DNS outboundTag must both use configured outbound tags")
	}
	if !strings.Contains(source, `var item = index === null ? {} : getPath(state, kind)[index];`) {
		t.Fatal("add and edit dialogs must share the same field-definition path")
	}
	for _, behavior := range []string{
		`input.addEventListener("change", updateOutboundFields);`,
		`input.parentElement.classList.toggle("hidden", !outboundFieldActive(field));`,
		`if (editing.kind === "outbounds") removeInactiveOutboundRoots(item);`,
		`if (!active) return;`,
		`field.jsonKind === "array" && !Array.isArray(value)`,
	} {
		if !strings.Contains(source, behavior) {
			t.Fatalf("dynamic outbound dialog behavior missing %q", behavior)
		}
	}
}

func TestCaddyConfigEditorUsesConservativeTopLevelParsing(t *testing.T) {
	script, err := uiFS.ReadFile("ui/static/caddy-config.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		`function parseCaddyfile(text)`,
		`if (text.length > 2 * 1024 * 1024)`,
		`检测到 heredoc（<<）`,
		`if (/^\([^\r\n]+\)$/.test(header)) return "other";`,
		`parts.splice(entry.partIndex, 1);`,
		`var candidate = parseCaddyfile(domain + " " + block);`,
		`if (name !== "raw" && activeTab === "raw" && rawDirty && !adoptRaw()) return false;`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Caddy structured editor safety behavior missing %q", required)
		}
	}
	style, err := uiFS.ReadFile("ui/static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(style), `#caddy-site-dialog { width: min(980px, calc(100vw - 32px)); }`) {
		t.Fatal("Caddy site dialog must expand to 980px while remaining viewport responsive")
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

func TestStatusRecorderAllowsResponseFlush(t *testing.T) {
	underlying := httptest.NewRecorder()
	recorder := &statusRecorder{ResponseWriter: underlying}
	recorder.WriteHeader(http.StatusSeeOther)
	if err := http.NewResponseController(recorder).Flush(); err != nil {
		t.Fatalf("flush through status recorder: %v", err)
	}
	if !underlying.Flushed {
		t.Fatal("underlying response was not flushed")
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

func TestParseOperationLogLineForTable(t *testing.T) {
	line := `2026-07-31T10:15:30+0800 host naivepanel[123]: ` + operationLogMarker +
		` user="admin" method="POST" path="/caddy/config" status=303 duration_ms=17`
	got := parseOperationLogLine(line)
	want := operationLogEntry{
		Time: "2026-07-31 10:15:30", User: "admin", Method: "POST",
		Path: "/caddy/config", Status: "303", StatusClass: "ok", Duration: "17 ms",
	}
	if got != want {
		t.Fatalf("parseOperationLogLine()=%#v, want %#v", got, want)
	}

	fallback := parseOperationLogLine("old " + operationLogMarker + " status=403")
	if fallback.Time != "—" || fallback.Path != "status=403" || fallback.Status != "—" {
		t.Fatalf("legacy operation log fallback lost information: %#v", fallback)
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

func TestUpdateSettingsSaveReturnsToApplicationManagement(t *testing.T) {
	server, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"mirror": {"https://mirror.example.com"},
	}
	request := httptest.NewRequest(http.MethodPost, "/manage-test/settings/update",
		strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	server.handleUpdateSettings(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/manage-test/settings" {
		t.Fatalf("code=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
	geoSettings := server.Cfg.GeoSnapshot()
	if geoSettings.Mirror != "https://mirror.example.com" {
		t.Fatalf("update settings were not saved: %#v", geoSettings)
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
