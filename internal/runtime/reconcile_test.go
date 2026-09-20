package runtime

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// A real tmux failure to list panes at all must surface, not be treated as
// "every session is gone."
func TestReconcilePropagatesAPanesError(t *testing.T) {
	s, tm, _ := clockStore(t)
	s.Tmux = &erroringTmux{fakeTmux: tm, panesErr: errors.New("tmux list-panes failed")}
	if err := s.Reconcile(context.Background()); err == nil {
		t.Fatal("a Panes failure must propagate")
	}
}

// erroringNotifier always fails, so a test can verify a notify failure
// actually surfaces instead of being silently swallowed.
type erroringNotifier struct{ err error }

func (e *erroringNotifier) Raise(context.Context, *sql.Tx, NotifyInput) error { return e.err }

// A notification failure on an unknown tmux session must surface, not vanish.
func TestReconcilePropagatesANotifyFailure(t *testing.T) {
	s, tm, _ := clockStore(t)
	s.Notify = &erroringNotifier{err: errors.New("notify store is down")}
	panes(tm, Pane{Session: "someone-elses-session", Command: "vim"})
	if err := s.Reconcile(context.Background()); err == nil {
		t.Fatal("a notify failure must propagate")
	}
}

// A stale notification failure must also surface.
func TestResolveAliveStalePropagatesANotifyFailure(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	s.Notify = &erroringNotifier{err: errors.New("notify store is down")}
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Quiet2", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	tm.captures[a.Name] = []string{"working on it…\n"}
	at.Advance(31 * time.Minute)
	if err := s.Reconcile(ctx); err == nil {
		t.Fatal("a stale notify failure must propagate")
	}
}

// A session that has JUST started must not be marked crashed just because
// this tick's Panes() snapshot doesn't yet contain its pane. P0-crash-2
// (2026-09-19): a real, live, actively-working orchestrator session was
// marked 'crashed' 4.3s after Start() -- confirmed live, the tmux pane was
// genuinely still alive and its SWARM_SESSION env still matched. Only after
// spawnGracePeriod elapses with still no matching pane does Reconcile treat
// it as really gone.
func TestReconcileGivesAFreshSessionAGracePeriodBeforeMarkingItCrashed(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Quiet3", Intent: "feature", Kind: Fake, Model: "fake-1"})
	panes(tm) // no pane at all yet -- simulates a tick whose snapshot raced Start()
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State == Crashed {
		t.Fatal("a session inside the grace period must not be marked crashed just because its pane isn't in this tick's snapshot yet")
	}
	at.Advance(11 * time.Second) // past spawnGracePeriod since StartedAt
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	// This session has still never been confirmed alive, so this tick grants
	// one more fresh grace window (P0-crash-4) rather than crashing outright.
	ses, err = s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State == Crashed {
		t.Fatal("a session never confirmed alive must get one fresh grace window before crashing, not crash on the first tick past its StartedAt-anchored window")
	}
	at.Advance(spawnGracePeriod + time.Second) // past that fresh window too
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ses, err = s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != Crashed {
		t.Fatalf("a session whose pane never appears must eventually be marked crashed once every grace window elapses, got %s", ses.State)
	}
}

// P0-crash-3 (2026-09-19): a real, live, actively-working orchestrator
// session -- long past its own start, and already confirmed alive on an
// earlier reconcile tick -- was marked crashed a second time when a single
// later tick's Panes() snapshot missed its pane (exit_code stayed NULL, §10.6's
// own signal that this was a listing miss, not tmux reporting the pane dead).
// spawnGracePeriod must protect this case too, not just a session's first few
// seconds: the grace window now counts from the last tick that confirmed the
// pane (lastAliveAt), not only from StartedAt.
func TestReconcileGivesAnEstablishedSessionTheSameGraceOnALaterMissingTick(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})

	// Move well past spawnGracePeriod's StartedAt-anchored window before the
	// session is ever seen once, so the old P0-crash-2 grace (which only ever
	// looked at StartedAt) cannot be what saves it below -- only lastAliveAt can.
	at.Advance(spawnGracePeriod + 5*time.Second)
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if ses, _ := s.LatestSession(ctx, w.ID); ses.State == Crashed {
		t.Fatal("the tick that first confirms an alive pane must not itself crash the session")
	}

	// The pane vanishes from a single later tick's snapshot. This must NOT
	// immediately crash it: it was just confirmed alive, so it gets the same
	// grace period a brand-new session gets.
	panes(tm) // pane missing from this tick's snapshot only
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if ses, _ := s.LatestSession(ctx, w.ID); ses.State == Crashed {
		t.Fatal("P0-crash-3: a single missing-pane tick on an already-confirmed-alive session must not immediately crash it")
	}

	// Only once it has stayed missing for spawnGracePeriod since it was last
	// confirmed alive is it really treated as gone.
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != Crashed {
		t.Fatalf("a session that stays missing past the grace period since it was last confirmed alive must eventually crash, got %s", ses.State)
	}
}

// P0-crash-4 (2026-09-19): a daemon restart (or an external correction back
// to a live state) wipes this process's in-memory lastAliveAt, even for a
// session that has genuinely been running fine for minutes. Confirmed live:
// right after a daemon restart, the very first reconcile tick for such a
// session saw a single !paneKnown miss, and because its real StartedAt was
// long past spawnGracePeriod, that one miss fell straight through to
// 'crashed' with zero chance to recover -- a crashed session is never
// re-evaluated by Reconcile again. Never having confirmed a session alive in
// THIS process's lifetime must not be treated as evidence it just started
// and is already overdue; it must get one fresh grace window from now.
func TestReconcileGivesAFreshGraceWindowToAnOldSessionNeverSeenAliveThisProcess(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})

	// The session is long past its own StartedAt-anchored grace window, and
	// this Store has never once called resolveAlive on it (no lastAliveAt
	// entry at all) -- exactly what a fresh daemon process sees for a
	// session that was already running before it started.
	at.Advance(spawnGracePeriod + 5*time.Second)
	panes(tm) // the very first tick this process ever runs already misses it
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if ses, _ := s.LatestSession(ctx, w.ID); ses.State == Crashed {
		t.Fatal("a session never seen alive by this process, but not actually gone, must get a fresh grace window instead of an immediately-expired one")
	}

	// It keeps missing past that fresh window: now it really is treated as gone.
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != Crashed {
		t.Fatalf("a session that keeps missing past its fresh grace window must still eventually crash, got %s", ses.State)
	}
}

// ReconcileLoop just wraps Reconcile in a ticker and stops on cancel; this
// only exercises that wiring, not the reconciliation logic itself (covered
// above).
func TestReconcileLoopStopsOnContextCancel(t *testing.T) {
	s, tm, _ := clockStore(t)
	panes(tm)
	// A hand-fed tick instead of the fake clock's instant one (which never
	// leaves the select block, so a cancel lands mid-Reconcile and races a
	// live query) or a real timer (same race, just rarer). One buffered tick
	// lets exactly one Reconcile complete; by the time we cancel, the loop is
	// parked back in the select with nothing left to receive, so cancel is the
	// only thing that can wake it — no race either way.
	tick := make(chan time.Time, 1)
	tick <- time.Now()
	s.After = func(time.Duration) <-chan time.Time { return tick }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.ReconcileLoop(ctx, time.Millisecond)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ReconcileLoop did not stop on cancel")
	}
}

func TestDeadPaneWithACompletedCheckpointCompletesTheSession(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Verification: []Verify{{Cmd: "go test", Phase: "red", OK: false}, {Cmd: "go test", Phase: "green", OK: true}},
		Git:          []GitRef{{Repo: "proj", Branch: "task/task-1", SHA: "abc1234"}}})
	panes(tm) // the pane is gone
	// spawnGracePeriod (P0-crash-2) needs real elapsed time before a
	// missing pane counts as gone, not just started -- advance past it.
	// This session has never gone through an alive-confirming Reconcile
	// tick, so the first tick past that window grants one more fresh grace
	// window (P0-crash-4) before a second tick actually resolves it dead.
	at.Advance(11 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != Completed {
		t.Fatalf("session state = %s", ses.State)
	}
	got, _ := s.Agent(ctx, w.Name)
	if got.State != AgentFinished {
		t.Fatalf("agent state = %s", got.State)
	}
}

func TestDeadPaneWithNoTerminalCheckpointCrashes(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	before, _ := s.Items.Get(ctx, "TASK-1")
	// The orchestrator gets its own live, matching pane too: without one, its
	// own session would also be resolved as dead-with-no-checkpoint and marked
	// crashed by the very logic this test exercises, racing the worker's own
	// crash notification.
	panes(tm, Pane{Session: w.Name, Dead: true, DeadStatus: 137, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != Crashed || ses.ExitCode == nil || *ses.ExitCode != 137 {
		t.Fatalf("session = %+v", ses)
	}
	after, _ := s.Items.Get(ctx, "TASK-1")
	if before.Status != after.Status {
		t.Fatalf("the item status must not change on a crash: %s → %s", before.Status, after.Status)
	}
	// The `attention` level is notify.Rules' (Task 23); here the assertion is that
	// the crash raised anything at all, against the right agent and item.
	n := notified(t, s, "agent.crashed")
	if n.ItemKey != "TASK-1" {
		t.Fatalf("notification = %+v", n)
	}
	var relays int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"crashed"%'`, orch.ID).Scan(&relays)
	if relays != 1 {
		t.Fatalf("relay crashed count = %d", relays)
	}
	// the agent stays listed until acknowledged (§10.6)
	got, _ := s.Agent(ctx, w.Name)
	if got.State != AgentActive {
		t.Fatalf("agent state = %s, want active until acknowledged", got.State)
	}
}

// A top-level crash (no parent to relay to) still records crashed.
// A crashed notify failure must surface too.
func TestDeadPaneCrashPropagatesANotifyFailure(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "WillCrash", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	s.Notify = &erroringNotifier{err: errors.New("notify store is down")}
	panes(tm)
	// spawnGracePeriod (P0-crash-2) needs real elapsed time before a
	// missing pane counts as gone, not just started -- advance past it.
	// This session has never gone through an alive-confirming Reconcile
	// tick, so the first tick past that window grants one more fresh grace
	// window (P0-crash-4) before a second tick actually resolves it dead.
	at.Advance(11 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err == nil {
		t.Fatal("a crash notify failure must propagate")
	}
}

// The "paused" notification on a dead-while-stopping session must also
// propagate a failure instead of leaving the session's state ambiguous.
func TestDeadPaneWhileStoppingPropagatesANotifyFailure(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	s.Notify = &erroringNotifier{err: errors.New("notify store is down")}
	_, _, wSes := worker(t, s)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, wSes.ID)
	panes(tm)
	// spawnGracePeriod (P0-crash-2) needs real elapsed time before a
	// missing pane counts as gone, not just started -- advance past it.
	// This session has never gone through an alive-confirming Reconcile
	// tick, so the first tick past that window grants one more fresh grace
	// window (P0-crash-4) before a second tick actually resolves it dead.
	at.Advance(11 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err == nil {
		t.Fatal("a paused notify failure must propagate")
	}
}

func TestDeadPaneCrashWithNoParentSkipsTheRelay(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Lonely", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	panes(tm)
	// spawnGracePeriod (P0-crash-2) needs real elapsed time before a
	// missing pane counts as gone, not just started -- advance past it.
	// This session has never gone through an alive-confirming Reconcile
	// tick, so the first tick past that window grants one more fresh grace
	// window (P0-crash-4) before a second tick actually resolves it dead.
	at.Advance(11 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LatestSession(ctx, a.ID)
	if got.State != Crashed {
		t.Fatalf("state = %s", got.State)
	}
}

func TestDeadPaneWithAFailedCheckpointFails(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: FailedCkp, Summary: "couldn't finish"})
	panes(tm)
	// spawnGracePeriod (P0-crash-2) needs real elapsed time before a
	// missing pane counts as gone, not just started -- advance past it.
	// This session has never gone through an alive-confirming Reconcile
	// tick, so the first tick past that window grants one more fresh grace
	// window (P0-crash-4) before a second tick actually resolves it dead.
	at.Advance(11 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != Failed {
		t.Fatalf("session state = %s", ses.State)
	}
}

// M6: an orchestrator with a live child owes something and is never waiting.
func TestOrchestratorWithALiveChildIsNeverWaiting(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	orchSes, _ := s.LatestSession(ctx, orch.ID)
	s.Sync(ctx, orchSes.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, orch.ID)
	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": orchSes.ID}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LatestSession(ctx, orch.ID)
	if got.Waiting {
		t.Fatal("an orchestrator with a live child owes something and must not be waiting")
	}
}

// M6: an agent with an open request it raised is never waiting.
func TestOwesNothingCountsAnOpenRequest(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Asking", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.Sync(ctx, ses.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, a.ID)
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "which one?"}); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LatestSession(ctx, a.ID)
	if got.Waiting {
		t.Fatal("an open question the agent raised means it owes something")
	}
}

// A real agent that crashes before it ever writes a single checkpoint (a bad
// launch flag, a model rejection, a network failure on its first turn, a pane
// killed externally) must still be marked crashed, notified and relayed
// within one reconcile tick — Reconcile must never quietly no-op just
// because nothing was ever recorded for this agent.
func TestDeadPaneCrashesEvenWithNoCheckpointAtAll(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	// No checkpoint written at all — not even "accepted".
	panes(tm, Pane{Session: w.Name, Dead: true, DeadStatus: 1, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != Crashed || ses.ExitCode == nil || *ses.ExitCode != 1 {
		t.Fatalf("session = %+v, want crashed with exit_code 1", ses)
	}
	n := notified(t, s, "agent.crashed")
	if n.AgentName != w.Name {
		t.Fatalf("notification = %+v", n)
	}
	var relays int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"crashed"%'`, orch.ID).Scan(&relays)
	if relays != 1 {
		t.Fatalf("relay crashed count = %d, want exactly one", relays)
	}
}

func TestDeadPaneWhileStoppingBecomesPaused(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, wSes.ID)
	panes(tm)
	// spawnGracePeriod (P0-crash-2) needs real elapsed time before a
	// missing pane counts as gone, not just started -- advance past it.
	// This session has never gone through an alive-confirming Reconcile
	// tick, so the first tick past that window grants one more fresh grace
	// window (P0-crash-4) before a second tick actually resolves it dead.
	at.Advance(11 * time.Second)
	s.Reconcile(ctx)
	at.Advance(11 * time.Second)
	s.Reconcile(ctx)
	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != Paused {
		t.Fatalf("state = %s", ses.State)
	}
}

func TestAliveWithAnOldCompletedCheckpointIsKilled(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	s.DB.ExecContext(ctx, `UPDATE items SET tdd_exempt = 'docs' WHERE key = 'TASK-1'`)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done"})
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	at.Advance(61 * time.Second)
	// P0-crash-1: worker()'s own spawns already recorded their (harmless)
	// startup kills; only what Reconcile itself adds is what this is about.
	before := len(tm.killed)
	s.Reconcile(ctx)
	if len(tm.killed)-before != 1 {
		t.Fatalf("killed = %v", tm.killed[before:])
	}
}

// M6: a waiting session is never marked stale.
func TestWaitingIsSetAndClearedAndNeverStale(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Waiting", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.Sync(ctx, ses.ID, nil, 20) // clear the inbox
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	s.Reconcile(ctx)
	got, _ := s.LatestSession(ctx, a.ID)
	if !got.Waiting {
		t.Fatal("an idle agent that owes nothing is waiting")
	}
	at.Advance(31 * time.Minute)
	s.Reconcile(ctx)
	if notifiedCount(s, "agent.stale") != 0 {
		t.Fatal("a waiting session is never marked stale (M6)")
	}
	// give it something to do; waiting clears
	enq(t, s, a.ID, a.RootItemID, "finding", `{"body":"x"}`, 1)
	s.Reconcile(ctx)
	got, _ = s.LatestSession(ctx, a.ID)
	if got.Waiting {
		t.Fatal("an un-acked message clears waiting")
	}
}

// A1: 30 min of silence while not waiting is stale.
func TestStaleAfterThirtyMinutesOfSilence(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Quiet", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	tm.captures[a.Name] = []string{"working on it…\n"} // not idle, so not waiting
	at.Advance(31 * time.Minute)
	s.Reconcile(ctx)
	if n := notifiedCount(s, "agent.stale"); n != 1 {
		t.Fatalf("agent.stale count = %d", n)
	}
	// a hook call clears it
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`,
		db.Millis(at.Now()), ses.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.stale"); n != 1 {
		t.Fatalf("the notification must not repeat while dedup holds: %d", n)
	}
}

// §10.6: a tmux session with no row is reported and never killed.
func TestUnknownTmuxSessionIsReportedNotKilled(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	panes(tm, Pane{Session: "someone-elses-session", Command: "vim"})
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	// §17.5's body is "{name} is running but Swarm has no record of it." — the name
	// is the tmux session, which runtime passes as AgentName.
	if n := notified(t, s, "tmux.unknown"); n.AgentName != "someone-elses-session" {
		t.Fatalf("notification = %+v", n)
	}
	if len(tm.killed) != 0 {
		t.Fatalf("an unknown session must never be killed: %v", tm.killed)
	}
}

// A failed/crashed/cancelled session's own leftover pane is not "someone
// else's" the way TestUnknownTmuxSessionIsReportedNotKilled's is: swarm knows
// exactly whose it is, so it must never be flagged tmux.unknown. A live one is
// still left alone for a human to inspect (P0-crash-1).
func TestReconcileLeavesALiveFailedSessionPaneAloneAndUnflagged(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Failed); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: w.Name, Command: "claude"})
	before := len(tm.killed) // Spawn's own pre-spawn cleanup kill already ran once
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if notifiedCount(s, "tmux.unknown") != 0 {
		t.Fatal("a failed session's own pane must not be flagged unknown")
	}
	if len(tm.killed) != before {
		t.Fatalf("a live pane must be left for inspection: %v", tm.killed)
	}
}

// A dead pane left behind by a failed session is just leftover tmux
// bookkeeping (the process already exited) -- safe to clean up, unlike a
// still-live one.
func TestReconcileKillsADeadPaneLeftByAFailedSession(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Failed); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: w.Name, Command: "claude", Dead: true})
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if notifiedCount(s, "tmux.unknown") != 0 {
		t.Fatal("a failed session's own dead pane must not be flagged unknown")
	}
	if !slices.Contains(tm.killed, w.Name) {
		t.Fatalf("killed = %v, want %s among them", tm.killed, w.Name)
	}
}

// §12.2: the sweep runs once the root is done and every agent has finished.
func TestSweepRunsOnlyWhenTheWholeTreeIsFinished(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	// A worktree the sweep can act on: its path is a plain temp dir, not a real
	// checkout, so DirtyStrict's `git status` fails and remove() retains it
	// (sweptCount's own doc: "retained" counts as swept too). Without a worktree
	// row at all, Sweep has nothing to touch and sweptCount could never move.
	epic, err := s.Items.Get(ctx, "EPIC-1")
	if err != nil {
		t.Fatal(err)
	}
	repoID := seedRepo(t, s, "proj")
	seedWorktreeReservation(t, s, repoID, w.ID, epic.ID)
	s.DB.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE key = 'EPIC-1'`)
	panes(tm)
	s.Reconcile(ctx)
	if sweptCount(t, s) != 0 {
		t.Fatal("the sweep waits for every agent in the tree to finish")
	}
	s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id IN (?, ?)`, orch.ID, w.ID)
	s.Reconcile(ctx)
	if sweptCount(t, s) == 0 {
		t.Fatal("the sweep should run now")
	}
}

// §10.6: at startup, queued spawns are retried FIFO.
func TestReconcileDrainsTheQueue(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	setLimits(t, s, 3, 1, 4)
	seedEpicWithTwoTasks(t, s)
	first, _, _ := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}})
	second, queued, _ := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "two"}})
	if !queued {
		t.Fatal("the second should queue")
	}
	s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, first.ID)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, first.ID)
	panes(tm)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Agent(ctx, second.Name)
	if got.State != AgentActive {
		t.Fatalf("state = %s", got.State)
	}
}

// The following two tests exercise the pause machine (Task 20's pause.go) but
// live here because they call s.Reconcile (Task 21's), per the batch brief's
// note under Task 20.

func TestFirstSyncMovesToQuiescingAndTheHandoffToStopping(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.Pause(ctx, w.Name, "session")
	if _, err := s.Sync(ctx, wSes.ID, nil, 20); err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != Quiescing {
		t.Fatalf("state after the first sync = %s", ses.State)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff,
		Summary: "stopped after the failing test"}); err != nil {
		t.Fatal(err)
	}
	ses, _ = s.LatestSession(ctx, w.ID)
	if ses.State != Stopping {
		t.Fatalf("state after the handoff = %s", ses.State)
	}
	// the parent is told
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"paused"%'`, orch.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("relay paused count = %d", n)
	}
	// the kill comes 5 s later, and only after the SWARM_SESSION check
	// (P0-crash-1: worker()'s own spawns already recorded their own harmless
	// startup kills, before that -- only TickPause's kill is new here)
	before := len(tm.killed)
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	at.Advance(6 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed)-before != 1 || tm.killed[len(tm.killed)-1] != w.Name {
		t.Fatalf("killed = %v", tm.killed[before:])
	}
	// paused only once the pane is dead, and past spawnGracePeriod
	// (P0-crash-2) since worker() started this session -- 6s so far. This
	// session has never gone through an alive-confirming Reconcile tick, so
	// the first tick past that window grants one more fresh grace window
	// (P0-crash-4) before a second tick actually resolves it dead.
	at.Advance(5 * time.Second)
	tm.panes = nil
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ses, _ = s.LatestSession(ctx, w.ID)
	if ses.State != Paused {
		t.Fatalf("state = %s, want paused once the pane is gone", ses.State)
	}
	// The notifier is runtime's fakeNotifier (internal/notify is Task 23 and sits
	// above runtime), so assert through it rather than against a `notifications`
	// table that nothing in this package writes.
	if got := s.Notify.(*fakeNotifier).kinds(); !slices.Contains(got, "agent.paused") {
		t.Fatalf("raised %v, want agent.paused", got)
	}
}

// L11: the deadline path sends the interrupt keys, waits 10 s, kills, and records
// interrupted.
func TestDeadlineInterruptsAndNeverRecordsPaused(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	s.Pause(ctx, w.Name, "session")
	at.Advance(121 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.keys) == 0 || !strings.HasSuffix(tm.keys[len(tm.keys)-1], "|Escape") {
		t.Fatalf("interrupt keys = %v", tm.keys)
	}
	// P0-crash-1: worker()'s own spawn already recorded its harmless startup
	// kill; only what this second TickPause adds is what this test is about.
	before := len(tm.killed)
	at.Advance(11 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed)-before != 1 {
		t.Fatalf("killed = %v", tm.killed[before:])
	}
	// This session has never gone through an alive-confirming Reconcile tick,
	// so the first tick past spawnGracePeriod grants one more fresh grace
	// window (P0-crash-4) before a second tick actually resolves it dead.
	tm.panes = nil
	s.Reconcile(ctx)
	at.Advance(spawnGracePeriod + time.Second)
	s.Reconcile(ctx)
	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != Interrupted {
		t.Fatalf("state = %s, want interrupted, never paused", ses.State)
	}
	if got := s.Notify.(*fakeNotifier).kinds(); !slices.Contains(got, "agent.interrupted") {
		t.Fatalf("raised %v, want agent.interrupted", got)
	}
	// The `attention` level belongs to notify.Rules, not to runtime; Task 23's
	// TestEveryRuleKeyIsComplete pins it. Asserting it here would be asserting
	// against a table this package never writes.
}

// A freshly spawned child that never writes a single checkpoint — stuck
// before its first swarm_sync, or crashed silently in a way that never trips
// resolveDead because its pane is still alive — leaves its parent with no
// signal at all. Two minutes after spawn with zero checkpoints, the daemon
// must raise agent.no_ack and relay it to the parent exactly once.
func TestNoAckAfterTwoMinutesWithNoCheckpointNotifiesAndRelaysOnce(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	tm.captures[w.Name] = []string{"starting up…\n"} // not idle
	tm.captures[orch.Name] = []string{"working…\n"}

	// under the timeout: nothing yet
	at.Advance(90 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.no_ack"); n != 0 {
		t.Fatalf("agent.no_ack count before the timeout = %d", n)
	}

	// past the timeout: fires once
	at.Advance(31 * time.Second) // total 121s > ackTimeout (2m)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	n := notified(t, s, "agent.no_ack")
	if n.AgentName != w.Name || n.ItemKey != "TASK-1" {
		t.Fatalf("notification = %+v", n)
	}
	var relays int
	// wake_class = 'immediate' matters as much as the row existing: a
	// deferred relay just sits in the inbox until the parent happens to
	// sync, which the orchestrator skill tells it never to do on its own.
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"no_ack"%' AND wake_class = 'immediate'`, orch.ID).Scan(&relays)
	if relays != 1 {
		t.Fatalf("relay no_ack count = %d, want exactly one immediate relay", relays)
	}

	// another tick, still stuck: must not repeat
	at.Advance(5 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.no_ack"); n != 1 {
		t.Fatalf("agent.no_ack must fire once, not every tick: count = %d", n)
	}
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"no_ack"%'`, orch.ID).Scan(&relays)
	if relays != 1 {
		t.Fatalf("relay no_ack must fire once, not every tick: count = %d", relays)
	}
}

// A checkpoint of any kind — not only "accepted" — counts as an ack: the
// signal the daemon needs is that the child is alive and talking, not that
// it led with a specific checkpoint kind.
func TestNoAckNeverFiresOnceAnyCheckpointIsWritten(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	tm.captures[w.Name] = []string{"blocked…\n"}
	tm.captures[orch.Name] = []string{"working…\n"}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: BlockedCkp,
		Summary: "waiting on a decision"}); err != nil {
		t.Fatal(err)
	}
	at.Advance(3 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.no_ack"); n != 0 {
		t.Fatalf("a blocked checkpoint already proves the child is alive: count = %d", n)
	}
}

// A retry starts a fresh attempt with zero checkpoints and a fresh
// started_at: if that retried attempt also never checkpoints, no_ack must
// fire again. Scoping the "already relayed" guard by item alone (rather than
// by attempt) would silence this — the very retry the orchestrator is told
// to make after a no_ack.
func TestNoAckFiresAgainAfterARetryThatAlsoHangs(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"}, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	// No scripted capture: the default idle prompt is fine here — owesNothing
	// (the un-acked assignment message) already keeps resolveAlive's "waiting"
	// false regardless of idle, and a scripted busy capture would also feed
	// the retried session's own synchronous watchStartup below, spinning it
	// past its 30 s startup deadline and failing the retry before Reconcile
	// ever runs.

	// first attempt hangs past the timeout: one no_ack
	at.Advance(3 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.no_ack"); n != 1 {
		t.Fatalf("agent.no_ack count after the first hang = %d", n)
	}

	// the pane dies; the attempt is recorded crashed and retried. spawnGracePeriod
	// (10s) must elapse from this session's last confirmed-alive tick before a
	// missing pane counts as crashed rather than a transient miss (P0-crash-4).
	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	at.Advance(spawnGracePeriod)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LatestSession(ctx, w.ID)
	if got.State != Crashed {
		t.Fatalf("state = %s, want crashed before the retry", got.State)
	}
	at.Advance(time.Second) // so the retried attempt's started_at strictly follows the prior relay
	if _, err := s.Retry(ctx, w.Name, "", "", ""); err != nil {
		t.Fatal(err)
	}
	newSes, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newSes.ID == wSes.ID {
		t.Fatal("retry must start a new session")
	}
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"}, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": newSes.ID}

	// the retried attempt also hangs past the timeout: no_ack must fire again
	at.Advance(3 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.no_ack"); n != 2 {
		t.Fatalf("agent.no_ack must fire again for the retried attempt: count = %d", n)
	}
	var relays int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"no_ack"%'`, orch.ID).Scan(&relays)
	if relays != 2 {
		t.Fatalf("relay no_ack must fire again for the retried attempt: count = %d", relays)
	}
}

// A root agent with no parent has nobody to relay to, so the ack-timeout
// never fires for it — mirroring TestDeadPaneCrashWithNoParentSkipsTheRelay
// for the crash path.
func TestNoAckWithNoParentNeverFires(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Lonely2", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	tm.captures[a.Name] = []string{"starting up…\n"}
	at.Advance(3 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.no_ack"); n != 0 {
		t.Fatalf("a parentless agent has nobody to relay to: count = %d", n)
	}
}
