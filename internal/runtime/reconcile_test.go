package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

func TestDeadPaneWithACompletedCheckpointCompletesTheSession(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Verification: []Verify{{Cmd: "go test", Phase: "red", OK: false}, {Cmd: "go test", Phase: "green", OK: true}},
		Git:          []GitRef{{Repo: "proj", Branch: "task/task-1", SHA: "abc1234"}}})
	panes(tm) // the pane is gone
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
	panes(tm, Pane{Session: w.Name, Dead: true, DeadStatus: 137, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
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

func TestDeadPaneWhileStoppingBecomesPaused(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, wSes.ID)
	panes(tm)
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
	s.Reconcile(ctx)
	if len(tm.killed) != 1 {
		t.Fatalf("killed = %v", tm.killed)
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
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	at.Advance(6 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != 1 || tm.killed[0] != w.Name {
		t.Fatalf("killed = %v", tm.killed)
	}
	// paused only once the pane is dead
	tm.panes = nil
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
	at.Advance(11 * time.Second)
	if err := s.TickPause(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != 1 {
		t.Fatalf("killed = %v", tm.killed)
	}
	tm.panes = nil
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
