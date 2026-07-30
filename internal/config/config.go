// Package config defines the panel's own configuration model and
// persistence. The config file lives at /etc/naivepanel/config.yaml (0600).
package config

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/kinmeic/NaivePanel/internal/sites"
	"gopkg.in/yaml.v3"
)

// Default filesystem layout.
const (
	DefaultListen       = "127.0.0.1:9000"
	DefaultConfigPath   = "/etc/naivepanel/config.yaml"
	DefaultBackupDir    = "/etc/naivepanel/backups"
	DefaultCaddyBin     = "/usr/bin/caddy" // 定制构建直接覆盖官方二进制路径
	DefaultCaddyMain    = "/etc/caddy/Caddyfile"
	DefaultCaddySites   = "/etc/caddy/sites"
	DefaultBypassBin    = "/usr/local/bin/bypasscore"
	DefaultBypassConf   = "/etc/bypasscore/config.json"
	DefaultBypassSock   = "/run/bypasscore/control.sock"
	DefaultBypassWork   = "/etc/bypasscore"
	DefaultSessionTTLH  = 12
	DefaultSocksPort    = 1080
	maxBackupsPerTarget = 20
)

// BypassCore holds BypassCore integration settings.
type BypassCore struct {
	SocksPort   int    `yaml:"socks_port"`
	BinPath     string `yaml:"bin_path"`
	ConfigPath  string `yaml:"config_path"`
	ControlSock string `yaml:"control_sock"`
	WorkDir     string `yaml:"work_dir"`
}

// Geo holds geodata update settings.
type Geo struct {
	Dir              string `yaml:"dir"`
	Mirror           string `yaml:"mirror"` // optional URL prefix for GitHub releases
	AutoUpdateWeekly bool   `yaml:"auto_update_weekly"`
}

// CaddyPaths locates the managed Caddy installation.
type CaddyPaths struct {
	Bin      string `yaml:"bin"`
	MainFile string `yaml:"main_file"`
	SitesDir string `yaml:"sites_dir"`
}

// Config is the root panel configuration.
type Config struct {
	Listen          string   `yaml:"listen"`
	Domain          string   `yaml:"domain"`
	BasePath        string   `yaml:"base_path"`
	AdminUser       string   `yaml:"admin_user"`
	AdminPassHash   string   `yaml:"admin_pass_hash"`
	TOTPEnabled     bool     `yaml:"totp_enabled"`
	TOTPSecret      string   `yaml:"totp_secret"`
	RecoveryHashes  []string `yaml:"recovery_hashes"`
	HostSite        string   `yaml:"host_site"`
	SessionTTLHours int      `yaml:"session_ttl_hours"`
	// ProxyToken is the shared secret Caddy injects via header_up when
	// reverse proxying to the panel; requests without it are rejected,
	// so the panel is only reachable over HTTPS through Caddy.
	ProxyToken string `yaml:"proxy_token"`

	Caddy      CaddyPaths   `yaml:"caddy"`
	BypassCore BypassCore   `yaml:"bypasscore"`
	Geo        Geo          `yaml:"geo"`
	BackupDir  string       `yaml:"backup_dir"`
	Sites      []sites.Site `yaml:"sites"`

	// mu guards every mutable field above (Sites, HostSite, AdminPassHash,
	// TOTP*, RecoveryHashes, Geo). Use the accessor methods below instead of
	// touching the fields directly from handlers or goroutines.
	// Listen, Domain, BasePath and AdminUser are immutable after Load.
	mu   sync.RWMutex
	path string
}

// SetDefaults fills zero-valued fields with the standard layout.
func (c *Config) SetDefaults() {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.SessionTTLHours <= 0 {
		c.SessionTTLHours = DefaultSessionTTLH
	}
	if c.Caddy.Bin == "" {
		c.Caddy.Bin = DefaultCaddyBin
	}
	if c.Caddy.MainFile == "" {
		c.Caddy.MainFile = DefaultCaddyMain
	}
	if c.Caddy.SitesDir == "" {
		c.Caddy.SitesDir = DefaultCaddySites
	}
	if c.BypassCore.SocksPort <= 0 {
		c.BypassCore.SocksPort = DefaultSocksPort
	}
	if c.BypassCore.BinPath == "" {
		c.BypassCore.BinPath = DefaultBypassBin
	}
	if c.BypassCore.ConfigPath == "" {
		c.BypassCore.ConfigPath = DefaultBypassConf
	}
	if c.BypassCore.ControlSock == "" {
		c.BypassCore.ControlSock = DefaultBypassSock
	}
	if c.BypassCore.WorkDir == "" {
		c.BypassCore.WorkDir = DefaultBypassWork
	}
	if c.Geo.Dir == "" {
		c.Geo.Dir = c.BypassCore.WorkDir
	}
	if c.BackupDir == "" {
		c.BackupDir = DefaultBackupDir
	}
}

// Validate checks the minimum viable configuration.
func (c *Config) Validate() error {
	if c.Domain == "" {
		return errors.New("domain 不能为空")
	}
	if c.BasePath == "" || c.BasePath[0] != '/' {
		return errors.New("base_path 必须以 / 开头")
	}
	if c.AdminUser == "" || c.AdminPassHash == "" {
		return errors.New("管理员账号或密码哈希为空")
	}
	if c.HostSite == "" {
		c.HostSite = c.Domain
	}
	return nil
}

// Load reads the config file. The returned Config keeps its path for Save.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	c.path = path
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.ProxyToken == "" {
		// Older configs lack the HTTPS gate token; generate and persist.
		c.ProxyToken = RandomToken()
		_ = c.Save()
	}
	return &c, nil
}

// Save atomically persists the config (0600).
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked()
}

func (c *Config) saveLocked() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Path returns the loaded config file path.
func (c *Config) Path() string { return c.path }

// FindSite returns the index of the site with the given domain, or -1.
func (c *Config) FindSite(domain string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.findSiteLocked(domain)
}

func (c *Config) findSiteLocked(domain string) int {
	for i := range c.Sites {
		if c.Sites[i].Domain == domain {
			return i
		}
	}
	return -1
}

// GetSite returns a copy of the site with the given domain.
func (c *Config) GetSite(domain string) (sites.Site, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if i := c.findSiteLocked(domain); i >= 0 {
		return c.Sites[i], true
	}
	return sites.Site{}, false
}

// SitesSnapshot returns a copy of the site list safe for concurrent use.
func (c *Config) SitesSnapshot() []sites.Site {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]sites.Site, len(c.Sites))
	copy(out, c.Sites)
	return out
}

// GetHostSite returns the domain currently hosting the panel.
func (c *Config) GetHostSite() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.HostSite
}

// GeoSnapshot returns a copy of the geo settings.
func (c *Config) GeoSnapshot() Geo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Geo
}

// TOTPState reports whether MFA is on and returns its secret.
func (c *Config) TOTPState() (enabled bool, secret string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.TOTPEnabled, c.TOTPSecret
}

// AdminPassHash returns the stored password hash.
func (c *Config) GetAdminPassHash() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AdminPassHash
}

// RecoveryCount returns how many one-time recovery codes remain.
func (c *Config) RecoveryCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.RecoveryHashes)
}

// Mutate runs fn under the write lock and persists the config when fn
// returns nil. All runtime config changes should go through Mutate so the
// in-memory model and the on-disk file stay consistent and race-free.
func (c *Config) Mutate(fn func(*Config) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := fn(c); err != nil {
		return err
	}
	return c.saveLocked()
}

// ConsumeRecoveryHash checks a pre-hashed one-time recovery code against the
// stored hashes; on match the code is removed and the config persisted.
// Returns (false, nil) when the code does not match.
func (c *Config) ConsumeRecoveryHash(hashHex string) (bool, error) {
	matched := false
	err := c.Mutate(func(cfg *Config) error {
		for i, stored := range cfg.RecoveryHashes {
			if subtle.ConstantTimeCompare([]byte(stored), []byte(hashHex)) == 1 {
				cfg.RecoveryHashes = append(cfg.RecoveryHashes[:i], cfg.RecoveryHashes[i+1:]...)
				matched = true
				return nil
			}
		}
		return errNoMutation // no match: skip the save
	})
	if err == errNoMutation {
		return false, nil
	}
	return matched, err
}

// errNoMutation lets a Mutate callback abort without saving and without
// treating it as a failure.
var errNoMutation = errors.New("config: no mutation")

// UpsertSite adds or replaces a site, then saves.
func (c *Config) UpsertSite(s sites.Site) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i := c.findSiteLocked(s.Domain); i >= 0 {
		c.Sites[i] = s
	} else {
		c.Sites = append(c.Sites, s)
	}
	return c.saveLocked()
}

// DeleteSite removes a site by domain. It refuses to delete the host site.
func (c *Config) DeleteSite(domain string) error {
	if domain == c.GetHostSite() {
		return fmt.Errorf("%s 是面板寄宿站点，请先在设置页迁移面板寄宿后再删除", domain)
	}
	return c.RemoveSiteForce(domain)
}

// RemoveSiteForce deletes a site without the host-site guard (internal use
// for change rollback).
func (c *Config) RemoveSiteForce(domain string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.findSiteLocked(domain)
	if i < 0 {
		return errors.New("站点不存在")
	}
	c.Sites = append(c.Sites[:i], c.Sites[i+1:]...)
	return c.saveLocked()
}

// RandomPath generates a random panel base path like /manage-1a2b3c4d.
func RandomPath() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "/manage-" + hex.EncodeToString(b)
}

// RandomToken returns a 48-hex-char random shared secret.
func RandomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// EnsureDirs creates the managed directories.
func (c *Config) EnsureDirs() error {
	for _, d := range []string{
		filepath.Dir(c.path),
		c.BackupDir,
		c.Caddy.SitesDir,
		c.BypassCore.WorkDir,
		c.Geo.Dir,
	} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return err
		}
	}
	return nil
}
