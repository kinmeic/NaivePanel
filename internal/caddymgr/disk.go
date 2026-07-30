package caddymgr

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kinmeic/NaivePanel/internal/sites"
)

// DiskSite is a site configuration found on disk: either a snippet file
// under the sites directory or an inline block in the main Caddyfile.
type DiskSite struct {
	FileName string // snippet file name, or "Caddyfile" for inline blocks
	Domain   string // parsed from the site block header; falls back to file base name
	Content  string
	Inline   bool // the block lives inside the main Caddyfile
}

// ListDiskSites reads every site configuration currently on disk — snippet
// files plus inline blocks in the main Caddyfile — sorted by domain.
func (m *Manager) ListDiskSites() []DiskSite {
	var out []DiskSite
	seen := map[string]bool{}
	if entries, err := os.ReadDir(m.cfg.Caddy.SitesDir); err == nil {
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
			seen[domain] = true
			out = append(out, DiskSite{FileName: e.Name(), Domain: domain, Content: string(data)})
		}
	}
	if data, err := os.ReadFile(m.cfg.Caddy.MainFile); err == nil {
		_, inline := sites.SplitMain(string(data))
		for _, ms := range inline {
			if ms.Domain == "" || seen[ms.Domain] {
				continue
			}
			out = append(out, DiskSite{FileName: "Caddyfile", Domain: ms.Domain, Content: ms.Content, Inline: true})
		}
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

// LivePreview returns the main Caddyfile exactly as stored on disk. Imported
// site snippets are deliberately omitted from this view.
func (m *Manager) LivePreview() string {
	data, err := os.ReadFile(m.cfg.Caddy.MainFile)
	if err != nil {
		return "读取失败: " + err.Error()
	}
	return string(data)
}
