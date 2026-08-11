package web

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"net/http"
	"time"

	"github.com/kinmeic/NaivePanel/internal/auth"
	"github.com/kinmeic/NaivePanel/internal/bypasscore"
	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/selfupdate"
	"github.com/pquerna/otp"
)

// handleSettings shows application installation and update management.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	bypassInstalled := s.Bypass.Installed()
	bypassVersion := ""
	if bypassInstalled {
		bypassVersion = s.Bypass.VersionTag()
	}
	data := map[string]any{
		"Version":    s.Version,
		"AutoUpdate": s.Cfg.SelfUpdateEnabled(),
		"Geo":        s.Cfg.GeoSnapshot(),
		"Bypass": map[string]any{
			"Installed":  bypassInstalled,
			"Version":    bypassVersion,
			"BinPath":    s.Cfg.BypassCore.BinPath,
			"ConfigPath": s.Cfg.BypassCore.ConfigPath,
		},
	}
	s.render(w, r, "settings", "应用管理", data)
}

// handleSecurity shows password and MFA controls.
func (s *Server) handleSecurity(w http.ResponseWriter, r *http.Request) {
	totpOn, _ := s.Cfg.TOTPState()
	data := map[string]any{
		"TOTPEnabled":    totpOn,
		"RecoveryLeft":   s.Cfg.RecoveryCount(),
		"PendingTOTPQR":  "",
		"PendingTOTPSet": false,
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
	s.render(w, r, "security", "账号安全", data)
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
		s.redirect(w, r, "/security")
		return
	}
	if len(new1) < 10 {
		s.setFlash(w, "新密码长度至少 10 位")
		s.redirect(w, r, "/security")
		return
	}
	if new1 != new2 {
		s.setFlash(w, "两次输入的新密码不一致")
		s.redirect(w, r, "/security")
		return
	}
	h, err := auth.HashPassword(new1)
	if err != nil {
		s.setFlash(w, "密码哈希失败")
		s.redirect(w, r, "/security")
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
	s.redirect(w, r, "/security")
}

// handleTOTPSetup starts MFA enrollment: generates a secret kept in the
// session until confirmed.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	if totpOn, _ := s.Cfg.TOTPState(); totpOn {
		s.setFlash(w, "MFA 已启用，如需重置请先关闭")
		s.redirect(w, r, "/security")
		return
	}
	secret, otpauthURL, err := auth.GenerateTOTP("NaivePanel", s.Cfg.AdminUser)
	if err != nil {
		s.setFlash(w, "生成 TOTP 密钥失败")
		s.redirect(w, r, "/security")
		return
	}
	sess := s.session(r)
	if sess == nil {
		s.redirect(w, r, "/login")
		return
	}
	sess.SetPendingTOTP(secret, otpauthURL)
	s.redirect(w, r, "/security")
}

// handleTOTPConfirm verifies the first code and enables MFA, issuing
// one-time recovery codes shown exactly once.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	if sess == nil {
		s.redirect(w, r, "/security")
		return
	}
	secret, _ := sess.PendingTOTP()
	if secret == "" {
		s.redirect(w, r, "/security")
		return
	}
	code := r.FormValue("code")
	if !auth.VerifyTOTP(secret, code) {
		s.setFlash(w, "验证码错误，请重试")
		s.redirect(w, r, "/security")
		return
	}
	codes, hashes, err := auth.GenerateRecoveryCodes(10)
	if err != nil {
		s.setFlash(w, "生成恢复码失败")
		s.redirect(w, r, "/security")
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
		s.redirect(w, r, "/security")
		return
	}
	sess.ClearPendingTOTP()
	s.render(w, r, "recovery", "恢复码（仅显示一次）", map[string]any{"Codes": codes})
}

// handleTOTPDisable turns MFA off after password + current TOTP check.
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	if !auth.VerifyPassword(r.FormValue("password"), s.Cfg.GetAdminPassHash()) {
		s.setFlash(w, "密码错误")
		s.redirect(w, r, "/security")
		return
	}
	_, secret := s.Cfg.TOTPState()
	if !auth.VerifyTOTP(secret, r.FormValue("code")) {
		s.setFlash(w, "TOTP 验证码错误")
		s.redirect(w, r, "/security")
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
	s.redirect(w, r, "/security")
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

// handleBypassUpdateCheck compares the installed BypassCore version with the
// latest published release without changing the installed binary.
func (s *Server) handleBypassUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !s.Bypass.Installed() {
		s.setFlash(w, "BypassCore 尚未安装")
		s.redirect(w, r, "/settings")
		return
	}
	rel, err := bypasscore.LatestRelease(s.Cfg.GeoSnapshot().Mirror)
	if err != nil {
		s.setFlash(w, "检查 BypassCore 更新失败: "+err.Error())
		s.redirect(w, r, "/settings")
		return
	}
	current := s.Bypass.VersionTag()
	if current == "" {
		s.setFlash(w, fmt.Sprintf("最新 BypassCore 版本为 %s；当前版本号无法识别", rel.TagName))
	} else if selfupdate.Newer(current, rel.TagName) {
		s.setFlash(w, fmt.Sprintf("发现 BypassCore 新版本 %s（当前 %s），可点击「立即更新」升级", rel.TagName, current))
	} else {
		s.setFlash(w, fmt.Sprintf("BypassCore 已是最新版本（当前 %s，最新 %s）", current, rel.TagName))
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
		// protect wraps the writer to record the operation status. A response
		// controller follows Unwrap(), so the redirect is still flushed before
		// the process restarts.
		_ = http.NewResponseController(w).Flush()
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
