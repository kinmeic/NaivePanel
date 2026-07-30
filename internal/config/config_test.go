package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinmeic/NaivePanel/internal/sites"
	"gopkg.in/yaml.v3"
)

func validConfig(t *testing.T) *Config {
	t.Helper()
	root := t.TempDir()
	c := &Config{
		Listen:          "127.0.0.1:9000",
		Domain:          "panel.example.com",
		BasePath:        "/manage-test",
		AdminUser:       "admin",
		AdminPassHash:   "hash",
		SessionTTLHours: 12,
		ProxyToken:      strings.Repeat("a", 48),
		Caddy: CaddyPaths{
			Bin:      "/usr/bin/caddy",
			MainFile: filepath.Join(root, "Caddyfile"),
			SitesDir: filepath.Join(root, "sites"),
		},
		BypassCore: BypassCore{
			SocksPort:   1080,
			BinPath:     "/usr/local/bin/bypasscore",
			ConfigPath:  filepath.Join(root, "bypass.json"),
			ControlSock: filepath.Join(root, "control.sock"),
			WorkDir:     root,
		},
		Geo:       Geo{Dir: root},
		BackupDir: filepath.Join(root, "backups"),
	}
	return c
}

func TestValidateSecurityBoundaries(t *testing.T) {
	if err := validConfig(t).Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"public listen", func(c *Config) { c.Listen = "0.0.0.0:9000" }},
		{"base path injection", func(c *Config) { c.BasePath = "/manage\nrespond ok" }},
		{"weak proxy token", func(c *Config) { c.ProxyToken = "short" }},
		{"relative config path", func(c *Config) { c.BypassCore.ConfigPath = "config.json" }},
		{"insecure mirror", func(c *Config) { c.Geo.Mirror = "http://mirror.example.com" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig(t)
			tc.mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateMirror(t *testing.T) {
	for _, good := range []string{"", "https://mirror.example.com", "https://mirror.example.com/github"} {
		if err := ValidateMirror(good); err != nil {
			t.Fatalf("%q: %v", good, err)
		}
	}
	for _, bad := range []string{
		"http://mirror.example.com",
		"https://user:pass@mirror.example.com",
		"https://mirror.example.com?token=secret",
	} {
		if err := ValidateMirror(bad); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}

func TestLoadMigratesLegacyCaddyBin(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	caddyPath := filepath.Join(binDir, "caddy")
	if err := os.WriteFile(caddyPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	for _, tc := range []struct {
		name   string
		legacy string
	}{
		{"bare name", "caddy"},
		{"documented shell literal", "$(command -v caddy)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.Caddy.Bin = tc.legacy
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			data, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, data, 0600); err != nil {
				t.Fatal(err)
			}

			loaded, err := Load(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Caddy.Bin != caddyPath {
				t.Fatalf("caddy.bin = %q, want %q", loaded.Caddy.Bin, caddyPath)
			}
			persisted, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			var saved Config
			if err := yaml.Unmarshal(persisted, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Caddy.Bin != caddyPath {
				t.Fatalf("迁移结果未持久化:\n%s", persisted)
			}
		})
	}
}

func TestLoadMigratesLegacyRouteModeFromDisk(t *testing.T) {
	cfg := validConfig(t)
	cfg.Sites = []sites.Site{{Domain: "legacy.example.com", Web: sites.Web{Type: sites.WebStatic, Root: "/srv/legacy"}}}
	if err := os.MkdirAll(cfg.Caddy.SitesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	snippet := "legacy.example.com {\n\troute {\n\t\troot * /srv/legacy\n\t\tfile_server\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(cfg.Caddy.SitesDir, "legacy.example.com.caddy"), []byte(snippet), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var legacyLines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "use_route: false" {
			legacyLines = append(legacyLines, line)
		}
	}
	data = []byte(strings.Join(legacyLines, "\n"))
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Sites) != 1 || !loaded.Sites[0].UseRoute {
		t.Fatalf("legacy disk route was not migrated: %+v", loaded.Sites)
	}
	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), "use_route: true") {
		t.Fatalf("migrated route mode was not persisted:\n%s", persisted)
	}
}

func TestLoadKeepsExplicitRouteFalse(t *testing.T) {
	cfg := validConfig(t)
	cfg.Sites = []sites.Site{{
		Domain: "explicit.example.com", UseRoute: false, RouteExplicit: true,
		Web: sites.Web{Type: sites.WebStatic, Root: "/srv/explicit"},
	}}
	if err := os.MkdirAll(cfg.Caddy.SitesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	snippet := "explicit.example.com {\n\troute {\n\t\troot * /srv/explicit\n\t\tfile_server\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(cfg.Caddy.SitesDir, "explicit.example.com.caddy"), []byte(snippet), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Sites[0].UseRoute {
		t.Fatal("explicit false was overwritten from disk")
	}
}

func TestLoadRejectsUnknownRelativeCaddyBin(t *testing.T) {
	cfg := validConfig(t)
	cfg.Caddy.Bin = "custom-caddy"
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "caddy.bin") {
		t.Fatalf("应拒绝未知相对路径，得到 %v", err)
	}
}
