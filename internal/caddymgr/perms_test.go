package caddymgr

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/kinmeic/NaivePanel/internal/sites"
)

// 站点片段必须让 caddy 服务用户可读：systemd 里 caddy 以 User=caddy 运行，
// 若片段是 0600 root:root，caddy 会以 "permission denied" 启动失败——而
// 以 root 执行的 caddy validate 依然通过，问题只能在权限上体现。
func TestSnippetsAreReadableByCaddyUser(t *testing.T) {
	st := sites.Site{Domain: "a.example.com"}
	st.Web.Type = sites.WebStatic
	st.Web.Root = "/var/www/a"
	m, dir := testManager(t, "{\n\temail admin@example.com\n}\n", st)

	files, err := m.RenderAll()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.applyLive(files); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "sites", "a.example.com.caddy")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	perm := info.Mode().Perm()
	if perm&0o444 == 0 {
		t.Fatalf("snippet must be readable, got %04o", perm)
	}
	if perm&0o007 != 0 {
		t.Fatalf("snippet must not be world-accessible via other bits, got %04o", perm)
	}

	if gid, ok := lookupCaddyGID(); ok {
		if perm != snippetFileMode {
			t.Fatalf("with caddy group present want mode %04o, got %04o", snippetFileMode, perm)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("unexpected FileInfo.Sys type")
		}
		if int(stat.Gid) != gid {
			t.Fatalf("snippet group = %d, want caddy gid %d", stat.Gid, gid)
		}
	} else if perm != snippetFallbackMode {
		t.Fatalf("without caddy group want mode %04o, got %04o", snippetFallbackMode, perm)
	}
}

// fixSnippetPerm 需幂等，便于修正历史遗留的 0600 片段。
func TestFixSnippetPermOnLegacyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.example.com.caddy")
	if err := os.WriteFile(path, []byte(":443, old.example.com {\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixSnippetPerm(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o444 == 0 {
		t.Fatalf("legacy snippet still unreadable: %04o", info.Mode().Perm())
	}
}
