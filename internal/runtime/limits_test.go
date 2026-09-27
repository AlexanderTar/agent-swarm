package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// setLimits sets max_concurrent_agents, the one admission limit (spec
// 2026-09-27-single-agent-limit-live).
func setLimits(t *testing.T, s *Store, agents int) {
	t.Helper()
	now := s.now().UnixMilli()
	if _, err := s.DB.ExecContext(context.Background(), `INSERT INTO settings (key, value_json, updated_at)
		VALUES ('max_concurrent_agents', ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		fmt.Sprintf("%d", agents), now); err != nil {
		t.Fatal(err)
	}
	s.Events.Notify()
}

// TestAdmitOrchestratorsShareTheGlobalLimit is the 2026-09-24
// unify-agent-limits inversion of this suite's old
// TestAdmitOrchestratorsUseTheirOwnLimit: orchestrators no longer have a
// separate max_orchestrators pool, so starting one now holds a slot in the
// same shared max_concurrent_agents pool every other role counts against.
func TestAdmitOrchestratorsShareTheGlobalLimit(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 1)
	seedEpicWithTask(t, s)
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"}); err != nil {
		t.Fatal(err)
	}
	// with max_concurrent_agents = 1, the orchestrator already holds the one
	// shared slot -- a coder child must queue behind it.
	a, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatalf("the child must queue; the orchestrator now holds the shared slot (agent %s)", a.Name)
	}
}

func TestAdmitQueuesTheSecondWorkerAndStartsItWhenASlotFrees(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 1)
	seedEpicWithTwoTasks(t, s)
	first, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}})
	if err != nil || queued {
		t.Fatalf("first = %v, queued = %v, err = %v", first.Name, queued, err)
	}
	second, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if !queued || second.State != AgentQueued {
		t.Fatalf("the second worker must queue: queued = %v, state = %s", queued, second.State)
	}
	if _, err := s.LatestSession(ctx, second.ID); err == nil {
		t.Fatal("a queued agent has no session yet")
	}
	if got := lastNotified(t, s).Kind; got != "agent.queued" {
		t.Fatalf("notification = %q", got)
	}
	// finish the first, then drain
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, first.ID)
	s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, first.ID)
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	out, _ := s.Agent(ctx, second.Name)
	if out.State != AgentActive {
		t.Fatalf("state = %s, want active after draining", out.State)
	}
	if _, err := s.LatestSession(ctx, second.ID); err != nil {
		t.Fatalf("the drained agent needs a session: %v", err)
	}
}

func TestDrainQueueIsFIFO(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 1)
	seedEpicWithThreeTasks(t, s)
	running, _, _ := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}})
	a2, _, _ := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "two"}})
	a3, _, _ := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-3", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "three"}})
	s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, running.ID)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, running.ID)
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.Agent(ctx, a2.Name)
	got3, _ := s.Agent(ctx, a3.Name)
	if got2.State != AgentActive || got3.State != AgentQueued {
		t.Fatalf("drained out of order: %s = %s, %s = %s", a2.Name, got2.State, a3.Name, got3.State)
	}
}

// Spec 2026-09-27-single-agent-limit-live: there is one limit. A stale
// max_agents_per_root row (even 1) no longer queues a second worker in a root.
func TestAdmitHasNoPerRootLimit(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_at)
		VALUES ('max_agents_per_root', '1', 1)`); err != nil {
		t.Fatal(err)
	}
	seedEpicWithTwoTasks(t, s)
	if _, queued, _ := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}}); queued {
		t.Fatal("the first worker fits")
	}
	_, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("the second worker in the same root must start: only max_concurrent_agents (8) applies")
	}
}

// Admit alone never stops a running agent; it only queues the next spawn.
// Pausing agents over a lowered limit is EnforceCapacity's job
// (capacity_test.go).
func TestLoweringALimitQueuesTheNextSpawn(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithTwoTasks(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	startedBefore := len(tm.started)
	setLimits(t, s, 1)
	out, err := s.Agent(ctx, a.Name)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != AgentActive {
		t.Fatalf("state = %s; Admit must not stop a running agent", out.State)
	}
	b, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("the second spawn must queue: max_concurrent_agents is 1 and the first agent holds the slot")
	}
	if b.State != AgentQueued {
		t.Fatalf("state = %s, want queued", b.State)
	}
	if len(tm.started) != startedBefore {
		t.Fatalf("a queued agent must not get a pane: started %v", tm.started[startedBefore:])
	}
}

// D32: a queued agent that cannot be started must not be re-selected forever.
func TestDrainQueueGivesUpOnAnAgentItCannotStart(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithTwoTasks(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'queued' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	// No adapter for a kind that is not registered: startQueued fails before it
	// changes the state, so the row is still `queued` when the loop comes round.
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = 'codex' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.DrainQueue(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DrainQueue did not return: it is re-selecting the same queued row")
	}
}

// §11.3: a queued agent that fails preflight on drain creates a failed session,
// relays spawn_failed to its parent, and notifies agent.preflight_failed.
func TestDrainQueuePreflightFailureRelaysToParent(t *testing.T) {
	s, _, f := newStore(t)
	ctx := context.Background()
	// agents=2: the orchestrator and the first child share the pool now, so
	// both need room before the second child can be the one that queues.
	setLimits(t, s, 2)
	seedEpicWithTwoTasks(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	first, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}, ParentAgentID: orch.ID})
	if err != nil || queued {
		t.Fatalf("first = %v, queued = %v, err = %v", first.Name, queued, err)
	}
	second, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "two"}, ParentAgentID: orch.ID})
	if err != nil || !queued {
		t.Fatalf("second = %v, queued = %v, err = %v", second.Name, queued, err)
	}
	// finish first
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, first.ID)
	s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, first.ID)
	// Make preflight fail on the fake adapter
	f.AuthError = errors.New("token expired")
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	drained, err := s.Agent(ctx, second.Name)
	if err != nil {
		t.Fatal(err)
	}
	if drained.State != AgentActive {
		t.Fatalf("state = %s, want active with failed session", drained.State)
	}
	ses, err := s.LatestSession(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != Failed {
		t.Fatalf("session state = %s, want failed", ses.State)
	}
	// verify relay message to parent
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'`, orch.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("relay message count = %d, err = %v", count, err)
	}
}

// TestAdmitIgnoresZombiesWhenCountingSlots is the 2026-09-22 zombie-slot fix: a
// session that dies to interrupted, crashed or failed has no live process, but
// its agent row stays 'active' (reconcile.go's FailedCkp and crashed branches
// only ever write the session row, never agents.state -- same as a manual
// interrupt; all three sit in retryableStates waiting on a human resume/ack/
// cancel, not auto-cleaned). Before this fix such an agent silently occupied a
// max_concurrent_agents slot forever. The zombie itself must stay
// exactly as it was -- still active, still resumable/ackable/cancellable by
// hand -- this only stops it from blocking the queue. paused joined the table
// 2026-09-24: a paused agent has no live process either, and Resume always
// starts a fresh generation, so excluding a not-yet-resumed 'paused' session
// is the same shape as the other three -- without it, pausing an agent never
// actually freed the capacity pausing exists to free.
func TestAdmitIgnoresZombiesWhenCountingSlots(t *testing.T) {
	for _, zombieState := range []SessionState{Interrupted, Crashed, Failed, Paused} {
		t.Run(string(zombieState), func(t *testing.T) {
			s, _, _ := newStore(t)
			ctx := context.Background()
			setLimits(t, s, 1)
			seedEpicWithTwoTasks(t, s)
			first, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
				Model: "fake-1", Brief: BriefInput{Objective: "one"}})
			if err != nil || queued {
				t.Fatalf("first = %v, queued = %v, err = %v", first.Name, queued, err)
			}
			// Simulate a crash: the session dies but nothing touches agents.state,
			// exactly what a real crash/interrupt leaves behind.
			if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = ? WHERE agent_id = ?`,
				string(zombieState), first.ID); err != nil {
				t.Fatal(err)
			}
			_, queued, err = s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
				Model: "fake-1", Brief: BriefInput{Objective: "two"}})
			if err != nil {
				t.Fatal(err)
			}
			if queued {
				t.Fatalf("the %s zombie must not hold the slot: second queued = %v", zombieState, queued)
			}
			out, err := s.Agent(ctx, first.Name)
			if err != nil {
				t.Fatal(err)
			}
			if out.State != AgentActive {
				t.Fatalf("the zombie's own agent state must stay untouched by this fix: %s", out.State)
			}
		})
	}
}

// TestAdmitIgnoresASelfTerminalCheckpointWhenCountingSlots is the 2026-09-23
// budget-overcounting bug: a worker's own `completed` or `failed` checkpoint
// never touches its own agents.state/sessions.state -- closeCompletedSiblings
// (checkpoint.go) explicitly excludes the checkpoint's own writer, and
// onPausingCheckpoint only reacts while the session is pausing. Only the async
// reconciler eventually flips agents.state to 'finished' (resolveAlive's
// 60s kill-after-completed, then resolveDead's terminalCheckpointKind once
// the pane is confirmed gone) -- nothing in WriteCheckpoint's own
// transaction does it. Before the reconciler runs (which this test never
// invokes, simulating the window it leaves open, or a daemon that never
// gets to it in time), the worker still reads agents.state = 'active' and
// NotAZombieSlot only excludes interrupted/crashed/failed sessions, so a
// genuinely finished-but-not-yet-reconciled worker keeps occupying its
// budget slot. Table-driven over both terminal kinds the SQL claims
// (`kind IN ('completed', 'failed')`), the same way
// TestAdmitIgnoresZombiesWhenCountingSlots tables over its three states.
func TestAdmitIgnoresASelfTerminalCheckpointWhenCountingSlots(t *testing.T) {
	for _, kind := range []CheckpointKind{CompletedCkp, FailedCkp} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, _ := newStore(t)
			ctx := context.Background()
			setLimits(t, s, 1)
			seedEpicWithTwoTasks(t, s)
			first, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
				Model: "fake-1", Brief: BriefInput{Objective: "one"}})
			if err != nil || queued {
				t.Fatalf("first = %v, queued = %v, err = %v", first.Name, queued, err)
			}
			ses, err := s.LatestSession(ctx, first.ID)
			if err != nil {
				t.Fatal(err)
			}
			// The worker writes its OWN terminal checkpoint (self-completion/
			// self-failure, not an orchestrator acting on the worker's behalf)
			// while its session's process has NOT exited -- the reconciler
			// never runs in this test, so nothing has touched
			// agents.state/sessions.state yet, exactly the window the live
			// incident hit.
			in := CheckpointInput{Kind: kind, Summary: "done"}
			if kind == CompletedCkp {
				in.Verification = []Verify{{Cmd: "go test ./..."}}
				in.Git = []GitRef{{Repo: "proj", Branch: "task/task-1", SHA: "abc1234"}}
			}
			if _, err := s.WriteCheckpoint(ctx, ses.ID, in); err != nil {
				t.Fatal(err)
			}
			out, err := s.Agent(ctx, first.Name)
			if err != nil {
				t.Fatal(err)
			}
			if out.State != AgentActive {
				t.Fatalf("precondition broken: agent state = %s, want still active (nothing reconciled yet)", out.State)
			}
			_, queued, err = s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
				Model: "fake-1", Brief: BriefInput{Objective: "two"}})
			if err != nil {
				t.Fatal(err)
			}
			if queued {
				t.Fatalf("the self-%s worker must not hold the slot: second queued = true", kind)
			}
			// The fix is a pure read-time predicate: writing the terminal
			// checkpoint and running Admit again must not have touched the
			// excluded agent's own row, exactly as the zombie test above
			// verifies for its own exclusion.
			out, err = s.Agent(ctx, first.Name)
			if err != nil {
				t.Fatal(err)
			}
			if out.State != AgentActive {
				t.Fatalf("the self-%s worker's own agent state must stay untouched by this fix: %s", kind, out.State)
			}
		})
	}
}

// TestAdmitCountsOrchestratorsOwnSlotDespiteChildItemCheckpoint guards
// NotAZombieSlot's c.item_id = agents.item_id scoping: an orchestrator
// routinely writes checkpoints against the CHILD items it's tracking, not
// its own root item. That must never be mistaken for the orchestrator's OWN
// completed/failed checkpoint -- the orchestrator's own slot has to stay
// held exactly as if no checkpoint had been written at all.
func TestAdmitCountsOrchestratorsOwnSlotDespiteChildItemCheckpoint(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 1)
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A routine checkpoint about the CHILD task the orchestrator is
	// overseeing, not about the orchestrator's own root -- c.item_id here is
	// TASK-1's item id, not EPIC-1's.
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: FailedCkp,
		ItemKey: "TASK-1", Summary: "the child task failed"}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Agent(ctx, orch.Name)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != AgentActive {
		t.Fatalf("precondition broken: orchestrator state = %s, want still active", out.State)
	}
	ep2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Second Epic"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id = ?`, ep2.ID); err != nil {
		t.Fatal(err)
	}
	second, queued, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep2.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !queued || second.State != AgentQueued {
		t.Fatalf("the orchestrator's OWN slot must still be held (a child-item checkpoint must not exclude it): "+
			"queued = %v, state = %s", queued, second.State)
	}
}

// TestAdmitCountsAResumedAgentAfterItsOwnFailedCheckpoint is the regression
// caught in review of the 2026-09-23 fix above: correlating the exclusion by
// c.attempt = s.attempt instead of by session leaked across a Resume.
// pause.go's Resume starts a NEW generation but the SAME attempt
// (startSession(ctx, a, ses.Attempt, ses.Generation+1, ...)), so an
// attempt-scoped match kept matching the OLD (now-dead) session's `failed`
// checkpoint forever, permanently excluding the now-genuinely-running
// resumed agent from every budget count -- even though a brand new session
// with a brand new pane is running. This reproduces the exact sequence: an
// agent writes `failed` while pause_requested (checkpoint.go's
// pauseAllowedKinds permits it), onPausingCheckpoint moves it to 'stopping',
// resolveDead's Stopping branch (reconcile.go) flips it to 'paused' once the
// pane is confirmed dead, then Resume starts generation 2 of the same
// attempt.
func TestAdmitCountsAResumedAgentAfterItsOwnFailedCheckpoint(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 1)
	seedEpicWithTwoTasks(t, s)
	first, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}})
	if err != nil || queued {
		t.Fatalf("first = %v, queued = %v, err = %v", first.Name, queued, err)
	}
	ses, err := s.LatestSession(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'pause_requested' WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: FailedCkp,
		Summary: "could not proceed"}); err != nil {
		t.Fatal(err)
	}
	// Simulates resolveDead's Stopping branch (reconcile.go:529-531) confirming
	// the pane gone, without driving the whole reconciler/tmux machinery.
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resume(ctx, first.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	resumed, err := s.LatestSession(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Attempt != ses.Attempt || resumed.Generation != ses.Generation+1 {
		t.Fatalf("resumed session = attempt %d generation %d, want attempt %d generation %d (same attempt, next generation)",
			resumed.Attempt, resumed.Generation, ses.Attempt, ses.Generation+1)
	}
	_, queued, err = s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("the resumed agent must hold its slot: second queued = false " +
			"(NotAZombieSlot wrongly matched the resumed session against the OLD generation's failed checkpoint)")
	}
}

// TestAdmitIgnoresZombiesForOrchestratorLimit is the same fix, exercised via
// an orchestrator spawn: since 2026-09-24 unify-agent-limits there is no
// separate orchestrator query any more -- this now goes through the same
// shared max_concurrent_agents count every other role uses, and the crashed
// orchestrator's slot must still be excluded from it.
func TestAdmitIgnoresZombiesForOrchestratorLimit(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 1)
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'crashed' WHERE agent_id = ?`, orch.ID); err != nil {
		t.Fatal(err)
	}
	ep2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Second Epic"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id = ?`, ep2.ID); err != nil {
		t.Fatal(err)
	}
	second, queued, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep2.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if queued || second.State != AgentActive {
		t.Fatalf("the crashed orchestrator must not hold the shared max_concurrent_agents slot: queued = %v, state = %s", queued, second.State)
	}
}

func mustExec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// TestAdmitConcurrentSpawnsNeverExceedLimit verifies that concurrent spawns hitting
// the limit never over-admit agents due to a TOCTOU race.
func TestAdmitConcurrentSpawnsNeverExceedLimit(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 2)

	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Concurrency Epic"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key, Title: "Concurrency Story"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	const numTasks = 10
	var taskKeys []string
	for i := 0; i < numTasks; i++ {
		tk, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: fmt.Sprintf("Task %d", i+1)}, items.User("board"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id = ?`, tk.ID); err != nil {
			t.Fatal(err)
		}
		taskKeys = append(taskKeys, tk.Key)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?)`, ep.ID, story.ID); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, numTasks)
	startBarrier := make(chan struct{})

	for i := 0; i < numTasks; i++ {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			<-startBarrier
			_, _, err := s.Spawn(ctx, SpawnInput{
				ItemKey: key,
				Role:    RoleCoder,
				Kind:    Fake,
				Model:   "fake-1",
				Brief:   BriefInput{Objective: "run concurrent task"},
			})
			if err != nil {
				errCh <- err
			}
		}(taskKeys[i])
	}

	close(startBarrier)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("spawn error: %v", err)
		}
	}

	var activeCount, queuedCount int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE state = 'active'`).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE state = 'queued'`).Scan(&queuedCount); err != nil {
		t.Fatal(err)
	}

	if activeCount != 2 {
		t.Fatalf("active agents = %d, want exactly 2 (exceeded MaxConcurrentAgents=2 limit due to race!)", activeCount)
	}
	if queuedCount != numTasks-2 {
		t.Fatalf("queued agents = %d, want %d", queuedCount, numTasks-2)
	}
}
