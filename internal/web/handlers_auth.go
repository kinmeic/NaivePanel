package web

import (
	"net"
	"net/http"
	"time"

	"github.com/kinmeic/NaivePanel/internal/auth"
)

// handleLoginGET shows the login form.
func (s *Server) handleLoginGET(w http.ResponseWriter, r *http.Request) {
	if sess := s.session(r); sess != nil && !sess.PendingMFA {
		s.redirect(w, r, "/")
		return
	}
	s.render(w, r, "login", "登录", map[string]any{"Error": r.URL.Query().Get("err")})
}

// handleLoginPOST verifies username + password, then either completes login
// or moves to the TOTP second step.
func (s *Server) handleLoginPOST(w http.ResponseWriter, r *http.Request) {
	user := r.FormValue("username")
	pass := r.FormValue("password")
	key := user + "|" + clientIP(r)
	if !s.Limiter.Allow(key) {
		s.render(w, r, "login", "登录", map[string]any{"Error": "失败次数过多，请 15 分钟后再试"})
		return
	}
	ok := user == s.Cfg.AdminUser && auth.VerifyPassword(pass, s.Cfg.AdminPassHash)
	if !ok {
		s.Limiter.Fail(key)
		s.render(w, r, "login", "登录", map[string]any{"Error": "账号或密码错误"})
		return
	}
	s.Limiter.Success(key)
	pending := auth.TOTPEnabled(s.Cfg.TOTPEnabled, s.Cfg.TOTPSecret)
	tok, sess, err := s.Sessions.Create(user, pending)
	if err != nil {
		http.Error(w, "会话创建失败", http.StatusInternalServerError)
		return
	}
	auth.SetCookie(w, s.Cfg.BasePath, tok, int(time.Duration(s.Cfg.SessionTTLHours)*time.Hour/time.Second))
	_ = sess
	if pending {
		s.redirect(w, r, "/login/totp")
		return
	}
	s.redirect(w, r, "/")
}

// handleTOTPGET shows the second-factor form for pending sessions.
func (s *Server) handleTOTPGET(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	if sess == nil || !sess.PendingMFA {
		s.redirect(w, r, "/login")
		return
	}
	s.render(w, r, "totp", "二次验证", map[string]any{"Error": r.URL.Query().Get("err")})
}

// handleTOTPPOST verifies a TOTP code or a one-time recovery code.
func (s *Server) handleTOTPPOST(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	if sess == nil || !sess.PendingMFA {
		s.redirect(w, r, "/login")
		return
	}
	code := r.FormValue("code")
	if auth.VerifyTOTP(s.Cfg.TOTPSecret, code) {
		s.Sessions.Promote(auth.TokenFromRequest(r))
		s.redirect(w, r, "/")
		return
	}
	if rest, ok := auth.ConsumeRecovery(s.Cfg.RecoveryHashes, code); ok {
		s.Cfg.RecoveryHashes = rest
		_ = s.Cfg.Save()
		s.Sessions.Promote(auth.TokenFromRequest(r))
		s.setFlash(w, "已使用恢复码登录，剩余恢复码请妥善保管")
		s.redirect(w, r, "/")
		return
	}
	s.render(w, r, "totp", "二次验证", map[string]any{"Error": "验证码错误"})
}

// handleLogout destroys the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := auth.TokenFromRequest(r); tok != "" {
		s.Sessions.Destroy(tok)
	}
	auth.ClearCookie(w, s.Cfg.BasePath)
	s.redirect(w, r, "/login")
}

// clientIP returns the direct peer address (the panel only listens on
// loopback behind Caddy, so this is informational only).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
