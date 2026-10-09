package items_test

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

func TestRemoveDepTxDeletesTheEdgeOnTheCallersTransaction(t *testing.T) {
	st := newStore(t)
	ep := mk(t, st, items.Epic, "", "Ship it")
	a := mk(t, st, items.Story, ep.Key, "First")
	b := mk(t, st, items.Story, ep.Key, "Second")
	if err := st.AddDep(ctx, b.Key, a.Key, items.Daemon()); err != nil {
		t.Fatal(err)
	}
	err := st.DB.Tx(ctx, func(tx *sql.Tx) error { return st.RemoveDepTx(ctx, tx, b.Key, a.Key, items.Daemon()) })
	if err != nil {
		t.Fatal(err)
	}
	blockedBy, blocks, err := st.Deps(ctx, b.Key)
	if err != nil || len(blockedBy) != 0 || len(blocks) != 0 {
		t.Fatalf("Deps(%s) = %v %v %v, want no edges", b.Key, blockedBy, blocks, err)
	}
	_, blocks, err = st.Deps(ctx, a.Key)
	if err != nil || len(blocks) != 0 {
		t.Fatalf("Deps(%s) blocks = %v %v, want none", a.Key, blocks, err)
	}
}

func TestRemoveDepTxOfAMissingEdgeIsANoOpWithoutEvents(t *testing.T) {
	st := newStore(t)
	ep := mk(t, st, items.Epic, "", "Ship it")
	a := mk(t, st, items.Story, ep.Key, "First")
	b := mk(t, st, items.Story, ep.Key, "Second")
	before := len(eventsOfType(t, st, "item.changed"))
	err := st.DB.Tx(ctx, func(tx *sql.Tx) error { return st.RemoveDepTx(ctx, tx, b.Key, a.Key, items.Daemon()) })
	if err != nil {
		t.Fatalf("removing an absent edge: %v, want nil", err)
	}
	if after := len(eventsOfType(t, st, "item.changed")); after != before {
		t.Fatalf("item.changed events %d -> %d, want no announcement for a no-op", before, after)
	}
}

func TestDepsEditsRefuseUnknownKeys(t *testing.T) {
	st := newStore(t)
	ep := mk(t, st, items.Epic, "", "Ship it")
	a := mk(t, st, items.Story, ep.Key, "First")
	for _, call := range []struct {
		name string
		fn   func(tx *sql.Tx) error
	}{
		{"unknown item", func(tx *sql.Tx) error { return st.RemoveDepTx(ctx, tx, "TASK-999", a.Key, items.Daemon()) }},
		{"unknown blocker", func(tx *sql.Tx) error { return st.RemoveDepTx(ctx, tx, a.Key, "TASK-999", items.Daemon()) }},
	} {
		err := st.DB.Tx(ctx, call.fn)
		var ie *items.Error
		if !errors.As(err, &ie) || ie.Code != items.CodeNotFound {
			t.Fatalf("%s: err = %v, want not_found", call.name, err)
		}
	}
	if _, _, err := st.Deps(ctx, "TASK-999"); err == nil {
		t.Fatal("Deps of an unknown item: want an error")
	}
}

func TestDepsListsBothDirections(t *testing.T) {
	st := newStore(t)
	ep := mk(t, st, items.Epic, "", "Ship it")
	a := mk(t, st, items.Story, ep.Key, "A")
	b := mk(t, st, items.Story, ep.Key, "B")
	c := mk(t, st, items.Story, ep.Key, "C")
	for _, e := range [][2]string{{b.Key, a.Key}, {c.Key, b.Key}} {
		if err := st.AddDep(ctx, e[0], e[1], items.Daemon()); err != nil {
			t.Fatal(err)
		}
	}
	blockedBy, blocks, err := st.Deps(ctx, b.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys(blockedBy), []string{a.Key}) || !slices.Equal(keys(blocks), []string{c.Key}) {
		t.Fatalf("B blockedBy=%v blocks=%v, want [A] and [C]", keys(blockedBy), keys(blocks))
	}
}

func TestGetTxReadsInsideATransactionAndReportsMissingKeys(t *testing.T) {
	st := newStore(t)
	ep := mk(t, st, items.Epic, "", "Ship it")
	err := st.DB.Tx(ctx, func(tx *sql.Tx) error {
		got, err := st.GetTx(ctx, tx, ep.Key)
		if err != nil || got.ID != ep.ID {
			t.Fatalf("GetTx = %+v, %v; want %s", got, err, ep.ID)
		}
		_, err = st.GetTx(ctx, tx, "EPIC-404")
		var ie *items.Error
		if !errors.As(err, &ie) || ie.Code != items.CodeNotFound {
			t.Fatalf("GetTx of a missing key err = %v, want not_found", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFinishedMarkerNamesTheIntegratedCheckpoint(t *testing.T) {
	if got := items.FinishedMarker("ckp_1"); got != "finished:ckp_1" {
		t.Fatalf("FinishedMarker = %q", got)
	}
}

func TestListOfOnlyPunctuationMatchesNothingAndFlatViewIsNeverNil(t *testing.T) {
	st := newStore(t)
	mk(t, st, items.Epic, "", "Ship it")
	got, n, err := st.List(ctx, items.ListFilter{Q: "!!! ???"})
	if err != nil || n != 0 || got == nil || len(got) != 0 {
		t.Fatalf("punctuation-only search = %v %d %v, want an empty non-nil list", got, n, err)
	}
	got, n, err = st.List(ctx, items.ListFilter{View: "flat", Type: items.Bug})
	if err != nil || n != 0 || got == nil || len(got) != 0 {
		t.Fatalf("flat list with no bugs = %#v %d %v, want an empty non-nil list", got, n, err)
	}
}

func TestOpenSpikeTasksListsOnlyUnfinishedOwnTasks(t *testing.T) {
	st := newStore(t)
	sp := mk(t, st, items.Spike, "", "Investigate")
	ready := mk(t, st, items.Task, sp.Key, "Ready one")
	running := mk(t, st, items.Task, sp.Key, "Running one")
	review := mk(t, st, items.Task, sp.Key, "Reviewing one")
	draft := mk(t, st, items.Task, sp.Key, "Draft one")
	done := mk(t, st, items.Task, sp.Key, "Done one")
	for status, it := range map[string]items.Item{"ready": ready, "in_progress": running, "in_review": review, "done": done} {
		exec(t, st.DB, `UPDATE items SET status = ? WHERE id = ?`, status, it.ID)
	}
	if draft.Status != items.Draft {
		t.Fatalf("draft task status = %s, want draft", draft.Status)
	}
	var got []string
	err := st.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		got, err = st.OpenSpikeTasksTx(ctx, tx, sp.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ready.Key, running.Key, review.Key}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("open spike tasks = %v, want %v", got, want)
	}
}

// Each item type validates a workflow spec at its own level: stories take
// after_tasks, epics and bugs take integration, and the other level's field is
// refused with the workflow package's copy.
func TestCreateValidatesAWorkflowAtTheItemsOwnLevel(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	for _, tc := range []struct {
		name   string
		in     items.CreateInput
		spec   workflow.Spec
		wantIn string
	}{
		{"story with integration", items.CreateInput{Type: items.Story, ParentKey: e.Key, Title: "S"},
			workflow.Spec{Integration: &workflow.Integration{}}, "integration is only for epics and bugs"},
		{"epic with after_tasks", items.CreateInput{Type: items.Epic, Title: "E2"},
			workflow.Spec{AfterTasks: &workflow.Step{}}, "after_tasks is only for stories"},
		{"bug with after_tasks", items.CreateInput{Type: items.Bug, Title: "B"},
			workflow.Spec{AfterTasks: &workflow.Step{}}, "after_tasks is only for stories"},
		{"task with after_tasks", items.CreateInput{Type: items.Task, ParentKey: e.Key, Title: "T"},
			workflow.Spec{AfterTasks: &workflow.Step{}}, "after_tasks is only for stories"},
	} {
		tc.in.Workflow = &tc.spec
		_, err := s.Create(ctx, tc.in, items.Daemon())
		if err == nil || !strings.Contains(err.Error(), tc.wantIn) || code(err) != items.CodeBadRequest {
			t.Fatalf("%s: err = %v, want bad_request containing %q", tc.name, err, tc.wantIn)
		}
	}
}

// Cancelling a root cascades to descendants, and a descendant's open approval
// request is resolved with it rather than left orphaned.
func TestCancelCascadeStalesADescendantsOpenApproval(t *testing.T) {
	s := newStore(t)
	e, st, task := tree(t, s)
	setStatus(t, s, task, items.InProgress)
	req := seedRequest(t, s.DB, task, "approve_plan", "open")
	if err := move(t, s, e.Key, items.Cancelled, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, st.Key, items.Cancelled)
	wantStatus(t, s, task.Key, items.Cancelled)
	var state string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE id = ?`, req).Scan(&state); err != nil || state != "stale" {
		t.Fatalf("descendant approval state = %q, %v; want stale", state, err)
	}
	resolved := eventsOfType(t, s, events.RequestResolved)
	if len(resolved) != 1 || resolved[0]["id"] != req || resolved[0]["item"] != task.Key || resolved[0]["state"] != "stale" {
		t.Fatalf("request.resolved = %v, want one stale event for %s", resolved, task.Key)
	}
}

func TestFinishApprovalOfAnUnknownRootIsNotFound(t *testing.T) {
	s := newStore(t)
	_, ok, err := s.FinishApprovalTx(ctx, s.DB, "itm_missing")
	if ok || code(err) != items.CodeNotFound {
		t.Fatalf("FinishApprovalTx = ok %v, err %v; want not_found", ok, err)
	}
}
