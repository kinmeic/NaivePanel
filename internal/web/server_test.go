package web

import (
	"net/http/httptest"
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
	for _, page := range []string{"dashboard", "cron", "cron_form", "site_form", "sites_list"} {
		if server.pages[page] == nil {
			t.Errorf("template %q was not parsed", page)
		}
	}
}
