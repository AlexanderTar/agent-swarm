package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// TestLegacyLineagePreservesHistory covers agents that predate the lineage
// table (or arrived without one): EnsureLineage backfills each agent as the
// root of its own chain without touching the agent row itself -- no
// deletion, no rename -- and later generations append to that history.
func TestLegacyLineagePreservesHistory(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	// Simulate legacy rows: the agents exist, their lineage does not.
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM agent_lineage`); err != nil {
		t.Fatal(err)
	}
	before := func() int {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}()
	if err := s.EnsureLineage(ctx, orch.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLineage(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	// Idempotent: a second pass adds nothing.
	if err := s.EnsureLineage(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != before {
		t.Fatalf("agents rows = %d, want %d (no deletion)", n, before)
	}
	after, err := s.AgentByID(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != w.Name {
		t.Fatalf("agent renamed %q -> %q (no auto-rename)", w.Name, after.Name)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_lineage`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("lineage rows = %d, want 2 roots", n)
	}
	chain, err := s.LineageChain(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 1 || chain[0].PredecessorAgentID != "" || chain[0].AgentID != w.ID {
		t.Fatalf("chain = %+v, want one root node for the worker", chain)
	}
}

// TestResolveCanonicalAmbiguity pins the merge rules: an exact
// (root, item, role, parent) match resolves to the sole active agent, falls
// back to the newest recoverable one, and conflicts without mutating
// anything when two workers share the assignment -- two coders on one task
// are never the same agent.
func TestResolveCanonicalAmbiguity(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)

	got, err := s.ResolveCanonical(ctx, w.RootItemID, w.ItemID, w.Role, w.ParentAgentID)
	if err != nil {
		t.Fatalf("sole active resolve err = %v", err)
	}
	if got.ID != w.ID {
		t.Fatalf("canonical = %s, want sole active worker %s", got.ID, w.ID)
	}

	// A second active coder on the same task: ambiguous, conflict, no mutation.
	w2, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "second worker"}})
	if err != nil {
		t.Fatal(err)
	}
	if w2.ID == w.ID {
		t.Fatal("second spawn reused the first coder's row")
	}
	agentsBefore, lineageBefore := countRows(t, s, "agents"), countRows(t, s, "agent_lineage")
	_, err = s.ResolveCanonical(ctx, w.RootItemID, w.ItemID, w.Role, w.ParentAgentID)
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict {
		t.Fatalf("err = %v, want conflict for two active workers", err)
	}
	if countRows(t, s, "agents") != agentsBefore || countRows(t, s, "agent_lineage") != lineageBefore {
		t.Fatal("ambiguous resolve mutated the database")
	}

	// Both sessions dead and both rows finished-but-restartable: the newest
	// recoverable worker is canonical.
	for _, a := range []Agent{w, w2} {
		ses, err := s.LatestSession(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetSessionState(ctx, ses.ID, Crashed); err != nil {
			t.Fatal(err)
		}
	}
	got, err = s.ResolveCanonical(ctx, w.RootItemID, w.ItemID, w.Role, w.ParentAgentID)
	if err != nil {
		t.Fatalf("recoverable resolve err = %v", err)
	}
	if got.ID != w2.ID {
		t.Fatalf("canonical = %s, want newest recoverable worker %s", got.ID, w2.ID)
	}
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
