package web

import (
	"net/http"

	"github.com/kinmeic/NaivePanel/internal/sysd"
)

// caddyPageData feeds the merged Caddy page (service card + tabs).
type caddyPageData struct {
	View     string // "sites" | "config"
	Active   bool   // caddy.service running
	Content  string // config view: live on-disk configuration
	HostSite string
}

func (s *Server) caddyData(view string) caddyPageData {
	return caddyPageData{
		View:     view,
		Active:   sysd.IsActive("caddy"),
		HostSite: s.Cfg.GetHostSite(),
	}
}

// handleCaddySites renders the Caddy page with the sites tab active. The
// table itself is loaded asynchronously from /caddy/sites/list so the page
// shows a loading state while the on-disk Caddyfile snippets are parsed.
func (s *Server) handleCaddySites(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "caddy", "Caddy", s.caddyData("sites"))
}

// handleCaddyConfig renders the Caddy page with the raw-config tab active:
// the real on-disk Caddyfile plus every site snippet, i.e. exactly what
// Caddy loads.
func (s *Server) handleCaddyConfig(w http.ResponseWriter, r *http.Request) {
	d := s.caddyData("config")
	d.Content = s.Caddy.LivePreview()
	s.render(w, r, "caddy", "Caddy", d)
}

// handleCaddyService starts/stops/restarts caddy.service.
func (s *Server) handleCaddyService(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	if action != "start" && action != "stop" && action != "restart" {
		s.setFlash(w, "未知操作")
		s.redirect(w, r, "/caddy/sites")
		return
	}
	if err := sysd.Action(action, "caddy"); err != nil {
		s.setFlash(w, "执行失败: "+err.Error())
	} else {
		s.setFlash(w, "Caddy 服务已执行 "+action)
	}
	s.redirect(w, r, "/caddy/sites")
}
