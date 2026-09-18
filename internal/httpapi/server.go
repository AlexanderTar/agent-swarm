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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/advisor"
	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/hook"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/mcpserver"
	"github.com/AlexanderTar/agent-swarm/internal/notify"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/usage"
)

type Deps struct {
	Version  string
	Token    string // daemon token (~/.swarm/run/daemon.token)
	DB       *db.DB
	Events   *events.Store
	Items    *items.Store
	Repos    *repos.Service
	Settings *settings.Store
	Catalog  *catalog.Service
	KB       *kb.Index
	RT       *runtime.Store
	Notify   *notify.Service
	Usage    *usage.Poller
	Advisor  *advisor.Service
	MCP      *mcpserver.Server
	// Hook is the daemon-side /hook/{agent}/{event} decision handler (P2 T34).
	// The brief's Task 31 field list for Deps didn't name this one, but the
	// hook route has no other way to reach it; noted in the batch report.
	Hook         *hook.Handler
	WriteTimeout time.Duration                    // per SSE write; 0 means 10 s
	PingInterval time.Duration                    // SSE keep-alive; 0 means 25 s
	Log          func(format string, args ...any) // nil means log.Printf
	Web          http.Handler                     // board for non-/api paths; nil means not built yet

	// Run is how this package starts a process. The only one it starts is the
	// Ghostty fallback for POST /api/agents/{name}/terminal (§10.4), and without
	// an injected runner that test launches the real Ghostty against the real
	// tmux server (R11). Nil is a programming error, like runtime.Store.BaseEnv:
	// New panics with "httpapi: Run is not wired" rather than defaulting to
	// execx.Run.
	Run execx.Runner
	// After is the 1.5 s menubar-reply timer, injected so the test does not sleep.
	After func(time.Duration) <-chan time.Time
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
	mux       *http.ServeMux
	routes    []route
	idemMu    sync.Mutex
	done      chan struct{} // closed by Close: ends the SSE streams
	closeOnce sync.Once

	// termMu guards termWait (the pending Ghostty-fallback cancel channel per
	// agent name) and termFallback (agents that already needed the fallback
	// once, so later opens skip straight to it, P0-6).
	termMu       sync.Mutex
	termWait     map[string]chan struct{}
	termFallback map[string]bool

	// mcpHandler is built once in New from Deps.MCP (P2 T34): /mcp resolves the
	// caller itself (session token or daemon token) and delegates to it.
	mcpHandler http.Handler
}

// Close ends every open SSE stream. They never go idle on their own, so
// http.Server.Shutdown would otherwise wait out the whole grace period.
func (s *Server) Close() { s.closeOnce.Do(func() { close(s.done) }) }

// New panics on an empty daemon token: it would authenticate every tokenless request.
func New(d Deps) *Server {
	if d.Token == "" {
		panic("httpapi: empty daemon token")
	}
	if d.Run == nil {
		panic("httpapi: Run is not wired")
	}
	if d.After == nil {
		d.After = time.After
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
	s := &Server{Deps: d, mux: http.NewServeMux(), done: make(chan struct{})}
	if d.MCP != nil {
		s.mcpHandler = d.MCP.Handler(s.resolveMCPCaller)
	}
	s.routes = slices.Concat(s.baseRoutes(), s.itemRoutes(), s.configRoutes(),
		s.runtimeRoutes(), s.spawnRoutes(), s.requestRoutes(), s.agentIORoutes())
	for _, rt := range s.routes {
		s.mux.HandleFunc(rt.method+" "+rt.pattern, s.wrap(rt))
	}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			s.writeErr(w, apiErr(http.StatusNotFound, "not_found", "Unknown API route."))
			return
		}
		if s.Web != nil {
			s.Web.ServeHTTP(w, r)
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

// sessionCaller is what a session token resolves to (P2 T34).
type sessionCaller struct {
	SessionID, AgentID, AgentName string
	Kind                          runtime.AgentKind
	Role                          runtime.Role
	AdvisorMode                   string
	SpikeOrchestrator             bool
}

// sessionAuth maps a session bearer token to its live session. An older
// generation's token is not live (§10.5, P0-10): the generation check is what
// makes a token a resumed/retried session left behind fail closed, not just a
// session whose row happens to still say "running".
func (s *Server) sessionAuth(r *http.Request) (sessionCaller, bool) {
	tok := bearer(r)
	if tok == "" {
		return sessionCaller{}, false
	}
	sum := sha256.Sum256([]byte(tok))
	var c sessionCaller
	var itemType string
	err := s.DB.QueryRowContext(r.Context(), `SELECT ses.id, a.id, a.name, a.kind, a.role, COALESCE(a.advisor_mode, ''), i.type
		FROM sessions ses JOIN agents a ON a.id = ses.agent_id JOIN items i ON i.id = a.item_id
		WHERE ses.token_hash = ? AND ses.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')
		AND ses.generation = (SELECT MAX(s2.generation) FROM sessions s2 WHERE s2.agent_id = ses.agent_id)`,
		hex.EncodeToString(sum[:])).Scan(&c.SessionID, &c.AgentID, &c.AgentName, &c.Kind, &c.Role, &c.AdvisorMode, &itemType)
	if err != nil {
		return sessionCaller{}, false
	}
	c.SpikeOrchestrator = c.Role == runtime.RoleOrchestrator && itemType == "spike"
	return c, true
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
