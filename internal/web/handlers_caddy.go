package web

import (
	"net/http"

	"github.com/kinmeic/NaivePanel/internal/sysd"
)

// caddyPageData feeds the Caddy service page.
type caddyPageData struct {
	Active  bool
	Enabled bool
}

type caddyConfigPageData struct {
	Config string
	Error  string
	Notice string
}

func (s *Server) caddyData() caddyPageData {
	return caddyPageData{
		Active:  sysd.IsActive("caddy"),
		Enabled: sysd.IsEnabled("caddy"),
	}
}

// handleCaddy renders service controls. Configuration editing lives on a
// separate raw-text page so the service page stays consistent with BypassCore.
func (s *Server) handleCaddy(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "caddy", "Caddy", s.caddyData())
}

// handleCaddyConfigGET renders the live main Caddyfile in a plain-text editor.
func (s *Server) handleCaddyConfigGET(w http.ResponseWriter, r *http.Request) {
	content, err := s.Caddy.ReadConfig()
	data := caddyConfigPageData{Config: string(content)}
	if err != nil {
		data.Error = err.Error()
	}
	s.render(w, r, "caddy_config", "Caddy 配置", data)
}

// handleCaddyConfigPOST formats, validates or saves the submitted Caddyfile.
func (s *Server) handleCaddyConfigPOST(w http.ResponseWriter, r *http.Request) {
	content := r.FormValue("config")
	data := caddyConfigPageData{Config: content}

	s.caddyMu.Lock()
	defer s.caddyMu.Unlock()
	switch r.FormValue("action") {
	case "format":
		formatted, err := s.Caddy.FormatConfig([]byte(content))
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Config = string(formatted)
			data.Notice = "格式化完成，尚未保存。"
		}
	case "validate":
		if err := s.Caddy.ValidateConfig([]byte(content)); err != nil {
			data.Error = err.Error()
		} else {
			data.Notice = "配置检查通过，尚未保存。"
		}
	case "save":
		if err := s.Caddy.SaveConfig([]byte(content)); err != nil {
			data.Error = err.Error()
		} else {
			s.setFlash(w, "Caddyfile 已校验、备份并保存；需要时请重载配置")
			s.redirect(w, r, "/caddy")
			return
		}
	default:
		data.Error = "未知操作"
	}
	s.render(w, r, "caddy_config", "Caddy 配置", data)
}

// handleCaddyService starts/stops/restarts caddy.service or changes autostart.
func (s *Server) handleCaddyService(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	if action != "start" && action != "stop" && action != "restart" &&
		action != "enable" && action != "disable" {
		s.setFlash(w, "未知操作")
		s.redirect(w, r, "/caddy")
		return
	}
	s.caddyMu.Lock()
	defer s.caddyMu.Unlock()
	if err := sysd.Action(action, "caddy"); err != nil {
		s.setFlash(w, "执行失败: "+err.Error())
	} else {
		switch action {
		case "enable":
			s.setFlash(w, "Caddy 已开启开机自启")
		case "disable":
			s.setFlash(w, "Caddy 已关闭开机自启（当前服务不会停止）")
		default:
			s.setFlash(w, "Caddy 服务已执行 "+action)
		}
	}
	s.redirect(w, r, "/caddy")
}
