package auth

import (
	"net/http"
	"sync"
	"time"
)

// Session is an authenticated (or half-authenticated) user session.
// Its mutable fields are guarded by mu; use the accessor methods.
type Session struct {
	User       string
	CSRF       string
	Created    time.Time
	Expires    time.Time
	PendingMFA bool // password verified, waiting for TOTP

	// PendingTOTPSecret / PendingTOTPURL hold an MFA enrollment in progress.
	PendingTOTPSecret string
	PendingTOTPURL    string

	// FailedMFA counts consecutive failed second-factor attempts; the
	// session is destroyed once it reaches maxMFAFails.
	FailedMFA int

	mu sync.Mutex
}

// maxMFAFails caps second-factor guesses per pending session.
const maxMFAFails = 5

// IsPendingMFA reports whether the session still awaits the second factor.
func (s *Session) IsPendingMFA() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PendingMFA
}

// Promote clears the pending-MFA flag after successful TOTP verification.
func (s *Session) Promote() {
	s.mu.Lock()
	s.PendingMFA = false
	s.FailedMFA = 0
	s.mu.Unlock()
}

// IncrMFAFail records one failed second-factor attempt and reports whether
// the session has exhausted its allowed attempts.
func (s *Session) IncrMFAFail() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FailedMFA++
	return s.FailedMFA >= maxMFAFails
}

// SetPendingTOTP stores an in-progress MFA enrollment.
func (s *Session) SetPendingTOTP(secret, otpauthURL string) {
	s.mu.Lock()
	s.PendingTOTPSecret = secret
	s.PendingTOTPURL = otpauthURL
	s.mu.Unlock()
}

// PendingTOTP returns the in-progress MFA enrollment, if any.
func (s *Session) PendingTOTP() (secret, otpauthURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PendingTOTPSecret, s.PendingTOTPURL
}

// ClearPendingTOTP discards the in-progress enrollment.
func (s *Session) ClearPendingTOTP() {
	s.mu.Lock()
	s.PendingTOTPSecret = ""
	s.PendingTOTPURL = ""
	s.mu.Unlock()
}

// Store keeps sessions in memory; a panel restart logs everyone out.
type Store struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]*Session
}

// NewStore creates a session store with the given absolute TTL.
func NewStore(ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Store{ttl: ttl, m: make(map[string]*Session)}
}

// Create issues a new session token. Expired sessions are swept on each
// create so pending-MFA leftovers cannot accumulate.
func (s *Store) Create(user string, pendingMFA bool) (string, *Session, error) {
	tok, err := RandomToken()
	if err != nil {
		return "", nil, err
	}
	csrf, err := RandomToken()
	if err != nil {
		return "", nil, err
	}
	sess := &Session{
		User:       user,
		CSRF:       csrf,
		Created:    time.Now(),
		Expires:    time.Now().Add(s.ttl),
		PendingMFA: pendingMFA,
	}
	s.mu.Lock()
	s.sweepLocked()
	s.m[tok] = sess
	s.mu.Unlock()
	return tok, sess, nil
}

// sweepLocked removes expired sessions; caller must hold s.mu.
func (s *Store) sweepLocked() {
	now := time.Now()
	for tok, sess := range s.m {
		if now.After(sess.Expires) {
			delete(s.m, tok)
		}
	}
}

// Get returns a session by token, or nil. Expired sessions are removed.
func (s *Store) Get(token string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.m[token]
	if sess == nil {
		return nil
	}
	if time.Now().After(sess.Expires) {
		delete(s.m, token)
		return nil
	}
	return sess
}

// Destroy removes a session.
func (s *Store) Destroy(token string) {
	s.mu.Lock()
	delete(s.m, token)
	s.mu.Unlock()
}

// DestroyAll removes every session except the one passed (typically the
// caller's own), e.g. after a password or MFA change.
func (s *Store) DestroyAll(exceptToken string) {
	s.mu.Lock()
	for tok := range s.m {
		if tok != exceptToken {
			delete(s.m, tok)
		}
	}
	s.mu.Unlock()
}

// CookieName is the session cookie name.
const CookieName = "np_session"

// SetCookie writes the session cookie scoped to the panel base path.
func SetCookie(w http.ResponseWriter, basePath, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     basePath,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

// ClearCookie expires the session cookie.
func ClearCookie(w http.ResponseWriter, basePath string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     basePath,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// TokenFromRequest extracts the session token from the request cookie.
func TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// Limiter implements a per-key login failure lockout.
type Limiter struct {
	mu       sync.Mutex
	maxFails int
	lockFor  time.Duration
	fails    map[string]*failRec
}

// maxLimiterEntries bounds the limiter map; the login endpoint is public so
// an attacker could otherwise grow memory with unlimited distinct keys.
const maxLimiterEntries = 10000

type failRec struct {
	count     int
	lockUntil time.Time
}

// NewLimiter creates a limiter: maxFails consecutive failures lock the key
// for lockFor.
func NewLimiter(maxFails int, lockFor time.Duration) *Limiter {
	return &Limiter{maxFails: maxFails, lockFor: lockFor, fails: make(map[string]*failRec)}
}

// Allow reports whether the key may attempt a login right now.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.fails[key]
	if rec == nil {
		return true
	}
	if !rec.lockUntil.IsZero() && time.Now().Before(rec.lockUntil) {
		return false
	}
	if !rec.lockUntil.IsZero() {
		delete(l.fails, key)
	}
	return true
}

// Fail records one failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.fails) >= maxLimiterEntries {
		l.sweepLocked()
	}
	if len(l.fails) >= maxLimiterEntries {
		return // map still full and nothing expired: drop the new key
	}
	rec := l.fails[key]
	if rec == nil {
		rec = &failRec{}
		l.fails[key] = rec
	}
	rec.count++
	if rec.count >= l.maxFails {
		rec.lockUntil = time.Now().Add(l.lockFor)
		rec.count = 0
	}
}

// sweepLocked removes entries whose lock has expired; caller holds l.mu.
func (l *Limiter) sweepLocked() {
	now := time.Now()
	for k, rec := range l.fails {
		if !rec.lockUntil.IsZero() && now.After(rec.lockUntil) {
			delete(l.fails, k)
		}
	}
}

// Success clears the failure counter.
func (l *Limiter) Success(key string) {
	l.mu.Lock()
	delete(l.fails, key)
	l.mu.Unlock()
}
