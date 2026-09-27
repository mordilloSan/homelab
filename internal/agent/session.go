package agent

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed web/login.html
var loginHTML []byte

const (
	sessionCookie = "failover_session"
	sessionTTL    = 7 * 24 * time.Hour // renewed on every request, so it only runs out when idle
)

var loginDelay = time.Second // after a wrong login, to slow down guessing; tests set it to 0

// sessions are kept in memory: a restart of the agent asks for the login again.
type sessions struct {
	mu sync.Mutex
	m  map[string]time.Time // token → expiry
}

func (s *sessions) create() string {
	tok := rand.Text() // 128 random bits
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]time.Time{}
	}
	s.m[tok] = time.Now().Add(sessionTTL)
	return tok
}

func token(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *sessions) valid(r *http.Request) bool {
	tok := token(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[tok]
	if !ok || time.Now().After(exp) {
		delete(s.m, tok)
		return false
	}
	s.m[tok] = time.Now().Add(sessionTTL)
	return true
}

func (s *sessions) drop(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, tok)
}

// keepOnly ends every other session, after a password change.
func (s *sessions) keepOnly(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for t := range s.m {
		if t != tok {
			delete(s.m, t)
		}
	}
}

func setSession(w http.ResponseWriter, r *http.Request, tok string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", MaxAge: maxAge, HttpOnly: true,
		// Lax, not Strict: a link from another page (Homepage) must still open
		// the UI logged in. The API only takes JSON posts, which Lax does not let through.
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

// auth lets a request through only with a live session: the page goes to the
// login form, the API answers 401 so the page can do the same.
func (a *Agent) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.sessions.valid(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, "sessão terminada: entra outra vez", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// JSON only: a cross-site form cannot send it without a CORS preflight (CSRF).
		if r.Method == http.MethodPost && r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "Content-Type tem de ser application/json", http.StatusUnsupportedMediaType)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Agent) getLogin(w http.ResponseWriter, r *http.Request) {
	if a.sessions.valid(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(loginHTML)
}

// postLogin takes a plain form post, the kind password managers recognise
// both to fill in and to offer saving.
func (a *Agent) postLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	user, pw := r.PostFormValue("username"), r.PostFormValue("password")
	c := a.creds.Load()
	// both are always checked, so the time taken does not tell which one was wrong
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(c.User)) == 1
	pwOK := bcrypt.CompareHashAndPassword([]byte(c.PasswordHash), []byte(pw)) == nil
	if !userOK || !pwOK {
		time.Sleep(loginDelay)
		http.Redirect(w, r, "/login?erro=1", http.StatusSeeOther)
		return
	}
	setSession(w, r, a.sessions.create(), int(sessionTTL.Seconds()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *Agent) postLogout(w http.ResponseWriter, r *http.Request) {
	a.sessions.drop(token(r))
	setSession(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}
