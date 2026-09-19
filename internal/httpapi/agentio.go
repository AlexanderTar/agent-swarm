// /hook/*, /mcp and the wake stream (P2 T34, §11.2, §11.3, §7.1).
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/mcpserver"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func (s *Server) agentIORoutes() []route {
	return []route{
		{"POST", "/hook/{agent}/{event}", authSession, s.hook},
		{"GET", "/api/sessions/self/wake", authSession, s.wakeStream},
		// /mcp takes either token, so it authenticates itself
		{"POST", "/mcp", authNone, s.mcp},
		{"GET", "/mcp", authNone, s.mcp},
		{"DELETE", "/mcp", authNone, s.mcp},
	}
}

// hook is POST /hook/{agent}/{event}. L16: the {agent} path segment is a hint
// only, written by the agent's own hook config and therefore caller-
// controlled; the adapter used is the one the session's own agent row names
// (hook.Handler.Handle resolves it from the session, not from kind).
func (s *Server) hook(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.sessionAuth(r)
	if !ok {
		s.writeErr(w, errUnauthorized)
		return
	}
	if s.notWired(w, s.Hook != nil) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "Invalid body."))
		return
	}
	out, err := s.Hook.Handle(r.Context(), runtime.AgentKind(r.PathValue("agent")), r.PathValue("event"), caller.SessionID, body)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if out == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Write(out)
}

// resolveMCPCaller is /mcp's own auth (I6, L16): a live session token gives
// that session's Caller; the daemon token gives an unbound (read-only)
// caller; anything else is 401.
func (s *Server) resolveMCPCaller(r *http.Request) (mcpserver.Caller, bool) {
	tok := bearer(r)
	if tok == "" {
		return mcpserver.Caller{}, false
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) == 1 {
		return mcpserver.Caller{Unbound: true}, true
	}
	c, ok := s.sessionAuth(r)
	if !ok {
		return mcpserver.Caller{}, false
	}
	return mcpserver.Caller{SessionID: c.SessionID, AgentID: c.AgentID, AgentName: c.AgentName,
		Role: c.Role, Kind: c.Kind, AdvisorMode: c.AdvisorMode, SpikeOrchestrator: c.SpikeOrchestrator}, true
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	if s.mcpHandler == nil {
		s.writeErr(w, apiErr(http.StatusInternalServerError, "internal", "The daemon isn't fully wired yet."))
		return
	}
	s.mcpHandler.ServeHTTP(w, r)
}

type wakeEventWire struct {
	Content string `json:"content"`
	MsgID   string `json:"msg_id"`
}

// wakeStream is GET /api/sessions/self/wake: an SSE feed of wake events for
// the calling session only (§7.1 — wake-ups are never broadcast on
// /api/events). It follows the same shutdown discipline as P1's /api/events.
func (s *Server) wakeStream(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.sessionAuth(r)
	if !ok {
		s.writeErr(w, errUnauthorized)
		return
	}
	if s.notWired(w, s.RT != nil) {
		return
	}
	notices, unsubscribe := s.RT.SubscribeWake(caller.SessionID)
	defer unsubscribe()

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	send := func(format string, args ...any) bool {
		rc.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		_, err := fmt.Fprintf(w, format, args...)
		return err == nil
	}
	flush := func() bool {
		rc.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		return rc.Flush() == nil
	}
	if !flush() {
		return
	}
	ping := time.NewTicker(s.PingInterval)
	defer ping.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done: // the daemon is shutting down
			return
		case notice := <-notices:
			body, _ := json.Marshal(wakeEventWire{Content: notice})
			if !send("event: wake\ndata: %s\n\n", body) || !flush() {
				return
			}
		case <-ping.C:
			if !send(": ping\n\n") || !flush() {
				return
			}
		}
	}
}
