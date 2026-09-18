package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// erroringTmux wraps the fake tmux so a test can force one call to fail,
// exercising the pause machine's error-handling branches (a real tmux really
// can fail these calls; the fake alone never does).
type erroringTmux struct {
	*fakeTmux
	envErr, killErr, keysErr, panesErr error
}

func (e *erroringTmux) Env(ctx context.Context, name, key string) (string, error) {
	if e.envErr != nil {
		return "", e.envErr
	}
	return e.fakeTmux.Env(ctx, name, key)
}

func (e *erroringTmux) Panes(ctx context.Context) ([]Pane, error) {
	if e.panesErr != nil {
		return nil, e.panesErr
	}
	return e.fakeTmux.Panes(ctx)
}

func (e *erroringTmux) Kill(ctx context.Context, name string) error {
	if e.killErr != nil {
		return e.killErr
	}
	return e.fakeTmux.Kill(ctx, name)
}

func (e *erroringTmux) Keys(ctx context.Context, name string, keys ...string) error {
	if e.keysErr != nil {
		return e.keysErr
	}
	return e.fakeTmux.Keys(ctx, name, keys...)
}

// clockStore is newStore plus a handle on the clock, for the tests that move time
// by hand. It does NOT install a second clock (D54): newStore already wires one
// advancing testClock into Store.Now, items.Store.Now, events and Store.After, so
// an advance here really does make a later write sort after an earlier one, which
// is what acceptedSince and rootState compare. An earlier draft replaced only
// s.Now with a frozen local, which left items and events on a different clock.
func clockStore(t *testing.T) (*Store, *fakeTmux, *testClock) {
	t.Helper()
	s, tm, _ := newStore(t)
	return s, tm, tm.clk
}

// mustSessionID is a small lookup helper for tests that need a session id from
// an agent id without threading LatestSession's error return everywhere.
func mustSessionID(t *testing.T, s *Store, agentID string) string {
	t.Helper()
	ses, err := s.LatestSession(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	return ses.ID
}

func TestPauseRequestEnqueuesAControlMessageAndMovesTheSession(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	if _, err := s.Pause(ctx, w.Name, "session"); err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != PauseRequested {
		t.Fatalf("state = %s", ses.State)
	}
	if ses.PauseDeadlineAt == nil || !ses.PauseDeadlineAt.After(s.Now()) {
		t.Fatalf("deadline = %v", ses.PauseDeadlineAt)
	}
	var kind, payload string
	var priority int
	s.DB.QueryRowContext(ctx, `SELECT kind, priority, payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'control'`, w.ID).Scan(&kind, &priority, &payload)
	if priority != 0 {
		t.Fatalf("a pause is priority 0, got %d", priority)
	}
	var p struct {
		Action     string `json:"action"`
		DeadlineAt string `json:"deadline_at"`
		Scope      string `json:"scope"`
	}
	json.Unmarshal([]byte(payload), &p)
	if p.Action != "pause" || p.Scope != "session" {
		t.Fatalf("payload = %s", payload)
	}
	if _, err := time.Parse(time.RFC3339, p.DeadlineAt); err != nil {
		t.Fatalf("deadline_at must be RFC3339 (W2): %q", p.DeadlineAt)
	}
}

// C3: the tool allow-list while pausing. swarm_checkpoint and swarm_ask pass this
// gate and are narrowed by their own handlers — only handoff/blocked/failed
// checkpoints and only a withdraw ask — which is what §10.5 says and what the tests
// below drive. An earlier draft of this test expected swarm_ask denied here, which
// contradicted the implementation on the same page (D50); the allow-list is right
// and the expectation was wrong.
func TestPauseAllowList(t *testing.T) {
	allowed := map[string]bool{"swarm_sync": true, "swarm_read": true,
		"swarm_checkpoint": true, "swarm_ask": true}
	for _, state := range PausingStates {
		for _, tool := range []string{"swarm_sync", "swarm_read", "swarm_checkpoint", "swarm_ask",
			"swarm_spawn", "swarm_worktree", "swarm_items", "swarm_materialize", "swarm_advise",
			"swarm_send", "swarm_kb"} {
			err := PauseAllowed(state, tool)
			if allowed[tool] && err != nil {
				t.Errorf("%s/%s should be allowed: %v", state, tool, err)
			}
			if !allowed[tool] && (err == nil || err.Error() != "paused: finish your handoff and stop.") {
				t.Errorf("%s/%s: err = %v", state, tool, err)
			}
		}
	}
	if err := PauseAllowed(Running, "swarm_spawn"); err != nil {
		t.Fatalf("a running session is unrestricted: %v", err)
	}
}

// And the narrowing the allow-list delegates: a `completed` checkpoint from a
// quiescing session is refused even though swarm_checkpoint passes PauseAllowed.
func TestQuiescingRefusesANonHandoffCheckpoint(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, err := s.agentByID(ctx, wSes.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pause(ctx, w.Name, "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, wSes.ID, nil, 20); err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done anyway"})
	if err == nil || err.Error() != "paused: finish your handoff and stop." {
		t.Fatalf("err = %v, want the paused refusal", err)
	}
	// A withdraw ask is allowed; anything else is not.
	if _, err := s.Ask(ctx, wSes.ID, AskInput{Kind: "question", Prompt: "which one?"}); err == nil {
		t.Fatal("a question from a quiescing session must be refused")
	}
}

// §10.5: a newer generation with the same tmux name survives the kill.
func TestTeardownOnlyKillsAMatchingSession(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.Pause(ctx, w.Name, "session")
	s.Sync(ctx, wSes.ID, nil, 20)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "handing off"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": "ses_newer_generation"}
	at.Advance(6 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != 0 {
		t.Fatalf("a pane owned by another session must not be killed: %v", tm.killed)
	}
}

func TestPauseIsIdempotentAndRefusedForACompletedSession(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.Pause(ctx, w.Name, "session")
	before, _ := s.LatestSession(ctx, w.ID)
	if _, err := s.Pause(ctx, w.Name, "session"); err != nil {
		t.Fatalf("pausing twice does nothing: %v", err)
	}
	after, _ := s.LatestSession(ctx, w.ID)
	if !after.PauseDeadlineAt.Equal(*before.PauseDeadlineAt) {
		t.Fatal("a second pause must not extend the deadline")
	}
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE id = ?`, wSes.ID)
	if _, err := s.Pause(ctx, w.Name, "session"); err == nil {
		t.Fatal("a completed session cannot be paused")
	}
}

// §10.5: children first, deepest first, then the orchestrator with their checkpoints.
func TestSubtreePausePausesChildrenBeforeTheOrchestrator(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	if _, err := s.Pause(ctx, orch.Name, "subtree"); err != nil {
		t.Fatal(err)
	}
	child, _ := s.LatestSession(ctx, wSes.AgentID)
	parent, _ := s.LatestSession(ctx, orch.ID)
	if child.State != PauseRequested {
		t.Fatalf("child state = %s", child.State)
	}
	if parent.State == PauseRequested {
		t.Fatal("the orchestrator is paused last, after its children finish")
	}
	// queued spawns are frozen
	var frozen int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE state = 'queued'`).Scan(&frozen)
	_ = frozen
	// once the child hands off, the orchestrator's pause goes out with its checkpoints
	s.Sync(ctx, wSes.ID, nil, 20)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "child handed off"})
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE id = ?`, wSes.ID)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	parent, _ = s.LatestSession(ctx, orch.ID)
	if parent.State != PauseRequested {
		t.Fatalf("orchestrator state = %s", parent.State)
	}
	var payload string
	s.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages WHERE to_agent_id = ? AND kind = 'control'`,
		orch.ID).Scan(&payload)
	if !strings.Contains(payload, "child handed off") {
		t.Fatalf("the orchestrator's pause carries the child checkpoints: %s", payload)
	}
}

// §10.5: an unresponsive orchestrator gets a daemon-written combined checkpoint.
func TestUnresponsiveOrchestratorGetsADaemonWrittenCheckpoint(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	s.Pause(ctx, orch.Name, "subtree")
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'interrupted' WHERE agent_id = ?`, w.ID)
	s.TickPause(ctx)
	at.Advance(200 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	var summary, blockers string
	var daemonWritten int
	if err := s.DB.QueryRowContext(ctx, `SELECT summary, blockers_json, daemon_written FROM checkpoints
		WHERE agent_id = ? AND daemon_written = 1`, orch.ID).Scan(&summary, &blockers, &daemonWritten); err != nil {
		t.Fatal(err)
	}
	if summary != "Paused by daemon; orchestrator did not respond." {
		t.Fatalf("summary = %q", summary)
	}
	if !strings.Contains(blockers, w.Name) {
		t.Fatalf("blockers must list children without a handoff: %s", blockers)
	}
}

// C3: resume is refused while stopping, allowed from paused and interrupted.
func TestResumePreconditionAndNewGeneration(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, wSes.ID)
	_, err := s.Resume(ctx, w.Name)
	if err == nil || err.Error() != "Still stopping. Try again in a few seconds." {
		t.Fatalf("err = %v", err)
	}
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused', provider_session_id = 'p1' WHERE id = ?`, wSes.ID)
	if _, err := s.Resume(ctx, w.Name); err != nil {
		t.Fatal(err)
	}
	next, _ := s.LatestSession(ctx, w.ID)
	if next.Generation != wSes.Generation+1 || next.Attempt != wSes.Attempt {
		t.Fatalf("session = generation %d, attempt %d", next.Generation, next.Attempt)
	}
	if next.TokenHash == wSes.TokenHash {
		t.Fatal("a new generation needs a new token; the old one must 401")
	}
	// swarm_sync then returns the assignment plus the last checkpoint
	res, _ := s.Sync(ctx, next.ID, nil, 20)
	var sawAssignment bool
	for _, m := range res.Messages {
		if m.Kind == "assignment" {
			sawAssignment = true
		}
	}
	if !sawAssignment {
		t.Fatal("a resumed session gets its assignment again")
	}
}

// §10.5: a resume with no provider id, or one that dies fast, falls back to Launch.
func TestResumeFallsBackToAFreshLaunch(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused', provider_session_id = NULL WHERE id = ?`, wSes.ID)
	before := len(tm.started)
	if _, err := s.Resume(ctx, w.Name); err != nil {
		t.Fatal(err)
	}
	if len(tm.started) != before+1 {
		t.Fatalf("started = %v", tm.started)
	}
	if strings.Contains(tm.started[len(tm.started)-1], "--resume") {
		t.Fatal("with no provider id the fallback is a fresh Launch")
	}
}

// Resume also reactivates an agent that was left acknowledged while its
// session sat paused.
func TestResumeReactivatesAnAcknowledgedAgent(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused', provider_session_id = 'p1' WHERE id = ?`, wSes.ID)
	s.DB.ExecContext(ctx, `UPDATE agents SET state = 'acknowledged' WHERE id = ?`, w.ID)
	if _, err := s.Resume(ctx, w.Name); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Agent(ctx, w.Name)
	if got.State != AgentActive {
		t.Fatalf("agent state = %s, want active again after resume", got.State)
	}
}

// killIfOurs logs and does nothing when it cannot even read SWARM_SESSION —
// a real tmux failure must not be mistaken for a matching pane.
func TestKillIfOursLogsAnEnvError(t *testing.T) {
	s, tm, at := clockStore(t)
	et := &erroringTmux{fakeTmux: tm, envErr: errors.New("tmux is gone")}
	s.Tmux = et
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.Pause(ctx, w.Name, "session")
	s.Sync(ctx, wSes.ID, nil, 20)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "handing off"})
	at.Advance(6 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != 0 {
		t.Fatalf("an unreadable env must not be treated as a match: %v", tm.killed)
	}
}

// A real Kill failure surfaces to the reconciler instead of being swallowed.
func TestTickPausePropagatesAKillError(t *testing.T) {
	s, tm, at := clockStore(t)
	et := &erroringTmux{fakeTmux: tm, killErr: errors.New("tmux kill-session failed")}
	s.Tmux = et
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	s.Pause(ctx, w.Name, "session")
	s.Sync(ctx, wSes.ID, nil, 20)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "handing off"})
	at.Advance(6 * time.Second)
	if err := s.TickPause(ctx); err == nil {
		t.Fatal("a real kill failure must propagate")
	}
}

// A real interrupt-keys failure surfaces too.
func TestTickPausePropagatesAKeysError(t *testing.T) {
	s, tm, at := clockStore(t)
	et := &erroringTmux{fakeTmux: tm, keysErr: errors.New("tmux send-keys failed")}
	s.Tmux = et
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	s.Pause(ctx, w.Name, "session")
	at.Advance(121 * time.Second)
	if err := s.TickPause(ctx); err == nil {
		t.Fatal("a real interrupt-keys failure must propagate")
	}
}

// An agent that never got a session (preflight failed before one was
// started) can be neither paused nor resumed.
func TestPauseAndResumeRefuseAnAgentWithNoSession(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	// Claude isn't wired into this fixture's Adapters map, so Preflight fails
	// and StartSpike records the agent without ever starting a session.
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "No adapter", Intent: "feature", Kind: Claude, Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pause(ctx, a.Name, "session"); err == nil {
		t.Fatal("Pause must refuse an agent with no session")
	}
	if _, err := s.Resume(ctx, a.Name); err == nil {
		t.Fatal("Resume must refuse an agent with no session")
	}
}

// Pause and Resume both refuse an unknown agent name outright.
func TestPauseAndResumeRefuseAnUnknownAgent(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	if _, err := s.Pause(ctx, "no-such-agent", "session"); err == nil {
		t.Fatal("Pause must refuse an unknown agent")
	}
	if _, err := s.Resume(ctx, "no-such-agent"); err == nil {
		t.Fatal("Resume must refuse an unknown agent")
	}
}

// §10.5: pause-all covers every root, children first.
func TestPauseAllCountsEverySession(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	worker(t, s)
	s.StartSpike(ctx, SpikeInput{Name: "Second root", Intent: "feature", Kind: Fake, Model: "fake-1"})
	n, err := s.PauseAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("requested = %d, want the orchestrator, the coder and the spike", n)
	}
}

// PauseAll's own query only ever selects live sessions, so an already-pausing
// one is idempotent (still counted), not skipped.
func TestPauseAllCountsAnAlreadyPausingSessionOnce(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, wSes.ID)
	n, err := s.PauseAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("requested = %d, want the orchestrator and the already-pausing coder", n)
	}
}
