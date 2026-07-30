package web

import (
	"encoding/json"
	"net/http"

	"github.com/kinmeic/NaivePanel/internal/bypasscore"
	"github.com/kinmeic/NaivePanel/internal/geo"
	"github.com/kinmeic/NaivePanel/internal/sysd"
)

// handleDashboard shows a service overview.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	bcInstalled := s.Bypass.Installed()
	data := map[string]any{
		"Domain":        s.Cfg.Domain,
		"BasePath":      s.Cfg.BasePath,
		"HostSite":      s.Cfg.HostSite,
		"SiteCount":     len(s.Cfg.Sites),
		"CaddyActive":   sysd.IsActive("caddy"),
		"BypassInstall": bcInstalled,
		"BypassActive":  bcInstalled && sysd.IsActive("bypasscore"),
		"BypassVersion": s.Bypass.Version(),
		"PanelActive":   sysd.IsActive("naivepanel"),
		"Geo":           geo.Stat(s.Cfg.Geo.Dir),
		"TOTPEnabled":   s.Cfg.TOTPEnabled,
	}
	s.render(w, r, "dashboard", "仪表盘", data)
}

// handleCaddyPreview shows the merged Caddy configuration.
func (s *Server) handleCaddyPreview(w http.ResponseWriter, r *http.Request) {
	preview, err := s.Caddy.Preview()
	data := map[string]any{"Preview": preview}
	if err != nil {
		data["Error"] = err.Error()
	}
	s.render(w, r, "caddy_preview", "Caddy 配置预览", data)
}

// handleCaddyReload reloads Caddy with the live on-disk config.
func (s *Server) handleCaddyReload(w http.ResponseWriter, r *http.Request) {
	if err := s.Caddy.ReloadOnly(); err != nil {
		s.setFlash(w, "Caddy 重载失败: "+err.Error())
	} else {
		s.setFlash(w, "Caddy 已重载")
	}
	s.redirect(w, r, "/caddy/preview")
}

// handleBypass shows BypassCore status and service controls.
func (s *Server) handleBypass(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Installed":  s.Bypass.Installed(),
		"Version":    s.Bypass.Version(),
		"Active":     sysd.IsActive("bypasscore"),
		"ConfigPath": s.Cfg.BypassCore.ConfigPath,
		"SocksPort":  s.Cfg.BypassCore.SocksPort,
	}
	if status, err := s.Bypass.Status(); err == nil {
		var v any
		if json.Unmarshal([]byte(status), &v) == nil {
			data["Status"] = v
		} else {
			data["StatusRaw"] = string(status)
		}
	} else {
		data["StatusError"] = err.Error()
	}
	s.render(w, r, "bypass", "BypassCore", data)
}

// handleBypassInstall downloads/updates the binary and unit, then enables it.
func (s *Server) handleBypassInstall(w http.ResponseWriter, r *http.Request) {
	tag, err := s.Bypass.Install(s.Cfg.Geo.Mirror)
	if err != nil {
		s.setFlash(w, "安装/更新失败: "+err.Error())
		s.redirect(w, r, "/bypasscore")
		return
	}
	// Make sure a minimal config exists so the service can start.
	if cur, err := s.Bypass.ReadConfig(); err != nil || len(cur) == 0 {
		minimal, _, _ := bypasscore.EnsureSocksInbound(nil, s.Cfg.BypassCore.SocksPort)
		if err := s.Bypass.ApplyConfig(minimal); err != nil {
			s.setFlash(w, "已安装 "+tag+"，但写入初始配置失败: "+err.Error())
			s.redirect(w, r, "/bypasscore")
			return
		}
	}
	if err := sysd.Action("enable", "bypasscore"); err != nil {
		s.setFlash(w, "已安装 "+tag+"，但设置开机启动失败: "+err.Error())
		s.redirect(w, r, "/bypasscore")
		return
	}
	if err := sysd.Action("restart", "bypasscore"); err != nil {
		s.setFlash(w, "已安装 "+tag+"，但启动失败（请检查配置）: "+err.Error())
		s.redirect(w, r, "/bypasscore")
		return
	}
	s.setFlash(w, "BypassCore "+tag+" 安装完成并已启动")
	s.redirect(w, r, "/bypasscore")
}

// handleBypassConfigGET shows the raw config editor.
func (s *Server) handleBypassConfigGET(w http.ResponseWriter, r *http.Request) {
	content, err := s.Bypass.ReadConfig()
	data := map[string]any{"Config": string(content)}
	if err != nil {
		data["Config"] = ""
		data["Error"] = "读取配置失败: " + err.Error()
	}
	s.render(w, r, "bypass_config", "BypassCore 配置", data)
}

// handleBypassConfigPOST applies a new config through the validate pipeline.
func (s *Server) handleBypassConfigPOST(w http.ResponseWriter, r *http.Request) {
	content := r.FormValue("config")
	if err := s.Bypass.ApplyConfig([]byte(content)); err != nil {
		s.render(w, r, "bypass_config", "BypassCore 配置", map[string]any{
			"Config": content,
			"Error":  err.Error(),
		})
		return
	}
	s.setFlash(w, "配置已校验并生效（热重载或按需重启）")
	s.redirect(w, r, "/bypasscore")
}

// handleBypassService runs start/stop/restart.
func (s *Server) handleBypassService(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	switch action {
	case "start", "stop", "restart":
		if err := sysd.Action(action, "bypasscore"); err != nil {
			s.setFlash(w, err.Error())
		} else {
			s.setFlash(w, "bypasscore "+action+" 完成")
		}
	default:
		s.setFlash(w, "未知操作")
	}
	s.redirect(w, r, "/bypasscore")
}

// handleGeo shows geodata file status.
func (s *Server) handleGeo(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "geo", "Geo 数据文件", map[string]any{
		"Files": geo.Stat(s.Cfg.Geo.Dir),
		"Dir":   s.Cfg.Geo.Dir,
		"Geo":   s.Cfg.Geo,
	})
}

// handleGeoUpdate downloads and verifies the latest geodata files.
func (s *Server) handleGeoUpdate(w http.ResponseWriter, r *http.Request) {
	if err := geo.Update(s.Cfg.Geo.Dir, s.Cfg.Geo.Mirror); err != nil {
		s.setFlash(w, "更新失败: "+err.Error())
		s.redirect(w, r, "/geo")
		return
	}
	// Best-effort hot reload so BypassCore picks up the new data.
	if s.Bypass.Installed() {
		if cur, err := s.Bypass.ReadConfig(); err == nil && len(cur) > 0 {
			_ = s.Bypass.ApplyConfig(cur)
		}
	}
	s.setFlash(w, "Geo 数据文件已更新")
	s.redirect(w, r, "/geo")
}

// handleGeoSettings saves mirror / auto-update preferences.
func (s *Server) handleGeoSettings(w http.ResponseWriter, r *http.Request) {
	s.Cfg.Geo.Mirror = r.FormValue("mirror")
	s.Cfg.Geo.AutoUpdateWeekly = r.FormValue("auto") == "on"
	if err := s.Cfg.Save(); err != nil {
		s.setFlash(w, "保存失败: "+err.Error())
	} else {
		s.setFlash(w, "设置已保存（自动更新在面板重启后生效）")
	}
	s.redirect(w, r, "/geo")
}
