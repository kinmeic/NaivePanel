package web

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/kinmeic/NaivePanel/internal/sites"
)

// siteForm is the view model for the site edit form.
type siteForm struct {
	Site       sites.Site
	IsNew      bool
	Accounts   string // "user pass" lines
	Preview    string
	Error      string
	HostSite   string
	PHPSockets []string
}

// parseSiteForm builds a Site from POSTed form values.
func parseSiteForm(r *http.Request) (*sites.Site, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	st := &sites.Site{
		Domain:  strings.TrimSpace(r.FormValue("domain")),
		RawMode: r.FormValue("raw_mode") == "on",
		Raw:     r.FormValue("raw"),
	}
	st.ForwardProxy.Enabled = r.FormValue("fp_enabled") == "on"
	st.ForwardProxy.UseBypassCore = r.FormValue("fp_bypass") == "on"
	st.ForwardProxy.Upstream = strings.TrimSpace(r.FormValue("fp_upstream"))
	for _, line := range strings.Split(r.FormValue("fp_accounts"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		user, pass, found := strings.Cut(line, " ")
		if !found {
			user, pass, found = strings.Cut(line, "\t")
		}
		if !found || user == "" || pass == "" {
			return nil, fmt.Errorf("账号行格式错误（应为“用户名 密码”）: %q", line)
		}
		st.ForwardProxy.Accounts = append(st.ForwardProxy.Accounts, sites.Account{User: user, Pass: pass})
	}
	st.Web.Type = r.FormValue("web_type")
	st.Web.Root = strings.TrimSpace(r.FormValue("web_root"))
	st.Web.PHPSocket = strings.TrimSpace(r.FormValue("web_php_socket"))
	st.Web.ProxyTo = strings.TrimSpace(r.FormValue("web_proxy_to"))
	types := r.Form["eb_type"]
	matchers := r.Form["eb_matcher"]
	contents := r.Form["eb_content"]
	for i := range types {
		content := ""
		if i < len(contents) {
			content = contents[i]
		}
		matcher := ""
		if i < len(matchers) {
			matcher = strings.TrimSpace(matchers[i])
		}
		if matcher == "" && strings.TrimSpace(content) == "" {
			continue // empty row
		}
		st.ExtraBlocks = append(st.ExtraBlocks, sites.ExtraBlock{
			Type:    types[i],
			Matcher: matcher,
			Content: content,
		})
	}
	if err := st.Validate(); err != nil {
		return nil, err
	}
	return st, nil
}

func accountsText(st *sites.Site) string {
	var b strings.Builder
	for _, a := range st.ForwardProxy.Accounts {
		b.WriteString(a.User + " " + a.Pass + "\n")
	}
	return b.String()
}

// applyWithRollback saves the site change and applies it to Caddy, reverting
// the model when the pipeline fails.
func (s *Server) applySiteChange(st *sites.Site, existed bool) error {
	old, _ := s.Cfg.GetSite(st.Domain)
	if err := s.Cfg.UpsertSite(*st); err != nil {
		return err
	}
	if err := s.Caddy.Apply(); err != nil {
		// Revert the model so it matches what Caddy actually runs.
		if existed {
			_ = s.Cfg.UpsertSite(old)
		} else {
			_ = s.Cfg.RemoveSiteForce(st.Domain)
		}
		return err
	}
	return nil
}

// siteRow pairs a disk-parsed site with its source file name.
type siteRow struct {
	Site      sites.Site
	FileName  string
	Inline    bool   // block lives inside the main Caddyfile (not yet migrated)
	ParseNote string // why a raw-mode row doesn't match the panel's shape
}

// handleSiteList is the async fragment behind the Caddy page's sites tab:
// every row is parsed fresh from the on-disk Caddyfile snippets, so the list
// always reflects what Caddy actually loads. Snippets that don't match the
// panel's rendered shape are shown in raw (高级) mode.
func (s *Server) handleSiteList(w http.ResponseWriter, r *http.Request) {
	var rows []siteRow
	for _, ds := range s.Caddy.ListDiskSites() {
		st, _, note, err := s.importSiteFromDisk(ds.Content)
		if err != nil {
			continue
		}
		rows = append(rows, siteRow{Site: *st, FileName: ds.FileName, Inline: ds.Inline, ParseNote: note})
	}
	s.renderFrag(w, r, "sites_list", map[string]any{
		"Rows":     rows,
		"HostSite": s.Cfg.GetHostSite(),
	})
}

func (s *Server) handleSiteNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "site_form", "新建站点", siteForm{
		IsNew: true,
		Site: sites.Site{
			Web: sites.Web{Type: sites.WebStatic, Root: "/var/www/"},
		},
		HostSite:   s.Cfg.GetHostSite(),
		PHPSockets: detectPHPSockets(),
	})
}

func (s *Server) handleSiteCreate(w http.ResponseWriter, r *http.Request) {
	st, err := parseSiteForm(r)
	if err != nil {
		s.renderFormError(w, r, st, true, err)
		return
	}
	if s.Cfg.FindSite(st.Domain) >= 0 {
		s.renderFormError(w, r, st, true, fmt.Errorf("站点 %s 已存在", st.Domain))
		return
	}
	if _, onDisk := s.Caddy.ReadDiskSite(st.Domain); onDisk {
		s.renderFormError(w, r, st, true, fmt.Errorf("站点 %s 的磁盘配置已存在", st.Domain))
		return
	}
	if err := s.applySiteChange(st, false); err != nil {
		s.renderFormError(w, r, st, true, err)
		return
	}
	s.setFlash(w, "站点 "+st.Domain+" 已创建并生效")
	s.redirect(w, r, "/caddy/sites")
}

func (s *Server) handleSiteEdit(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	// Edit what is actually on disk, not the cached panel model.
	ds, ok := s.Caddy.ReadDiskSite(domain)
	if !ok {
		s.setFlash(w, "磁盘上未找到站点 "+domain+" 的配置")
		s.redirect(w, r, "/caddy/sites")
		return
	}
	st, _, _, err := s.importSiteFromDisk(ds.Content)
	if err != nil {
		s.setFlash(w, "解析磁盘配置失败: "+err.Error())
		s.redirect(w, r, "/caddy/sites")
		return
	}
	preview, _ := s.Caddy.RenderSite(st)
	s.render(w, r, "site_form", "编辑站点 "+domain, siteForm{
		Site:       *st,
		Accounts:   accountsText(st),
		Preview:    preview,
		HostSite:   s.Cfg.GetHostSite(),
		PHPSockets: detectPHPSockets(),
	})
}

func (s *Server) handleSiteUpdate(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	_, onDisk := s.Caddy.ReadDiskSite(domain)
	if s.Cfg.FindSite(domain) < 0 && !onDisk {
		s.setFlash(w, "站点不存在")
		s.redirect(w, r, "/caddy/sites")
		return
	}
	st, err := parseSiteForm(r)
	if err != nil {
		s.renderFormError(w, r, st, false, err)
		return
	}
	if st.Domain != domain {
		s.renderFormError(w, r, st, false, fmt.Errorf("域名不可修改；如需更换请新建站点后删除旧站点"))
		return
	}
	if err := s.applySiteChange(st, true); err != nil {
		s.renderFormError(w, r, st, false, err)
		return
	}
	s.setFlash(w, "站点 "+domain+" 已更新并生效")
	s.redirect(w, r, "/caddy/sites")
}

func (s *Server) handleSiteDelete(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	ds, onDisk := s.Caddy.ReadDiskSite(domain)
	st, ok := s.Cfg.GetSite(domain)
	if !ok {
		// Disk-only site (never managed by the panel): adopt it into the
		// model first so deletion goes through the same safe path (staging,
		// validation, rollback).
		if !onDisk {
			s.setFlash(w, "站点不存在")
			s.redirect(w, r, "/caddy/sites")
			return
		}
		parsed, _, _, err := s.importSiteFromDisk(ds.Content)
		if err != nil {
			s.setFlash(w, "解析磁盘配置失败: "+err.Error())
			s.redirect(w, r, "/caddy/sites")
			return
		}
		st = *parsed
		_ = s.Cfg.UpsertSite(st)
	}
	if onDisk {
		// Make sure the next Apply removes the on-disk copy too: an inline
		// block would otherwise be migrated right back, and a foreign
		// snippet is outside the panel's managed set.
		s.Caddy.DropDomain(domain)
	}
	if err := s.Cfg.DeleteSite(domain); err != nil {
		s.setFlash(w, err.Error())
		s.redirect(w, r, "/caddy/sites")
		return
	}
	if err := s.Caddy.Apply(); err != nil {
		// Caddy 回滚后仍在运行旧配置，把站点加回模型保持一致。
		_ = s.Cfg.UpsertSite(st)
		s.setFlash(w, "删除失败，站点已保留: "+err.Error())
		s.redirect(w, r, "/caddy/sites")
		return
	}
	s.setFlash(w, "站点 "+domain+" 已删除并生效")
	s.redirect(w, r, "/caddy/sites")
}

// handleSitePreviewRender renders an unsaved form into a Caddyfile snippet.
func (s *Server) handleSitePreviewRender(w http.ResponseWriter, r *http.Request) {
	st, err := parseSiteForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out, err := s.Caddy.RenderSite(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(out))
}

func (s *Server) renderFormError(w http.ResponseWriter, r *http.Request, st *sites.Site, isNew bool, err error) {
	if st == nil {
		st = &sites.Site{Web: sites.Web{Type: sites.WebStatic}}
	}
	preview, _ := s.Caddy.RenderSite(st)
	form := siteForm{
		Site:       *st,
		IsNew:      isNew,
		Accounts:   accountsText(st),
		Preview:    preview,
		Error:      err.Error(),
		HostSite:   s.Cfg.GetHostSite(),
		PHPSockets: detectPHPSockets(),
	}
	title := "编辑站点 " + st.Domain
	if isNew {
		title = "新建站点"
	}
	s.render(w, r, "site_form", title, form)
}

// detectPHPSockets lists installed php-fpm sockets.
func detectPHPSockets() []string {
	matches, _ := filepath.Glob("/run/php/php*-fpm.sock")
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, "unix"+m)
	}
	return out
}
