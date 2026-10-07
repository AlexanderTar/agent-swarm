package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// BUG-46/47/48: an agent whose parent sits in another root is detached once.
func TestReconcileDetachesCrossRootParents(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep1, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "First"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	ep2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Second"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?)`, ep1.ID, ep2.ID)
	s.DB.ExecContext(ctx, `UPDATE settings SET value = '5' WHERE key = 'max_concurrent_agents'`)
	parent, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep1.Key, Kind: Fake})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep2.Key, Kind: Fake})
	if err != nil {
		t.Fatal(err)
	}
	brief := strings.Replace(child.Brief, "parent: none", "parent: "+parent.Name, 1)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET parent_agent_id = ?, brief = ? WHERE id = ?`, parent.ID, brief, child.ID); err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) int {
		var n int
		if err := s.DB.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for tick := 0; tick < 2; tick++ {
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := s.agentByID(ctx, child.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ParentAgentID != "" || !strings.Contains(got.Brief, "parent: none") {
			t.Fatalf("tick %d: parent=%q brief=%q", tick, got.ParentAgentID, got.Brief)
		}
		if n := count(`SELECT COUNT(*) FROM messages WHERE kind = 'assignment_update' AND to_agent_id = ? AND payload_json LIKE '%You are now a top-level orchestrator: ask the user with your native question tool.%'`, child.ID); n != 1 {
			t.Fatalf("tick %d: assignment_update count = %d, want 1", tick, n)
		}
		if n := count(`SELECT COUNT(*) FROM events WHERE type = 'agent.detached'`); n != 1 {
			t.Fatalf("tick %d: agent.detached count = %d, want 1", tick, n)
		}
	}
}
