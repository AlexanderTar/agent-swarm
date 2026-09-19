package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/events"
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

// Required fix 4 (I-5): Pause and Resume must raise agent.changed (contracts
// §5) so P3's board invalidates its agent list.
func TestPauseAndResumePublishAgentChanged(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	countAgentChanged := func() int {
		evs, _ := s.Events.After(ctx, 0, 1000)
		n := 0
		for _, e := range evs {
			if e.Type == events.AgentChanged {
				n++
			}
		}
		return n
	}
	before := countAgentChanged()
	if _, err := s.Pause(ctx, w.Name, "session"); err != nil {
		t.Fatal(err)
	}
	if countAgentChanged() != before+1 {
		t.Fatalf("agent.changed count after Pause = %d, want %d", countAgentChanged(), before+1)
	}
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused', provider_session_id = 'p1' WHERE id = ?`, wSes.ID)
	afterPause := countAgentChanged()
	if _, err := s.Resume(ctx, w.Name); err != nil {
		t.Fatal(err)
	}
	if countAgentChanged() != afterPause+1 {
		t.Fatalf("agent.changed count after Resume = %d, want %d", countAgentChanged(), afterPause+1)
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

// §10.5: a plain worker inside a subtree pause must not get a daemon-written
// "orchestrator did not respond" checkpoint fabricated on its own behalf.
// Unlike the test above, this one does NOT hand-set the worker to
// 'interrupted' first — it lets TickPause's own per-session interrupt loop
// and overdueSubtreePauses (both reading the same overdue, subtree-scoped
// row) run in the same tick, the race that produced a bogus checkpoint for
// the worker before overdueSubtreePauses gained its EXISTS(children) guard
// (found writing scripts/e2e/pause_test.go's scenario 9).
func TestSubtreePauseNeverWritesADaemonCheckpointForAChildlessWorker(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	if _, err := s.Pause(ctx, orch.Name, "subtree"); err != nil {
		t.Fatal(err)
	}
	at.Advance(200 * time.Second) // past the default 120s pause deadline
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints
		WHERE agent_id = ? AND daemon_written = 1`, w.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("worker got %d daemon-written checkpoint(s), want 0 -- it has no children to report on", n)
	}
	got, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != PauseRequested && got.State != Quiescing {
		t.Fatalf("worker session = %s, want still pause_requested/quiescing (interrupted, not converted to a fake handoff)", got.State)
	}
	if s.getInterrupted(got.ID) == nil {
		t.Fatal("worker never got its interrupt keys -- the ordinary per-session timeout path must still run")
	}
}

// §10.5 step 4 / Ruling A: the daemon's fallback checkpoint for an
// unresponsive orchestrator is unconditional on child count (the spec text
// never gates it on having any) -- a childless orchestrator explicitly
// paused with scope="subtree" must still get it and end 'paused', not
// silently take the ordinary interrupt-then-kill path (ending 'interrupted')
// the earlier EXISTS(children) guard sent it down. promotePendingSubtreePauses
// re-stamps a fresh deadline at promotion time (Important #2), so this needs
// two advances: one past the original deadline (promotes running ->
// pause_requested), one past the fresh one (triggers the fallback).
func TestChildlessSubtreePauseStillGetsTheDaemonFallbackCheckpoint(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pause(ctx, orch.Name, "subtree"); err != nil {
		t.Fatal(err)
	}
	at.Advance(200 * time.Second)
	if err := s.TickPause(ctx); err != nil { // promotes running -> pause_requested, fresh deadline
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != PauseRequested {
		t.Fatalf("orchestrator session = %s, want pause_requested after promotion", ses.State)
	}
	at.Advance(200 * time.Second)
	if err := s.TickPause(ctx); err != nil { // now past the fresh deadline
		t.Fatal(err)
	}
	var daemonWritten int
	var summary, blockers string
	if err := s.DB.QueryRowContext(ctx, `SELECT daemon_written, summary, blockers_json FROM checkpoints
		WHERE agent_id = ? AND daemon_written = 1`, orch.ID).Scan(&daemonWritten, &summary, &blockers); err != nil {
		t.Fatalf("no daemon-written checkpoint for a childless orchestrator: %v", err)
	}
	if summary != daemonPauseSummary {
		t.Fatalf("summary = %q", summary)
	}
	if blockers != "[]" {
		t.Fatalf("blockers = %s, want empty -- it has no children", blockers)
	}
}

// §10.5 / Ruling A: a mid-tree agent -- has a child of its own, but is
// itself only a descendant cascaded onto by a higher subtree pause, never
// the direct target of its own Pause call -- must never independently match
// overdueSubtreePauses just because it happens to have children. Reproduces
// the reviewer's hand-built 3-level probe (root -> mid -> leaf) that showed
// the EXISTS(children) guard let mid race TickPause's own interrupt loop the
// same way the original worker bug did.
func TestMidTreeDescendantNeverGetsItsOwnDaemonCheckpoint(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	root, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	mid, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: root.ID, Brief: BriefInput{Objective: "mid"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: mid.ID, Brief: BriefInput{Objective: "leaf"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Pause(ctx, root.Name, "subtree"); err != nil {
		t.Fatal(err)
	}
	at.Advance(200 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints
		WHERE agent_id = ? AND daemon_written = 1`, mid.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("mid-tree agent got %d daemon-written checkpoint(s), want 0 -- it is a descendant of this pause, not its target", n)
	}
	midSes, err := s.LatestSession(ctx, mid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if midSes.State != PauseRequested && midSes.State != Quiescing {
		t.Fatalf("mid-tree session = %s, want still pause_requested/quiescing (interrupted like any other descendant, not faked into stopping)", midSes.State)
	}
	if s.getInterrupted(midSes.ID) == nil {
		t.Fatal("mid-tree agent never got its interrupt keys")
	}
}

// Ruling B: PauseAll must route a root with a live child through subtree
// scope, or DrainQueue's own freeze (rootHasLiveSubtreePause, gated on
// pause_scope='subtree') never engages, and a queued sibling can be
// admitted while pause-all is supposedly in effect. This is the same
// scenario TestDrainQueueSkipsARootUnderALiveSubtreePause covers for a
// direct scope="subtree" call, driven through PauseAll instead: that
// indirection is exactly what silently downgraded to session-scope before
// this fix (confirmed by temporarily reverting PauseAll to its flat
// per-agent scope="session" loop and re-running this test: it failed with
// the queued sibling admitted immediately).
func TestPauseAllFreezesAQueuedSpawnTheSameWayASubtreePauseDoes(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	setLimits(t, s, 3, 1, 4) // only one non-orchestrator agent globally
	seedEpicWithTwoTasks(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	first, queued1, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if queued1 {
		t.Fatal("the first child should be admitted; the slot is free")
	}
	second, queued2, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if !queued2 {
		t.Fatal("the second child should queue; the global agent limit is 1")
	}

	if _, err := s.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}

	var scope string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(pause_scope, '') FROM sessions
		WHERE agent_id = ?`, orch.ID).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	if scope != "subtree" {
		t.Fatalf("orchestrator pause_scope = %q after pause-all, want subtree -- it has a live child", scope)
	}

	// Free the admission slot the way a normal completion would, so DrainQueue
	// would admit the queued child if pause-all's own freeze didn't stop it.
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`,
		first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Agent(ctx, second.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AgentQueued {
		t.Fatalf("state = %s, want still queued -- pause-all must freeze the queue like any subtree pause", got.State)
	}
}

// Ruling B follow-up (Important #2): a live child whose root's own session
// has already ended (paused/interrupted/completed/failed/crashed) must not
// be silently skipped by PauseAll just because its root is no longer live --
// Ruling B's own text already says a session with no live orchestrator
// above it is "functionally standalone", and this is exactly that case,
// just discovered after the fact instead of from the start. Reproduces the
// root's own pause running its full, real course (deadline -> interrupt ->
// kill -> reconcile, the same sequence TestDeadlineInterruptsAndNeverRecordsPaused
// exercises on a worker) rather than forcing 'interrupted' via raw SQL, so
// this is the actual state machine producing a finished root with a still-
// live child, not a synthetic one.
func TestPauseAllStillReachesALiveChildUnderAFinishedRoot(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}

	s.Pause(ctx, orch.Name, "session")
	at.Advance(121 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(11 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	// Only the orchestrator's own pane is gone; the worker's is still very
	// much alive (tm.panes has no entry for either by default, which would
	// make Reconcile treat BOTH as dead -- unlike
	// TestDeadlineInterruptsAndNeverRecordsPaused, which has only the one
	// live session to worry about).
	tm.panes = []Pane{{Session: w.Name, Command: "swarm-fake-agent"}}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orchSes.State != Interrupted {
		t.Fatalf("orchestrator state = %s, want interrupted -- its own pause must have run its full course", orchSes.State)
	}
	wBefore, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !wBefore.State.Live() {
		t.Fatalf("worker state = %s, want still live (nothing paused it)", wBefore.State)
	}

	n, err := s.PauseAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("requested = %d, want 1 -- the still-live worker, standalone now that its root has ended", n)
	}
	wAfter, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wAfter.State != PauseRequested {
		t.Fatalf("worker state = %s, want pause_requested -- pause-all must still reach it", wAfter.State)
	}
}

// Sibling regression to Important #2, found in re-review: Pause's own
// idempotency guard ("a second pause must not extend the deadline") sat
// above the scope dispatch, so Pause(x, "subtree") on an x already
// Pausing() under scope="session" returned early -- the descendant cascade
// never ran at all, x stayed pause_scope="session"/pause_root=0, and
// rootHasLiveSubtreePause stayed false, reopening the exact queue-freeze
// guarantee Ruling B closed, for this one shape. Reachable via two
// ordinary calls: Pause(orch, "session") then PauseAll. Exercises the fix
// directly, bypassing PauseAll's own grouping, since Pause/pauseSubtree is
// the actual fix location.
func TestPauseUpgradesAnAlreadySessionPausingTargetToSubtree(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	if _, err := s.Pause(ctx, orch.Name, "session"); err != nil {
		t.Fatal(err)
	}
	before, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.State != PauseRequested || before.PauseScope != "session" {
		t.Fatalf("setup: orchestrator = %+v, want pause_requested/session", before)
	}

	got, err := s.Pause(ctx, orch.Name, "subtree")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != before.State {
		t.Fatalf("state = %s, want untouched at %s (Important #1's guarantee, applied to the target itself)", got.State, before.State)
	}
	if got.PauseScope != "subtree" {
		t.Fatalf("pause_scope = %q, want upgraded to subtree", got.PauseScope)
	}
	if got.PauseDeadlineAt == nil || before.PauseDeadlineAt == nil || !got.PauseDeadlineAt.Equal(*before.PauseDeadlineAt) {
		t.Fatalf("deadline changed by the upgrade (before=%v after=%v), want untouched", before.PauseDeadlineAt, got.PauseDeadlineAt)
	}
	var root int
	if err := s.DB.QueryRowContext(ctx, `SELECT pause_root FROM sessions WHERE id = ?`, got.ID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if root != 1 {
		t.Fatal("pause_root = 0, want 1 after the upgrade")
	}
	wSes, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wSes.State != PauseRequested {
		t.Fatalf("worker state = %s, want pause_requested -- the cascade must still run despite the upgrade", wSes.State)
	}
	live, err := s.rootHasLiveSubtreePause(ctx, orch.RootItemID)
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		t.Fatal("rootHasLiveSubtreePause = false, want true after the upgrade")
	}
}

// Required test #1 (round 4): the exact two-call sequence the reviewer used
// to reproduce the bug, through PauseAll's own grouping this time. n must
// count only the descendant PauseAll's cascade actually reaches -- the
// orchestrator itself was already pausing, not newly requested.
func TestPauseAllUpgradesAnAlreadySessionPausedTargetToSubtree(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	if _, err := s.Pause(ctx, orch.Name, "session"); err != nil {
		t.Fatal(err)
	}
	before, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.PauseAll(ctx)
	if err != nil {
		t.Fatal(err)
	}

	after, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != before.State {
		t.Fatalf("orchestrator state changed %s -> %s, want untouched", before.State, after.State)
	}
	if before.PauseDeadlineAt == nil || after.PauseDeadlineAt == nil || !before.PauseDeadlineAt.Equal(*after.PauseDeadlineAt) {
		t.Fatalf("orchestrator deadline changed (before=%v after=%v), want untouched", before.PauseDeadlineAt, after.PauseDeadlineAt)
	}
	if after.PauseScope != "subtree" {
		t.Fatalf("orchestrator pause_scope = %q, want upgraded to subtree", after.PauseScope)
	}
	var root int
	if err := s.DB.QueryRowContext(ctx, `SELECT pause_root FROM sessions WHERE id = ?`, after.ID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if root != 1 {
		t.Fatal("orchestrator pause_root = 0, want 1 after pause-all's upgrade")
	}
	wSes, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wSes.State != PauseRequested {
		t.Fatalf("worker state = %s, want pause_requested", wSes.State)
	}
	if n != 1 {
		t.Fatalf("requested = %d, want 1 -- only the worker is newly transitioned", n)
	}
	live, err := s.rootHasLiveSubtreePause(ctx, orch.RootItemID)
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		t.Fatal("rootHasLiveSubtreePause = false, want true after pause-all's upgrade")
	}
}

// Required test #2 (round 4): the same shape, but with a queued sibling
// present, proving the queue-freeze guarantee actually holds for it now.
func TestPauseAllUpgradeFreezesAQueuedSiblingSpawn(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	setLimits(t, s, 3, 1, 4) // only one non-orchestrator agent globally
	seedEpicWithTwoTasks(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	first, queued1, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if queued1 {
		t.Fatal("the first child should be admitted; the slot is free")
	}
	second, queued2, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if !queued2 {
		t.Fatal("the second child should queue; the global agent limit is 1")
	}

	if _, err := s.Pause(ctx, orch.Name, "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}

	var scope string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(pause_scope, '') FROM sessions
		WHERE agent_id = ?`, orch.ID).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	if scope != "subtree" {
		t.Fatalf("orchestrator pause_scope = %q after the upgrade, want subtree", scope)
	}

	// Free the admission slot the way a normal completion would, so
	// DrainQueue would admit the queued sibling if the upgrade's own freeze
	// didn't stop it.
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`,
		first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Agent(ctx, second.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AgentQueued {
		t.Fatalf("state = %s, want still queued -- the upgrade must freeze the queue too", got.State)
	}
}

// Boundary of the same fix: a target already session-pausing with NO live
// descendant has nothing to cascade to, so it must stay at session scope
// (never spuriously upgraded) and PauseAll must report 0 for it -- matching
// the pre-upgrade idempotency guarantee exactly, just reached via the new
// code path instead of the old unconditional early return.
func TestPauseAllLeavesAnAlreadyPausingChildlessTargetAtSessionScope(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pause(ctx, orch.Name, "session"); err != nil {
		t.Fatal(err)
	}
	before, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.PauseAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("requested = %d, want 0 -- nothing to cascade to, the target itself was not newly requested", n)
	}
	after, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PauseScope != "session" {
		t.Fatalf("pause_scope = %q, want left at session -- no live descendant to justify an upgrade", after.PauseScope)
	}
	if before.PauseDeadlineAt == nil || after.PauseDeadlineAt == nil || !before.PauseDeadlineAt.Equal(*after.PauseDeadlineAt) {
		t.Fatal("deadline changed on a plain re-pause")
	}
}

// §10.5: a subtree pause freezes a root's queued spawns. DrainQueue must not
// admit them while the pause is live, and must go back to admitting them
// normally once it resolves — otherwise a newly-launched, never-paused agent
// gets picked up by promotePendingSubtreePauses as a live descendant on the
// very next tick and blocks the pause from ever completing.
func TestDrainQueueSkipsARootUnderALiveSubtreePause(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	setLimits(t, s, 3, 1, 4) // only one non-orchestrator agent globally
	seedEpicWithTwoTasks(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	first, queued1, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if queued1 {
		t.Fatal("the first child should be admitted; the slot is free")
	}
	second, queued2, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if !queued2 {
		t.Fatal("the second child should queue; the global agent limit is 1")
	}

	if _, err := s.Pause(ctx, orch.Name, "subtree"); err != nil {
		t.Fatal(err)
	}

	// Free the admission slot the way a normal completion would, so DrainQueue
	// would admit the queued child if the live subtree pause didn't stop it.
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`,
		first.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Agent(ctx, second.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AgentQueued {
		t.Fatalf("state = %s, want still queued while the subtree pause is live", got.State)
	}

	// Resolve the pause (the orchestrator's own subtree-pause bookkeeping
	// clears once it and its children are done, however that happens).
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET pause_scope = NULL WHERE agent_id = ?`,
		orch.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = s.Agent(ctx, second.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AgentActive {
		t.Fatalf("state = %s, want admitted once the pause resolved", got.State)
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

// An already-pausing descendant is left alone by pauseSubtree's own
// Pausing() skip (Important #1), not by anything in PauseAll's own query --
// that query counts every live session, an already-pausing one included.
// The extension below (added the same round as Important #1) confirms the
// already-pausing worker's row is genuinely untouched by a second pause-all,
// not just that it was (still) counted.
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
	// The count alone doesn't catch a pauseSubtree cascade that resets an
	// already-pausing descendant's own state/deadline back to
	// pause_requested (Important #1): Pause()'s own idempotency guard ("a
	// second pause must not extend the deadline") only protects the root
	// PauseAll calls Pause() on directly, not whatever pauseSubtree cascades
	// onto underneath it. Confirm the worker's row -- state, pause_scope,
	// pause_deadline_at -- is untouched by pause-all finding it already
	// pausing.
	var state string
	var scope sql.NullString
	var deadline sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT state, pause_scope, pause_deadline_at
		FROM sessions WHERE id = ?`, wSes.ID).Scan(&state, &scope, &deadline); err != nil {
		t.Fatal(err)
	}
	if state != "stopping" {
		t.Fatalf("already-pausing worker's state = %s, want untouched at stopping", state)
	}
	if scope.Valid {
		t.Fatalf("already-pausing worker's pause_scope = %q, want untouched (never set)", scope.String)
	}
	if deadline.Valid {
		t.Fatalf("already-pausing worker's pause_deadline_at = %d, want untouched (never set)", deadline.Int64)
	}
}
