package web

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"net/http"
	"time"

	"github.com/kinmeic/NaivePanel/internal/auth"
	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/selfupdate"
	"github.com/pquerna/otp"
)

// handleSettings shows the settings page.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	totpOn, _ := s.Cfg.TOTPState()
	bypassInstalled := s.Bypass.Installed()
	bypassVersion := ""
	if bypassInstalled {
		bypassVersion = s.Bypass.Version()
	}
	data := map[string]any{
		"HostSite":       s.Cfg.GetHostSite(),
		"TOTPEnabled":    totpOn,
		"RecoveryLeft":   s.Cfg.RecoveryCount(),
		"Sites":          s.Cfg.SitesSnapshot(),
		"Version":        s.Version,
		"AutoUpdate":     s.Cfg.SelfUpdateEnabled(),
		"PendingTOTPQR":  "",
		"PendingTOTPSet": false,
		"Bypass": map[string]any{
			"Installed":  bypassInstalled,
			"Version":    bypassVersion,
			"BinPath":    s.Cfg.BypassCore.BinPath,
			"ConfigPath": s.Cfg.BypassCore.ConfigPath,
			"SocksPort":  s.Cfg.BypassCore.SocksPort,
		},
	}
	if sess := s.session(r); sess != nil {
		if secret, otpauthURL := sess.PendingTOTP(); secret != "" {
			if qr, err := totpQR(otpauthURL); err == nil {
				data["PendingTOTPQR"] = qr
				data["PendingTOTPURL"] = otpauthURL
				data["PendingTOTPSecret"] = secret
				data["PendingTOTPSet"] = true
			}
		}
	}
	s.render(w, r, "settings", "设置", data)
}

// totpQR renders the otpauth URL as a PNG data URI.
func totpQR(otpauthURL string) (string, error) {
	key, err := otp.NewKeyFromURL(otpauthURL)
	if err != nil {
		return "", err
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// handlePasswordChange changes the admin password.
func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	cur := r.FormValue("current")
	new1 := r.FormValue("new1")
	new2 := r.FormValue("new2")
	if !auth.VerifyPassword(cur, s.Cfg.GetAdminPassHash()) {
		s.setFlash(w, "当前密码错误")
		s.redirect(w, r, "/settings")
		return
	}
	if len(new1) < 10 {
		s.setFlash(w, "新密码长度至少 10 位")
		s.redirect(w, r, "/settings")
		return
	}
	if new1 != new2 {
		s.setFlash(w, "两次输入的新密码不一致")
		s.redirect(w, r, "/settings")
		return
	}
	h, err := auth.HashPassword(new1)
	if err != nil {
		s.setFlash(w, "密码哈希失败")
		s.redirect(w, r, "/settings")
		return
	}
	err = s.Cfg.Mutate(func(c *config.Config) error {
		c.AdminPassHash = h
		return nil
	})
	if err != nil {
		s.setFlash(w, "保存失败: "+err.Error())
	} else {
		// Kick every other session: they authenticated with the old password.
		s.Sessions.DestroyAll(auth.TokenFromRequest(r))
		s.setFlash(w, "密码已修改，其他登录会话已失效")
	}
	s.redirect(w, r, "/settings")
}

// handleTOTPSetup starts MFA enrollment: generates a secret kept in the
// session until confirmed.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	if totpOn, _ := s.Cfg.TOTPState(); totpOn {
		s.setFlash(w, "MFA 已启用，如需重置请先关闭")
		s.redirect(w, r, "/settings")
		return
	}
	secret, otpauthURL, err := auth.GenerateTOTP("NaivePanel", s.Cfg.AdminUser)
	if err != nil {
		s.setFlash(w, "生成 TOTP 密钥失败")
		s.redirect(w, r, "/settings")
		return
	}
	sess := s.session(r)
	if sess == nil {
		s.redirect(w, r, "/login")
		return
	}
	sess.SetPendingTOTP(secret, otpauthURL)
	s.redirect(w, r, "/settings")
}

// handleTOTPConfirm verifies the first code and enables MFA, issuing
// one-time recovery codes shown exactly once.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	if sess == nil {
		s.redirect(w, r, "/settings")
		return
	}
	secret, _ := sess.PendingTOTP()
	if secret == "" {
		s.redirect(w, r, "/settings")
		return
	}
	code := r.FormValue("code")
	if !auth.VerifyTOTP(secret, code) {
		s.setFlash(w, "验证码错误，请重试")
		s.redirect(w, r, "/settings")
		return
	}
	codes, hashes, err := auth.GenerateRecoveryCodes(10)
	if err != nil {
		s.setFlash(w, "生成恢复码失败")
		s.redirect(w, r, "/settings")
		return
	}
	err = s.Cfg.Mutate(func(c *config.Config) error {
		c.TOTPSecret = secret
		c.TOTPEnabled = true
		c.RecoveryHashes = hashes
		return nil
	})
	if err != nil {
		s.setFlash(w, "保存失败: "+err.Error())
		s.redirect(w, r, "/settings")
		return
	}
	sess.ClearPendingTOTP()
	s.render(w, r, "recovery", "恢复码（仅显示一次）", map[string]any{"Codes": codes})
}

// handleTOTPDisable turns MFA off after password + current TOTP check.
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	if !auth.VerifyPassword(r.FormValue("password"), s.Cfg.GetAdminPassHash()) {
		s.setFlash(w, "密码错误")
		s.redirect(w, r, "/settings")
		return
	}
	_, secret := s.Cfg.TOTPState()
	if !auth.VerifyTOTP(secret, r.FormValue("code")) {
		s.setFlash(w, "TOTP 验证码错误")
		s.redirect(w, r, "/settings")
		return
	}
	err := s.Cfg.Mutate(func(c *config.Config) error {
		c.TOTPEnabled = false
		c.TOTPSecret = ""
		c.RecoveryHashes = nil
		return nil
	})
	if err != nil {
		s.setFlash(w, "保存失败: "+err.Error())
	} else {
		// MFA requirements changed; other sessions re-authenticate.
		s.Sessions.DestroyAll(auth.TokenFromRequest(r))
		s.setFlash(w, "MFA 已关闭")
	}
	s.redirect(w, r, "/settings")
}

// handleHostSite migrates panel hosting to another site.
func (s *Server) handleHostSite(w http.ResponseWriter, r *http.Request) {
	target := r.FormValue("host_site")
	s.caddyMu.Lock()
	defer s.caddyMu.Unlock()
	if s.Cfg.FindSite(target) < 0 {
		s.setFlash(w, "目标站点不存在")
		s.redirect(w, r, "/settings")
		return
	}
	old := s.Cfg.GetHostSite()
	err := s.Cfg.Mutate(func(c *config.Config) error {
		c.HostSite = target
		return nil
	})
	if err != nil {
		s.setFlash(w, "保存失败: "+err.Error())
		s.redirect(w, r, "/settings")
		return
	}
	if err := s.Caddy.Apply(); err != nil {
		_ = s.Cfg.Mutate(func(c *config.Config) error {
			c.HostSite = old
			return nil
		})
		s.setFlash(w, "迁移失败已回滚: "+err.Error())
		s.redirect(w, r, "/settings")
		return
	}
	s.setFlash(w, "面板寄宿站点已迁移到 "+target+"，请记住新的访问地址")
	s.redirect(w, r, "/settings")
}

// handleSelfUpdateCheck queries GitHub for the latest release and reports
// how it compares to the running version.
func (s *Server) handleSelfUpdateCheck(w http.ResponseWriter, r *http.Request) {
	rel, err := selfupdate.Latest(s.Cfg.GeoSnapshot().Mirror)
	if err != nil {
		s.setFlash(w, "检查更新失败: "+err.Error())
		s.redirect(w, r, "/settings")
		return
	}
	if selfupdate.Newer(s.Version, rel.TagName) {
		s.setFlash(w, fmt.Sprintf("发现新版本 %s（当前 %s），可点击「立即更新」升级", rel.TagName, s.Version))
	} else {
		s.setFlash(w, fmt.Sprintf("已是最新版本（当前 %s，最新 %s）", s.Version, rel.TagName))
	}
	s.redirect(w, r, "/settings")
}

// handleSelfUpdate toggles auto-update (action=toggle) or installs the
// latest release right away (action=apply). A successful install schedules a
// service restart after the response is sent.
func (s *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	switch r.FormValue("action") {
	case "toggle":
		on := r.FormValue("auto_update") == "on"
		if err := s.Cfg.Mutate(func(c *config.Config) error {
			c.AutoUpdate = on
			return nil
		}); err != nil {
			s.setFlash(w, "保存失败: "+err.Error())
		} else if on {
			s.setFlash(w, "已开启自动更新：每天检查一次，发现新版本会自动升级并重启面板（面板重启后生效）")
		} else {
			s.setFlash(w, "已关闭自动更新（面板重启后生效）")
		}
	case "apply":
		tag, err := selfupdate.Apply(s.Cfg.GeoSnapshot().Mirror)
		if err != nil {
			s.setFlash(w, "更新失败: "+err.Error())
			s.redirect(w, r, "/settings")
			return
		}
		s.setFlash(w, "已更新到 "+tag+"，面板即将重启，请稍候刷新页面")
		s.redirect(w, r, "/settings")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			time.Sleep(2 * time.Second)
			selfupdate.RestartSelf()
		}()
		return
	default:
		s.setFlash(w, "未知操作")
	}
	s.redirect(w, r, "/settings")
}
