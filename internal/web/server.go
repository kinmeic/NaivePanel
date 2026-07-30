// Package web implements the panel HTTP interface.
package web

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kinmeic/NaivePanel/internal/auth"
	"github.com/kinmeic/NaivePanel/internal/bypasscore"
	"github.com/kinmeic/NaivePanel/internal/caddymgr"
	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/sites"
)

//go:embed ui/templates ui/static
var uiFS embed.FS

// Server serves the panel UI under the configured base path.
type Server struct {
	Cfg      *config.Config
	Caddy    *caddymgr.Manager
	Bypass   *bypasscore.Manager
	Sessions *auth.Store
	Limiter  *auth.Limiter

	pages map[string]*template.Template
	mux   *http.ServeMux
}

// pageData is the common template payload.
type pageData struct {
	Base  string
	CSRF  string
	User  string
	Flash string
	Title string
	Data  any
}

// New builds the server and its routes.
func New(cfg *config.Config) (*Server, error) {
	s := &Server{
		Cfg:      cfg,
		Caddy:    caddymgr.New(cfg),
		Bypass:   bypasscore.New(cfg),
		Sessions: auth.NewStore(time.Duration(cfg.SessionTTLHours) * time.Hour),
		Limiter:  auth.NewLimiter(5, 15*time.Minute),
		pages:    map[string]*template.Template{},
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

func (s *Server) parseTemplates() error {
	funcs := template.FuncMap{
		"prettyJSON": func(v any) string {
			b, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				return fmt.Sprint(v)
			}
			return string(b)
		},
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return "-"
			}
			return t.Format("2006-01-02 15:04:05")
		},
	}
	entries, err := fs.ReadDir(uiFS, "ui/templates")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".html") || name == "layout.html" {
			continue
		}
		page := strings.TrimSuffix(name, ".html")
		files := []string{"ui/templates/" + name}
		if page != "login" && page != "totp" {
			files = append([]string{"ui/templates/layout.html"}, files...)
		}
		t, err := template.New(page).Funcs(funcs).ParseFS(uiFS, files...)
		if err != nil {
			return fmt.Errorf("解析模板 %s: %w", name, err)
		}
		s.pages[page] = t
	}
	return nil
}

func (s *Server) routes() {
	bp := s.Cfg.BasePath
	s.mux = http.NewServeMux()

	// Static assets (no auth needed: they carry no secrets).
	staticSub, _ := fs.Sub(uiFS, "ui/static")
	s.mux.Handle("GET "+bp+"/static/", http.StripPrefix(bp+"/static/", http.FileServer(http.FS(staticSub))))

	s.mux.HandleFunc("GET "+bp, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, bp+"/", http.StatusMovedPermanently)
	})

	s.mux.HandleFunc("GET "+bp+"/login", s.handleLoginGET)
	s.mux.HandleFunc("POST "+bp+"/login", s.handleLoginPOST)
	s.mux.HandleFunc("GET "+bp+"/login/totp", s.handleTOTPGET)
	s.mux.HandleFunc("POST "+bp+"/login/totp", s.handleTOTPPOST)
	s.mux.HandleFunc("POST "+bp+"/logout", s.protect(s.handleLogout))

	s.mux.HandleFunc("GET "+bp+"/{$}", s.protect(s.handleDashboard))

	s.mux.HandleFunc("GET "+bp+"/sites", s.protect(s.handleSites))
	s.mux.HandleFunc("GET "+bp+"/sites/new", s.protect(s.handleSiteNew))
	s.mux.HandleFunc("POST "+bp+"/sites/new", s.protect(s.handleSiteCreate))
	s.mux.HandleFunc("GET "+bp+"/sites/{domain}/edit", s.protect(s.handleSiteEdit))
	s.mux.HandleFunc("POST "+bp+"/sites/{domain}/edit", s.protect(s.handleSiteUpdate))
	s.mux.HandleFunc("POST "+bp+"/sites/{domain}/delete", s.protect(s.handleSiteDelete))
	s.mux.HandleFunc("POST "+bp+"/sites/preview-render", s.protect(s.handleSitePreviewRender))
	s.mux.HandleFunc("POST "+bp+"/sites/import", s.protect(s.handleSiteImport))
	s.mux.HandleFunc("POST "+bp+"/sites/{domain}/sync-disk", s.protect(s.handleSiteSyncDisk))
	s.mux.HandleFunc("POST "+bp+"/sites/{domain}/sync-model", s.protect(s.handleSiteSyncModel))

	s.mux.HandleFunc("GET "+bp+"/caddy/preview", s.protect(s.handleCaddyPreview))
	s.mux.HandleFunc("POST "+bp+"/caddy/reload", s.protect(s.handleCaddyReload))

	s.mux.HandleFunc("GET "+bp+"/bypasscore", s.protect(s.handleBypass))
	s.mux.HandleFunc("POST "+bp+"/bypasscore/install", s.protect(s.handleBypassInstall))
	s.mux.HandleFunc("GET "+bp+"/bypasscore/config", s.protect(s.handleBypassConfigGET))
	s.mux.HandleFunc("POST "+bp+"/bypasscore/config", s.protect(s.handleBypassConfigPOST))
	s.mux.HandleFunc("POST "+bp+"/bypasscore/service", s.protect(s.handleBypassService))

	s.mux.HandleFunc("GET "+bp+"/geo", s.protect(s.handleGeo))
	s.mux.HandleFunc("POST "+bp+"/geo/update", s.protect(s.handleGeoUpdate))
	s.mux.HandleFunc("POST "+bp+"/geo/settings", s.protect(s.handleGeoSettings))

	s.mux.HandleFunc("GET "+bp+"/settings", s.protect(s.handleSettings))
	s.mux.HandleFunc("POST "+bp+"/settings/password", s.protect(s.handlePasswordChange))
	s.mux.HandleFunc("POST "+bp+"/settings/totp/setup", s.protect(s.handleTOTPSetup))
	s.mux.HandleFunc("POST "+bp+"/settings/totp/confirm", s.protect(s.handleTOTPConfirm))
	s.mux.HandleFunc("POST "+bp+"/settings/totp/disable", s.protect(s.handleTOTPDisable))
	s.mux.HandleFunc("POST "+bp+"/settings/hostsite", s.protect(s.handleHostSite))
}

// Handler returns the root handler with the HTTPS gate and security headers.
func (s *Server) Handler() http.Handler {
	return s.proxyGate(s.securityHeaders(s.mux))
}

// proxyGate only serves requests that arrive through Caddy's HTTPS reverse
// proxy, identified by the shared-secret header Caddy injects via header_up.
// Direct/plaintext-HTTP hits get an indistinguishable 404.
func (s *Server) proxyGate(next http.Handler) http.Handler {
	want := []byte(s.Cfg.ProxyToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(want) > 0 && subtle.ConstantTimeCompare([]byte(r.Header.Get(sites.ProxyTokenHeader)), want) != 1 {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self'; img-src 'self' data:; script-src 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// protect requires a fully-authenticated session and validates CSRF on POST.
func (s *Server) protect(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.session(r)
		if sess == nil {
			http.Redirect(w, r, s.Cfg.BasePath+"/login", http.StatusSeeOther)
			return
		}
		if sess.IsPendingMFA() {
			http.Redirect(w, r, s.Cfg.BasePath+"/login/totp", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost {
			if subtle.ConstantTimeCompare([]byte(r.FormValue("_csrf")), []byte(sess.CSRF)) != 1 {
				http.Error(w, "CSRF 校验失败", http.StatusForbidden)
				return
			}
		}
		h(w, r)
	}
}

func (s *Server) session(r *http.Request) *auth.Session {
	tok := auth.TokenFromRequest(r)
	if tok == "" {
		return nil
	}
	return s.Sessions.Get(tok)
}

// render executes a page template.
func (s *Server) render(w http.ResponseWriter, r *http.Request, page, title string, data any) {
	t := s.pages[page]
	if t == nil {
		http.Error(w, "模板不存在: "+page, http.StatusInternalServerError)
		return
	}
	d := pageData{
		Base:  s.Cfg.BasePath,
		Title: title,
		Flash: s.takeFlash(w, r),
		Data:  data,
	}
	if sess := s.session(r); sess != nil {
		d.CSRF = sess.CSRF
		d.User = sess.User
	}
	name := page + ".html"
	if page != "login" && page != "totp" {
		name = "layout.html"
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, d); err != nil {
		http.Error(w, "模板渲染失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	buf.WriteTo(w)
}

// maxFlashLen keeps escaped flash messages well under the browser cookie
// size cap (worst case ~9 bytes per CJK rune after QueryEscape).
const maxFlashLen = 350

// setFlash stores a one-time message in a cookie scoped to the base path.
// The value is URL-escaped because cookie values can't carry non-ASCII
// bytes — net/http would silently strip them.
func (s *Server) setFlash(w http.ResponseWriter, msg string) {
	if len([]rune(msg)) > maxFlashLen {
		msg = string([]rune(msg)[:maxFlashLen]) + "…"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "np_flash",
		Value:    url.QueryEscape(msg),
		Path:     s.Cfg.BasePath,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   60,
	})
}

func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie("np_flash")
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: "np_flash", Value: "", Path: s.Cfg.BasePath,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	if v, err := url.QueryUnescape(c.Value); err == nil {
		return v
	}
	return c.Value
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, s.Cfg.BasePath+path, http.StatusSeeOther)
}
