// Package caddymgr renders, validates, applies and rolls back Caddy config.
package caddymgr

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/sites"
)

const keepBackups = 20
const maxRawConfigSize = 2 << 20
const forwardProxyModule = "http.handlers.forward_proxy"
const moduleCheckCacheTTL = 30 * time.Second

type moduleCheckCache struct {
	checkedAt time.Time
	installed bool
	errorText string
}

// Manager applies panel site models to the on-disk Caddy configuration.
type Manager struct {
	cfg *config.Config

	// managed tracks the domains the panel has rendered, so applyLive only
	// prunes snippets it owns and never deletes foreign files that happen to
	// sit in the sites directory.
	//
	// drop holds domains whose on-disk presence (inline block in the main
	// Caddyfile or foreign snippet) must be removed on the next Apply —
	// set when deleting a disk-only site.
	mu      sync.Mutex
	managed map[string]bool
	drop    map[string]bool

	applyMu sync.Mutex

	// moduleMu prevents concurrent page loads from spawning duplicate Caddy
	// processes. Module availability changes only when the binary is replaced,
	// so a short cache keeps the status fresh without work on every log refresh.
	moduleMu          sync.Mutex
	forwardProxyCache moduleCheckCache
}

// New creates a Manager bound to the panel config.
func New(cfg *config.Config) *Manager {
	m := &Manager{cfg: cfg, managed: map[string]bool{}, drop: map[string]bool{}}
	for _, s := range cfg.SitesSnapshot() {
		m.managed[s.Domain] = true
	}
	return m
}

// DropDomain marks a domain's on-disk configuration for removal on the next
// Apply, whether it lives as an inline block in the main Caddyfile or as a
// foreign snippet the panel never rendered. Used when deleting a disk-only
// site; consumed by the next Apply.
func (m *Manager) DropDomain(domain string) {
	m.mu.Lock()
	m.drop[domain] = true
	m.mu.Unlock()
}

// CancelDropDomain clears a pending deletion when the surrounding model
// transaction fails before Apply consumes it.
func (m *Manager) CancelDropDomain(domain string) {
	m.mu.Lock()
	delete(m.drop, domain)
	m.mu.Unlock()
}

// RenderAll renders the main Caddyfile and every site snippet into a map of
// relative path → content (relative to a staging root containing Caddyfile
// and sites/). The main file's import path targets importDir; pass the live
// sites dir for production renders.
func (m *Manager) RenderAll() (map[string]string, error) {
	return m.renderAll(m.cfg.Caddy.SitesDir)
}

func (m *Manager) renderAll(importDir string) (map[string]string, error) {
	out := map[string]string{}
	panel := sites.PanelInfo{BasePath: m.cfg.BasePath, Listen: m.cfg.Listen, ProxyToken: m.cfg.ProxyToken}
	hostSite := m.cfg.GetHostSite()
	modelDomains := map[string]bool{}
	for _, s := range m.cfg.SitesSnapshot() {
		snippet, err := sites.Render(&s, panel, s.Domain == hostSite, m.cfg.BypassCore.SocksPort)
		if err != nil {
			return nil, fmt.Errorf("渲染站点 %s: %w", s.Domain, err)
		}
		out[filepath.Join("sites", s.Domain+".caddy")] = snippet
		modelDomains[s.Domain] = true
	}

	// Migrate inline site blocks from the live main Caddyfile into snippet
	// files, verbatim, so nothing the operator wrote is lost when the main
	// file is rewritten as head + import. Model-rendered sites and domains
	// being deleted win over (skip) the inline copy.
	head := ""
	if live, err := os.ReadFile(m.cfg.Caddy.MainFile); err == nil {
		splitHead, inline := sites.SplitMain(string(live))
		head = splitHead
		m.mu.Lock()
		for _, ms := range inline {
			if ms.Domain == "" || modelDomains[ms.Domain] || m.drop[ms.Domain] {
				continue
			}
			rel := filepath.Join("sites", ms.Domain+".caddy")
			if _, err := os.Stat(filepath.Join(m.cfg.Caddy.SitesDir, ms.Domain+".caddy")); err == nil {
				continue // a snippet file already owns this domain
			}
			out[rel] = ms.Content + "\n"
		}
		m.mu.Unlock()
	}
	needsForwardProxyOrder := false
	for rel, content := range out {
		if strings.HasPrefix(rel, "sites/") && sites.ContainsDirective(content, "forward_proxy") {
			needsForwardProxyOrder = true
			break
		}
	}
	out["Caddyfile"] = sites.RenderMainPreserve(head, "admin@"+m.cfg.Domain, importDir, needsForwardProxyOrder)
	return out, nil
}

// RenderSite renders a single site snippet (for preview, unsaved sites OK).
func (m *Manager) RenderSite(s *sites.Site) (string, error) {
	panel := sites.PanelInfo{BasePath: m.cfg.BasePath, Listen: m.cfg.Listen, ProxyToken: m.cfg.ProxyToken}
	return sites.Render(s, panel, s.Domain == m.cfg.GetHostSite(), m.cfg.BypassCore.SocksPort)
}

// writeStaging writes rendered files into a fresh staging dir and returns
// it. The staged main file's import path is retargeted to the staged sites
// dir so that validation covers the new snippets, not the live ones.
func (m *Manager) writeStaging(files map[string]string) (string, error) {
	dir, err := os.MkdirTemp("", "naivepanel-caddy-*")
	if err != nil {
		return "", err
	}
	files = maps.Clone(files)
	files["Caddyfile"] = strings.ReplaceAll(files["Caddyfile"],
		"import "+filepath.Join(m.cfg.Caddy.SitesDir, "*.caddy"),
		"import "+filepath.Join(dir, "sites", "*.caddy"))
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

// validate runs caddy validate against the staged config.
func (m *Manager) validate(staging string) error {
	main := filepath.Join(staging, "Caddyfile")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.cfg.Caddy.Bin, "validate", "--config", main, "--adapter", "caddyfile")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+filepath.Join(staging, ".data"))
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("caddy validate 超时")
		}
		return fmt.Errorf("caddy validate 失败: %v\n%s", err, out.String())
	}
	return nil
}

// ReadConfig returns the main Caddyfile exactly as stored on disk.
func (m *Manager) ReadConfig() ([]byte, error) {
	if info, err := os.Stat(m.cfg.Caddy.MainFile); err == nil && info.Size() > maxRawConfigSize {
		return nil, fmt.Errorf("Caddyfile 超过 2 MiB")
	}
	data, err := os.ReadFile(m.cfg.Caddy.MainFile)
	if err != nil {
		return nil, fmt.Errorf("读取 Caddyfile: %w", err)
	}
	if len(data) > maxRawConfigSize {
		return nil, fmt.Errorf("Caddyfile 超过 2 MiB")
	}
	return data, nil
}

// ForwardProxyInstalled reports whether the configured Caddy binary contains
// the forward_proxy HTTP handler. Matching is performed in Go instead of
// invoking a shell pipeline.
func (m *Manager) ForwardProxyInstalled() (bool, error) {
	m.moduleMu.Lock()
	defer m.moduleMu.Unlock()
	now := time.Now()
	if age := now.Sub(m.forwardProxyCache.checkedAt); !m.forwardProxyCache.checkedAt.IsZero() &&
		age >= 0 && age < moduleCheckCacheTTL {
		if m.forwardProxyCache.errorText != "" {
			return false, errors.New(m.forwardProxyCache.errorText)
		}
		return m.forwardProxyCache.installed, nil
	}
	installed, err := m.detectForwardProxyInstalled()
	m.forwardProxyCache = moduleCheckCache{checkedAt: now, installed: installed}
	if err != nil {
		m.forwardProxyCache.errorText = err.Error()
	}
	return installed, err
}

func (m *Manager) detectForwardProxyInstalled() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.cfg.Caddy.Bin, "list-modules")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return false, fmt.Errorf("Caddy 模块检测超时")
		}
		return false, fmt.Errorf("运行 caddy list-modules 失败: %v: %s", err, strings.TrimSpace(string(output)))
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == forwardProxyModule {
			return true, nil
		}
	}
	return false, nil
}

// FormatConfig formats Caddyfile text with the configured Caddy binary.
// Stdin is used deliberately: formatting must not modify the live file.
func (m *Manager) FormatConfig(content []byte) ([]byte, error) {
	if len(content) > maxRawConfigSize {
		return nil, fmt.Errorf("配置超过 2 MiB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.cfg.Caddy.Bin, "fmt", "-")
	cmd.Stdin = bytes.NewReader(content)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("caddy fmt 超时")
		}
		return nil, fmt.Errorf("caddy fmt 失败: %v\n%s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() > maxRawConfigSize {
		return nil, fmt.Errorf("格式化后的配置超过 2 MiB")
	}
	return stdout.Bytes(), nil
}

// ValidateConfig validates unsaved Caddyfile text without touching the live
// configuration. The temporary file lives beside the real Caddyfile and the
// command runs from that directory so relative import paths keep their normal
// meaning.
func (m *Manager) ValidateConfig(content []byte) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	return m.validateContent(content)
}

func (m *Manager) validateContent(content []byte) error {
	if len(content) > maxRawConfigSize {
		return fmt.Errorf("配置超过 2 MiB")
	}
	dir := filepath.Dir(m.cfg.Caddy.MainFile)
	tmp, err := os.CreateTemp(dir, ".naivepanel-caddy-validate-*")
	if err != nil {
		return fmt.Errorf("创建校验文件: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	dataDir, err := os.MkdirTemp("", "naivepanel-caddy-data-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dataDir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.cfg.Caddy.Bin, "validate", "--config", name, "--adapter", "caddyfile")
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dataDir)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("Caddy 配置检查超时")
		}
		return fmt.Errorf("Caddy 配置检查失败: %v\n%s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// SaveConfig validates, backs up and atomically replaces the main Caddyfile.
// It intentionally does not reload the service; operators can review the
// saved file and use the explicit "重载配置" control when ready.
func (m *Manager) SaveConfig(content []byte) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	if err := m.validateContent(content); err != nil {
		return err
	}
	if _, err := m.backup(); err != nil {
		return fmt.Errorf("备份当前配置失败: %w", err)
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(m.cfg.Caddy.MainFile); err == nil {
		mode = info.Mode().Perm()
	}
	if err := writeFileAtomic(m.cfg.Caddy.MainFile, content, mode); err != nil {
		return fmt.Errorf("保存 Caddyfile: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
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
	return os.Rename(name, path)
}

// backup snapshots the current live config into a timestamped folder under
// <Caddyfile dir>/backup/ (e.g. /etc/caddy/backup/), keeping the newest
// keepBackups snapshots.
func (m *Manager) backup() (string, error) {
	stamp := time.Now().Format("20060102-150405.000000000")
	root := filepath.Join(filepath.Dir(m.cfg.Caddy.MainFile), "backup")
	dst := filepath.Join(root, stamp)
	if err := os.MkdirAll(dst, 0700); err != nil {
		return "", err
	}
	if data, err := os.ReadFile(m.cfg.Caddy.MainFile); err == nil {
		if err := os.WriteFile(filepath.Join(dst, "Caddyfile"), data, 0600); err != nil {
			return "", err
		}
	}
	entries, _ := os.ReadDir(m.cfg.Caddy.SitesDir)
	if len(entries) > 0 {
		if err := os.MkdirAll(filepath.Join(dst, "sites"), 0700); err != nil {
			return "", err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".caddy") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(m.cfg.Caddy.SitesDir, e.Name()))
			if err != nil {
				continue
			}
			if err := os.WriteFile(filepath.Join(dst, "sites", e.Name()), data, 0600); err != nil {
				return "", err
			}
		}
	}
	pruneBackups(root, keepBackups)
	return dst, nil
}

func pruneBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= keep {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries[:len(entries)-keep] {
		os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// restore copies a backup back to the live paths.
func (m *Manager) restore(backupDir string) error {
	if data, err := os.ReadFile(filepath.Join(backupDir, "Caddyfile")); err == nil {
		if err := os.WriteFile(m.cfg.Caddy.MainFile, data, 0644); err != nil {
			return err
		}
	}
	// Remove live snippets and restore backed-up ones.
	entries, _ := os.ReadDir(m.cfg.Caddy.SitesDir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".caddy") {
			os.Remove(filepath.Join(m.cfg.Caddy.SitesDir, e.Name()))
		}
	}
	bSites := filepath.Join(backupDir, "sites")
	if bEntries, err := os.ReadDir(bSites); err == nil {
		for _, e := range bEntries {
			data, err := os.ReadFile(filepath.Join(bSites, e.Name()))
			if err != nil {
				continue
			}
			if err := os.WriteFile(filepath.Join(m.cfg.Caddy.SitesDir, e.Name()), data, 0600); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyLive writes staged files to the live locations.
func (m *Manager) applyLive(files map[string]string) error {
	if content, ok := files["Caddyfile"]; ok {
		if err := os.WriteFile(m.cfg.Caddy.MainFile, []byte(content), 0644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(m.cfg.Caddy.SitesDir, 0755); err != nil {
		return err
	}
	// Delete snippets the panel manages that are no longer rendered. Foreign
	// files (never rendered by the panel, or only just imported under a
	// different file name) are left alone — except ones colliding with a
	// domain the model now owns under a different file name.
	want := map[string]bool{}
	wantDomains := map[string]bool{}
	for rel := range files {
		if strings.HasPrefix(rel, "sites/") {
			base := filepath.Base(rel)
			want[base] = true
			wantDomains[strings.TrimSuffix(base, ".caddy")] = true
		}
	}
	m.mu.Lock()
	entries, _ := os.ReadDir(m.cfg.Caddy.SitesDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".caddy") || want[e.Name()] {
			continue
		}
		domain := ""
		if data, err := os.ReadFile(filepath.Join(m.cfg.Caddy.SitesDir, e.Name())); err == nil {
			domain = sites.DomainFromHeader(string(data))
		}
		if domain == "" {
			domain = strings.TrimSuffix(e.Name(), ".caddy")
		}
		if m.managed[domain] || wantDomains[domain] || m.drop[domain] {
			os.Remove(filepath.Join(m.cfg.Caddy.SitesDir, e.Name()))
		}
	}
	// Only model domains count as managed: snippets migrated verbatim from
	// the live main Caddyfile stay foreign, so a later Apply that no longer
	// renders them (their inline copy is gone) must not prune them.
	m.managed = map[string]bool{}
	for _, s := range m.cfg.SitesSnapshot() {
		m.managed[s.Domain] = true
	}
	m.drop = map[string]bool{}
	m.mu.Unlock()
	for rel, content := range files {
		if !strings.HasPrefix(rel, "sites/") {
			continue
		}
		p := filepath.Join(m.cfg.Caddy.SitesDir, filepath.Base(rel))
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			return err
		}
	}
	return nil
}

// reload asks the running Caddy to adopt the live config.
func (m *Manager) reload() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.cfg.Caddy.Bin, "reload", "--config", m.cfg.Caddy.MainFile, "--adapter", "caddyfile")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("caddy reload 超时")
		}
		return fmt.Errorf("caddy reload 失败: %v\n%s", err, out.String())
	}
	return nil
}

// healthCheck verifies Caddy is answering TLS on the panel domain.
func (m *Manager) healthCheck() error {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, "127.0.0.1:443")
			if err == nil {
				return conn, nil
			}
			return dialer.DialContext(ctx, network, "[::1]:443")
		},
		TLSClientConfig: &tls.Config{ServerName: m.cfg.Domain, MinVersion: tls.VersionTLS12},
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://"+m.cfg.Domain+"/", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("站点探活失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("站点探活返回 %d", resp.StatusCode)
	}
	return nil
}

// Apply runs the full pipeline: render → stage → validate → backup →
// apply → reload → health check, rolling back on failure.
func (m *Manager) Apply() error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	files, err := m.RenderAll()
	if err != nil {
		return err
	}
	staging, err := m.writeStaging(files)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := m.validate(staging); err != nil {
		return err
	}
	backupDir, err := m.backup()
	if err != nil {
		return fmt.Errorf("备份当前配置失败: %w", err)
	}
	if err := m.applyLive(files); err != nil {
		if rollbackErr := m.rollback(backupDir); rollbackErr != nil {
			return fmt.Errorf("写入配置失败: %v；回滚也失败: %v", err, rollbackErr)
		}
		return fmt.Errorf("写入配置失败，已回滚: %w", err)
	}
	if err := m.reload(); err != nil {
		if rollbackErr := m.rollback(backupDir); rollbackErr != nil {
			return fmt.Errorf("%v；回滚也失败: %v", err, rollbackErr)
		}
		return fmt.Errorf("%w（已回滚）", err)
	}
	if err := m.healthCheck(); err != nil {
		if rollbackErr := m.rollback(backupDir); rollbackErr != nil {
			return fmt.Errorf("探活失败: %v；回滚也失败: %v", err, rollbackErr)
		}
		return fmt.Errorf("配置已回滚: %w", err)
	}
	return nil
}

func (m *Manager) rollback(backupDir string) error {
	if err := m.restore(backupDir); err != nil {
		return err
	}
	return m.reload()
}

// ReloadOnly re-renders nothing and just reloads the live config.
func (m *Manager) ReloadOnly() error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	return m.reload()
}
