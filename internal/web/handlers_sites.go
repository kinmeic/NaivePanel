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

func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "sites", "站点管理", map[string]any{
		"Sites":    s.Cfg.SitesSnapshot(),
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
	if err := s.applySiteChange(st, false); err != nil {
		s.renderFormError(w, r, st, true, err)
		return
	}
	s.setFlash(w, "站点 "+st.Domain+" 已创建并生效")
	s.redirect(w, r, "/sites")
}

func (s *Server) handleSiteEdit(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, ok := s.Cfg.GetSite(domain)
	if !ok {
		s.setFlash(w, "站点不存在")
		s.redirect(w, r, "/sites")
		return
	}
	preview, _ := s.Caddy.RenderSite(&st)
	s.render(w, r, "site_form", "编辑站点 "+domain, siteForm{
		Site:       st,
		Accounts:   accountsText(&st),
		Preview:    preview,
		HostSite:   s.Cfg.GetHostSite(),
		PHPSockets: detectPHPSockets(),
	})
}

func (s *Server) handleSiteUpdate(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	i := s.Cfg.FindSite(domain)
	if i < 0 {
		s.setFlash(w, "站点不存在")
		s.redirect(w, r, "/sites")
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
	s.redirect(w, r, "/sites")
}

func (s *Server) handleSiteDelete(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, ok := s.Cfg.GetSite(domain)
	if !ok {
		s.setFlash(w, "站点不存在")
		s.redirect(w, r, "/sites")
		return
	}
	if err := s.Cfg.DeleteSite(domain); err != nil {
		s.setFlash(w, err.Error())
		s.redirect(w, r, "/sites")
		return
	}
	if err := s.Caddy.Apply(); err != nil {
		// Caddy 回滚后仍在运行旧配置，把站点加回模型保持一致。
		_ = s.Cfg.UpsertSite(st)
		s.setFlash(w, "删除失败，站点已保留: "+err.Error())
		s.redirect(w, r, "/sites")
		return
	}
	s.setFlash(w, "站点 "+domain+" 已删除并生效")
	s.redirect(w, r, "/sites")
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
