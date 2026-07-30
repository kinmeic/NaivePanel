package web

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kinmeic/NaivePanel/internal/auth"
)

// dummyPassHash is a valid argon2id hash of a non-password. Login verifies
// against it when the username is wrong so the response time does not
// reveal whether the account exists.
var dummyPassHash = func() string {
	h, err := auth.HashPassword("dummy-password-for-constant-time-login")
	if err != nil {
		panic(err)
	}
	return h
}()

// handleLoginGET shows the login form.
func (s *Server) handleLoginGET(w http.ResponseWriter, r *http.Request) {
	if sess := s.session(r); sess != nil && !sess.IsPendingMFA() {
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
	// Always run the argon2 verification, even for unknown users, so the
	// timing does not disclose whether the username exists.
	hash := s.Cfg.GetAdminPassHash()
	userOK := subtleStringEq(user, s.Cfg.AdminUser)
	if !userOK {
		hash = dummyPassHash
	}
	passOK := auth.VerifyPassword(pass, hash)
	if !userOK || !passOK {
		s.Limiter.Fail(key)
		s.render(w, r, "login", "登录", map[string]any{"Error": "账号或密码错误"})
		return
	}
	s.Limiter.Success(key)
	totpOn, secret := s.Cfg.TOTPState()
	pending := auth.TOTPEnabled(totpOn, secret)
	tok, _, err := s.Sessions.Create(user, pending)
	if err != nil {
		http.Error(w, "会话创建失败", http.StatusInternalServerError)
		return
	}
	auth.SetCookie(w, s.Cfg.BasePath, tok, int(time.Duration(s.Cfg.SessionTTLHours)*time.Hour/time.Second))
	if pending {
		s.redirect(w, r, "/login/totp")
		return
	}
	s.redirect(w, r, "/")
}

// handleTOTPGET shows the second-factor form for pending sessions.
func (s *Server) handleTOTPGET(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	if sess == nil || !sess.IsPendingMFA() {
		s.redirect(w, r, "/login")
		return
	}
	s.render(w, r, "totp", "二次验证", map[string]any{"Error": r.URL.Query().Get("err")})
}

// handleTOTPPOST verifies a TOTP code or a one-time recovery code.
// Failed attempts are limited per session; exhausting them destroys the
// session and forces a fresh password login.
func (s *Server) handleTOTPPOST(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	if sess == nil || !sess.IsPendingMFA() {
		s.redirect(w, r, "/login")
		return
	}
	code := r.FormValue("code")
	_, secret := s.Cfg.TOTPState()
	if auth.VerifyTOTP(secret, code) {
		sess.Promote()
		s.redirect(w, r, "/")
		return
	}
	if ok, err := s.Cfg.ConsumeRecoveryHash(auth.HashRecovery(code)); ok && err == nil {
		sess.Promote()
		s.setFlash(w, "已使用恢复码登录，剩余恢复码请妥善保管")
		s.redirect(w, r, "/")
		return
	}
	if sess.IncrMFAFail() {
		// Too many guesses: kill the pending session, require password again.
		s.Sessions.Destroy(auth.TokenFromRequest(r))
		auth.ClearCookie(w, s.Cfg.BasePath)
		s.setFlash(w, "二次验证失败次数过多，请重新登录")
		s.redirect(w, r, "/login")
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

// subtleStringEq compares two strings in (length-dependent) constant time.
func subtleStringEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// clientIP returns the real client address. The panel is only reachable
// through Caddy (enforced by the proxyGate shared secret). Caddy's
// reverse_proxy appends the true client IP to X-Forwarded-For, so the LAST
// entry is trustworthy — earlier entries may be client-spoofed.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
