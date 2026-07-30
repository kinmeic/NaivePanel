package caddymgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/sites"
)

const inlineMain = `# 服务器主配置
{
	email admin@example.com
}

:443, a.example.com {
	route {
		root * /var/www/a
		encode gzip zstd
		file_server
	}
}

:443, b.example.com {
	route {
		reverse_proxy 127.0.0.1:8080
	}
}
`

func testManager(t *testing.T, mainContent string, modelSites ...sites.Site) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Listen:   "127.0.0.1:19001",
		Domain:   "example.com",
		BasePath: "/manage",
		Sites:    modelSites,
	}
	cfg.Caddy.MainFile = filepath.Join(dir, "Caddyfile")
	cfg.Caddy.SitesDir = filepath.Join(dir, "sites")
	cfg.BackupDir = filepath.Join(dir, "backups")
	if err := os.WriteFile(cfg.Caddy.MainFile, []byte(mainContent), 0644); err != nil {
		t.Fatal(err)
	}
	return New(cfg), dir
}

// Inline blocks migrate verbatim into snippets; the head is preserved; a
// second Apply must not prune the migrated snippets.
func TestRenderAllMigratesInlineSites(t *testing.T) {
	m, dir := testManager(t, inlineMain)

	files, err := m.RenderAll()
	if err != nil {
		t.Fatal(err)
	}
	a := files["sites/a.example.com.caddy"]
	if !strings.Contains(a, "root * /var/www/a") {
		t.Fatalf("inline site not migrated verbatim:\n%s", a)
	}
	if _, ok := files["sites/b.example.com.caddy"]; !ok {
		t.Fatal("second inline site missing from render")
	}
	main := files["Caddyfile"]
	for _, want := range []string{"# 服务器主配置", "email admin@example.com", "import " + filepath.Join(dir, "sites", "*.caddy")} {
		if !strings.Contains(main, want) {
			t.Fatalf("rendered main file missing %q:\n%s", want, main)
		}
	}
	if strings.Contains(main, "a.example.com {") {
		t.Fatalf("inline site block left in main file:\n%s", main)
	}

	// Apply to live, then re-render: nothing may be pruned.
	if err := m.applyLive(files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sites", "a.example.com.caddy")); err != nil {
		t.Fatal("migrated snippet not written to live sites dir")
	}
	liveMain, _ := os.ReadFile(filepath.Join(dir, "Caddyfile"))
	if strings.Contains(string(liveMain), "a.example.com {") {
		t.Fatal("live main file still contains inline block")
	}

	files2, err := m.RenderAll()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files2["sites/a.example.com.caddy"]; ok {
		t.Fatal("second render should not re-migrate (block is gone from main)")
	}
	if err := m.applyLive(files2); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"a.example.com", "b.example.com"} {
		if _, err := os.Stat(filepath.Join(dir, "sites", d+".caddy")); err != nil {
			t.Fatalf("migrated snippet %s must survive later applies: %v", d, err)
		}
	}
}

// A model site with the same domain wins over the inline copy.
func TestRenderAllModelWinsOverInline(t *testing.T) {
	st := sites.Site{Domain: "a.example.com"}
	st.Web.Type = sites.WebStatic
	st.Web.Root = "/var/www/from-model"
	m, dir := testManager(t, inlineMain, st)

	files, err := m.RenderAll()
	if err != nil {
		t.Fatal(err)
	}
	a := files["sites/a.example.com.caddy"]
	if !strings.Contains(a, "/var/www/from-model") || strings.Contains(a, "/var/www/a\n") {
		t.Fatalf("model render should win over inline copy:\n%s", a)
	}
	_ = dir
}

func TestRenderAllAddsForwardProxyOrderOnlyWhenNeeded(t *testing.T) {
	ordinary := sites.Site{Domain: "ordinary.example.com", Web: sites.Web{Type: sites.WebStatic, Root: "/srv/ordinary"}}
	manager, _ := testManager(t, "{\n\temail admin@example.com\n}\n", ordinary)
	files, err := manager.RenderAll()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files["Caddyfile"], "order forward_proxy") {
		t.Fatalf("ordinary config gained plugin-only order:\n%s", files["Caddyfile"])
	}

	proxy := sites.Site{
		Domain: "proxy.example.com",
		ForwardProxy: sites.ForwardProxy{
			Enabled: true, Accounts: []sites.Account{{User: "user", Pass: "pass"}},
		},
		Web: sites.Web{Type: sites.WebNone},
	}
	manager, _ = testManager(t, "{\n\temail admin@example.com\n}\n", proxy)
	files, err = manager.RenderAll()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files["Caddyfile"], "order forward_proxy before file_server") {
		t.Fatalf("forward proxy order missing:\n%s", files["Caddyfile"])
	}
}

// DropDomain skips inline migration and prunes the snippet on Apply.
func TestDropDomain(t *testing.T) {
	m, dir := testManager(t, inlineMain)
	m.DropDomain("a.example.com")

	files, err := m.RenderAll()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["sites/a.example.com.caddy"]; ok {
		t.Fatal("dropped domain must not be migrated")
	}
	if err := m.applyLive(files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sites", "a.example.com.caddy")); !os.IsNotExist(err) {
		t.Fatal("dropped domain snippet must not exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "sites", "b.example.com.caddy")); err != nil {
		t.Fatal("other inline site should still migrate")
	}

	// Drop also prunes a pre-existing foreign snippet.
	m2, dir2 := testManager(t, inlineMain)
	os.MkdirAll(filepath.Join(dir2, "sites"), 0755)
	os.WriteFile(filepath.Join(dir2, "sites", "foreign.example.net.caddy"),
		[]byte(":443, foreign.example.net {\n\trespond ok\n}\n"), 0600)
	m2.DropDomain("foreign.example.net")
	files2, err := m2.RenderAll()
	if err != nil {
		t.Fatal(err)
	}
	if err := m2.applyLive(files2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "sites", "foreign.example.net.caddy")); !os.IsNotExist(err) {
		t.Fatal("dropped foreign snippet must be pruned")
	}
}

// Backups land in <Caddyfile dir>/backup/<timestamp>/ with the main file and
// every snippet.
func TestBackupLocation(t *testing.T) {
	m, dir := testManager(t, inlineMain)
	os.MkdirAll(filepath.Join(dir, "sites"), 0755)
	os.WriteFile(filepath.Join(dir, "sites", "x.example.com.caddy"),
		[]byte(":443, x.example.com {\n\trespond ok\n}\n"), 0600)

	dst, err := m.backup()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dst) != filepath.Join(dir, "backup") {
		t.Fatalf("backup must live under <Caddyfile dir>/backup/, got %s", dst)
	}
	if _, err := os.Stat(filepath.Join(dst, "Caddyfile")); err != nil {
		t.Fatal("main Caddyfile not backed up")
	}
	if _, err := os.Stat(filepath.Join(dst, "sites", "x.example.com.caddy")); err != nil {
		t.Fatal("snippet not backed up")
	}
}

// Foreign snippets are never pruned by unrelated applies.
func TestForeignSnippetSurvives(t *testing.T) {
	m, dir := testManager(t, inlineMain)
	os.MkdirAll(filepath.Join(dir, "sites"), 0755)
	os.WriteFile(filepath.Join(dir, "sites", "foreign.example.net.caddy"),
		[]byte(":443, foreign.example.net {\n\trespond ok\n}\n"), 0600)
	files, err := m.RenderAll()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.applyLive(files); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "sites", "foreign.example.net.caddy"))
	if err != nil || !strings.Contains(string(data), "respond ok") {
		t.Fatal("foreign snippet must survive untouched")
	}
}

func TestLivePreviewOnlyReturnsMainFile(t *testing.T) {
	const main = "{\n\temail admin@example.com\n}\n\nimport /etc/caddy/sites/*.caddy\n"
	m, dir := testManager(t, main)
	if err := os.MkdirAll(filepath.Join(dir, "sites"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sites", "private.example.caddy"),
		[]byte("private.example {\n\trespond ok\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := m.LivePreview()
	if got != main {
		t.Fatalf("preview must be exact main file content:\n%s", got)
	}
	if strings.Contains(got, "# =====") || strings.Contains(got, "private.example") {
		t.Fatalf("preview contains synthetic header or site snippet:\n%s", got)
	}
}
