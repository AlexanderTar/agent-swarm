package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
)

// erroringTmux wraps the fake tmux so a test can force one call to fail,
// exercising the pause machine's error-handling branches (a real tmux really
// can fail these calls; the fake alone never does).
type erroringTmux struct {
	*fakeTmux
	envErr, killErr, keysErr, panesErr, startErr error
}

func (e *erroringTmux) Start(ctx context.Context, name, cwd string, env map[string]string, argv []string) error {
	if e.startErr != nil {
		return e.startErr
	}
	return e.fakeTmux.Start(ctx, name, cwd, env, argv)
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
	if _, err := s.Resume(ctx, w.Name, "", ""); err != nil {
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
	// P0-crash-1: spawning the two workers above already recorded their own
	// (harmless, defensive) startup kills; only what TickPause itself adds
	// is what this test is about.
	before := len(tm.killed)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != before {
		t.Fatalf("a pane owned by another session must not be killed: %v", tm.killed[before:])
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

// Fix R-1 (final review): the case above still has a live child (first) at
// the moment PauseAll runs, so liveDescendants(ds) > 0 was already true and
// masked the real gap. Here the ONLY descendant left when PauseAll is called
// is the queued one -- the live sibling was cancelled first, freeing its
// slot -- so a scope decision keyed on liveDescendants alone computes 0 and
// leaves the orchestrator at session scope, invisible to DrainQueue's freeze
// (rootHasLiveSubtreePause is keyed on pause_scope='subtree'). The queued
// sibling then gets admitted on the very next reconcile-style DrainQueue
// pass, completely unaffected by the pause-all that just ran. Falsified by
// reverting hasWaitingDescendant's queued() branch back to liveDescendants:
// this test then fails with the sibling admitted.
func TestPauseAllFreezesAQueuedSpawnWithNoLiveSiblingLeft(t *testing.T) {
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

	// Cancel the first child through the real API (§10.5 Cancel, not raw
	// SQL) to free its slot, leaving the queued sibling as the orchestrator's
	// only descendant by the time PauseAll runs.
	if _, err := s.Cancel(ctx, first.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Agent(ctx, first.Name); err != nil || got.State != AgentFinished {
		t.Fatalf("first.State = %v, err = %v, want finished after Cancel", got.State, err)
	}

	if _, err := s.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}

	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orchSes.PauseScope != "subtree" {
		t.Fatalf("orchestrator pause_scope = %q after pause-all, want subtree -- it still has a queued descendant", orchSes.PauseScope)
	}
	if !orchSes.PauseRoot {
		t.Fatal("orchestrator pause_root = false, want true -- pause-all's own target must be the subtree root")
	}
	live, err := s.rootHasLiveSubtreePause(ctx, orch.RootItemID)
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		t.Fatal("rootHasLiveSubtreePause = false, want true -- a subtree pause is in flight over the whole root")
	}

	// The queued sibling must not be admitted by a drain pass while the
	// pause is still live, even though its slot has been free since Cancel.
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Agent(ctx, second.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AgentQueued {
		t.Fatalf("state = %s, want still queued -- pause-all must freeze the queue even with no live descendant left", got.State)
	}

	// It must not be blocked forever either: once the orchestrator's own
	// pause resolves (promoted, then handed off/paused -- the same "process
	// exited" simulation TestSubtreePausePausesChildrenBeforeTheOrchestrator
	// uses), the freeze thaws and the next drain admits it.
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	orchSes, err = s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orchSes.State != PauseRequested {
		t.Fatalf("orchestrator state = %s, want pause_requested -- nothing live left to wait on", orchSes.State)
	}
	// The combined handoff must not list the queued sibling as a paused
	// child: it never ran, so it never had a checkpoint to report.
	var payload string
	if err := s.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages WHERE to_agent_id = ? AND kind = 'control'
		ORDER BY created_at DESC LIMIT 1`, orch.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, second.Name) {
		t.Fatalf("combined handoff payload lists the never-ran queued sibling: %s", payload)
	}

	s.WriteCheckpoint(ctx, orchSes.ID, CheckpointInput{Kind: Handoff, Summary: "handing off"})
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE id = ?`, orchSes.ID); err != nil {
		t.Fatal(err)
	}

	live, err = s.rootHasLiveSubtreePause(ctx, orch.RootItemID)
	if err != nil {
		t.Fatal(err)
	}
	if live {
		t.Fatal("rootHasLiveSubtreePause = true, want false once the root's own session has ended")
	}
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = s.Agent(ctx, second.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AgentActive {
		t.Fatalf("state = %s, want active -- the pause resolved, so the queue must thaw and admit it", got.State)
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

	upgradedAt := s.Now()
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
	// The upgrade re-stamps the deadline: it is a new pause operation, and
	// reusing the part-spent session-scope deadline let step 4's fallback fire
	// on the very next tick (F4), cutting the agent's own handoff window short.
	// Repeat-call protection comes from the upgraded row now being a root, not
	// from refusing to stamp -- asserted by the second call below.
	if got.PauseDeadlineAt == nil || !got.PauseDeadlineAt.After(*before.PauseDeadlineAt) {
		t.Fatalf("deadline = %v, want re-stamped past the old %v by the upgrade", got.PauseDeadlineAt, before.PauseDeadlineAt)
	}
	if want := upgradedAt.Add(time.Duration(s.pauseDeadlineSec(ctx)) * time.Second); got.PauseDeadlineAt.Before(want) ||
		got.PauseDeadlineAt.After(want.Add(time.Second)) {
		t.Fatalf("deadline = %v, want a fresh full window (~%v)", got.PauseDeadlineAt, want)
	}
	again, err := s.Pause(ctx, orch.Name, "subtree")
	if err != nil {
		t.Fatal(err)
	}
	if again.PauseDeadlineAt == nil || !again.PauseDeadlineAt.Equal(*got.PauseDeadlineAt) {
		t.Fatalf("a second subtree pause moved the deadline again (%v -> %v): an upgraded target is already this pause's root, so it must be a no-op",
			got.PauseDeadlineAt, again.PauseDeadlineAt)
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
	if after.PauseDeadlineAt == nil || before.PauseDeadlineAt == nil || !after.PauseDeadlineAt.After(*before.PauseDeadlineAt) {
		t.Fatalf("orchestrator deadline = %v, want re-stamped past the old %v by the upgrade (F4: a part-spent deadline arms step 4's fallback immediately)",
			before.PauseDeadlineAt, after.PauseDeadlineAt)
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

	// Resolve the pause the way the daemon does: the root's session ends
	// (reconcile writes 'paused'/'interrupted'). Nothing in the daemon ever
	// clears pause_scope or pause_root -- a resolved pause is one whose rows
	// have left the live states -- so an earlier version of this step, which
	// hand-nulled pause_scope while leaving pause_root = 1 behind, resolved
	// the pause through a row shape no writer can produce.
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE agent_id = ?`,
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
	_, err := s.Resume(ctx, w.Name, "", "")
	if err == nil || err.Error() != "Still stopping. Try again in a few seconds." {
		t.Fatalf("err = %v", err)
	}
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused', provider_session_id = 'p1' WHERE id = ?`, wSes.ID)
	if _, err := s.Resume(ctx, w.Name, "", ""); err != nil {
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
	if _, err := s.Resume(ctx, w.Name, "", ""); err != nil {
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
	if _, err := s.Resume(ctx, w.Name, "", ""); err != nil {
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
	// P0-crash-1: worker()'s own spawns already recorded their (harmless)
	// startup kills before this env error was even wired up.
	before := len(tm.killed)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != before {
		t.Fatalf("an unreadable env must not be treated as a match: %v", tm.killed[before:])
	}
}

// A real Kill failure surfaces to the reconciler instead of being swallowed.
func TestTickPausePropagatesAKillError(t *testing.T) {
	s, tm, at := clockStore(t)
	// P0-crash-1: startSession now kills any stale pane before it starts one,
	// so killErr must not be set until after worker()'s own spawns are done,
	// or their own defensive kill would fail spawning, not TickPause's.
	et := &erroringTmux{fakeTmux: tm}
	s.Tmux = et
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	et.killErr = errors.New("tmux kill-session failed")
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
	if _, err := s.Resume(ctx, a.Name, "", ""); err == nil {
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
	if _, err := s.Resume(ctx, "no-such-agent", "", ""); err == nil {
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

// An already-pausing descendant is left alone by pauseSubtree's own role gate
// (only roleIdle is cascaded onto), and -- since the predicate refactor -- is
// no longer counted either: n reports what this call actually brought into a
// pause, and this worker was already in one. PauseAll's own query still
// enumerates it (that is how its target is found), it just adds nothing.
// Renamed from ...CountsAnAlreadyPausingSessionOnce, body kept: the count
// contract changed under it (F5), the shape it probes did not.
func TestPauseAllDoesNotCountAnAlreadyPausingSession(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, wSes.ID)
	n, err := s.PauseAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("requested = %d, want only the orchestrator -- the coder was already pausing, so this call transitioned nothing for it", n)
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

// pauseCols is one session's three pause-bookkeeping columns plus its
// deadline, read straight from the row so a test can assert on what the
// subtree-pause predicate actually sees (Session itself carries pause_root
// too, but reading the row proves the write, not the struct).
type pauseCols struct {
	state    SessionState
	scope    string
	root     bool
	deadline *time.Time
}

func mustPauseCols(t *testing.T, s *Store, sesID string) pauseCols {
	t.Helper()
	var c pauseCols
	var st string
	var rootInt int
	var deadline sql.NullInt64
	if err := s.DB.QueryRowContext(context.Background(), `SELECT state, COALESCE(pause_scope, ''),
		pause_root, pause_deadline_at FROM sessions WHERE id = ?`, sesID).Scan(&st, &c.scope, &rootInt, &deadline); err != nil {
		t.Fatal(err)
	}
	c.state, c.root = SessionState(st), rootInt != 0
	if deadline.Valid {
		d := db.FromMillis(deadline.Int64)
		c.deadline = &d
	}
	return c
}

// threeLevel builds root -> mid -> leaf, the shape every mid-tree finding in
// this cluster needs (a descendant that is itself an orchestrator).
func threeLevel(t *testing.T, s *Store) (root, mid, leaf Agent) {
	t.Helper()
	ctx := context.Background()
	seedEpicWithTask(t, s)
	root, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	mid, _, err = s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: root.ID, Brief: BriefInput{Objective: "mid"}})
	if err != nil {
		t.Fatal(err)
	}
	leaf, _, err = s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: mid.ID, Brief: BriefInput{Objective: "leaf"}})
	if err != nil {
		t.Fatal(err)
	}
	return root, mid, leaf
}

// F1 (round-4 review): Pause(x, "subtree") where x is already a MEMBER of an
// outer subtree pause (cascaded onto: pause_scope='subtree', pause_root=0)
// must be a no-op. The outer pause already owns x; letting a member become a
// root lets it fabricate its own "orchestrator did not respond" checkpoint one
// tick later, exactly the state Ruling A defines pause_root to make
// impossible for a mid-tree descendant.
func TestSubtreePauseOnACascadedMemberIsANoOp(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	root, mid, _ := threeLevel(t, s)
	if _, err := s.Pause(ctx, root.Name, "subtree"); err != nil {
		t.Fatal(err)
	}
	midSes := mustSessionID(t, s, mid.ID)
	before := mustPauseCols(t, s, midSes)
	if before.state != PauseRequested || before.scope != "subtree" || before.root {
		t.Fatalf("setup: mid = %+v, want pause_requested/subtree/pause_root=0 (a cascaded member)", before)
	}

	if _, err := s.Pause(ctx, mid.Name, "subtree"); err != nil {
		t.Fatal(err)
	}

	after := mustPauseCols(t, s, midSes)
	if after.root {
		t.Fatal("mid got pause_root=1: a member of an outer subtree pause cannot become its own root (Ruling A)")
	}
	if after.state != before.state || after.scope != before.scope {
		t.Fatalf("mid = %+v, want untouched at %+v", after, before)
	}
	if after.deadline == nil || before.deadline == nil || !after.deadline.Equal(*before.deadline) {
		t.Fatalf("mid deadline moved (before=%v after=%v), want untouched", before.deadline, after.deadline)
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
		t.Fatalf("mid got %d daemon-written checkpoint(s), want 0 -- it is a member of the outer pause, not a root", n)
	}
}

// F2 (round-4 review, pre-existing): a descendant that is itself a PENDING
// subtree-pause root (running/subtree/pause_root=1, awaiting its own
// promotion) must be skipped by an outer cascade. pauseOne'ing it moves its
// state off 'running', which drops it out of promotePendingSubtreePauses for
// good and permanently loses the combined handoff payload §10.5 step 3
// promises ("the list of child checkpoints in the payload"). An inner subtree
// pause manages itself; the outer pause waits on it like any live descendant.
func TestOuterSubtreePauseSkipsADescendantThatIsItselfAPendingRoot(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	root, mid, leaf := threeLevel(t, s)
	if _, err := s.Pause(ctx, mid.Name, "subtree"); err != nil {
		t.Fatal(err)
	}
	midSes := mustSessionID(t, s, mid.ID)
	before := mustPauseCols(t, s, midSes)
	if before.state != Running || before.scope != "subtree" || !before.root {
		t.Fatalf("setup: mid = %+v, want running/subtree/pause_root=1 (a pending inner root)", before)
	}

	if _, err := s.Pause(ctx, root.Name, "subtree"); err != nil {
		t.Fatal(err)
	}

	after := mustPauseCols(t, s, midSes)
	if after.state != Running {
		t.Fatalf("mid state = %s, want still running -- an inner pending root must not be pauseOne'd by the outer cascade (it would lose its own combined handoff)", after.state)
	}
	if !after.root || after.scope != "subtree" {
		t.Fatalf("mid = %+v, want its own root marking intact", after)
	}
	if after.deadline == nil || before.deadline == nil || !after.deadline.Equal(*before.deadline) {
		t.Fatalf("mid deadline moved (before=%v after=%v), want untouched by the outer cascade", before.deadline, after.deadline)
	}

	// The payload guarantee itself: once the leaf hands off, mid's own
	// promotion still fires and still carries the leaf's checkpoint.
	leafSes, err := s.LatestSession(ctx, leaf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, leafSes.ID, nil, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, leafSes.ID, CheckpointInput{Kind: Handoff, Summary: "leaf handed off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE id = ?`, leafSes.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	promoted := mustPauseCols(t, s, midSes)
	if promoted.state != PauseRequested {
		t.Fatalf("mid state = %s, want pause_requested after its own promotion", promoted.state)
	}
	var payload string
	if err := s.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages WHERE to_agent_id = ?
		AND kind = 'control' ORDER BY rowid DESC LIMIT 1`, mid.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "leaf handed off") {
		t.Fatalf("mid's pause carries its child's checkpoints (§10.5 step 3): %s", payload)
	}
}

// F4 (round-4 review): a CHILDLESS already-pausing target reached through the
// direct Pause(x, "subtree") API must be a no-op. An upgrade with nothing to
// cascade to only re-marks the row as a subtree root, which arms step 4's
// daemon fallback against a deadline the agent is already part-way through --
// cutting its legitimate remaining handoff window short. PauseAll already
// effectively guaranteed this by computing scope="session" for a childless
// target (TestPauseAllLeavesAnAlreadyPausingChildlessTargetAtSessionScope);
// this is the same guarantee for the direct entry point.
func TestSubtreePauseOnAChildlessAlreadyPausingTargetIsANoOp(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pause(ctx, orch.Name, "session"); err != nil {
		t.Fatal(err)
	}
	sesID := mustSessionID(t, s, orch.ID)
	before := mustPauseCols(t, s, sesID)

	if _, err := s.Pause(ctx, orch.Name, "subtree"); err != nil {
		t.Fatal(err)
	}

	after := mustPauseCols(t, s, sesID)
	if after.scope != "session" || after.root {
		t.Fatalf("childless target = %+v, want left at session scope with pause_root=0 -- no live descendant to justify an upgrade", after)
	}
	if after.deadline == nil || before.deadline == nil || !after.deadline.Equal(*before.deadline) {
		t.Fatalf("deadline moved (before=%v after=%v) on a no-op", before.deadline, after.deadline)
	}
	at.Advance(200 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints
		WHERE agent_id = ? AND daemon_written = 1`, orch.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("childless target got %d daemon-written checkpoint(s) on the next tick, want 0 -- its own handoff window must not be cut short by a spurious upgrade", n)
	}
}

// Sixth sibling shape, found while routing every gate through the predicate
// (not one of the review's five): Pause(x, "session") on an x that is a
// PENDING subtree root (running/subtree/pause_root=1) used to fall through to
// pauseOne -- 'running' was never Pausing(), so the idempotency guard missed
// it -- overwriting pause_scope with 'session' and moving it to
// pause_requested. That drops it out of promotePendingSubtreePauses (state no
// longer 'running') AND out of rootHasLiveSubtreePause once its descendants
// finish, so the tail-window queue freeze reopens. Two ordinary CLI calls.
func TestSessionPauseOnAPendingSubtreeRootIsANoOp(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	if _, err := s.Pause(ctx, orch.Name, "subtree"); err != nil {
		t.Fatal(err)
	}
	sesID := mustSessionID(t, s, orch.ID)
	before := mustPauseCols(t, s, sesID)
	if before.state != Running || before.scope != "subtree" || !before.root {
		t.Fatalf("setup: orchestrator = %+v, want running/subtree/pause_root=1", before)
	}

	if _, err := s.Pause(ctx, orch.Name, "session"); err != nil {
		t.Fatal(err)
	}

	after := mustPauseCols(t, s, sesID)
	if after.state != Running || after.scope != "subtree" || !after.root {
		t.Fatalf("orchestrator = %+v, want untouched at %+v -- a session pause cannot downgrade a live subtree pause", after, before)
	}
	if after.deadline == nil || before.deadline == nil || !after.deadline.Equal(*before.deadline) {
		t.Fatalf("deadline moved (before=%v after=%v) on a no-op", before.deadline, after.deadline)
	}
	live, err := s.rootHasLiveSubtreePause(ctx, orch.RootItemID)
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		t.Fatal("rootHasLiveSubtreePause = false: the queue freeze must survive a session-scope pause on the same root")
	}

	// The promotion it would have lost still happens, with the payload.
	wSes, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, wSes.ID, nil, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "worker handed off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE id = ?`, wSes.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := s.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages WHERE to_agent_id = ?
		AND kind = 'control' ORDER BY rowid DESC LIMIT 1`, orch.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "worker handed off") {
		t.Fatalf("the orchestrator's promotion lost its child payload: %s", payload)
	}
}

// F5 (round-4 review): PauseAll's n must count only what this call actually
// brought into a pause -- descendants its cascade pauseOne'd, plus a target
// newly marked as a subtree root. A tree already fully under a pause
// transitions nothing, so n is 0, and a repeated pause-all must not re-stamp
// the root's deadline either.
func TestPauseAllCountsNothingForATreeAlreadyPaused(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	first, err := s.PauseAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != 2 {
		t.Fatalf("requested = %d, want 2 (the orchestrator's own subtree pause and the worker it cascades onto)", first)
	}
	sesID := mustSessionID(t, s, orch.ID)
	before := mustPauseCols(t, s, sesID)

	second, err := s.PauseAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second != 0 {
		t.Fatalf("requested = %d on a tree already fully paused, want 0 -- nothing was transitioned", second)
	}
	after := mustPauseCols(t, s, sesID)
	if after.deadline == nil || before.deadline == nil || !after.deadline.Equal(*before.deadline) {
		t.Fatalf("the root's deadline moved (before=%v after=%v) on a repeated pause-all", before.deadline, after.deadline)
	}
}

// TestSubtreeRoleTable is the predicate's entire state table: every
// SessionState (all 11, taken from sessionLabels so a new one added to the
// model fails here until it is classified) x every pause_scope value ("",
// "session", "subtree") x both pause_root values. 66 cells, each with its
// expected role and, where the combination cannot occur in production, why.
//
// Four fix rounds in this cluster each closed one bare boolean check in
// pause.go and had review find the adjacent one, because the "table" being
// reasoned about was a hand-maintained cross-product over pairs of ROWS. This
// one is mechanical and about a single row: the inter-row logic (cascade skip,
// promotion eligibility, overdue detection, queue freeze) is then just "which
// roles does this gate accept", proved by the DB-level tests below.
func TestSubtreeRoleTable(t *testing.T) {
	type cell struct {
		state SessionState
		scope string
		root  bool
		want  subtreeRole
		// unreachable is empty when the combination occurs in production, and
		// otherwise says why it cannot -- checked against every writer of these
		// columns, not assumed.
		unreachable string
	}
	const (
		neverRespawns = "unreachable: nothing writes 'spawning' after the session INSERT, which leaves both columns at their defaults"
		startupRace   = "unreachable but for a watchStartup TOCTOU (it checks 'still spawning?' and only later writes 'running', so a pause landing in between can be overwritten); the stale scope is deliberately read as no pause in flight"
		scopeFirst    = "unreachable: pauseOne writes pause_scope in the same statement as 'pause_requested', and the quiescing/stopping hops only move a row that already has one"
		rootWithScope = "unreachable: pause_root = 1 is only ever written by pauseSubtree, in the same statement as pause_scope = 'subtree', and nothing clears either column"
		endedStale    = "reachable: a resolved session keeps its pause columns as history, and !Live() is the first thing the predicate answers"
	)
	cells := []cell{
		// pause_scope NULL, pause_root = 0: a session never touched by a pause.
		{Spawning, "", false, roleIdle, ""},
		{Running, "", false, roleIdle, ""},
		{PauseRequested, "", false, roleSessionPausing, scopeFirst},
		{Quiescing, "", false, roleSessionPausing, scopeFirst},
		{Stopping, "", false, roleSessionPausing, scopeFirst},
		{Paused, "", false, roleNone, ""},
		{Interrupted, "", false, roleNone, ""},
		{Completed, "", false, roleNone, ""},
		{Failed, "", false, roleNone, ""},
		{Crashed, "", false, roleNone, ""},
		{Cancelled, "", false, roleNone, ""},

		// pause_scope NULL, pause_root = 1: a root marking with no scope.
		{Spawning, "", true, roleInvalid, rootWithScope},
		{Running, "", true, roleInvalid, rootWithScope},
		{PauseRequested, "", true, roleInvalid, rootWithScope},
		{Quiescing, "", true, roleInvalid, rootWithScope},
		{Stopping, "", true, roleInvalid, rootWithScope},
		{Paused, "", true, roleNone, endedStale},
		{Interrupted, "", true, roleNone, endedStale},
		{Completed, "", true, roleNone, endedStale},
		{Failed, "", true, roleNone, endedStale},
		{Crashed, "", true, roleNone, endedStale},
		{Cancelled, "", true, roleNone, endedStale},

		// pause_scope 'session', pause_root = 0: an ordinary single-session pause.
		{Spawning, "session", false, roleIdle, neverRespawns},
		{Running, "session", false, roleIdle, startupRace},
		{PauseRequested, "session", false, roleSessionPausing, ""},
		{Quiescing, "session", false, roleSessionPausing, ""},
		{Stopping, "session", false, roleSessionPausing, ""},
		{Paused, "session", false, roleNone, ""},
		{Interrupted, "session", false, roleNone, ""},
		{Completed, "session", false, roleNone, ""},
		{Failed, "session", false, roleNone, ""},
		{Crashed, "session", false, roleNone, ""},
		{Cancelled, "session", false, roleNone, ""},

		// pause_scope 'session', pause_root = 1: a root marking the scope
		// contradicts. Round 3's own bug produced exactly this row
		// (Pause(x,"session") on a pending root overwrote the scope); the
		// predicate closes that write, so no writer can reach it any more.
		{Spawning, "session", true, roleInvalid, rootWithScope},
		{Running, "session", true, roleInvalid, rootWithScope},
		{PauseRequested, "session", true, roleInvalid, rootWithScope},
		{Quiescing, "session", true, roleInvalid, rootWithScope},
		{Stopping, "session", true, roleInvalid, rootWithScope},
		{Paused, "session", true, roleNone, endedStale},
		{Interrupted, "session", true, roleNone, endedStale},
		{Completed, "session", true, roleNone, endedStale},
		{Failed, "session", true, roleNone, endedStale},
		{Crashed, "session", true, roleNone, endedStale},
		{Cancelled, "session", true, roleNone, endedStale},

		// pause_scope 'subtree', pause_root = 0: a descendant cascaded onto by
		// someone else's subtree pause. That pause owns it: it is never
		// re-paused, never promoted and never given a fallback checkpoint.
		{Spawning, "subtree", false, roleIdle, neverRespawns},
		{Running, "subtree", false, roleIdle, startupRace},
		{PauseRequested, "subtree", false, roleMember, ""},
		{Quiescing, "subtree", false, roleMember, ""},
		{Stopping, "subtree", false, roleMember, ""},
		{Paused, "subtree", false, roleNone, ""},
		{Interrupted, "subtree", false, roleNone, ""},
		{Completed, "subtree", false, roleNone, ""},
		{Failed, "subtree", false, roleNone, ""},
		{Crashed, "subtree", false, roleNone, ""},
		{Cancelled, "subtree", false, roleNone, ""},

		// pause_scope 'subtree', pause_root = 1: the target of a subtree pause,
		// in each phase. roleRootSpawning is reachable (Pause(x,"subtree") on a
		// session whose pane is still starting up) and is the F3 row carried to
		// the final whole-branch review: no gate advances it, and watchStartup's
		// 30 s bound is not re-armed on a daemon restart. It is inert here --
		// never promoted, never given a fallback -- and holds the queue freeze.
		{Spawning, "subtree", true, roleRootSpawning, ""},
		{Running, "subtree", true, roleRootPending, ""},
		{PauseRequested, "subtree", true, roleRootActive, ""},
		{Quiescing, "subtree", true, roleRootActive, ""},
		{Stopping, "subtree", true, roleRootStopping, ""},
		{Paused, "subtree", true, roleNone, ""},
		{Interrupted, "subtree", true, roleNone, ""},
		{Completed, "subtree", true, roleNone, ""},
		{Failed, "subtree", true, roleNone, ""},
		{Crashed, "subtree", true, roleNone, ""},
		{Cancelled, "subtree", true, roleNone, ""},
	}

	seen := map[string]bool{}
	for _, c := range cells {
		key := string(c.state) + "/" + c.scope + "/" + map[bool]string{true: "1", false: "0"}[c.root]
		if seen[key] {
			t.Fatalf("duplicate cell %s", key)
		}
		seen[key] = true
		got := subtreeRoleOf(c.state, c.scope, c.root)
		if got != c.want {
			t.Errorf("subtreeRoleOf(%s, %q, %v) = %v, want %v", c.state, c.scope, c.root, got, c.want)
		}
		// Invariants the gates below rely on, asserted for every cell rather
		// than trusted: liveness is exactly the session's own liveness, and
		// only a genuinely marked subtree root reads as one.
		if got.live() != c.state.Live() {
			t.Errorf("%s: live() = %v, want %v", key, got.live(), c.state.Live())
		}
		if got.isRoot() && !(c.root && c.scope == "subtree") {
			t.Errorf("%s: isRoot() = true without both root columns set", key)
		}
		if got == roleNone && got.inSubtreePause() {
			t.Errorf("%s: an ended session must never hold a queue freeze", key)
		}
	}
	// Exhaustiveness: the cells above must be the whole cross-product.
	for state := range sessionLabels {
		for _, scope := range []string{"", "session", "subtree"} {
			for _, root := range []bool{false, true} {
				key := string(state) + "/" + scope + "/" + map[bool]string{true: "1", false: "0"}[root]
				if !seen[key] {
					t.Errorf("missing cell %s -- every state x scope x pause_root must be classified", key)
				}
			}
		}
	}
	if want := len(sessionLabels) * 3 * 2; len(cells) != want {
		t.Fatalf("%d cells, want %d (every SessionState x 3 scopes x 2 pause_root values)", len(cells), want)
	}
}

// The roleInvalid and roleRootSpawning rows, forced by raw SQL because no
// writer can produce the first and the second needs a pause landing inside
// startup: neither is ever acted on -- not cascaded onto, not promoted, not
// given a fallback checkpoint, not re-paused -- and both keep the root's queue
// frozen, since a row nothing here understands must not thaw one.
func TestUnactionableSubtreeRowsAreInertButStillFreezeTheQueue(t *testing.T) {
	for _, tc := range []struct {
		name         string
		state, scope string
		root         int
		wantRole     subtreeRole
	}{
		{"invalid", "pause_requested", "session", 1, roleInvalid},
		{"root spawning", "spawning", "subtree", 1, roleRootSpawning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, at := clockStore(t)
			ctx := context.Background()
			orch, w, _ := worker(t, s)
			wSes := mustSessionID(t, s, w.ID)
			if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = ?, pause_scope = ?,
				pause_root = ?, pause_deadline_at = ? WHERE id = ?`, tc.state, tc.scope, tc.root,
				db.Millis(s.Now()), wSes); err != nil {
				t.Fatal(err)
			}
			before := mustPauseCols(t, s, wSes)
			if got := subtreeRoleOf(before.state, before.scope, before.root); got != tc.wantRole {
				t.Fatalf("forced row has role %v, want %v", got, tc.wantRole)
			}

			// Its own pause is a no-op, an outer cascade steps over it, and no
			// tick promotes it or writes it a fallback checkpoint.
			if _, err := s.Pause(ctx, w.Name, "subtree"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pause(ctx, orch.Name, "subtree"); err != nil {
				t.Fatal(err)
			}
			at.Advance(200 * time.Second)
			if err := s.TickPause(ctx); err != nil {
				t.Fatal(err)
			}
			after := mustPauseCols(t, s, wSes)
			if after.state != before.state || after.scope != before.scope || after.root != before.root {
				t.Fatalf("row = %+v, want inert at %+v", after, before)
			}
			if after.deadline == nil || before.deadline == nil || !after.deadline.Equal(*before.deadline) {
				t.Fatalf("deadline moved (before=%v after=%v)", before.deadline, after.deadline)
			}
			var n int
			if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints
				WHERE agent_id = ? AND daemon_written = 1`, w.ID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("got %d daemon-written checkpoint(s), want 0", n)
			}
			var msgs int
			if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
				WHERE to_agent_id = ? AND kind = 'control'`, w.ID).Scan(&msgs); err != nil {
				t.Fatal(err)
			}
			if msgs != 0 {
				t.Fatalf("got %d control message(s), want 0 -- nothing may be requested of a row nothing understands", msgs)
			}
			live, err := s.rootHasLiveSubtreePause(ctx, orch.RootItemID)
			if err != nil {
				t.Fatal(err)
			}
			if !live {
				t.Fatal("rootHasLiveSubtreePause = false: an unactionable row must hold the freeze, not thaw it")
			}
		})
	}
}

// Seventh sibling shape, found while routing PauseAll's count through the
// predicate: a child can START under a live subtree pause. Admit is purely
// limit-based and DrainQueue is the only caller of rootHasLiveSubtreePause, so
// a direct spawn while the root is still 'running' (its own pause has not
// reached it yet) is admitted immediately. A repeated pause-all must cascade
// onto that child -- while still leaving the root's own row alone, since
// re-stamping it on every repeat call is what the deadline-extension guard
// exists to prevent.
func TestRepeatedPauseAllCascadesOntoAChildSpawnedUnderAPendingRoot(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	setLimits(t, s, 3, 4, 4)
	seedEpicWithTwoTasks(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pause(ctx, orch.Name, "subtree"); err != nil {
		t.Fatal(err)
	}
	orchSes := mustSessionID(t, s, orch.ID)
	firstSes := mustSessionID(t, s, first.ID)
	orchBefore, firstBefore := mustPauseCols(t, s, orchSes), mustPauseCols(t, s, firstSes)
	if orchBefore.state != Running || !orchBefore.root {
		t.Fatalf("setup: orchestrator = %+v, want a pending root", orchBefore)
	}

	second, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("setup: the second child must be admitted directly (Admit is limit-based, not freeze-gated) for this shape to exist")
	}

	n, err := s.PauseAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondCols := mustPauseCols(t, s, mustSessionID(t, s, second.ID))
	if secondCols.state != PauseRequested || secondCols.scope != "subtree" || secondCols.root {
		t.Fatalf("late child = %+v, want pause_requested/subtree/pause_root=0 -- a root's repeat pause must still cascade onto descendants that appeared since", secondCols)
	}
	if n != 1 {
		t.Fatalf("requested = %d, want 1 -- only the late child was transitioned", n)
	}
	orchAfter := mustPauseCols(t, s, orchSes)
	if orchAfter.state != orchBefore.state || orchAfter.scope != orchBefore.scope || orchAfter.root != orchBefore.root ||
		!orchAfter.deadline.Equal(*orchBefore.deadline) {
		t.Fatalf("orchestrator = %+v, want untouched at %+v -- a root re-cascades without re-stamping its own row", orchAfter, orchBefore)
	}
	firstAfter := mustPauseCols(t, s, firstSes)
	if firstAfter.state != firstBefore.state || !firstAfter.deadline.Equal(*firstBefore.deadline) {
		t.Fatalf("the already-paused child = %+v, want untouched at %+v", firstAfter, firstBefore)
	}
}
