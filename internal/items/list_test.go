package items_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func listKeys(its []items.Item) (keys []string, context []string) {
	for _, it := range its {
		keys = append(keys, it.Key)
		if it.Context {
			context = append(context, it.Key)
		}
	}
	return keys, context
}

func listFixture(t *testing.T) *items.Store {
	s := newStore(t)
	e1 := mk(t, s, items.Epic, "", "Authentication")      // EPIC-1, priority 2
	s1 := mk(t, s, items.Story, e1.Key, "Login")          // STORY-1
	mk(t, s, items.Task, s1.Key, "Build login form")      // TASK-1
	t2 := mk(t, s, items.Task, s1.Key, "Persist session") // TASK-2
	p0 := 0
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Urgent payments", Priority: &p0}, user); err != nil { // EPIC-2
		t.Fatal(err)
	}
	b := mk(t, s, items.Bug, "", "Login crash")            // BUG-1 (priority 2, newer than EPIC-1)
	mk(t, s, items.Task, b.Key, "Fix null session")        // TASK-3
	old := mk(t, s, items.Epic, "", "Archived login work") // EPIC-3
	exec(t, s.DB, `UPDATE items SET archived_at = 1 WHERE id = ?`, old.ID)
	setStatus(t, s, t2, items.Blocked)
	return s
}

func TestTreeListing(t *testing.T) {
	s := listFixture(t)
	all, n, err := s.List(ctx, items.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	keys, context := listKeys(all)
	want := []string{"EPIC-2", "BUG-1", "TASK-3", "EPIC-1", "STORY-1", "TASK-1", "TASK-2"}
	if !slices.Equal(keys, want) || n != 7 || len(context) != 0 {
		t.Fatalf("tree = %v (%d) context %v", keys, n, context)
	}
	if all[4].Progress == nil || all[4].Progress.Unit != "tasks" || all[4].Progress.Total != 2 {
		t.Fatalf("story progress = %+v", all[4].Progress)
	}

	cases := []struct {
		f           items.ListFilter
		keys, ctxKs []string
		matches     int
	}{
		{items.ListFilter{Q: "login form"}, []string{"EPIC-1", "STORY-1", "TASK-1"}, []string{"EPIC-1", "STORY-1"}, 1},
		{items.ListFilter{Q: "TASK-2"}, []string{"EPIC-1", "STORY-1", "TASK-2"}, []string{"EPIC-1", "STORY-1"}, 1},
		{items.ListFilter{Q: "login"}, []string{"BUG-1", "EPIC-1", "STORY-1", "TASK-1"}, []string{"EPIC-1"}, 3},
		{items.ListFilter{Type: items.Task}, []string{"BUG-1", "TASK-3", "EPIC-1", "STORY-1", "TASK-1", "TASK-2"}, []string{"BUG-1", "EPIC-1", "STORY-1"}, 3},
		{items.ListFilter{Status: items.Blocked}, []string{"EPIC-1", "STORY-1", "TASK-2"}, []string{"EPIC-1", "STORY-1"}, 1},
		{items.ListFilter{Root: "BUG-1"}, []string{"BUG-1", "TASK-3"}, nil, 2},
		{items.ListFilter{Q: "archived"}, nil, nil, 0},
		{items.ListFilter{Q: `"weird" -x (`}, nil, nil, 0},
	}
	for _, c := range cases {
		got, n, err := s.List(ctx, c.f)
		if err != nil {
			t.Errorf("%+v: %v", c.f, err)
			continue
		}
		keys, context := listKeys(got)
		if !slices.Equal(keys, c.keys) || !slices.Equal(context, c.ctxKs) || n != c.matches {
			t.Errorf("%+v: keys %v context %v matches %d", c.f, keys, context, n)
		}
	}
}

func TestFlatListingAndValidation(t *testing.T) {
	s := listFixture(t)
	got, n, err := s.List(ctx, items.ListFilter{View: "flat", Q: "login"})
	if err != nil {
		t.Fatal(err)
	}
	keys, context := listKeys(got)
	if n != 3 || len(keys) != 3 || len(context) != 0 || !slices.Contains(keys, "BUG-1") {
		t.Fatalf("flat = %v %v %d", keys, context, n)
	}
	for _, f := range []items.ListFilter{{View: "grid"}, {Type: "saga"}, {Status: "doing"}} {
		if _, _, err := s.List(ctx, f); code(err) != items.CodeBadRequest {
			t.Errorf("%+v: %v", f, err)
		}
	}
	if _, _, err := s.List(ctx, items.ListFilter{Root: "EPIC-99"}); err != nil {
		t.Errorf("unknown root is an empty result, not an error: %v", err)
	}
}

func TestChildrenSortByKeyNumber(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	for i := range 11 {
		mk(t, s, items.Task, st.Key, fmt.Sprint("T", i))
	}
	all, _, _ := s.List(ctx, items.ListFilter{Root: e.Key})
	keys, _ := listKeys(all)
	if keys[2] != "TASK-1" || keys[3] != "TASK-2" || keys[12] != "TASK-11" {
		t.Fatalf("order = %v", keys)
	}
}

// Migrated v1 rows share timestamps; the top level must not reshuffle between fetches.
func TestTopLevelOrderIsStableOnTies(t *testing.T) {
	s := newStore(t)
	var want []string
	for i := range 6 {
		e := mk(t, s, items.Epic, "", fmt.Sprint("E", i))
		exec(t, s.DB, `UPDATE items SET created_at = 1, updated_at = 1 WHERE id = ?`, e.ID)
		want = append(want, e.Key)
	}
	for range 5 {
		all, _, err := s.List(ctx, items.ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if keys, _ := listKeys(all); !slices.Equal(keys, want) {
			t.Fatalf("order = %v, want %v", keys, want)
		}
	}
}
