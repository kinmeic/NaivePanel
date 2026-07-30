package caddymgr

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kinmeic/NaivePanel/internal/sites"
)

// DiskSite is a site snippet found on disk under the sites directory.
type DiskSite struct {
	FileName string
	Domain   string // parsed from the site block header; falls back to file base name
	Content  string
}

// ListDiskSites reads every site snippet currently on disk, sorted by domain.
func (m *Manager) ListDiskSites() []DiskSite {
	entries, err := os.ReadDir(m.cfg.Caddy.SitesDir)
	if err != nil {
		return nil
	}
	var out []DiskSite
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".caddy") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.cfg.Caddy.SitesDir, e.Name()))
		if err != nil {
			continue
		}
		domain := sites.DomainFromHeader(string(data))
		if domain == "" {
			domain = strings.TrimSuffix(e.Name(), ".caddy")
		}
		out = append(out, DiskSite{FileName: e.Name(), Domain: domain, Content: string(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

// ReadDiskSite returns the on-disk snippet whose domain (or file name)
// matches domain.
func (m *Manager) ReadDiskSite(domain string) (DiskSite, bool) {
	for _, ds := range m.ListDiskSites() {
		if ds.Domain == domain || ds.FileName == domain+".caddy" {
			return ds, true
		}
	}
	return DiskSite{}, false
}

// LivePreview returns the real on-disk configuration: the main Caddyfile
// plus every site snippet, with path headers.
func (m *Manager) LivePreview() string {
	var b strings.Builder
	if data, err := os.ReadFile(m.cfg.Caddy.MainFile); err == nil {
		b.WriteString("# ===== " + m.cfg.Caddy.MainFile + " =====\n\n")
		b.WriteString(string(data))
		b.WriteString("\n")
	} else {
		b.WriteString("# ===== " + m.cfg.Caddy.MainFile + " =====\n\n# (读取失败: " + err.Error() + ")\n\n")
	}
	for _, ds := range m.ListDiskSites() {
		b.WriteString("# ===== " + filepath.Join(m.cfg.Caddy.SitesDir, ds.FileName) + " =====\n\n")
		b.WriteString(ds.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// NormalizeSnippet normalizes a snippet for drift comparison: trims every
// line and drops empty lines.
func NormalizeSnippet(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// SiteDrift reports whether a managed site's on-disk snippet differs from
// what the panel model would render. missing is true when the site has no
// snippet on disk at all.
func (m *Manager) SiteDrift(domain string) (drift, missing bool) {
	st, ok := m.cfg.GetSite(domain)
	if !ok {
		return false, false
	}
	ds, found := m.ReadDiskSite(domain)
	if !found {
		return true, true
	}
	rendered, err := m.RenderSite(&st)
	if err != nil {
		return false, false
	}
	return NormalizeSnippet(ds.Content) != NormalizeSnippet(rendered), false
}
