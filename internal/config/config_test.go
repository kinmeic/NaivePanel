package config

import (
	"path/filepath"
	"strings"
	"testing"
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
