// spikes, orchestrators, agent actions and the terminal route (P2 T32, §7, §10.7).
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func (s *Server) spawnRoutes() []route {
	return []route{
		{"POST", "/api/spikes", authDaemon, s.createSpike},
		{"POST", "/api/items/{key}/orchestrator", authDaemon, s.startOrchestrator},
		{"POST", "/api/agents/{name}/pause", authDaemon, s.pauseAgent},
		{"POST", "/api/agents/{name}/resume", authDaemon, s.resumeAgent},
		{"POST", "/api/agents/{name}/cancel", authDaemon, s.cancelAgent},
		{"POST", "/api/agents/{name}/ack", authDaemon, s.ackAgent},
		{"POST", "/api/agents/{name}/retry", authDaemon, s.retryAgent},
		{"POST", "/api/agents/{name}/terminal", authDaemon, s.terminal},
		{"POST", "/api/agents/{name}/terminal-opened", authDaemon, s.terminalOpened},
		{"POST", "/api/pause-all", authDaemon, s.pauseAll},
	}
}

func (s *Server) createSpike(w http.ResponseWriter, r *http.Request) {
	var body spikeRequestBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	s.idempotent(w, r, body.RequestID, "POST /api/spikes", http.StatusOK, func(ctx context.Context) (any, error) {
		key, a, queued, err := s.RT.StartSpike(ctx, runtime.SpikeInput{Name: body.Name, Intent: body.Intent,
			Kind: runtime.AgentKind(body.Agent), Model: body.Model, Effort: body.Effort,
			Advisor: advisorChoiceFromBody(body.Advisor), Request: body.Request, Repos: body.Repos})
		if err != nil {
			return nil, err
		}
		it, err := s.Items.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		itemW, err := s.itemOut(ctx, it)
		if err != nil {
			return nil, err
		}
		agentW, err := s.agentNodeOut(ctx, a, s.livePanes(ctx))
		if err != nil {
			return nil, err
		}
		return spikeResponseWire{Item: itemW, Agent: agentW, Queued: queued}, nil
	})
}

// wrapPreflightErr maps a plain error from Store.Preflight (agents.go's own
// errors aren't *items.Error) to the wire's 422 preflight_failed (§7). An
// already-typed error (the "orchestrator exists" conflict, an unknown item)
// passes through unchanged.
func wrapPreflightErr(err error) error {
	if err == nil {
		return nil
	}
	var ie *items.Error
	if errors.As(err, &ie) {
		return err
	}
	return apiErr(http.StatusUnprocessableEntity, "preflight_failed", err.Error())
}

func (s *Server) startOrchestrator(w http.ResponseWriter, r *http.Request) {
	var body orchestratorRequestBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	key := r.PathValue("key")
	s.idempotent(w, r, body.RequestID, "POST /api/items/{key}/orchestrator", http.StatusOK, func(ctx context.Context) (any, error) {
		// L25: repos picked on the spawn sheet are validated (repos_version,
		// existence, live reservations on anything dropped) BEFORE the
		// orchestrator spawns, and their paths feed Preflight's §11.4 steps
		// 6-7 (repo exists, signing) — mirroring StartSpike's own
		// RepoPaths-into-Preflight pattern. The item itself is confirmed
		// only after StartOrchestrator succeeds, so a Preflight failure never
		// leaves the item's repos confirmed with nothing running.
		var repoPaths []string
		if len(body.Repos) > 0 {
			paths, err := s.RT.ValidateItemRepos(ctx, key, body.Repos, body.ReposVersion)
			if err != nil {
				return nil, err
			}
			repoPaths = paths
		}
		a, _, err := s.RT.StartOrchestrator(ctx, runtime.OrchestratorInput{ItemKey: key, Kind: runtime.AgentKind(body.Agent),
			Model: body.Model, Effort: body.Effort, Advisor: advisorChoiceFromBody(body.Advisor), Name: body.Name,
			RepoPaths: repoPaths})
		if err != nil {
			return nil, wrapPreflightErr(err)
		}
		if len(body.Repos) > 0 {
			if err := s.RT.CommitItemRepos(ctx, key, body.Repos); err != nil {
				return nil, err
			}
		}
		return s.agentNodeOut(ctx, a, s.livePanes(ctx))
	})
}

// loadAgentStatus is the §10.7 gate's read: the agent and its latest session,
// or a nil session when the agent never spawned (queued, or failed at preflight).
func (s *Server) loadAgentStatus(ctx context.Context, name string) (runtime.Agent, *runtime.Session, error) {
	a, err := s.RT.Agent(ctx, name)
	if err != nil {
		return a, nil, err
	}
	ses, err := s.RT.LatestSession(ctx, a.ID)
	if err != nil {
		return a, nil, nil
	}
	return a, &ses, nil
}

func actionSet(actions ...string) map[string]bool {
	m := make(map[string]bool, len(actions))
	for _, a := range actions {
		m[a] = true
	}
	return m
}

// allowedActions is §10.7's table: the only actions each agent/session state
// permits. A nil map means "none" (completed, cancelled, acknowledged).
func allowedActions(a runtime.Agent, ses *runtime.Session) map[string]bool {
	switch a.State {
	case runtime.AgentFinished, runtime.AgentAcknowledged:
		return nil
	case runtime.AgentQueued:
		return actionSet("cancel")
	}
	if ses == nil {
		// failed at preflight: never spawned (§10.7)
		return actionSet("retry", "cancel")
	}
	switch ses.State {
	case runtime.Spawning:
		return actionSet("terminal", "cancel")
	case runtime.Running:
		return actionSet("terminal", "pause", "cancel")
	case runtime.PauseRequested, runtime.Quiescing, runtime.Stopping:
		return actionSet("terminal")
	case runtime.Paused:
		return actionSet("resume", "cancel")
	case runtime.Interrupted:
		return actionSet("resume", "ack", "cancel")
	case runtime.Crashed, runtime.Failed:
		return actionSet("retry", "ack")
	default: // completed, cancelled
		return nil
	}
}

// conflictMessage picks the one §17.3 sentence pinned for a specific refusal;
// every other refusal gets a generic conflict (no test in this batch checks
// its exact text).
func conflictMessage(ses *runtime.Session, action string) string {
	if action == "resume" && ses != nil && ses.State.Pausing() {
		return "Still stopping. Try again in a few seconds."
	}
	return "This action isn't available from the current state."
}

// doAction is the shared §10.7 gate + call + AgentNode response every action
// route but ack/terminal/terminal-opened uses.
func (s *Server) doAction(w http.ResponseWriter, r *http.Request, action string, fn func(ctx context.Context) (any, error)) {
	ctx, name := r.Context(), r.PathValue("name")
	a, ses, err := s.loadAgentStatus(ctx, name)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if !allowedActions(a, ses)[action] {
		s.writeErr(w, apiErr(http.StatusConflict, "conflict", conflictMessage(ses, action)))
		return
	}
	v, err := fn(ctx)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) agentNodeAfter(ctx context.Context, name string) (agentNodeWire, error) {
	a, err := s.RT.Agent(ctx, name)
	if err != nil {
		return agentNodeWire{}, err
	}
	return s.agentNodeOut(ctx, a, s.livePanes(ctx))
}

func (s *Server) pauseAgent(w http.ResponseWriter, r *http.Request) {
	var body pauseBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	name := r.PathValue("name")
	s.doAction(w, r, "pause", func(ctx context.Context) (any, error) {
		if _, err := s.RT.Pause(ctx, name, body.Scope); err != nil {
			return nil, err
		}
		return s.agentNodeAfter(ctx, name)
	})
}

func (s *Server) resumeAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.doAction(w, r, "resume", func(ctx context.Context) (any, error) {
		a, err := s.RT.Resume(ctx, name)
		if err != nil {
			return nil, err
		}
		return s.agentNodeOut(ctx, a, s.livePanes(ctx))
	})
}

func (s *Server) cancelAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.doAction(w, r, "cancel", func(ctx context.Context) (any, error) {
		a, err := s.RT.Cancel(ctx, name)
		if err != nil {
			return nil, err
		}
		return s.agentNodeOut(ctx, a, s.livePanes(ctx))
	})
}

func (s *Server) retryAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	name := r.PathValue("name")
	s.doAction(w, r, "retry", func(ctx context.Context) (any, error) {
		a, err := s.RT.Retry(ctx, name, body.Note)
		if err != nil {
			return nil, err
		}
		return s.agentNodeOut(ctx, a, s.livePanes(ctx))
	})
}

func (s *Server) ackAgent(w http.ResponseWriter, r *http.Request) {
	ctx, name := r.Context(), r.PathValue("name")
	a, ses, err := s.loadAgentStatus(ctx, name)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if !allowedActions(a, ses)["ack"] {
		s.writeErr(w, apiErr(http.StatusConflict, "conflict", conflictMessage(ses, "ack")))
		return
	}
	if err := s.RT.Ack(ctx, name); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ghosttyFallback is the only process this package starts. It goes through
// the injected runner, and the socket comes from the store, never from a
// literal: hard-coding -L swarm would attach a dev or test daemon's terminal
// to the production tmux server, and without the runner a test would launch
// the real Ghostty against it (R11, S-1, S-2).
func (s *Server) ghosttyFallback(ctx context.Context, tmuxName string) error {
	_, err := s.Deps.Run(ctx, "open", "-na", "Ghostty", "--args", "-e",
		s.Deps.RT.TmuxBin(), "-L", s.Deps.RT.TmuxSocket(), "attach", "-t", tmuxName)
	return err
}

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	ctx, name := r.Context(), r.PathValue("name")
	a, ses, err := s.loadAgentStatus(ctx, name)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if !allowedActions(a, ses)["terminal"] {
		s.writeErr(w, apiErr(http.StatusConflict, "conflict", conflictMessage(ses, "terminal")))
		return
	}
	tmuxName, _, err := s.RT.Terminal(ctx, name)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.termMu.Lock()
	openedBy := "menubar"
	if s.termFallback[name] {
		openedBy = "fallback"
	}
	var cancel chan struct{}
	if openedBy == "menubar" {
		cancel = make(chan struct{})
		if s.termWait == nil {
			s.termWait = map[string]chan struct{}{}
		}
		if s.termFallback == nil {
			s.termFallback = map[string]bool{}
		}
		s.termWait[name] = cancel
	}
	s.termMu.Unlock()
	if cancel != nil {
		go func() {
			select {
			case <-s.Deps.After(1500 * time.Millisecond):
				s.termMu.Lock()
				fire := s.termWait[name] == cancel
				if fire {
					delete(s.termWait, name)
					s.termFallback[name] = true
				}
				s.termMu.Unlock()
				if fire {
					if err := s.ghosttyFallback(context.Background(), tmuxName); err != nil {
						s.Log("httpapi: ghostty fallback for %s: %v", name, err)
					}
				}
			case <-cancel:
			}
		}()
	}
	writeJSON(w, http.StatusOK, terminalWire{Tmux: tmuxName, TmuxSocket: s.Deps.RT.TmuxSocket(), OpenedBy: openedBy})
}

// terminalOpened is sent by the menubar once it runs the AppleScript; it
// cancels the fallback timer.
func (s *Server) terminalOpened(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.RT.TerminalOpened(r.Context(), name, "menubar"); err != nil {
		s.writeErr(w, err)
		return
	}
	s.termMu.Lock()
	if ch, ok := s.termWait[name]; ok {
		close(ch)
		delete(s.termWait, name)
	}
	s.termMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pauseAll(w http.ResponseWriter, r *http.Request) {
	n, err := s.RT.PauseAll(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pauseAllWire{Requested: n})
}
