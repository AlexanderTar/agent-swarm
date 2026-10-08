package runtime

import (
	"context"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

func TestLowTokenOverrideSurvivesHandoff(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	panes(tm)
	if _, err := s.RequestReplacement(ctx, orch.ID, ModeHandoff, "lt-handoff", ""); err != nil {
		t.Fatal(err)
	}
	succ, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.ID == ses.ID {
		t.Fatal("no successor session started")
	}
	got, err := s.AgentByID(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LowToken == nil || !*got.LowToken {
		t.Fatalf("low_token lost on handoff: %v", got.LowToken)
	}
}

// addAgent inserts a bare live agent row under parent (item scope copied from it).
func addAgent(t *testing.T, s *Store, parent Agent, name string, role Role, low *int) Agent {
	t.Helper()
	id := ids.New("agt")
	mustExec(t, s.DB, `INSERT INTO agents
		(id, name, kind, model, role, item_id, root_item_id, parent_agent_id, brief, state, created_at, low_token)
		VALUES (?, ?, 'fake', 'fake-1', ?, ?, ?, ?, 'b', 'active', ?, ?)`,
		id, name, string(role), parent.ItemID, parent.RootItemID, parent.ID, db.Millis(s.Now()), low)
	a, err := s.AgentByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLowTokenFor(t *testing.T) {
	ctx := context.Background()
	one, zero := 1, 0
	cases := []struct {
		name   string
		global bool
		root   *int // override on the root orchestrator
		mid    *int // override on a nested orchestrator under root
		want   bool
		probe  string // "root", "worker", "nested", "nestedWorker"
	}{
		{"global off, no override", false, nil, nil, false, "root"},
		{"global on", true, nil, nil, true, "root"},
		{"override 0 with global on", true, &zero, nil, false, "root"},
		{"worker under override-1 orchestrator", false, &one, nil, true, "worker"},
		{"nested override 0 under root override 1: its worker", false, &one, &zero, false, "nestedWorker"},
		{"nested follows root override", false, &one, nil, true, "nestedWorker"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := newStore(t)
			orch, w, _ := worker(t, s)
			if err := s.Settings.SetLowTokenMode(ctx, c.global); err != nil {
				t.Fatal(err)
			}
			mustExec(t, s.DB, `UPDATE agents SET low_token = ? WHERE id = ?`, c.root, orch.ID)
			nested := addAgent(t, s, orch, "nested", RoleOrchestrator, c.mid)
			nw := addAgent(t, s, nested, "nested-worker", RoleCoder, nil)
			probe := map[string]Agent{"root": orch, "worker": w, "nested": nested, "nestedWorker": nw}[c.probe]
			probe, err := s.AgentByID(ctx, probe.ID) // reload: the structs predate the overrides
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.LowTokenFor(ctx, probe)
			if err != nil || got != c.want {
				t.Fatalf("LowTokenFor = %v, %v; want %v", got, err, c.want)
			}
		})
	}
}
