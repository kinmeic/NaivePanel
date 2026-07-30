// Package caddymgr renders, validates, applies and rolls back Caddy config.
package caddymgr

import (
	"bytes"
	"crypto/tls"
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

// Manager applies panel site models to the on-disk Caddy configuration.
type Manager struct {
	cfg *config.Config

	// managed tracks the domains the panel has rendered, so applyLive only
	// prunes snippets it owns and never deletes foreign files that happen to
	// sit in the sites directory.
	mu      sync.Mutex
	managed map[string]bool
}

// New creates a Manager bound to the panel config.
func New(cfg *config.Config) *Manager {
	m := &Manager{cfg: cfg, managed: map[string]bool{}}
	for _, s := range cfg.SitesSnapshot() {
		m.managed[s.Domain] = true
	}
	return m
}

// RenderAll renders the main Caddyfile and every site snippet into a map of
// relative path → content (relative to a staging root containing Caddyfile
// and sites/). The main file's import path targets importDir; pass the live
// sites dir for production renders.
func (m *Manager) RenderAll() (map[string]string, error) {
	return m.renderAll(m.cfg.Caddy.SitesDir)
}

func (m *Manager) renderAll(importDir string) (map[string]string, error) {
	out := map[string]string{
		"Caddyfile": sites.RenderMain("admin@"+m.cfg.Domain, importDir),
	}
	panel := sites.PanelInfo{BasePath: m.cfg.BasePath, Listen: m.cfg.Listen, ProxyToken: m.cfg.ProxyToken}
	hostSite := m.cfg.GetHostSite()
	for _, s := range m.cfg.SitesSnapshot() {
		snippet, err := sites.Render(&s, panel, s.Domain == hostSite, m.cfg.BypassCore.SocksPort)
		if err != nil {
			return nil, fmt.Errorf("渲染站点 %s: %w", s.Domain, err)
		}
		out[filepath.Join("sites", s.Domain+".caddy")] = snippet
	}
	return out, nil
}

// RenderSite renders a single site snippet (for preview, unsaved sites OK).
func (m *Manager) RenderSite(s *sites.Site) (string, error) {
	panel := sites.PanelInfo{BasePath: m.cfg.BasePath, Listen: m.cfg.Listen, ProxyToken: m.cfg.ProxyToken}
	return sites.Render(s, panel, s.Domain == m.cfg.GetHostSite(), m.cfg.BypassCore.SocksPort)
}

// Preview returns a combined, human-readable view of the whole Caddy config.
func (m *Manager) Preview() (string, error) {
	files, err := m.RenderAll()
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	// Caddyfile first.
	sort.SliceStable(names, func(i, j int) bool { return names[i] == "Caddyfile" })
	var b strings.Builder
	for _, n := range names {
		b.WriteString("# ===== " + n + " =====\n\n")
		b.WriteString(files[n])
		b.WriteString("\n")
	}
	return b.String(), nil
}

// writeStaging writes rendered files into a fresh staging dir and returns
// it. The staged main file is rewritten to import the staged sites dir so
// that validation covers the new snippets, not the live ones.
func (m *Manager) writeStaging(files map[string]string) (string, error) {
	dir, err := os.MkdirTemp("", "naivepanel-caddy-*")
	if err != nil {
		return "", err
	}
	files = maps.Clone(files)
	files["Caddyfile"] = sites.RenderMain("admin@"+m.cfg.Domain, filepath.Join(dir, "sites"))
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
	cmd := exec.Command(m.cfg.Caddy.Bin, "validate", "--config", main, "--adapter", "caddyfile")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+filepath.Join(staging, ".data"))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("caddy validate 失败: %v\n%s", err, out.String())
	}
	return nil
}

// backup snapshots the current live config into the backup dir.
func (m *Manager) backup() (string, error) {
	stamp := time.Now().Format("20060102-150405")
	dst := filepath.Join(m.cfg.BackupDir, "caddy", stamp)
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
	pruneBackups(filepath.Join(m.cfg.BackupDir, "caddy"), keepBackups)
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
		if m.managed[domain] || wantDomains[domain] {
			os.Remove(filepath.Join(m.cfg.Caddy.SitesDir, e.Name()))
		}
	}
	m.managed = wantDomains
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
	cmd := exec.Command(m.cfg.Caddy.Bin, "reload", "--config", m.cfg.Caddy.MainFile, "--adapter", "caddyfile")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("caddy reload 失败: %v\n%s", err, out.String())
	}
	return nil
}

// healthCheck verifies Caddy is answering TLS on the panel domain.
func (m *Manager) healthCheck() error {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		DialContext:     dialer.DialContext,
		TLSClientConfig: &tls.Config{ServerName: m.cfg.Domain},
	}
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
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := m.reload(); err != nil {
		m.rollback(backupDir)
		return err
	}
	if err := m.healthCheck(); err != nil {
		m.rollback(backupDir)
		return fmt.Errorf("配置已回滚: %w", err)
	}
	return nil
}

func (m *Manager) rollback(backupDir string) {
	if err := m.restore(backupDir); err == nil {
		_ = m.reload()
	}
}

// ReloadOnly re-renders nothing and just reloads the live config.
func (m *Manager) ReloadOnly() error {
	return m.reload()
}
