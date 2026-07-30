package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kinmeic/NaivePanel/internal/bypasscore"
	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/geo"
	"github.com/kinmeic/NaivePanel/internal/sysd"
)

// handleDashboard shows a service overview.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	bcInstalled := s.Bypass.Installed()
	totpOn, _ := s.Cfg.TOTPState()
	geoCfg := s.Cfg.GeoSnapshot()
	data := map[string]any{
		"Domain":        s.Cfg.Domain,
		"Version":       s.Version,
		"BasePath":      s.Cfg.BasePath,
		"Listen":        s.Cfg.Listen,
		"HostSite":      s.Cfg.GetHostSite(),
		"SiteCount":     len(s.Cfg.SitesSnapshot()),
		"CaddyActive":   sysd.IsActive("caddy"),
		"BypassInstall": bcInstalled,
		"BypassActive":  bcInstalled && sysd.IsActive("bypasscore"),
		"BypassVersion": s.Bypass.Version(),
		"PanelActive":   sysd.IsActive("naivepanel"),
		"Geo":           geo.Stat(geoCfg.Dir),
		"TOTPEnabled":   totpOn,
	}
	s.render(w, r, "dashboard", "仪表盘", data)
}

// handleCaddyReload reloads Caddy with the live on-disk config.
func (s *Server) handleCaddyReload(w http.ResponseWriter, r *http.Request) {
	s.caddyMu.Lock()
	defer s.caddyMu.Unlock()
	if err := s.Caddy.ReloadOnly(); err != nil {
		s.setFlash(w, "Caddy 重载失败: "+err.Error())
	} else {
		s.setFlash(w, "Caddy 已重载")
	}
	s.redirect(w, r, "/caddy/preview")
}

// handleBypass shows BypassCore status and service controls.
func (s *Server) handleBypass(w http.ResponseWriter, r *http.Request) {
	installed := s.Bypass.Installed()
	active := installed && sysd.IsActive("bypasscore")
	data := map[string]any{
		"Installed":   installed,
		"Version":     s.Bypass.Version(),
		"Active":      active,
		"Enabled":     installed && sysd.IsEnabled("bypasscore"),
		"ConfigPath":  s.Cfg.BypassCore.ConfigPath,
		"SocksPort":   s.Cfg.BypassCore.SocksPort,
		"ControlSock": s.Cfg.BypassCore.ControlSock,
	}
	content, configErr := s.Bypass.ReadConfig()
	control, inspectErr := bypasscore.InspectControlConfig(content)
	switch {
	case !installed:
		data["StatusInfo"] = "安装 BypassCore 后可查看运行状态。"
	case !active:
		data["StatusInfo"] = "服务当前未运行，启动后可查看实时状态。"
	case configErr != nil:
		data["StatusError"] = "无法读取配置以检查控制面: " + configErr.Error()
	case inspectErr != nil:
		data["StatusError"] = inspectErr.Error()
	case !control.Enabled:
		data["ControlNeedsEnable"] = true
		data["StatusWarning"] = "服务正在运行，但配置中尚未启用本地控制面。启用后才能查看状态并使用热重载。"
	case control.Socket != s.Cfg.BypassCore.ControlSock:
		data["ControlNeedsEnable"] = true
		data["StatusWarning"] = fmt.Sprintf("控制面 socket 配置为 %q，与面板期望的 %q 不一致。", control.Socket, s.Cfg.BypassCore.ControlSock)
	default:
		status, err := s.Bypass.Status()
		if err != nil {
			data["StatusWarning"] = "控制面已在配置中启用，但 socket 尚不可达。请先重启服务；若仍不可用，请查看 BypassCore 日志。"
			data["StatusDetail"] = err.Error()
			break
		}
		var v any
		if json.Unmarshal([]byte(status), &v) == nil {
			data["Status"] = v
		} else {
			data["StatusRaw"] = string(status)
		}
	}
	s.render(w, r, "bypass", "BypassCore", data)
}

// handleBypassInstall downloads/updates the binary and unit, then enables it.
func (s *Server) handleBypassInstall(w http.ResponseWriter, r *http.Request) {
	tag, err := s.Bypass.Install(s.Cfg.GeoSnapshot().Mirror)
	if err != nil {
		s.setFlash(w, "安装/更新失败: "+err.Error())
		s.redirect(w, r, "/bypasscore")
		return
	}
	// Make sure a minimal config exists so the service can start.
	if cur, err := s.Bypass.ReadConfig(); err != nil || len(cur) == 0 {
		minimal, _, _ := bypasscore.EnsureSocksInbound(nil, s.Cfg.BypassCore.SocksPort)
		minimal, _, _ = bypasscore.EnsureControlPlane(minimal, s.Cfg.BypassCore.ControlSock)
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

// handleBypassControlEnable upgrades an existing config to expose the local
// Unix-socket control API and applies it transactionally.
func (s *Server) handleBypassControlEnable(w http.ResponseWriter, r *http.Request) {
	cur, err := s.Bypass.ReadConfig()
	if err != nil {
		s.setFlash(w, "读取 BypassCore 配置失败: "+err.Error())
		s.redirect(w, r, "/bypasscore")
		return
	}
	next, changed, err := bypasscore.EnsureControlPlane(cur, s.Cfg.BypassCore.ControlSock)
	if err != nil {
		s.setFlash(w, "启用控制面失败: "+err.Error())
		s.redirect(w, r, "/bypasscore")
		return
	}
	if !changed {
		s.setFlash(w, "控制面配置已启用；如 socket 仍不存在，请重启 BypassCore 并查看日志")
		s.redirect(w, r, "/bypasscore")
		return
	}
	if err := s.Bypass.ApplyConfig(next); err != nil {
		s.setFlash(w, "启用控制面失败: "+err.Error())
	} else {
		s.setFlash(w, "控制面已启用并生效")
	}
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
	case "start", "stop", "restart", "enable", "disable":
		if err := sysd.Action(action, "bypasscore"); err != nil {
			s.setFlash(w, err.Error())
		} else {
			switch action {
			case "enable":
				s.setFlash(w, "BypassCore 已开启开机自启")
			case "disable":
				s.setFlash(w, "BypassCore 已关闭开机自启（当前服务不会停止）")
			default:
				s.setFlash(w, "bypasscore "+action+" 完成")
			}
		}
	default:
		s.setFlash(w, "未知操作")
	}
	s.redirect(w, r, "/bypasscore")
}

// handleGeo shows geodata file status.
func (s *Server) handleGeo(w http.ResponseWriter, r *http.Request) {
	geoCfg := s.Cfg.GeoSnapshot()
	s.render(w, r, "geo", "Geo 数据文件", map[string]any{
		"Files": geo.Stat(geoCfg.Dir),
		"Dir":   geoCfg.Dir,
		"Geo":   geoCfg,
	})
}

// handleGeoUpdate downloads and verifies the latest geodata files.
func (s *Server) handleGeoUpdate(w http.ResponseWriter, r *http.Request) {
	geoCfg := s.Cfg.GeoSnapshot()
	if err := geo.Update(geoCfg.Dir, geoCfg.Mirror); err != nil {
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
	mirror := strings.TrimSpace(r.FormValue("mirror"))
	auto := r.FormValue("auto") == "on"
	if err := config.ValidateMirror(mirror); err != nil {
		s.setFlash(w, "保存失败: "+err.Error())
		s.redirect(w, r, "/geo")
		return
	}
	err := s.Cfg.Mutate(func(c *config.Config) error {
		c.Geo.Mirror = mirror
		c.Geo.AutoUpdateWeekly = auto
		return nil
	})
	if err != nil {
		s.setFlash(w, "保存失败: "+err.Error())
	} else {
		s.setFlash(w, "设置已保存（自动更新在面板重启后生效）")
	}
	s.redirect(w, r, "/geo")
}
