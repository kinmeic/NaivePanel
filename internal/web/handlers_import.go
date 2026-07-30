package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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

// handleSiteImport adopts an unmanaged on-disk snippet into the panel model.
func (s *Server) handleSiteImport(w http.ResponseWriter, r *http.Request) {
	file := filepath.Base(r.FormValue("file"))
	if file == "." || !strings.HasSuffix(file, ".caddy") {
		s.setFlash(w, "非法的文件名")
		s.redirect(w, r, "/sites")
		return
	}
	data, err := os.ReadFile(filepath.Join(s.Cfg.Caddy.SitesDir, file))
	if err != nil {
		s.setFlash(w, "读取失败: "+err.Error())
		s.redirect(w, r, "/sites")
		return
	}
	st, mode, err := s.importSiteFromDisk(string(data))
	if err != nil {
		s.setFlash(w, "导入失败: "+err.Error())
		s.redirect(w, r, "/sites")
		return
	}
	if err := s.applySiteChange(st, s.Cfg.FindSite(st.Domain) >= 0); err != nil {
		s.setFlash(w, "导入失败: "+err.Error())
		s.redirect(w, r, "/sites")
		return
	}
	s.setFlash(w, fmt.Sprintf("已从 %s 导入站点 %s（%s）", file, st.Domain, mode))
	s.redirect(w, r, "/sites")
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

// handleSiteSyncModel re-renders from the panel model onto disk, letting the
// model win over external edits.
func (s *Server) handleSiteSyncModel(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if s.Cfg.FindSite(domain) < 0 {
		s.setFlash(w, "站点不存在")
		s.redirect(w, r, "/sites")
		return
	}
	if err := s.Caddy.Apply(); err != nil {
		s.setFlash(w, "覆盖失败: "+err.Error())
		s.redirect(w, r, "/sites")
		return
	}
	s.setFlash(w, "已用面板模型覆盖 "+domain+" 的磁盘配置")
	s.redirect(w, r, "/sites")
}
