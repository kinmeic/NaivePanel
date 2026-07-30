package web

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"net/http"

	"github.com/kinmeic/NaivePanel/internal/auth"
	"github.com/pquerna/otp"
)

// handleSettings shows the settings page.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Domain":         s.Cfg.Domain,
		"BasePath":       s.Cfg.BasePath,
		"Listen":         s.Cfg.Listen,
		"HostSite":       s.Cfg.HostSite,
		"TOTPEnabled":    s.Cfg.TOTPEnabled,
		"RecoveryLeft":   len(s.Cfg.RecoveryHashes),
		"Sites":          s.Cfg.Sites,
		"SocksPort":      s.Cfg.BypassCore.SocksPort,
		"PendingTOTPQR":  "",
		"PendingTOTPSet": false,
	}
	if sess := s.session(r); sess != nil && sess.PendingTOTPSecret != "" {
		qr, err := totpQR(sess.PendingTOTPURL)
		if err == nil {
			data["PendingTOTPQR"] = qr
			data["PendingTOTPURL"] = sess.PendingTOTPURL
			data["PendingTOTPSecret"] = sess.PendingTOTPSecret
			data["PendingTOTPSet"] = true
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
	if !auth.VerifyPassword(cur, s.Cfg.AdminPassHash) {
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
	s.Cfg.AdminPassHash = h
	if err := s.Cfg.Save(); err != nil {
		s.setFlash(w, "保存失败: "+err.Error())
	} else {
		s.setFlash(w, "密码已修改")
	}
	s.redirect(w, r, "/settings")
}

// handleTOTPSetup starts MFA enrollment: generates a secret kept in the
// session until confirmed.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.TOTPEnabled {
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
	sess.PendingTOTPSecret = secret
	sess.PendingTOTPURL = otpauthURL
	s.redirect(w, r, "/settings")
}

// handleTOTPConfirm verifies the first code and enables MFA, issuing
// one-time recovery codes shown exactly once.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	if sess == nil || sess.PendingTOTPSecret == "" {
		s.redirect(w, r, "/settings")
		return
	}
	code := r.FormValue("code")
	if !auth.VerifyTOTP(sess.PendingTOTPSecret, code) {
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
	s.Cfg.TOTPSecret = sess.PendingTOTPSecret
	s.Cfg.TOTPEnabled = true
	s.Cfg.RecoveryHashes = hashes
	if err := s.Cfg.Save(); err != nil {
		s.setFlash(w, "保存失败: "+err.Error())
		s.redirect(w, r, "/settings")
		return
	}
	sess.PendingTOTPSecret = ""
	s.render(w, r, "recovery", "恢复码（仅显示一次）", map[string]any{"Codes": codes})
}

// handleTOTPDisable turns MFA off after password + current TOTP check.
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	if !auth.VerifyPassword(r.FormValue("password"), s.Cfg.AdminPassHash) {
		s.setFlash(w, "密码错误")
		s.redirect(w, r, "/settings")
		return
	}
	if !auth.VerifyTOTP(s.Cfg.TOTPSecret, r.FormValue("code")) {
		s.setFlash(w, "TOTP 验证码错误")
		s.redirect(w, r, "/settings")
		return
	}
	s.Cfg.TOTPEnabled = false
	s.Cfg.TOTPSecret = ""
	s.Cfg.RecoveryHashes = nil
	if err := s.Cfg.Save(); err != nil {
		s.setFlash(w, "保存失败: "+err.Error())
	} else {
		s.setFlash(w, "MFA 已关闭")
	}
	s.redirect(w, r, "/settings")
}

// handleHostSite migrates panel hosting to another site.
func (s *Server) handleHostSite(w http.ResponseWriter, r *http.Request) {
	target := r.FormValue("host_site")
	if s.Cfg.FindSite(target) < 0 {
		s.setFlash(w, "目标站点不存在")
		s.redirect(w, r, "/settings")
		return
	}
	old := s.Cfg.HostSite
	s.Cfg.HostSite = target
	if err := s.Cfg.Save(); err != nil {
		s.Cfg.HostSite = old
		s.setFlash(w, "保存失败: "+err.Error())
		s.redirect(w, r, "/settings")
		return
	}
	if err := s.Caddy.Apply(); err != nil {
		s.Cfg.HostSite = old
		_ = s.Cfg.Save()
		s.setFlash(w, "迁移失败已回滚: "+err.Error())
		s.redirect(w, r, "/settings")
		return
	}
	s.setFlash(w, "面板寄宿站点已迁移到 " + target + "，请记住新的访问地址")
	s.redirect(w, r, "/settings")
}
