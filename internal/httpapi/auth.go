package httpapi

import (
	"errors"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/webauth"
)

const sessionCookie = "swarm_session"

var errBadLogin = apiErr(http.StatusUnauthorized, "unauthorized", "Wrong email or password.")

func (s *Server) authRoutes() []route {
	return []route{
		{"POST", "/api/auth/login", authNone, s.login},
		{"POST", "/api/auth/logout", authNone, s.logout},
		{"GET", "/api/auth/me", authNone, s.me},
		// swarm auth set|status|logout-all: the daemon owns the DB, so the CLI goes through it
		{"PUT", "/api/auth/credentials", authDaemon, s.setCredentials},
		{"GET", "/api/auth/credentials", authDaemon, s.credentialsStatus},
		{"DELETE", "/api/auth/sessions", authDaemon, s.logoutAll},
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
	origin := RequestOrigin(r)
	ip := clientIP(r, origin)
	if ok, wait := s.limiter.Allow(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		s.writeErr(w, apiErr(http.StatusTooManyRequests, "rate_limited", "Too many login attempts. Try again later."))
		return
	}
	if s.Auth == nil || !s.Auth.Verify(r.Context(), body.Email, body.Password) {
		s.limiter.Fail(ip)
		s.writeErr(w, errBadLogin)
		return
	}
	s.limiter.Success(ip)
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

// clientIP is who the login limiter counts: the peer on lan, and on tunnel the
// first X-Forwarded-For hop, which our relay sets. A lan caller's own
// X-Forwarded-For is ignored. "" means unknown (counts only against the global cap).
func clientIP(r *http.Request, origin Origin) string {
	if origin == OriginTunnel {
		first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
		if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
			return ip.String()
		}
		return ""
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return ""
}

// sameOrigin is the CSRF guard for cookie-authenticated writes: the browser's
// Origin header must name the host this request was sent to.
func sameOrigin(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

func (s *Server) setCredentials(w http.ResponseWriter, r *http.Request) {
	var body struct{ Email, Password string }
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	if err := s.Auth.SetCredentials(r.Context(), body.Email, body.Password); err != nil {
		if errors.Is(err, webauth.ErrWeakPassword) {
			err = apiErr(http.StatusBadRequest, "bad_request", "The password must be at least 12 characters.")
		}
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) credentialsStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.Auth.SessionCount(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out := map[string]any{"configured": false, "sessions": n}
	if email, ok := s.Auth.Email(r.Context()); ok {
		out["configured"], out["email"] = true, email
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := s.Auth.DeleteAllSessions(r.Context()); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
