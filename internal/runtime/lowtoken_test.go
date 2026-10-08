package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
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

// lowTokenNotes counts pending assignment_update notes for the agent whose text starts with prefix.
func lowTokenNotes(t *testing.T, s *Store, agentID, prefix string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'
		AND json_extract(payload_json, '$.note') LIKE ?`, agentID, prefix+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSetLowTokenNotifiesChangedOnly(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	zero := 0
	nested := addAgent(t, s, orch, "nested", RoleOrchestrator, &zero) // stays off: override 0
	nestedW := addAgent(t, s, nested, "nested-worker", RoleCoder, nil)
	n, err := s.SetLowToken(ctx, orch.Name, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("notified = %d, want 2 (orchestrator and its worker)", n)
	}
	on := "Low-token mode is now on"
	for id, want := range map[string]int{orch.ID: 1, w.ID: 1, nested.ID: 0, nestedW.ID: 0} {
		if got := lowTokenNotes(t, s, id, on); got != want {
			t.Fatalf("agent %s: on-notes = %d, want %d", id, got, want)
		}
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	if got.LowToken == nil || !*got.LowToken {
		t.Fatalf("override = %v, want true", got.LowToken)
	}
	// Turning it off again notes the same two agents.
	if n, err = s.SetLowToken(ctx, orch.Name, false); err != nil || n != 2 {
		t.Fatalf("off: notified = %d, %v; want 2", n, err)
	}
	if got := lowTokenNotes(t, s, w.ID, "Low-token mode is now off"); got != 1 {
		t.Fatalf("off-notes = %d, want 1", got)
	}
}

func TestSetLowTokenRejectsWorkerAndFinished(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	check := func(name, msg string) {
		t.Helper()
		_, err := s.SetLowToken(ctx, name, true)
		var ie *items.Error
		if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest || ie.Message != msg {
			t.Fatalf("SetLowToken(%s) = %v, want bad_request %q", name, err, msg)
		}
	}
	check(w.Name, w.Name+" is not an orchestrator.")
	mustExec(t, s.DB, `UPDATE agents SET state = 'finished' WHERE id = ?`, orch.ID)
	check(orch.Name, orch.Name+" has finished.")
	_, err := s.SetLowToken(ctx, "nope", true)
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeNotFound || !strings.Contains(ie.Message, "nope") {
		t.Fatalf("unknown agent err = %v", err)
	}
}

func TestSetLowTokenAllClearsOverrides(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	zero, one := 0, 1
	mustExec(t, s.DB, `UPDATE agents SET low_token = ? WHERE id = ?`, zero, orch.ID)
	other := addAgent(t, s, orch, "other", RoleOrchestrator, &one) // effective on already
	n, err := s.SetLowTokenAll(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.Settings.Get(ctx)
	if !cfg.LowTokenMode {
		t.Fatal("global flag not set")
	}
	for _, a := range []Agent{orch, other} {
		got, _ := s.AgentByID(ctx, a.ID)
		if got.LowToken != nil {
			t.Fatalf("%s override = %v, want cleared", a.Name, *got.LowToken)
		}
	}
	// orch (was off) and its worker (was off) change; other (was on via override) does not.
	if n != 2 {
		t.Fatalf("notified = %d, want 2", n)
	}
	if lowTokenNotes(t, s, orch.ID, "Low-token mode is now on") != 1 || lowTokenNotes(t, s, w.ID, "Low-token mode is now on") != 1 ||
		lowTokenNotes(t, s, other.ID, "Low-token mode is now on") != 0 {
		t.Fatal("notes went to the wrong agents")
	}
}
