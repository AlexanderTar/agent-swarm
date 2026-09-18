package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func setLimits(t *testing.T, s *Store, orchestrators, agents, perRoot int) {
	t.Helper()
	ctx := context.Background()
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxOrchestrators, cfg.MaxAgents, cfg.MaxAgentsPerRoot = orchestrators, agents, perRoot
	if _, err := s.Settings.Put(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestAdmitOrchestratorsUseTheirOwnLimit(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 1, 1, 1)
	seedEpicWithTask(t, s)
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"}); err != nil {
		t.Fatal(err)
	}
	// with max_agents = 1 and no worker running, a child is still admitted:
	// the orchestrator does not occupy an agent slot (I20)
	a, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatalf("the child must start; the orchestrator does not hold an agent slot (agent %s)", a.Name)
	}
}

func TestAdmitQueuesTheSecondWorkerAndStartsItWhenASlotFrees(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 3, 1, 4)
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
	setLimits(t, s, 3, 1, 4)
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

// A2: the per-root limit is separate from the global one.
func TestAdmitEnforcesThePerRootLimit(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 3, 8, 1)
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
	if !queued {
		t.Fatal("the second worker in the same root exceeds max_agents_per_root = 1")
	}
}

// §17.3: lowering a limit never kills a running agent, and the agent already over
// the new limit still holds its slot, so the *next* spawn queues instead of
// starting. Asserting only "the running one is still active" is unfireable —
// nothing in the code path reacts to a settings change at all — so the test also
// drives the next spawn, which is the behaviour a wrong Admit would break.
func TestLoweringALimitQueuesTheNextSpawnAndLeavesRunningAgentsAlone(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 3, 8, 4)
	seedEpicWithTwoTasks(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	startedBefore := len(tm.started)
	setLimits(t, s, 1, 1, 1)
	out, err := s.Agent(ctx, a.Name)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != AgentActive {
		t.Fatalf("state = %s; a lowered limit must not stop a running agent", out.State)
	}
	b, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("the second spawn must queue: max_agents is 1 and the first agent holds the slot")
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
	setLimits(t, s, 3, 8, 4)
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
	setLimits(t, s, 3, 1, 4)
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

