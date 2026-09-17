// Package httpapi serves the daemon's REST API and SSE feed (§7).
package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

type Deps struct {
	Version      string
	Token        string // daemon token (~/.swarm/run/daemon.token)
	DB           *db.DB
	Events       *events.Store
	Items        *items.Store
	Repos        *repos.Service
	Settings     *settings.Store
	Catalog      *catalog.Service
	KB           *kb.Index
	WriteTimeout time.Duration                    // per SSE write; 0 means 10 s
	PingInterval time.Duration                    // SSE keep-alive; 0 means 25 s
	Log          func(format string, args ...any) // nil means log.Printf
}

type authMode int

const (
	authNone authMode = iota
	authDaemon
	authLoopback
	authSession
)

type route struct {
	method, pattern string
	auth            authMode
	h               http.HandlerFunc
}

type Server struct {
	Deps
	mux    *http.ServeMux
	routes []route
	idemMu sync.Mutex
}

// New panics on an empty daemon token: it would authenticate every tokenless request.
func New(d Deps) *Server {
	if d.Token == "" {
		panic("httpapi: empty daemon token")
	}
	if d.WriteTimeout == 0 {
		d.WriteTimeout = 10 * time.Second
	}
	if d.PingInterval == 0 {
		d.PingInterval = 25 * time.Second
	}
	if d.Log == nil {
		d.Log = log.Printf
	}
	s := &Server{Deps: d, mux: http.NewServeMux()}
	s.routes = append(s.baseRoutes(), s.itemRoutes()...)
	for _, rt := range s.routes {
		s.mux.HandleFunc(rt.method+" "+rt.pattern, s.wrap(rt))
	}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.writeErr(w, apiErr(http.StatusNotFound, "not_found", "Unknown API route."))
			return
		}
		s.writeErr(w, apiErr(http.StatusNotFound, "not_found", "The board isn't built yet."))
	})
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) baseRoutes() []route {
	return []route{
		{"GET", "/api/health", authNone, s.health},
		{"GET", "/api/bootstrap", authLoopback, s.bootstrap},
		{"GET", "/api/events", authDaemon, s.events},
	}
}

func bearer(r *http.Request) string {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return tok
}

var errUnauthorized = apiErr(http.StatusUnauthorized, "unauthorized", "Missing or invalid token.")

func (s *Server) wrap(rt route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch rt.auth {
		case authDaemon:
			if tok := bearer(r); tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 {
				s.writeErr(w, errUnauthorized)
				return
			}
		case authLoopback:
			if !isLocal(r) {
				s.writeErr(w, apiErr(http.StatusUnauthorized, "unauthorized", "Bootstrap is only available on this Mac."))
				return
			}
		case authSession:
			if _, ok := s.sessionAuth(r); !ok {
				s.writeErr(w, errUnauthorized)
				return
			}
		}
		rt.h(w, r)
	}
}

// isLocal requires a loopback peer and a loopback Host (guards against DNS rebinding).
func isLocal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return false
	}
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	} else {
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

// sessionAuth maps a session bearer token to a live session id (Phase 2 /hook routes).
func (s *Server) sessionAuth(r *http.Request) (string, bool) {
	tok := bearer(r)
	if tok == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(tok))
	var id string
	err := s.DB.QueryRowContext(r.Context(), `SELECT id FROM sessions WHERE token_hash = ?
		AND state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')`, hex.EncodeToString(sum[:])).Scan(&id)
	return id, err == nil
}

type apiError struct {
	Status  int
	Code    string
	Message string
	Reason  string
}

func (e *apiError) Error() string { return e.Message }

func apiErr(status int, code, msg string) *apiError {
	return &apiError{Status: status, Code: code, Message: msg}
}

var statusFor = map[string]int{
	"bad_request": 400, "unauthorized": 401, "not_found": 404, "conflict": 409,
	"transition_denied": 422, "preflight_failed": 422, "limit_reached": 429, "internal": 500,
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeErr writes the §7 error envelope; unmapped errors are logged and become 500 internal.
func (s *Server) writeErr(w http.ResponseWriter, err error) {
	var (
		ae *apiError
		ie *items.Error
		ve *settings.ValidationError
		nf *kb.NotFoundError
	)
	switch {
	case errors.As(err, &ae):
	case errors.As(err, &ie):
		status, ok := statusFor[ie.Code]
		if !ok {
			s.Log("httpapi: unmapped item error code %q: %v", ie.Code, err)
			ae = apiErr(http.StatusInternalServerError, "internal", ie.Message)
			break
		}
		ae = apiErr(status, ie.Code, ie.Message)
		if ie.Code == items.CodeTransitionDenied {
			ae.Reason = ie.Message
		}
	case errors.As(err, &ve):
		ae = apiErr(http.StatusBadRequest, "bad_request", ve.Message)
	case errors.As(err, &nf):
		ae = apiErr(http.StatusNotFound, "not_found", nf.Error())
	case errors.Is(err, repos.ErrNotRepo):
		ae = apiErr(http.StatusUnprocessableEntity, "bad_request", err.Error())
	case errors.Is(err, kb.ErrUnavailable):
		ae = apiErr(http.StatusServiceUnavailable, "internal", err.Error())
	default:
		s.Log("httpapi: %v", err)
		ae = apiErr(http.StatusInternalServerError, "internal", "Something went wrong.")
	}
	body := map[string]string{"code": ae.Code, "message": ae.Message}
	if ae.Reason != "" {
		body["reason"] = ae.Reason
	}
	writeJSON(w, ae.Status, map[string]any{"error": body})
}

// readJSON decodes a body of at most 1 MB; an empty body leaves v unchanged.
func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return apiErr(http.StatusBadRequest, "bad_request", "Invalid JSON body.")
	}
	return nil
}

// via is the UI surface making a change (X-Swarm-Via), defaulting to board.
func via(r *http.Request) string {
	switch v := r.Header.Get("X-Swarm-Via"); v {
	case "menubar", "cli", "board":
		return v
	}
	return "board"
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.Version, "schema": db.SchemaVersion})
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	if s.Token == "" {
		s.writeErr(w, errors.New("bootstrap: empty daemon token"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": s.Token})
}
