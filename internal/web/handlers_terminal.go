package web

import (
	"fmt"
	"net/http"
	"net/http/httputil"

	"github.com/kinmeic/NaivePanel/internal/sysd"
)

// handleTerminal shows the ttyd status card and embeds the web terminal
// when the service is running.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	installed := s.Terminal.Installed()
	s.render(w, r, "terminal", "终端", map[string]any{
		"Installed": installed,
		"Version":   s.Terminal.Version(),
		"Active":    installed && sysd.IsActive(s.Terminal.UnitName()),
		"Port":      s.Cfg.Terminal.Port,
	})
}

// handleTerminalInstall downloads ttyd, installs the unit and starts it.
func (s *Server) handleTerminalInstall(w http.ResponseWriter, r *http.Request) {
	tag, err := s.Terminal.Install(s.Cfg.GeoSnapshot().Mirror, s.Cfg.BasePath)
	if err != nil {
		s.setFlash(w, "安装/更新失败: "+err.Error())
		s.redirect(w, r, "/terminal")
		return
	}
	if err := sysd.Action("enable", s.Terminal.UnitName()); err != nil {
		s.setFlash(w, "已安装 "+tag+"，但设置开机启动失败: "+err.Error())
		s.redirect(w, r, "/terminal")
		return
	}
	if err := sysd.Action("restart", s.Terminal.UnitName()); err != nil {
		s.setFlash(w, "已安装 "+tag+"，但启动失败: "+err.Error())
		s.redirect(w, r, "/terminal")
		return
	}
	s.setFlash(w, "ttyd "+tag+" 安装完成并已启动")
	s.redirect(w, r, "/terminal")
}

// handleTerminalService runs start/stop/restart on the ttyd unit.
func (s *Server) handleTerminalService(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	switch action {
	case "start", "stop", "restart":
		if err := sysd.Action(action, s.Terminal.UnitName()); err != nil {
			s.setFlash(w, err.Error())
		} else {
			s.setFlash(w, "ttyd "+action+" 完成")
		}
	default:
		s.setFlash(w, "未知操作")
	}
	s.redirect(w, r, "/terminal")
}

// terminalUIProxy reverse-proxies /terminal/ui/* (WebSocket included) to the
// localhost-only ttyd listener. The path is forwarded unchanged because ttyd
// runs with -b <base_path>/terminal/ui.
func (s *Server) terminalUIProxy() http.Handler {
	proxy := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = "http"
			r.URL.Host = fmt.Sprintf("127.0.0.1:%d", s.Cfg.Terminal.Port)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "ttyd 未响应（服务可能未运行）", http.StatusBadGateway)
		},
	}
	return s.protect(proxy.ServeHTTP)
}
