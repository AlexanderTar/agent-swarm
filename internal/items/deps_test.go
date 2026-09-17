package items_test

import (
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func keys(its []items.Item) []string {
	var out []string
	for _, it := range its {
		out = append(out, it.Key)
	}
	return out
}

func TestAddAndRemoveDeps(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	t1 := mk(t, s, items.Task, st.Key, "T1")
	t2 := mk(t, s, items.Task, st.Key, "T2")
	if err := s.AddDep(ctx, t2.Key, t1.Key, user); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, t2.Key, t1.Key, user); err != nil {
		t.Fatalf("duplicate must be a no-op: %v", err)
	}
	if got := mustGet(t, s, t2.Key).BlockedBy; !slices.Equal(got, []string{"TASK-1"}) {
		t.Fatalf("BlockedBy = %v", got)
	}
	by, blocks, err := s.Deps(ctx, t1.Key)
	if err != nil || len(by) != 0 || !slices.Equal(keys(blocks), []string{"TASK-2"}) {
		t.Fatalf("Deps(T1) = %v %v %v", keys(by), keys(blocks), err)
	}
	if err := s.RemoveDep(ctx, t2.Key, t1.Key, user); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, s, t2.Key).BlockedBy; len(got) != 0 {
		t.Fatalf("after remove BlockedBy = %v", got)
	}
	if err := s.RemoveDep(ctx, t2.Key, t1.Key, user); err != nil {
		t.Fatalf("removing a missing edge is a no-op: %v", err)
	}
	if err := s.AddDep(ctx, t2.Key, "TASK-99", user); code(err) != items.CodeNotFound {
		t.Fatalf("unknown key: %v", err)
	}
}

// A cross-root edge changes both trees, so both must get an item.changed.
func TestCrossRootDepsChangeBothTrees(t *testing.T) {
	s := newStore(t)
	e1 := mk(t, s, items.Bug, "", "E1")
	e2 := mk(t, s, items.Bug, "", "E2")
	a := mk(t, s, items.Task, e1.Key, "A")
	b := mk(t, s, items.Task, e2.Key, "B")
	roots := func() []string {
		var out []string
		for _, e := range eventsOfType(t, s, events.ItemChanged) {
			if e["key"] == a.Key || e["key"] == b.Key {
				out = append(out, e["root_key"])
			}
		}
		return out
	}
	before := len(roots())
	if err := s.AddDep(ctx, a.Key, b.Key, user); err != nil {
		t.Fatal(err)
	}
	if got := roots()[before:]; !slices.Equal(got, []string{e1.Key, e2.Key}) {
		t.Fatalf("AddDep announced %v", got)
	}
	before = len(roots())
	if err := s.RemoveDep(ctx, a.Key, b.Key, user); err != nil {
		t.Fatal(err)
	}
	if got := roots()[before:]; !slices.Equal(got, []string{e1.Key, e2.Key}) {
		t.Fatalf("RemoveDep announced %v", got)
	}
}

// An exhausted frontier ends the walk: a huge ?hops= must not spin.
func TestGraphHopsStopWhenTheFrontierEmpties(t *testing.T) {
	s := newStore(t)
	a := mk(t, s, items.Epic, "", "E")
	done := make(chan items.Graph, 1)
	go func() {
		g, err := s.Graph(ctx, a.Key, "neighbourhood", math.MaxInt32)
		if err != nil {
			t.Error(err)
		}
		done <- g
	}()
	select {
	case g := <-done:
		if len(g.Nodes) != 1 {
			t.Fatalf("nodes = %v", g.Nodes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Graph is still walking an empty frontier")
	}
}

func TestResolvedDepsDoNotBlock(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	a, b, c := mk(t, s, items.Task, st.Key, "A"), mk(t, s, items.Task, st.Key, "B"), mk(t, s, items.Task, st.Key, "C")
	s.AddDep(ctx, c.Key, a.Key, user)
	s.AddDep(ctx, c.Key, b.Key, user)
	exec(t, s.DB, `UPDATE items SET status = 'done' WHERE id = ?`, a.ID)
	if got := mustGet(t, s, c.Key).BlockedBy; !slices.Equal(got, []string{"TASK-2"}) {
		t.Fatalf("BlockedBy = %v", got)
	}
	exec(t, s.DB, `UPDATE items SET status = 'cancelled' WHERE id = ?`, b.ID)
	if got := mustGet(t, s, c.Key).BlockedBy; len(got) != 0 {
		t.Fatalf("cancelled dep still blocks: %v", got)
	}
}

func TestCyclesAreRefused(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	var ts []items.Item
	for i := range 20 {
		ts = append(ts, mk(t, s, items.Task, st.Key, fmt.Sprint("T", i)))
	}
	for i := 0; i < 19; i++ {
		if err := s.AddDep(ctx, ts[i+1].Key, ts[i].Key, user); err != nil {
			t.Fatal(err)
		}
	}
	cases := [][2]string{
		{ts[0].Key, ts[0].Key},  // self
		{ts[0].Key, ts[1].Key},  // direct
		{ts[0].Key, ts[2].Key},  // 3-node
		{ts[0].Key, ts[19].Key}, // 20-node
	}
	for _, c := range cases {
		err := s.AddDep(ctx, c[0], c[1], user)
		if code(err) != items.CodeConflict || err.Error() != items.CycleMessage {
			t.Errorf("%s blocked by %s: %v", c[0], c[1], err)
		}
	}
	if err := s.AddDep(ctx, ts[19].Key, ts[0].Key, user); err != nil {
		t.Errorf("a redundant forward edge is not a cycle: %v", err)
	}
}

func TestHierarchyAndScopeRules(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	task := mk(t, s, items.Task, st.Key, "T")
	for _, pair := range [][2]string{{task.Key, st.Key}, {st.Key, task.Key}, {task.Key, e.Key}, {e.Key, task.Key}} {
		err := s.AddDep(ctx, pair[0], pair[1], user)
		if code(err) != items.CodeConflict || err.Error() != items.HierarchyDepMessage {
			t.Errorf("%v: %v", pair, err)
		}
	}
	other := mk(t, s, items.Bug, "", "Other root")
	otherTask := mk(t, s, items.Task, other.Key, "OT")
	if err := s.AddDep(ctx, task.Key, otherTask.Key, user); err != nil {
		t.Fatalf("cross-root edge must be allowed: %v", err)
	}
	orch := items.Orchestrator("agt_1", other.ID)
	if err := s.AddDep(ctx, task.Key, other.Key, orch); err == nil || err.Error() != "TASK-1 is outside BUG-1." {
		t.Fatalf("orchestrator outside its root: %v", err)
	}
	if err := s.RemoveDep(ctx, task.Key, otherTask.Key, orch); err == nil {
		t.Fatal("orchestrator outside its root must not remove edges")
	}
}

func TestGraph(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	a, b := mk(t, s, items.Task, st.Key, "A"), mk(t, s, items.Task, st.Key, "B")
	c, d := mk(t, s, items.Task, st.Key, "C"), mk(t, s, items.Task, st.Key, "D")
	bug := mk(t, s, items.Bug, "", "Crash")
	s.AddDep(ctx, b.Key, a.Key, user) // B waits for A
	s.AddDep(ctx, c.Key, b.Key, user)
	s.AddDep(ctx, d.Key, c.Key, user)
	s.AddDep(ctx, d.Key, bug.Key, user) // external

	g, err := s.Graph(ctx, st.Key, "root", 0)
	if err != nil {
		t.Fatal(err)
	}
	var nodeKeys []string
	external := map[string]bool{}
	for _, n := range g.Nodes {
		nodeKeys = append(nodeKeys, n.Key)
		external[n.Key] = n.External
	}
	if !slices.Equal(nodeKeys, []string{"EPIC-1", "STORY-1", "TASK-1", "TASK-2", "TASK-3", "TASK-4", "BUG-1"}) {
		t.Fatalf("root nodes = %v", nodeKeys)
	}
	if !external["BUG-1"] || external["TASK-1"] || g.Nodes[6].RootKey != "BUG-1" {
		t.Fatalf("external flags = %v", external)
	}
	wantEdges := []items.GraphEdge{{From: "BUG-1", To: "TASK-4"}, {From: "TASK-1", To: "TASK-2"}, {From: "TASK-2", To: "TASK-3"}, {From: "TASK-3", To: "TASK-4"}}
	if !slices.Equal(g.Edges, wantEdges) {
		t.Fatalf("edges = %v", g.Edges)
	}

	hood := func(hops int) []string {
		g, err := s.Graph(ctx, b.Key, "neighbourhood", hops)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range g.Nodes {
			out = append(out, n.Key)
		}
		return out
	}
	if got := hood(1); !slices.Equal(got, []string{"TASK-1", "TASK-2", "TASK-3"}) {
		t.Fatalf("hops=1 %v", got)
	}
	if got := hood(2); !slices.Equal(got, []string{"TASK-1", "TASK-2", "TASK-3", "TASK-4"}) {
		t.Fatalf("hops=2 %v", got)
	}
	if got := hood(0); !slices.Equal(got, []string{"TASK-1", "TASK-2", "TASK-3"}) {
		t.Fatalf("hops=0 means 1: %v", got)
	}
	if _, err := s.Graph(ctx, b.Key, "galaxy", 1); code(err) != items.CodeBadRequest {
		t.Fatalf("bad scope: %v", err)
	}
}
