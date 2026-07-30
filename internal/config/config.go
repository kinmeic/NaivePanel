// Package config defines the panel's own configuration model and
// persistence. The config file lives at /etc/naivepanel/config.yaml (0600).
package config

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	Dir    string `yaml:"dir"`
	Mirror string `yaml:"mirror"` // optional URL prefix for GitHub releases
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

	// AutoUpdate enables daily self-update checks: when a newer GitHub
	// release exists the panel downloads, verifies and installs it, then
	// restarts itself.
	AutoUpdate bool `yaml:"auto_update"`

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

// SourcePath returns the absolute path this configuration was loaded from.
// Callers use its directory for panel-owned auxiliary state.
func (c *Config) SourcePath() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	path := c.path
	if path == "" {
		path = DefaultConfigPath
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return filepath.Clean(path)
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
	if err := (&sites.Site{Domain: c.Domain, RawMode: true, Raw: "validation"}).Validate(); err != nil {
		return fmt.Errorf("domain: %w", err)
	}
	if c.BasePath == "" || c.BasePath[0] != '/' || c.BasePath == "/" ||
		strings.HasSuffix(c.BasePath, "/") || len(c.BasePath) > 128 ||
		strings.ContainsAny(c.BasePath, " \t\r\n{}?#") {
		return errors.New("base_path 必须是以 / 开头、不以 / 结尾且不含空白或特殊字符的路径")
	}
	if c.AdminUser == "" || c.AdminPassHash == "" {
		return errors.New("管理员账号或密码哈希为空")
	}
	if strings.ContainsAny(c.AdminUser, "\r\n\x00") {
		return errors.New("管理员账号包含控制字符")
	}
	host, portText, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen 必须是 host:port: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("listen 端口必须在 1–65535 之间")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("listen 必须绑定到 loopback 地址（127.0.0.1、::1 或 localhost）")
		}
	}
	if c.SessionTTLHours < 1 || c.SessionTTLHours > 24*30 {
		return errors.New("session_ttl_hours 必须在 1–720 之间")
	}
	if c.BypassCore.SocksPort < 1 || c.BypassCore.SocksPort > 65535 {
		return errors.New("bypasscore.socks_port 必须在 1–65535 之间")
	}
	if c.ProxyToken != "" && len(c.ProxyToken) < 32 {
		return errors.New("proxy_token 长度至少为 32 个字符")
	}
	for name, path := range map[string]string{
		"caddy.bin":               c.Caddy.Bin,
		"caddy.main_file":         c.Caddy.MainFile,
		"caddy.sites_dir":         c.Caddy.SitesDir,
		"bypasscore.bin_path":     c.BypassCore.BinPath,
		"bypasscore.config_path":  c.BypassCore.ConfigPath,
		"bypasscore.control_sock": c.BypassCore.ControlSock,
		"bypasscore.work_dir":     c.BypassCore.WorkDir,
		"geo.dir":                 c.Geo.Dir,
		"backup_dir":              c.BackupDir,
	} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
			return fmt.Errorf("%s 必须是无控制字符的绝对路径", name)
		}
	}
	if err := ValidateMirror(c.Geo.Mirror); err != nil {
		return err
	}
	if c.HostSite == "" {
		c.HostSite = c.Domain
	} else if err := (&sites.Site{Domain: c.HostSite, RawMode: true, Raw: "validation"}).Validate(); err != nil {
		return fmt.Errorf("host_site: %w", err)
	}
	return nil
}

// ValidateMirror accepts only HTTPS URL prefixes without credentials, query
// parameters or fragments. The panel downloads update artifacts as root, so
// accepting arbitrary schemes or embedded credentials would create an
// avoidable SSRF/secret-leak footgun.
func ValidateMirror(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("GitHub 镜像必须是有效的 https:// URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("GitHub 镜像 URL 不能包含账号、查询参数或片段")
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
	migrated, err := c.migrateLegacyValues()
	if err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.migrateSiteRouteModes() {
		migrated = true
	}
	if c.ProxyToken == "" {
		// Older configs lack the HTTPS gate token; generate and persist.
		c.ProxyToken = RandomToken()
		migrated = true
	}
	if migrated {
		if err := c.Save(); err != nil {
			return nil, fmt.Errorf("保存迁移后的配置: %w", err)
		}
	}
	return &c, nil
}

// migrateSiteRouteModes upgrades configs written before use_route existed.
// It reads the live snippet only when the YAML field was absent; an explicit
// false selection in a current config is never overridden.
func (c *Config) migrateSiteRouteModes() bool {
	legacy := false
	mainSites := map[string]string{}
	mainLoaded := false
	for i := range c.Sites {
		if c.Sites[i].RouteExplicit {
			continue
		}
		legacy = true
		content, err := os.ReadFile(filepath.Join(c.Caddy.SitesDir, c.Sites[i].Domain+".caddy"))
		if err != nil && !mainLoaded {
			mainLoaded = true
			if main, readErr := os.ReadFile(c.Caddy.MainFile); readErr == nil {
				_, blocks := sites.SplitMain(string(main))
				for _, block := range blocks {
					mainSites[block.Domain] = block.Content
				}
			}
		}
		if err != nil {
			content = []byte(mainSites[c.Sites[i].Domain])
		}
		if len(content) > 0 {
			if parsed, parseErr := sites.Parse(string(content), c.BasePath, c.BypassCore.SocksPort); parseErr == nil {
				c.Sites[i].UseRoute = parsed.UseRoute
			}
		}
		c.Sites[i].RouteExplicit = true
	}
	return legacy
}

// migrateLegacyValues upgrades only values emitted or commonly copied from
// older NaivePanel documentation. Shell expressions are never evaluated:
// LookPath resolves the known executable name and the resulting absolute path
// is persisted before the regular security validation runs.
func (c *Config) migrateLegacyValues() (bool, error) {
	switch strings.TrimSpace(c.Caddy.Bin) {
	case "caddy", "$(command -v caddy)":
		path, err := exec.LookPath("caddy")
		if err != nil {
			return false, fmt.Errorf("迁移 caddy.bin 失败：未找到 caddy 可执行文件，请将其改为绝对路径（例如 /usr/bin/caddy）")
		}
		path, err = filepath.Abs(path)
		if err != nil || !filepath.IsAbs(path) {
			return false, fmt.Errorf("迁移 caddy.bin 失败：无法确定 caddy 的绝对路径")
		}
		c.Caddy.Bin = path
		return true, nil
	default:
		return false, nil
	}
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
	dir := filepath.Dir(c.path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(c.path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, c.path)
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

// SelfUpdateEnabled reports whether daily self-update checks are on.
func (c *Config) SelfUpdateEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AutoUpdate
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
