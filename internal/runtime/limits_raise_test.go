package runtime

import (
	"context"
	"testing"
	"time"
)

func spawnThreeAtLimitOne(t *testing.T, s *Store) (a2, a3 Agent) {
	t.Helper()
	ctx := context.Background()
	setLimits(t, s, 1)
	seedEpicWithThreeTasks(t, s)
	var as [3]Agent
	for i, key := range []string{"TASK-1", "TASK-2", "TASK-3"} {
		a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: key, Role: RoleCoder, Kind: Claude, Model: "claude-sonnet-5", Brief: BriefInput{Objective: key}})
		if err != nil {
			t.Fatal(err)
		}
		as[i] = a
	}
	if as[1].State != AgentQueued || as[2].State != AgentQueued {
		t.Fatalf("setup: want the 2nd and 3rd agent queued, got %s %s", as[1].State, as[2].State)
	}
	return as[1], as[2]
}

func raiseLimit(t *testing.T, s *Store, n int) {
	t.Helper()
	cfg, err := s.Settings.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConcurrentAgents = n
	if _, err := s.Settings.Put(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func waitAgentsActive(t *testing.T, s *Store, within time.Duration, names ...string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		all := true
		for _, n := range names {
			if a, _ := s.Agent(context.Background(), n); a.State != AgentActive {
				all = false
			}
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agents %v not all active within %s", names, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A raised limit must start queued agents on the running reconcile loop at
// once, not at its next timer tick: Kick wakes the loop (TASK-1325). The
// injected After never fires, so only Kick can start a tick here.
func TestKickRunsAReconcileTickBeforeTheTimer(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	s.After = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	a2, a3 := spawnThreeAtLimitOne(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.ReconcileLoop(ctx, time.Hour); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	raiseLimit(t, s, 3)
	s.Kick()
	waitAgentsActive(t, s, 2*time.Second, a2.Name, a3.Name)
}
