package httpapi

import (
	"net/http"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/webauth"
)

const sessionCookie = "swarm_session"

var errBadLogin = apiErr(http.StatusUnauthorized, "unauthorized", "Wrong email or password.")

func (s *Server) authRoutes() []route {
	return []route{
		{"POST", "/api/auth/login", authNone, s.login},
		{"POST", "/api/auth/logout", authNone, s.logout},
		{"GET", "/api/auth/me", authNone, s.me},
	}
}

// cookieSession resolves the request's login cookie to a live session.
func (s *Server) cookieSession(r *http.Request) (webauth.Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || s.Auth == nil {
		return webauth.Session{}, false
	}
	return s.Auth.LookupSession(r.Context(), c.Value)
}

func setSessionCookie(w http.ResponseWriter, origin Origin, value string, exp time.Time) {
	c := &http.Cookie{Name: sessionCookie, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: origin == OriginTunnel} // the relay serves https; plain-HTTP lan can't carry Secure
	if value == "" {
		c.MaxAge = -1
	} else {
		c.Expires, c.MaxAge = exp, int(webauth.SessionTTL/time.Second)
	}
	http.SetCookie(w, c)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ Email, Password string }
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	if s.Auth == nil || !s.Auth.Verify(r.Context(), body.Email, body.Password) {
		s.writeErr(w, errBadLogin)
		return
	}
	origin := RequestOrigin(r)
	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	token, exp, err := s.Auth.CreateSession(r.Context(), string(origin), ua)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	setSessionCookie(w, origin, token, exp)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && s.Auth != nil {
		if err := s.Auth.DeleteSession(r.Context(), c.Value); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	setSessionCookie(w, RequestOrigin(r), "", time.Time{})
	w.WriteHeader(http.StatusNoContent)
}

// me tells the board how to authenticate: this Mac needs no login.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	origin := RequestOrigin(r)
	out := map[string]any{"origin": origin, "authenticated": origin == OriginLocal}
	if origin != OriginLocal {
		if _, ok := s.cookieSession(r); ok {
			out["authenticated"] = true
			if email, ok := s.Auth.Email(r.Context()); ok {
				out["email"] = email
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
