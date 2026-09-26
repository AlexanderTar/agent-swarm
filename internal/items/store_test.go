package items_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

func code(err error) string {
	var e *items.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestHierarchyMatrix(t *testing.T) {
	s := newStore(t)
	allowed := map[[2]items.Type]bool{
		{items.Epic, items.Story}: true, {items.Story, items.Task}: true,
		{items.Bug, items.Task}: true, {items.Spike, items.Task}: true,
	}
	epic := mk(t, s, items.Epic, "", "E")
	parents := map[items.Type]items.Item{
		items.Epic:  epic,
		items.Story: mk(t, s, items.Story, epic.Key, "S"),
		items.Bug:   mk(t, s, items.Bug, "", "B"),
		items.Spike: mk(t, s, items.Spike, "", "P"),
	}
	parents[items.Task] = mk(t, s, items.Task, parents[items.Story].Key, "T")
	all := []items.Type{items.Epic, items.Story, items.Task, items.Bug, items.Spike}
	for _, pt := range all {
		for _, ct := range all {
			in := items.CreateInput{Type: ct, ParentKey: parents[pt].Key, Title: "child"}
			if ct == items.Spike {
				in.SpikeIntent = "debug"
			}
			_, err := s.Create(ctx, in, items.Daemon())
			if allowed[[2]items.Type{pt, ct}] != (err == nil) {
				t.Errorf("%s under %s: err = %v", ct, pt, err)
			}
			if err != nil && code(err) != items.CodeBadRequest {
				t.Errorf("%s under %s: code = %q", ct, pt, code(err))
			}
		}
	}
	for typ, msg := range map[items.Type]string{
		items.Story: "A story needs a parent epic.",
		items.Task:  "A task needs a parent story, bug, spike or chore.",
	} {
		_, err := s.Create(ctx, items.CreateInput{Type: typ, Title: "top"}, user)
		if err == nil || err.Error() != msg {
			t.Errorf("top-level %s: err = %v", typ, err)
		}
	}
	_, err := s.Create(ctx, items.CreateInput{Type: items.Epic, ParentKey: epic.Key, Title: "x"}, user)
	if err == nil || err.Error() != "An epic can't be a child of an epic." {
		t.Errorf("epic under epic: %v", err)
	}
}

func TestRootAndParentAtEveryDepth(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "Auth")
	st := mk(t, s, items.Story, e.Key, "Login")
	task := mk(t, s, items.Task, st.Key, "Form")
	if e.RootID != e.ID || e.RootKey != "EPIC-1" || e.ParentID != "" {
		t.Errorf("epic = %+v", e)
	}
	if st.RootID != e.ID || st.ParentKey != "EPIC-1" {
		t.Errorf("story = %+v", st)
	}
	if task.RootID != e.ID || task.RootKey != "EPIC-1" || task.ParentKey != "STORY-1" || task.ParentID != st.ID {
		t.Errorf("task = %+v", task)
	}
	anc, err := s.Ancestors(ctx, task.Key)
	if err != nil || len(anc) != 2 || anc[0].Key != "EPIC-1" || anc[1].Key != "STORY-1" {
		t.Fatalf("Ancestors = %+v, %v", anc, err)
	}
	mk(t, s, items.Task, st.Key, "Second")
	kids, _ := s.Children(ctx, st.Key)
	if len(kids) != 2 || kids[0].Key != "TASK-1" || kids[1].Key != "TASK-2" {
		t.Fatalf("Children = %+v", kids)
	}
	if _, err := s.Ancestors(ctx, "TASK-99"); code(err) != items.CodeNotFound {
		t.Fatalf("missing key: %v", err)
	}
}

func TestCreateDefaultsAndKeys(t *testing.T) {
	s := newStore(t)
	e, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "  Auth  ", Brief: "b"}, user)
	if err != nil {
		t.Fatal(err)
	}
	if e.Key != "EPIC-1" || e.Status != items.Draft || e.Priority != 2 || e.Revision != 1 ||
		e.Title != "Auth" || e.Acceptance == nil || len(e.Acceptance) != 0 || e.Repos == nil {
		t.Fatalf("epic = %+v", e)
	}
	if b := mk(t, s, items.Bug, "", "B"); b.Key != "BUG-1" {
		t.Fatalf("bug key = %s", b.Key)
	}
	ready, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "R", Status: items.Ready, Acceptance: []string{"a", "b"}}, items.Daemon())
	if err != nil || ready.Status != items.Ready || ready.Key != "EPIC-2" || !slices.Equal(ready.Acceptance, []string{"a", "b"}) {
		t.Fatalf("ready = %+v, %v", ready, err)
	}
	evs, _ := s.Events.After(ctx, 0, 10)
	if len(evs) != 3 || evs[0].Type != "item.changed" || !strings.Contains(string(evs[0].Payload), `"key":"EPIC-1"`) {
		t.Fatalf("events = %+v", evs)
	}
}

func TestCreateValidation(t *testing.T) {
	s := newStore(t)
	p := func(n int) *int { return &n }
	cases := []struct {
		in   items.CreateInput
		msg  string
		code string
	}{
		{items.CreateInput{Type: items.Epic, Title: "   "}, "Title must be 1–200 characters.", items.CodeBadRequest},
		{items.CreateInput{Type: items.Epic, Title: strings.Repeat("é", 201)}, "Title must be 1–200 characters.", items.CodeBadRequest},
		{items.CreateInput{Type: items.Epic, Title: "t", Priority: p(4)}, "Priority must be between 0 and 3.", items.CodeBadRequest},
		{items.CreateInput{Type: items.Spike, Title: "t"}, "Spikes start with an intent. Use New spike.", items.CodeBadRequest},
		{items.CreateInput{Type: items.Spike, Title: "t", SpikeIntent: "vibes"}, "Spikes start with an intent. Use New spike.", items.CodeBadRequest},
		{items.CreateInput{Type: "saga", Title: "t"}, `Unknown item type "saga".`, items.CodeBadRequest},
		{items.CreateInput{Type: items.Story, ParentKey: "EPIC-9", Title: "t"}, "No item EPIC-9.", items.CodeNotFound},
		{items.CreateInput{Type: items.Epic, Title: "t", Status: items.InProgress}, "New items start as Draft or Ready.", items.CodeBadRequest},
	}
	for _, c := range cases {
		_, err := s.Create(ctx, c.in, user)
		if err == nil || err.Error() != c.msg || code(err) != c.code {
			t.Errorf("%+v: err = %v (%s)", c.in, err, code(err))
		}
	}
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: strings.Repeat("é", 200)}, user); err != nil {
		t.Errorf("200 runes must be accepted: %v", err)
	}
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "t", Brief: strings.Repeat("b", 2000)}, user); err != nil {
		t.Errorf("2000 runes brief must be accepted: %v", err)
	}
}

func TestTddExemptRules(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	orch := items.Orchestrator("agt_1", e.ID)
	in := items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "Docs", TddExempt: "docs"}
	if _, err := s.Create(ctx, in, user); err == nil || err.Error() != "Only an orchestrator or a plan can set tdd_exempt." {
		t.Errorf("user: %v", err)
	}
	in.Workflow = &workflow.Spec{Template: "tdd-reviewed"}
	for _, v := range []string{"docs", "config", "mechanical-rename", "spike-research"} {
		in.TddExempt = v
		it, err := s.Create(ctx, in, orch)
		if err != nil || it.TddExempt != v {
			t.Errorf("orchestrator %s: %+v, %v", v, it, err)
		}
	}
	in.TddExempt = "tests-later"
	if _, err := s.Create(ctx, in, items.Daemon()); err == nil ||
		err.Error() != "tdd_exempt must be one of docs, config, mechanical-rename, spike-research." {
		t.Errorf("bad value: %v", err)
	}
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: e.Key, Title: "S2", TddExempt: "docs"}, orch); err == nil ||
		err.Error() != "Only tasks can be TDD-exempt." {
		t.Errorf("story: %v", err)
	}
}

func TestOrchestratorStaysInItsRoot(t *testing.T) {
	s := newStore(t)
	e1 := mk(t, s, items.Epic, "", "One")
	e2 := mk(t, s, items.Epic, "", "Two")
	st2 := mk(t, s, items.Story, e2.Key, "Other")
	_, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st2.Key, Title: "x"}, items.Orchestrator("agt_1", e1.ID))
	if err == nil || err.Error() != "STORY-1 is outside EPIC-1." || code(err) != items.CodeBadRequest {
		t.Fatalf("err = %v", err)
	}
	// An orchestrator may now propose a brand-new top-level item (the
	// caller-side, top-level-only restriction lives in internal/mcpserver,
	// which knows ParentAgentID; internal/items itself no longer refuses
	// every orchestrator create with no parent).
	proposed, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "mine"}, items.Orchestrator("agt_1", e1.ID))
	if err != nil || proposed.Status != items.Draft || proposed.OriginSpikeID != e1.ID {
		t.Fatalf("propose top-level = %+v, %v", proposed, err)
	}
	p := "new"
	_, err = s.Update(ctx, st2.Key, items.Patch{Title: &p, Revision: st2.Revision}, items.Orchestrator("agt_1", e1.ID))
	if err == nil || err.Error() != "STORY-1 is outside EPIC-1." {
		t.Fatalf("update err = %v", err)
	}
}

// TestOrchestratorCanProposeRoot is the 2026-09-26 top-level-items spec: an
// orchestrator proposing a new root item lands Draft, with any repos moved
// to suggested (never confirmed) and origin_spike_id set to its own root.
func TestOrchestratorCanProposeRoot(t *testing.T) {
	s := newStore(t)
	root := mk(t, s, items.Epic, "", "Root")
	orch := items.Orchestrator("agt_1", root.ID)
	for _, in := range []items.CreateInput{
		{Type: items.Epic, Title: "New epic", Repos: []string{"repo_a"}},
		{Type: items.Bug, Title: "New bug"},
		{Type: items.Chore, Title: "New chore"},
		{Type: items.Spike, Title: "New spike", SpikeIntent: "feature"},
	} {
		it, err := s.Create(ctx, in, orch)
		if err != nil {
			t.Fatalf("%s: %v", in.Type, err)
		}
		if it.Status != items.Draft {
			t.Errorf("%s: status = %s, want draft", in.Type, it.Status)
		}
		if it.OriginSpikeID != root.ID {
			t.Errorf("%s: origin_spike_id = %s, want %s", in.Type, it.OriginSpikeID, root.ID)
		}
		if len(in.Repos) > 0 {
			if !slices.Equal(it.SuggestedRepos, in.Repos) || len(it.Repos) != 0 {
				t.Errorf("%s: repos = %v, suggested = %v", in.Type, it.Repos, it.SuggestedRepos)
			}
		}
		if it.RootID != it.ID {
			t.Errorf("%s: not its own root", in.Type)
		}
	}
}

// TestIntentOnlyForSpikes: SpikeIntent is trust-boundary input from the
// mcpserver's new "intent" wire field; only a spike may set it.
func TestIntentOnlyForSpikes(t *testing.T) {
	s := newStore(t)
	root := mk(t, s, items.Epic, "", "Root")
	orch := items.Orchestrator("agt_1", root.ID)
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "e", SpikeIntent: "feature"}, orch); err == nil ||
		err.Error() != "Only spikes have an intent." {
		t.Fatalf("err = %v", err)
	}
}

// TestProposedRootCannotStartReady is decision 2: a proposed top-level item
// always starts Draft; explicitly asking for ready is refused, not silently
// downgraded.
func TestProposedRootCannotStartReady(t *testing.T) {
	s := newStore(t)
	root := mk(t, s, items.Epic, "", "Root")
	orch := items.Orchestrator("agt_1", root.ID)
	_, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "New epic", Status: items.Ready}, orch)
	if err == nil || err.Error() != "A proposed top-level item starts as Draft. The user starts it." || code(err) != items.CodeBadRequest {
		t.Fatalf("err = %v", err)
	}
}

// TestProposedStoryTaskStillNeedParent: story/task always require a parent,
// regardless of actor -- the new top-level-propose path only opens up for
// epic/bug/chore/spike.
func TestProposedStoryTaskStillNeedParent(t *testing.T) {
	s := newStore(t)
	root := mk(t, s, items.Epic, "", "Root")
	orch := items.Orchestrator("agt_1", root.ID)
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Story, Title: "S"}, orch); err == nil ||
		err.Error() != "A story needs a parent epic." {
		t.Fatalf("story err = %v", err)
	}
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Task, Title: "T"}, orch); err == nil ||
		err.Error() != "A task needs a parent story, bug, spike or chore." {
		t.Fatalf("task err = %v", err)
	}
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Story, Title: "S"}, items.Daemon()); err == nil ||
		err.Error() != "A story needs a parent epic." {
		t.Fatalf("daemon story err = %v", err)
	}
}

func TestRepoHintsMustBeConfirmedForTheRoot(t *testing.T) {
	s := newStore(t)
	e, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "E", Repos: []string{"repo_a"}}, items.Daemon())
	if err != nil || !slices.Equal(e.Repos, []string{"repo_a"}) || e.ReposVersion != 1 {
		t.Fatalf("epic = %+v, %v", e, err)
	}
	st := mk(t, s, items.Story, e.Key, "S")
	_, err = s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "T", Repos: []string{"repo_b"}}, items.Daemon())
	if err == nil || err.Error() != "repo_b isn't confirmed for EPIC-1." {
		t.Fatalf("err = %v", err)
	}
	task, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "T", Repos: []string{"repo_a"}}, items.Daemon())
	if err != nil || !slices.Equal(task.Repos, []string{"repo_a"}) {
		t.Fatalf("task = %+v, %v", task, err)
	}
}

func TestUpdateUsesRevision(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "Old")
	title, brief, acc, prio := "New", "brief", []string{"works"}, 0
	got, err := s.Update(ctx, e.Key, items.Patch{Title: &title, Brief: &brief, Acceptance: &acc, Priority: &prio, Revision: 1}, user)
	if err != nil || got.Title != "New" || got.Brief != "brief" || got.Priority != 0 || got.Revision != 2 || !slices.Equal(got.Acceptance, acc) {
		t.Fatalf("update = %+v, %v", got, err)
	}
	_, err = s.Update(ctx, e.Key, items.Patch{Title: &title, Revision: 1}, user)
	if code(err) != items.CodeConflict || err.Error() != items.StaleRevision {
		t.Fatalf("stale: %v", err)
	}
	bad := strings.Repeat("x", 201)
	if _, err := s.Update(ctx, e.Key, items.Patch{Title: &bad, Revision: 2}, user); code(err) != items.CodeBadRequest {
		t.Fatalf("invalid title: %v", err)
	}
	if _, err := s.Update(ctx, "EPIC-9", items.Patch{Title: &title, Revision: 1}, user); code(err) != items.CodeNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestUpdateTddExempt(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	task := mk(t, s, items.Task, st.Key, "T")
	orch := items.Orchestrator("agt1", e.ID)

	exempt := "docs"
	got, err := s.Update(ctx, task.Key, items.Patch{TddExempt: &exempt, Revision: task.Revision}, orch)
	if err != nil || got.TddExempt != "docs" || got.Revision != task.Revision+1 {
		t.Fatalf("update = %+v, %v", got, err)
	}

	bad := "true"
	if _, err := s.Update(ctx, task.Key, items.Patch{TddExempt: &bad, Revision: got.Revision}, orch); code(err) != items.CodeBadRequest {
		t.Fatalf("invalid value: %v", err)
	}

	if _, err := s.Update(ctx, task.Key, items.Patch{TddExempt: &exempt, Revision: got.Revision}, user); code(err) != items.CodeBadRequest {
		t.Fatalf("non-orchestrator: %v", err)
	}
}

// TestCreateTaskStoresResolvedWorkflowAndRoleHint is spec B3: CreateTx runs
// workflow.Resolve on the given spec and stores the resolved form (a
// template expands to its steps), and sets role_hint from the resolved
// spec's first run step.
func TestCreateTaskStoresResolvedWorkflowAndRoleHint(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	orch := items.Orchestrator("agt_1", e.ID)
	it, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "T",
		Workflow: &workflow.Spec{Template: "tdd-reviewed"}}, orch)
	if err != nil {
		t.Fatal(err)
	}
	if it.Workflow == nil || it.Workflow.Template != "" || len(it.Workflow.Steps) != 2 {
		t.Fatalf("workflow = %+v", it.Workflow)
	}
	if it.RoleHint != "coder" {
		t.Fatalf("role_hint = %q, want coder", it.RoleHint)
	}
	got, err := s.Get(ctx, it.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Workflow == nil || len(got.Workflow.Steps) != 2 || got.RoleHint != "coder" {
		t.Fatalf("get round trip = %+v", got)
	}
}

// TestCreateRejectsInvalidWorkflow is spec B2: an invalid workflow spec is
// refused with the workflow package's own validation copy.
func TestCreateRejectsInvalidWorkflow(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	orch := items.Orchestrator("agt_1", e.ID)
	_, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "T",
		Workflow: &workflow.Spec{Template: "no-such-template"}}, orch)
	want := `unknown template "no-such-template"`
	if err == nil || err.Error() != want || code(err) != items.CodeBadRequest {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestCreateRejectsStepsAndUnits is spec B3/C4: a task has either steps or
// units, never both, and at most 8 units.
func TestCreateRejectsStepsAndUnits(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	orch := items.Orchestrator("agt_1", e.ID)
	_, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "Both",
		Workflow: &workflow.Spec{Template: "tdd-reviewed"},
		Steps:    []string{"do the thing"},
		Units:    []items.Unit{{Title: "u1", Steps: []string{"s1"}}},
	}, orch)
	want := "Task Both has both steps and units; use one."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}

	units := make([]items.Unit, 9)
	for i := range units {
		units[i] = items.Unit{Title: fmt.Sprintf("u%d", i), Steps: []string{"s"}}
	}
	_, err = s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "TooMany",
		Workflow: &workflow.Spec{Template: "tdd-reviewed"}, Units: units}, orch)
	want2 := "Task TooMany has 9 units (max 8)."
	if err == nil || err.Error() != want2 {
		t.Fatalf("err = %v, want %q", err, want2)
	}
}

// TestOrchestratorTaskNeedsWorkflow is spec B3/locked decision 15: a task
// created by an orchestrator must carry a workflow.
func TestOrchestratorTaskNeedsWorkflow(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	orch := items.Orchestrator("agt_1", e.ID)
	_, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "NoFlow"}, orch)
	want := "Task NoFlow has no workflow. Plans assign every role: pick a template or write steps."
	if err == nil || err.Error() != want || code(err) != items.CodeBadRequest {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestBoardTaskMayOmitWorkflow is spec B3: only orchestrator-created tasks
// require a workflow; a task the user creates on the board is the legacy
// flow and may omit it.
func TestBoardTaskMayOmitWorkflow(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	it, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "Board task"}, user)
	if err != nil {
		t.Fatal(err)
	}
	if it.Workflow != nil {
		t.Fatalf("workflow = %+v, want nil", it.Workflow)
	}
}

// TestUserCannotSetWorkflow is spec B3's judgment call: only an orchestrator
// or the daemon (a plan materializing) may set workflow/steps/units/solo/verify.
func TestUserCannotSetWorkflow(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	_, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "T",
		Workflow: &workflow.Spec{Template: "tdd-reviewed"}}, user)
	want := "Only an orchestrator or a plan can set workflow, steps, units, solo or verify."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestUpdateSetsWorkflowAndKeepsItOnBoardEdits covers UpdateTx's workflow
// path: an orchestrator can set Workflow on an existing (legacy, daemon-
// created) task, the resolved spec and derived role_hint persist through
// the post-update getByID re-read (role_hint must be in the UPDATE's own
// SET clause, not just the in-memory merge), and a later board edit that
// doesn't touch workflow/steps/units/solo/verify must not trip the
// permission check just because the task already carries a workflow.
func TestUpdateSetsWorkflowAndKeepsItOnBoardEdits(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	task := mk(t, s, items.Task, st.Key, "T") // daemon-created, no workflow
	orch := items.Orchestrator("agt_1", e.ID)

	got, err := s.Update(ctx, task.Key,
		items.Patch{Workflow: &workflow.Spec{Template: "tdd-reviewed"}, Revision: task.Revision}, orch)
	if err != nil || got.Workflow == nil || len(got.Workflow.Steps) != 2 || got.RoleHint != "coder" {
		t.Fatalf("update = %+v, %v", got, err)
	}

	title := "Renamed"
	got2, err := s.Update(ctx, task.Key, items.Patch{Title: &title, Revision: got.Revision}, user)
	if err != nil || got2.Workflow == nil || got2.RoleHint != "coder" || got2.Title != "Renamed" {
		t.Fatalf("board edit = %+v, %v", got2, err)
	}
}

// TestOnlyTasksCanSetStepsUnitsSoloVerify is P7 fix round 1 finding #1: steps
// /units/solo/verify are task-only fields; a story or root may carry its own
// level of workflow (after_tasks / integration) but not these.
func TestOnlyTasksCanSetStepsUnitsSoloVerify(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	orch := items.Orchestrator("agt_1", e.ID)
	want := "Only tasks can set steps, units, solo or verify."

	if _, err := s.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: e.Key, Title: "S",
		Steps: []string{"do it"}}, orch); err == nil || err.Error() != want {
		t.Fatalf("steps on story: err = %v, want %q", err, want)
	}
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: e.Key, Title: "S2",
		Verify: []string{"go test ./..."}}, orch); err == nil || err.Error() != want {
		t.Fatalf("verify on story: err = %v, want %q", err, want)
	}

	story := mk(t, s, items.Story, e.Key, "S3")
	solo := "why"
	if _, err := s.Update(ctx, story.Key, items.Patch{Solo: &solo, Revision: story.Revision}, orch); err == nil || err.Error() != want {
		t.Fatalf("solo on story update: err = %v, want %q", err, want)
	}
}

// TestCreateRejectsMalformedUnits is P7 fix round 1 finding #2: a unit needs
// a title and at least one non-empty step.
func TestCreateRejectsMalformedUnits(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	orch := items.Orchestrator("agt_1", e.ID)
	want := "Unit 1 needs a title and at least one step."

	if _, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "NoTitle",
		Workflow: &workflow.Spec{Template: "tdd-reviewed"},
		Units:    []items.Unit{{Title: "", Steps: []string{"s1"}}}}, orch); err == nil || err.Error() != want {
		t.Fatalf("empty title: err = %v, want %q", err, want)
	}
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "NoSteps",
		Workflow: &workflow.Spec{Template: "tdd-reviewed"},
		Units:    []items.Unit{{Title: "u1", Steps: nil}}}, orch); err == nil || err.Error() != want {
		t.Fatalf("no steps: err = %v, want %q", err, want)
	}
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "EmptyStep",
		Workflow: &workflow.Spec{Template: "tdd-reviewed"},
		Units:    []items.Unit{{Title: "u1", Steps: []string{"  "}}}}, orch); err == nil || err.Error() != want {
		t.Fatalf("empty step string: err = %v, want %q", err, want)
	}

	want2 := "Unit 2 needs a title and at least one step."
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "SecondBad",
		Workflow: &workflow.Spec{Template: "tdd-reviewed"},
		Units:    []items.Unit{{Title: "ok", Steps: []string{"s"}}, {Title: "", Steps: []string{"s"}}}}, orch); err == nil || err.Error() != want2 {
		t.Fatalf("second unit: err = %v, want %q", err, want2)
	}
}

// TestUpdateWorkflowRefusedForUser is P7 fix round 1 finding #4a: the
// set-workflow-fields permission check also applies to UpdateTx, not just
// CreateTx.
func TestUpdateWorkflowRefusedForUser(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	task := mk(t, s, items.Task, st.Key, "T")
	_, err := s.Update(ctx, task.Key,
		items.Patch{Workflow: &workflow.Spec{Template: "tdd-reviewed"}, Revision: task.Revision}, user)
	want := "Only an orchestrator or a plan can set workflow, steps, units, solo or verify."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestCreateTddExemptWorkflowDropsTddGate is P7 fix round 1 finding #4b:
// resolving a template against a tdd_exempt task drops the tdd gate from the
// stored spec.
func TestCreateTddExemptWorkflowDropsTddGate(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	orch := items.Orchestrator("agt_1", e.ID)
	it, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "Exempt",
		TddExempt: "docs", Workflow: &workflow.Spec{Template: "tdd-reviewed"}}, orch)
	if err != nil {
		t.Fatal(err)
	}
	if it.Workflow == nil || len(it.Workflow.Steps) == 0 {
		t.Fatalf("workflow = %+v", it.Workflow)
	}
	for _, step := range it.Workflow.Steps {
		if slices.Contains(step.Gates, workflow.GateTDD) {
			t.Fatalf("tdd gate should be dropped when tdd_exempt: steps = %+v", it.Workflow.Steps)
		}
	}
}

func TestConcurrentUpdatesOneWins(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "Old")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			title := []string{"A", "B"}[i]
			_, errs[i] = s.Update(ctx, e.Key, items.Patch{Title: &title, Revision: e.Revision}, user)
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, err := range errs {
		switch code(err) {
		case "":
			ok++
		case items.CodeConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("errs = %v", errs)
	}
}

func TestGetComputesCounts(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	s1 := mk(t, s, items.Story, e.Key, "S1")
	mk(t, s, items.Story, e.Key, "S2")
	t1 := mk(t, s, items.Task, s1.Key, "T1")
	t2 := mk(t, s, items.Task, s1.Key, "T2")
	t3 := mk(t, s, items.Task, s1.Key, "T3")
	exec(t, s.DB, `UPDATE items SET status = 'done' WHERE id = ?`, t1.ID)
	exec(t, s.DB, `UPDATE items SET status = 'cancelled' WHERE id = ?`, t3.ID)
	exec(t, s.DB, `UPDATE items SET status = 'done' WHERE key = 'STORY-2'`)
	exec(t, s.DB, `INSERT INTO item_deps (item_id, blocked_by_id, created_at) VALUES (?, ?, 1), (?, ?, 1)`, t2.ID, t1.ID, t2.ID, t3.ID)
	b := mk(t, s, items.Bug, "", "B")
	exec(t, s.DB, `INSERT INTO item_deps (item_id, blocked_by_id, created_at) VALUES (?, ?, 1)`, t2.ID, b.ID)
	seedSession(t, s.DB, t2, "running")
	seedSession(t, s.DB, t2, "completed")
	seedSession(t, s.DB, s1, "pause_requested")
	seedRequest(t, s.DB, t2, "question", "open")
	seedRequest(t, s.DB, t2, "question", "answered")

	got := mustGet(t, s, t2.Key)
	if !slices.Equal(got.BlockedBy, []string{"BUG-1"}) || got.OpenRequests != 1 || got.ActiveAgents != 1 || got.Progress != nil {
		t.Errorf("task = %+v", got)
	}
	got = mustGet(t, s, s1.Key)
	if got.Progress == nil || *got.Progress != (items.Progress{Done: 1, Total: 2, Unit: "tasks"}) || got.ActiveAgents != 2 {
		t.Errorf("story = %+v %+v", got, got.Progress)
	}
	got = mustGet(t, s, e.Key)
	if got.Progress == nil || *got.Progress != (items.Progress{Done: 1, Total: 2, Unit: "stories"}) || got.ActiveAgents != 2 {
		t.Errorf("epic = %+v %+v", got, got.Progress)
	}
	if items.StatusLabel(items.AwaitingApproval) != "Awaiting approval" || items.StatusLabel(items.InProgress) != "In progress" {
		t.Error("labels")
	}
}

func TestCorruptListColumnIsAnError(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	exec(t, s.DB, `UPDATE items SET acceptance_json = 'not json' WHERE id = ?`, e.ID)
	if _, err := s.Get(ctx, e.Key); err == nil || !strings.Contains(err.Error(), "acceptance_json") {
		t.Fatalf("Get err = %v", err)
	}
	title := "New"
	if _, err := s.Update(ctx, e.Key, items.Patch{Title: &title, Revision: e.Revision}, user); err == nil {
		t.Fatal("Update succeeded on a corrupt row")
	}
	var stored string
	if err := s.DB.QueryRow(`SELECT acceptance_json FROM items WHERE id = ?`, e.ID).Scan(&stored); err != nil || stored != "not json" {
		t.Fatalf("stored = %q, %v", stored, err)
	}
	if _, err := s.Children(ctx, e.Key); err == nil {
		t.Fatal("Children succeeded on a corrupt parent row")
	}
}

func TestChoreItemLifecycleAndChildren(t *testing.T) {
	s := newStore(t)
	// Top-level chore
	ch, err := s.Create(ctx, items.CreateInput{Type: items.Chore, Title: "Upgrade dependencies"}, user)
	if err != nil {
		t.Fatalf("create chore: %v", err)
	}
	if !strings.HasPrefix(ch.Key, "CHORE-") {
		t.Fatalf("key = %s, want CHORE- prefix", ch.Key)
	}

	// Task under chore
	task, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: ch.Key, Title: "Update go.mod"}, user)
	if err != nil {
		t.Fatalf("create task under chore: %v", err)
	}
	if task.ParentKey != ch.Key {
		t.Fatalf("task parent = %s, want %s", task.ParentKey, ch.Key)
	}

	// Chore cannot have a parent
	_, err = s.Create(ctx, items.CreateInput{Type: items.Chore, ParentKey: ch.Key, Title: "Nested chore"}, user)
	if err == nil {
		t.Fatal("nested chore must be rejected")
	}
}

func TestCreateSpikeWithChoreIntent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	it, err := s.Create(ctx, items.CreateInput{
		Type:        items.Spike,
		Title:       "Maintenance chore",
		SpikeIntent: "chore",
	}, items.User("test"))
	if err != nil {
		t.Fatalf("expected chore spike creation to succeed, got: %v", err)
	}
	if it.SpikeIntent != "chore" {
		t.Fatalf("expected spike_intent 'chore', got: %q", it.SpikeIntent)
	}
}
