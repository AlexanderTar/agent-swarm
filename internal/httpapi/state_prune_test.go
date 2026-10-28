package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/db"
	"github.com/AlexanderTar/swarmrun/internal/runtime"
)

func pruneAgent(id, parent string, state runtime.AgentState, finishedSec int) runtime.Agent {
	a := runtime.Agent{ID: id, ParentAgentID: parent, State: state, CreatedAt: time.Unix(1000, 0)}
	if finishedSec > 0 {
		f := time.Unix(int64(finishedSec), 0)
		a.FinishedAt = &f
	}
	return a
}

// The prune runs on the flat list, before any node is built: a pruned agent is
// simply absent from the list agentNodesFor receives, so it costs no queries.
func TestPruneFinishedForStateCapsAndKeepsLiveAncestors(t *testing.T) {
	flat := []runtime.Agent{pruneAgent("root", "", runtime.AgentActive, 0)}
	for i := 1; i <= 15; i++ {
		flat = append(flat, pruneAgent(fmt.Sprintf("fin%02d", i), "root", runtime.AgentFinished, 100+i))
	}
	// Oldest finished child of root has a running grandchild: must stay.
	flat = append(flat,
		pruneAgent("oldfin", "root", runtime.AgentFinished, 1),
		pruneAgent("live-gc", "oldfin", runtime.AgentActive, 0),
		pruneAgent("oldfin-fin", "oldfin", runtime.AgentFinished, 50), // finished, no live descendant: dropped
	)
	for i := 1; i <= 13; i++ {
		flat = append(flat, pruneAgent(fmt.Sprintf("top%02d", i), "", runtime.AgentFinished, 200+i))
	}

	p := pruneFinishedForState(flat)

	kept := map[string]bool{}
	for _, a := range p.kept {
		kept[a.ID] = true
	}
	if !kept["root"] || !kept["oldfin"] || !kept["live-gc"] {
		t.Fatalf("live agents and their ancestors must be kept: %v", kept)
	}
	if kept["oldfin-fin"] {
		t.Fatal("a finished descendant with no live descendant of its own must be dropped")
	}
	// root: the needed oldfin takes one of the 10 slots, so the 9 newest others fit.
	for i := 1; i <= 15; i++ {
		id := fmt.Sprintf("fin%02d", i)
		if want := i >= 7; kept[id] != want {
			t.Fatalf("%s kept=%v, want %v", id, kept[id], want)
		}
	}
	for i := 1; i <= 13; i++ {
		id := fmt.Sprintf("top%02d", i)
		if want := i >= 4; kept[id] != want {
			t.Fatalf("%s kept=%v, want %v", id, kept[id], want)
		}
	}
	if got := p.prunedByParent["root"]; got != 6 {
		t.Fatalf("root pruned = %d, want 6", got)
	}
	if got := p.prunedByParent["oldfin"]; got != 1 {
		t.Fatalf("oldfin pruned = %d, want 1", got)
	}
	if p.prunedTop != 3 {
		t.Fatalf("prunedTop = %d, want 3", p.prunedTop)
	}
}

func TestStateTrimsFinishedAgentsAndReportsTotals(t *testing.T) {
	s, seed := newRuntimeServer(t)
	var rootOrchID, taskID, epicID string
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM agents WHERE name = ?`, seed.AgentName).Scan(&rootOrchID); err != nil {
		t.Fatal(err)
	}
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.TaskKey).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.RootKey).Scan(&epicID); err != nil {
		t.Fatal(err)
	}
	finish := func(id string, sec int) {
		t.Helper()
		ms := db.Millis(time.Unix(int64(sec), 0))
		if _, err := s.s.DB.ExecContext(bg, `UPDATE agents SET state = 'finished', created_at = ?, finished_at = ? WHERE id = ?`, ms, ms, id); err != nil {
			t.Fatal(err)
		}
	}
	// A finished sub-orchestrator (oldest) with a running child and 12 finished children.
	sub := seedAgent(t, s, runtime.RoleOrchestrator, taskID, epicID, rootOrchID, "old-sub")
	seedSession(t, s, sub, "old-sub", 1, "cancelled")
	finish(sub, 1)
	live := seedAgent(t, s, runtime.RoleCoder, taskID, epicID, sub, "live-under-finished")
	seedSession(t, s, live, "live-under-finished", 1, "running")
	for i := 1; i <= 12; i++ {
		finish(seedAgent(t, s, runtime.RoleCoder, taskID, epicID, sub, fmt.Sprintf("sub-fin-%02d", i)), 10+i)
	}
	// 14 more finished children of the live root orchestrator.
	for i := 1; i <= 14; i++ {
		finish(seedAgent(t, s, runtime.RoleCoder, taskID, epicID, rootOrchID, fmt.Sprintf("fin-%02d", i)), 100+i)
	}
	// 12 finished top-level agents.
	for i := 1; i <= 12; i++ {
		finish(seedAgent(t, s, runtime.RoleOrchestrator, taskID, epicID, "", fmt.Sprintf("top-%02d", i)), 500+i)
	}

	var body struct {
		ActiveCount   int  `json:"active_count"`
		FinishedCount *int `json:"finished_count"`
		Agents        []struct {
			Name          string `json:"name"`
			State         string `json:"state"`
			FinishedCount *int   `json:"finished_count"`
			Children      []struct {
				Name string `json:"name"`
			} `json:"children"`
			Finished []struct {
				Name          string `json:"name"`
				FinishedCount *int   `json:"finished_count"`
				Children      []struct {
					Name string `json:"name"`
				} `json:"children"`
				Finished []json.RawMessage `json:"finished"`
			} `json:"finished"`
		} `json:"agents"`
	}
	rec := s.get(t, "/api/state")
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// base tree (root-orchestrator, task-worker) + live-under-finished.
	if body.ActiveCount != 3 {
		t.Fatalf("active_count = %d, want 3", body.ActiveCount)
	}
	if body.FinishedCount == nil || *body.FinishedCount != 12 {
		t.Fatalf("state finished_count = %v, want 12", body.FinishedCount)
	}
	finishedTop := 0
	var root *int
	for _, a := range body.Agents {
		if a.State == "finished" {
			finishedTop++
		}
		if a.Name == seed.AgentName {
			root = new(int)
			*root = len(a.Finished)
			if a.FinishedCount == nil || *a.FinishedCount != 15 {
				t.Fatalf("root finished_count = %v, want 15 (14 + old-sub)", a.FinishedCount)
			}
			if len(a.Finished) != 10 {
				t.Fatalf("root finished = %d, want 10", len(a.Finished))
			}
			var oldSub bool
			for _, f := range a.Finished {
				if f.Name != "old-sub" {
					continue
				}
				oldSub = true
				if f.FinishedCount == nil || *f.FinishedCount != 12 {
					t.Fatalf("old-sub finished_count = %v, want 12", f.FinishedCount)
				}
				if len(f.Finished) != 0 {
					t.Fatalf("old-sub's finished descendants must be dropped, got %d", len(f.Finished))
				}
				if len(f.Children) != 1 || f.Children[0].Name != "live-under-finished" {
					t.Fatalf("old-sub must keep its live child: %+v", f.Children)
				}
			}
			if !oldSub {
				t.Fatal("old-sub leads to a live agent and must be kept")
			}
		}
	}
	if root == nil {
		t.Fatal("root orchestrator missing")
	}
	if finishedTop != 10 {
		t.Fatalf("finished top-level agents = %d, want 10", finishedTop)
	}
}

func TestStateFinishedCountIsZeroNotMissing(t *testing.T) {
	s := newEmptyServer(t)
	var body map[string]any
	if err := json.Unmarshal(s.get(t, "/api/state").Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if v, ok := body["finished_count"].(float64); !ok || v != 0 {
		t.Fatalf("finished_count = %v, want 0", body["finished_count"])
	}
}

// Pruned agents must never reach agentNodeOut. The two oldest finished
// top-level agents point at an item that doesn't exist, so building their node
// (itemKeyByID) fails the whole request; /api/state only succeeds if they were
// dropped from the flat list first.
func TestStateNeverBuildsPrunedAgents(t *testing.T) {
	s, seed := newRuntimeServer(t)
	var taskID, epicID string
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.TaskKey).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.RootKey).Scan(&epicID); err != nil {
		t.Fatal(err)
	}
	conn, err := s.s.DB.Conn(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(bg, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(bg, `PRAGMA foreign_keys = ON`)
	for i := 1; i <= stateFinishedCap+2; i++ {
		id := seedAgent(t, s, runtime.RoleOrchestrator, taskID, epicID, "", fmt.Sprintf("top-%02d", i))
		ms := db.Millis(time.Unix(int64(500+i), 0))
		if _, err := conn.ExecContext(bg, `UPDATE agents SET state = 'finished', created_at = ?, finished_at = ? WHERE id = ?`, ms, ms, id); err != nil {
			t.Fatal(err)
		}
		if i <= 2 { // the two oldest are pruned
			if _, err := conn.ExecContext(bg, `UPDATE agents SET item_id = 'missing-item' WHERE id = ?`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if rec := s.get(t, "/api/state"); rec.Code != 200 {
		t.Fatalf("/api/state = %d; a pruned agent was built: %s", rec.Code, rec.Body.String())
	}
}
