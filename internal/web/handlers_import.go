package web

import (
	"fmt"
	"net/http"

	"github.com/kinmeic/NaivePanel/internal/sites"
)

// importSiteFromDisk parses an on-disk snippet into a site model. Snippets
// that don't match the panel's rendered shape are imported in raw mode so
// their content is preserved verbatim. Returns the site and the import mode
// label ("结构化" / "高级模式").
func (s *Server) importSiteFromDisk(content string) (*sites.Site, string, error) {
	if st, err := sites.Parse(content, s.Cfg.BasePath, s.Cfg.BypassCore.SocksPort); err == nil {
		return st, "结构化", nil
	}
	domain := sites.DomainFromHeader(content)
	if domain == "" {
		return nil, "", fmt.Errorf("无法从配置中识别站点域名")
	}
	return &sites.Site{Domain: domain, RawMode: true, Raw: content}, "高级模式", nil
}

// handleSiteSyncDisk re-imports a managed site's on-disk snippet, letting
// external edits win over the panel model.
func (s *Server) handleSiteSyncDisk(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	ds, ok := s.Caddy.ReadDiskSite(domain)
	if !ok {
		s.setFlash(w, "磁盘上未找到站点 "+domain+" 的配置文件")
		s.redirect(w, r, "/sites")
		return
	}
	st, mode, err := s.importSiteFromDisk(ds.Content)
	if err != nil {
		s.setFlash(w, "解析磁盘配置失败: "+err.Error())
		s.redirect(w, r, "/sites")
		return
	}
	if err := s.applySiteChange(st, true); err != nil {
		s.setFlash(w, "同步失败: "+err.Error())
		s.redirect(w, r, "/sites")
		return
	}
	s.setFlash(w, fmt.Sprintf("已用磁盘配置更新站点 %s 的模型（%s）", domain, mode))
	s.redirect(w, r, "/sites")
}
