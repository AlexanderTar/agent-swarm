package runtime

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// startedWorkflow seeds a build/review task, starts its workflow on an owned
// rw worktree and returns everything the refusal tests below poke at.
func startedWorkflow(t *testing.T, s *Store, spec workflow.Spec) (orch Agent, taskKey, wtID string, st WorkflowState, head string) {
	t.Helper()
	orch, taskKey = seedWorkflowTask(t, s, spec)
	wtID, _, _, head = seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(context.Background(), orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	return orch, taskKey, wtID, st, head
}

func TestStartWorkflowRefusesForeignAndUnknownItems(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
	ep2 := seedEpicWithTask(t, s)
	foreign, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep2.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	in := StartWorkflowInput{ItemKey: taskKey, Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}}
	if _, err := s.StartWorkflow(ctx, foreign, in); err == nil || err.Error() != taskKey+" is outside your assignment." {
		t.Fatalf("foreign start err = %v", err)
	}
	if _, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: "TASK-999"}); err == nil {
		t.Fatal("start on an unknown item succeeded")
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("workflows rows = %d err=%v, want none after refused starts", n, err)
	}
}

func TestStartWorkflowStoryNeedsOwnedWorktree(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _ := seedWorkflowTask(t, s, buildReviewSpec(t))
	setItemWorkflow(t, s, "STORY-1", workflow.Spec{AfterTasks: &workflow.Step{ID: "story-review", Review: []string{"reviewer"}}})

	// A foreign orchestrator's worktree doesn't count for this story.
	ep2 := seedEpicWithTask(t, s)
	other, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep2.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	foreignWT, _, _, _ := seedOwnedRepoWorktree(t, s, other)
	_, err = s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: "STORY-1",
		Worktrees: []WorkflowWorktree{{WorktreeID: foreignWT, Mode: "ro"}}})
	if err == nil || err.Error() != "Start needs a worktree you own." {
		t.Fatalf("err = %v, want \"Start needs a worktree you own.\"", err)
	}
}

func TestResumeAndCancelRefuseWithoutLiveWorkflow(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))

	want := taskKey + "'s workflow isn't waiting on you (state: none)."
	if _, err := s.ResumeWorkflow(ctx, orch, taskKey, "retry", "", "", ""); err == nil || err.Error() != want {
		t.Fatalf("resume err = %v, want %q", err, want)
	}
	if _, err := s.CancelWorkflow(ctx, orch, taskKey, "", ""); err == nil || err.Error() != want {
		t.Fatalf("cancel err = %v, want %q", err, want)
	}
	if _, ok, err := s.WorkflowFor(ctx, taskKey); err != nil || ok {
		t.Fatalf("WorkflowFor = ok %v err %v, want no workflow", ok, err)
	}
	if _, _, err := s.WorkflowFor(ctx, "TASK-999"); err == nil {
		t.Fatal("WorkflowFor on an unknown item succeeded")
	}
}

func TestResumeWorkflowRejectsUnknownDecision(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey, _, st, head := startedWorkflow(t, s, gatedBuildReviewSpec(t, 1))
	escalateViaRoundsExhausted(t, s, orch, taskKey, st.ID, head)

	_, err := s.ResumeWorkflow(ctx, orch, taskKey, "shrug", "", "", "")
	if err == nil || err.Error() != `decision must be "retry", "accept" or "fail".` {
		t.Fatalf("err = %v", err)
	}
	got, _, err := s.workflowStateByID(ctx, st.ID)
	if err != nil || got.State != "escalated" {
		t.Fatalf("state = %q err=%v, want still escalated after a refused decision", got.State, err)
	}
}

func TestCancelWorkflowFromEscalatedThenRefusesSecondCancel(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey, _, st, head := startedWorkflow(t, s, gatedBuildReviewSpec(t, 1))
	escalateViaRoundsExhausted(t, s, orch, taskKey, st.ID, head)

	final, err := s.CancelWorkflow(ctx, orch, taskKey, "", "")
	if err != nil || final.State != "cancelled" {
		t.Fatalf("cancel = %+v err=%v, want cancelled", final, err)
	}
	it, err := s.Items.Get(ctx, taskKey)
	if err != nil || it.Status != items.Ready {
		t.Fatalf("task = %v err=%v, want ready", it.Status, err)
	}
	_, err = s.CancelWorkflow(ctx, orch, taskKey, "", "")
	if err == nil || !strings.Contains(err.Error(), "state: cancelled") {
		t.Fatalf("second cancel err = %v, want a state: cancelled refusal", err)
	}
	// A resume of a cancelled workflow is refused the same way.
	_, err = s.ResumeWorkflow(ctx, orch, taskKey, "accept", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "state: cancelled") {
		t.Fatalf("resume err = %v, want a state: cancelled refusal", err)
	}
}

func TestStepForAgentFormatsRound(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey, _, st, head := startedWorkflow(t, s, gatedBuildReviewSpec(t, 2))
	coderID := agentIDForStep(t, s, st.ID, "build")

	step, ok, err := s.StepForAgent(ctx, coderID)
	if err != nil || !ok || step != "build" {
		t.Fatalf("round 1 = %q ok=%v err=%v, want \"build\"", step, ok, err)
	}
	if _, ok, err := s.StepForAgent(ctx, "agt_nobody"); err != nil || ok {
		t.Fatalf("unknown agent = ok %v err %v, want not found", ok, err)
	}

	// Drive one fix round: reviewer asks for changes, the same builder agent
	// gets a round 2 run.
	coderSes := agentSessionForStep(t, s, st.ID, "build")
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head}}}); err != nil {
		t.Fatal(err)
	}
	revSes := agentSessionForStep(t, s, st.ID, "review")
	if _, err := s.WriteCheckpoint(ctx, revSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "no",
		Verdict:  "changes_requested",
		Findings: []workflow.Finding{{Severity: "major", File: "a.go", Summary: "bug"}}}); err != nil {
		t.Fatal(err)
	}
	step, ok, err = s.StepForAgent(ctx, coderID)
	if err != nil || !ok || step != "build r2" {
		t.Fatalf("round 2 = %q ok=%v err=%v, want \"build r2\"", step, ok, err)
	}
	_ = orch
	_ = taskKey
}

func TestAppendWorkflowContextAndDeliverNote(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, _, st, _ := startedWorkflow(t, s, buildReviewSpec(t))
	coderID := agentIDForStep(t, s, st.ID, "build")

	if err := s.appendWorkflowContext(ctx, st.ID, "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.appendWorkflowContext(ctx, st.ID, "second"); err != nil {
		t.Fatal(err)
	}
	row, ok, err := s.workflowRowByID(ctx, st.ID)
	if err != nil || !ok {
		t.Fatalf("row ok=%v err=%v", ok, err)
	}
	if got := row.contextLines(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("context = %v, want [first second]", got)
	}
	// An unknown workflow is a no-op, not an error.
	if err := s.appendWorkflowContext(ctx, "wf_missing", "x"); err != nil {
		t.Fatalf("append on a missing workflow = %v, want nil", err)
	}
	if err := s.appendWorkflowContext(canceledCtx(), st.ID, "x"); err == nil {
		t.Fatal("append with a canceled ctx succeeded")
	}

	if err := s.deliverNote(ctx, coderID, "mind the edge case"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ?
		AND kind = 'assignment_update' AND payload_json LIKE '%mind the edge case%'`, coderID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("delivered notes = %d err=%v, want 1", n, err)
	}
	if err := s.deliverNote(ctx, "agt_nobody", "x"); err == nil {
		t.Fatal("deliverNote to an unknown agent succeeded")
	}
}

func TestBriefWorktreesResolvesRepoAndMode(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _ := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)

	got, err := s.BriefWorktrees(ctx, []WorkflowWorktree{{WorktreeID: wtID, Mode: "ro"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo != "proj" || got[0].Mode != "ro" || got[0].Path == "" {
		t.Fatalf("brief worktrees = %+v, want one proj/ro entry with a path", got)
	}
	if _, err := s.BriefWorktrees(ctx, []WorkflowWorktree{{WorktreeID: "wt_missing", Mode: "rw"}}); err == nil {
		t.Fatal("unknown worktree resolved")
	}
}

func TestWorkflowValidationHelpersPropagateDBErrors(t *testing.T) {
	s, _, _ := newStore(t)
	orch, taskKey, wtID, st, _ := startedWorkflow(t, s, buildReviewSpec(t))
	bad := canceledCtx()
	wts := []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}

	if err := s.validateWorktrees(bad, wts); err == nil {
		t.Error("validateWorktrees swallowed a DB error")
	}
	if _, err := s.callerOwnsRWWorktree(bad, wts, orch.ID); err == nil {
		t.Error("callerOwnsRWWorktree swallowed a DB error")
	}
	if _, err := s.callerOwnsWorktree(bad, wts, orch.ID); err == nil {
		t.Error("callerOwnsWorktree swallowed a DB error")
	}
	// ro entries are skipped by the rw check; an unknown id never counts as owned.
	if ok, err := s.callerOwnsRWWorktree(context.Background(), []WorkflowWorktree{{WorktreeID: wtID, Mode: "ro"}}, orch.ID); err != nil || ok {
		t.Errorf("ro entry counted as rw ownership: ok=%v err=%v", ok, err)
	}
	if ok, err := s.callerOwnsWorktree(context.Background(), []WorkflowWorktree{{WorktreeID: "wt_missing"}}, orch.ID); err != nil || ok {
		t.Errorf("unknown worktree counted as owned: ok=%v err=%v", ok, err)
	}
	if _, _, err := s.workflowStateByID(bad, st.ID); err == nil {
		t.Error("workflowStateByID swallowed a DB error")
	}
	if _, ok, err := s.workflowStateByID(context.Background(), "wf_missing"); err != nil || ok {
		t.Errorf("workflowStateByID(missing) = ok %v err %v, want not found", ok, err)
	}
	if _, err := s.loadWorkflowRuns(bad, st.ID); err == nil {
		t.Error("loadWorkflowRuns swallowed a DB error")
	}
	if _, _, err := s.StepForAgent(bad, "agt_x"); err == nil {
		t.Error("StepForAgent swallowed a DB error")
	}
	if _, err := s.StartWorkflow(bad, orch, StartWorkflowInput{ItemKey: taskKey}); err == nil {
		t.Error("StartWorkflow swallowed a DB error")
	}
	if err := s.advanceWaitingForOwner(bad, orch.ID); err == nil {
		t.Error("advanceWaitingForOwner swallowed a DB error")
	}
	if err := s.recoverWorkflows(bad); err == nil {
		t.Error("recoverWorkflows swallowed a DB error")
	}
}

func TestEngineSpecHelpersRefuseMissingPieces(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey, _, st, _ := startedWorkflow(t, s, buildReviewSpec(t))
	it, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.engineSpec(ctx, items.Item{Key: "TASK-X"}, orch.ID); err == nil || !strings.Contains(err.Error(), "has no workflow") {
		t.Errorf("engineSpec without a workflow err = %v", err)
	}
	if _, err := s.engineSpec(ctx, it, "agt_missing"); err == nil {
		t.Error("engineSpec with an unknown owner succeeded")
	}
	if _, err := s.runSpec(ctx, it, workflowRun{WorkflowID: "wf_missing"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("runSpec on a missing workflow err = %v", err)
	}
	spec, err := s.runSpec(ctx, it, workflowRun{WorkflowID: st.ID})
	if err != nil || len(spec.Steps) != 2 {
		t.Errorf("runSpec = %+v err=%v, want the two-step spec", spec, err)
	}
	// No integration block -> no final-review roles; with one, the owner's clamped list.
	if roles, err := s.finalReviewRoles(ctx, it, orch.ID); err != nil || roles != nil {
		t.Errorf("finalReviewRoles without integration = %v err=%v", roles, err)
	}
	withIntegration := it
	wf := *it.Workflow
	wf.Integration = &workflow.Integration{FinalReview: []string{"reviewer"}}
	withIntegration.Workflow = &wf
	if roles, err := s.finalReviewRoles(ctx, withIntegration, orch.ID); err != nil || len(roles) != 1 || roles[0] != "reviewer" {
		t.Errorf("finalReviewRoles = %v err=%v, want [reviewer]", roles, err)
	}
	if _, err := s.finalReviewRoles(ctx, withIntegration, "agt_missing"); err == nil {
		t.Error("finalReviewRoles with an unknown owner succeeded")
	}
}

func TestAdvanceRefusesBrokenWorkflowState(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, taskKey, _, st, _ := startedWorkflow(t, s, buildReviewSpec(t))

	// Unknown workflow ids are a quiet no-op; a dead ctx surfaces the DB error.
	if err := s.advance(ctx, "wf_missing"); err != nil {
		t.Fatalf("advance on a missing workflow = %v, want nil", err)
	}
	if err := s.advance(canceledCtx(), st.ID); err == nil {
		t.Fatal("advance with a canceled ctx succeeded")
	}

	// A task that lost its workflow_json mid-flight can't be advanced.
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET workflow_json = NULL WHERE key = ?`, taskKey); err != nil {
		t.Fatal(err)
	}
	if err := s.advance(ctx, st.ID); err == nil || !strings.Contains(err.Error(), "lost its workflow_json") {
		t.Fatalf("advance err = %v, want a lost workflow_json refusal", err)
	}
}

func TestApplySucceedAndEscalateAreNoOpsOnceWorkflowResolved(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey, _, st, _ := startedWorkflow(t, s, buildReviewSpec(t))
	if _, err := s.CancelWorkflow(ctx, orch, taskKey, "", ""); err != nil {
		t.Fatal(err)
	}
	wf, ok, err := s.workflowRowByID(ctx, st.ID)
	if err != nil || !ok || wf.State != "cancelled" {
		t.Fatalf("setup: wf = %+v ok=%v err=%v", wf, ok, err)
	}
	it, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	before := len(relayEvents(t, s, orch.ID))

	if err := s.applySucceed(ctx, wf, it, workflow.Action{Kind: workflow.ActionSucceed}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.applyEscalate(ctx, wf, it, workflow.Action{Kind: workflow.ActionEscalate, Reason: "late"}, nil); err != nil {
		t.Fatal(err)
	}
	if after := relayEvents(t, s, orch.ID); len(after) != before {
		t.Fatalf("relays grew from %d to %d (%v); a resolved workflow must not relay again", before, len(after), after)
	}
	got, _, err := s.workflowStateByID(ctx, st.ID)
	if err != nil || got.State != "cancelled" {
		t.Fatalf("state = %q err=%v, want cancelled to stick", got.State, err)
	}
}

func TestHealStrandedActiveRunIgnoresPreStartCompletedSession(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, _, st, _ := startedWorkflow(t, s, buildReviewSpec(t))
	runs, err := s.loadWorkflowRuns(ctx, st.ID)
	if err != nil || len(runs) != 1 || runs[0].State != "active" {
		t.Fatalf("setup: runs = %+v err=%v", runs, err)
	}
	// A run whose agent has no session row at all is a race, not a stranding.
	orphan := runs[0]
	orphan.AgentID = "agt_without_sessions"
	healed, err := s.healStrandedActiveRuns(ctx, []wfRunRow{orphan})
	if err != nil || healed[0].State != "active" {
		t.Fatalf("orphan run = %+v err=%v, want left active", healed, err)
	}
	if _, err := s.healStrandedActiveRuns(canceledCtx(), runs); err == nil {
		t.Fatal("heal with a canceled ctx succeeded")
	}
}

func TestStepBriefCarriesDesignArtifactsAndRefusesUnknownStep(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey, _, st, _ := startedWorkflow(t, s, buildReviewSpec(t))
	it, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct{ kind, path string }{
		{"research", "docs/research/zed.md"}, {"design", "docs/design/alpha.md"}, {"spec", "docs/specs/ignored.md"}} {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
			VALUES (?, ?, ?, ?, 1, ?, 1)`, ids.New("art"), it.ID, a.kind, a.path, orch.ID); err != nil {
			t.Fatal(err)
		}
	}

	lines, err := s.artifactContextLines(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, ",") != "docs/design/alpha.md,docs/research/zed.md" {
		t.Fatalf("artifact lines = %v, want design+research paths sorted, no spec", lines)
	}
	if _, err := s.artifactContextLines(canceledCtx(), it.ID); err == nil {
		t.Fatal("artifactContextLines swallowed a DB error")
	}

	wf, _, err := s.workflowRowByID(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	brief, shared, err := s.stepBrief(ctx, wf, it, wfRunRow{StepID: "build", Role: "coder", Round: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(shared) != 1 || len(brief.Worktrees) != 1 || brief.Worktrees[0].Mode != "rw" {
		t.Fatalf("build brief shares %v / worktrees %+v, want the one rw worktree", shared, brief.Worktrees)
	}
	if !slices.Contains(brief.Context, "docs/design/alpha.md") {
		t.Fatalf("build brief context = %v, want the design artifact path", brief.Context)
	}
	if _, _, err := s.stepBrief(ctx, wf, it, wfRunRow{StepID: "nope", Role: "coder", Round: 1}); err == nil ||
		!strings.Contains(err.Error(), `step "nope" not found`) {
		t.Fatalf("unknown step err = %v", err)
	}
	if _, err := s.spawnRunAgent(ctx, wf, it, wfRunRow{StepID: "nope", Role: "coder", Round: 1}); err == nil {
		t.Fatal("spawnRunAgent on an unknown step succeeded")
	}
	if err := s.applySpawn(ctx, wf, it, workflow.Action{Kind: workflow.ActionSpawn, StepID: "nope", Round: 1}); err == nil {
		t.Fatal("applySpawn on an unknown step succeeded")
	}
	if err := s.fillWaitingRuns(ctx, "wf_missing"); err != nil {
		t.Fatalf("fillWaitingRuns on a missing workflow = %v, want nil", err)
	}
}

func TestApplyAutoRetryGuardsAndAgentlessRequeue(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, _, st, _ := startedWorkflow(t, s, buildReviewSpec(t))
	wf, _, err := s.workflowRowByID(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.loadWorkflowRuns(ctx, st.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("setup runs = %+v err=%v", runs, err)
	}
	build := runs[0]
	act := workflow.Action{Kind: workflow.ActionAutoRetry, Run: &workflow.Run{StepID: "build", Round: 1, Role: "coder"}}

	// No run in the action, an unknown run, or a run that isn't failed: nothing happens.
	if err := s.applyAutoRetry(ctx, wf, workflow.Action{Kind: workflow.ActionAutoRetry}, runs); err != nil {
		t.Fatal(err)
	}
	other := workflow.Action{Run: &workflow.Run{StepID: "ghost", Round: 1, Role: "coder"}}
	if err := s.applyAutoRetry(ctx, wf, other, runs); err != nil {
		t.Fatal(err)
	}
	if err := s.applyAutoRetry(ctx, wf, act, runs); err != nil { // build is still 'active'
		t.Fatal(err)
	}
	var retries int
	var state string
	read := func() {
		t.Helper()
		if err := s.DB.QueryRowContext(ctx, `SELECT state, auto_retries FROM workflow_runs WHERE id = ?`, build.ID).
			Scan(&state, &retries); err != nil {
			t.Fatal(err)
		}
	}
	if read(); state != "active" || retries != 0 {
		t.Fatalf("guarded calls changed the run: state=%s retries=%d", state, retries)
	}

	// A failed run that never got an agent goes back to waiting with the budget spent.
	if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'failed', agent_id = NULL, error = 'boom' WHERE id = ?`, build.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := s.loadWorkflowRuns(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.applyAutoRetry(ctx, wf, act, failed); err != nil {
		t.Fatal(err)
	}
	if read(); state != "waiting" || retries != 1 {
		t.Fatalf("agentless failed run: state=%s retries=%d, want waiting/1", state, retries)
	}

	// A failed run whose agent row is gone is a hard error, not a silent skip.
	if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'failed' WHERE id = ?`, build.ID); err != nil {
		t.Fatal(err)
	}
	ghost := failed
	ghost[0].AgentID = "agt_gone"
	if err := s.applyAutoRetry(ctx, wf, act, ghost); err == nil {
		t.Fatal("applyAutoRetry with a missing agent succeeded")
	}
}

func TestRetryFixHelpers(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, _, st, _ := startedWorkflow(t, s, buildReviewSpec(t))
	wf, _, err := s.workflowRowByID(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	coderID := agentIDForStep(t, s, st.ID, "build")

	if id, err := s.runAgentIDAt(ctx, st.ID, "build", 1); err != nil || id != coderID {
		t.Fatalf("runAgentIDAt = %q err=%v, want %q", id, err, coderID)
	}
	if id, err := s.runAgentIDAt(ctx, st.ID, "build", 7); err != nil || id != "" {
		t.Fatalf("runAgentIDAt round 7 = %q err=%v, want empty", id, err)
	}
	if _, err := s.runAgentIDAt(canceledCtx(), st.ID, "build", 1); err == nil {
		t.Fatal("runAgentIDAt swallowed a DB error")
	}
	// Nothing recorded for the review step yet: removing its worktrees is a no-op.
	if err := s.removeReviewWorktrees(ctx, wf, "review", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.removeReviewWorktrees(canceledCtx(), wf, "review", 1); err == nil {
		t.Fatal("removeReviewWorktrees swallowed a DB error")
	}
	if err := s.releaseAndRemove(ctx, wf, nil); err != nil {
		t.Fatal(err)
	}

	coder, err := s.agentByID(ctx, coderID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.closeSessionForRetry(canceledCtx(), coder); err == nil {
		t.Fatal("closeSessionForRetry swallowed a DB error")
	}
	if err := s.closeSessionForRetry(ctx, coder); err != nil { // kills the live pane, marks the session done
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, coderID)
	if err != nil || ses.State != Completed {
		t.Fatalf("session = %v err=%v, want completed after closeSessionForRetry", ses.State, err)
	}
	if err := s.closeSessionForRetry(ctx, coder); err != nil { // already terminal: no-op
		t.Fatalf("second close = %v, want nil", err)
	}
}

func TestRenderFindingsAndLastFindings(t *testing.T) {
	if got := renderFindings(nil); !strings.Contains(got, "Changes requested") {
		t.Fatalf("empty findings = %q", got)
	}
	got := renderFindings([]workflow.Finding{
		{Reviewer: "reviewer", Severity: "major", File: "a.go", Line: 12, Summary: "off by one"},
		{Reviewer: "reviewer", Severity: "nit", File: "b.go", Summary: "naming"},
		{Reviewer: "ui_reviewer", Severity: "minor", File: "c.swift", Line: 3, Summary: "clipped"},
	})
	want := "reviewer:\n[major] a.go:12 off by one\n[nit] b.go naming\n\nui_reviewer:\n[minor] c.swift:3 clipped"
	if got != want {
		t.Fatalf("rendered =\n%q\nwant\n%q", got, want)
	}

	runs := []wfRunRow{
		{Round: 1, FindingsJSON: `[{"severity":"major","summary":"old"}]`},
		{Round: 2, FindingsJSON: `[{"severity":"minor","summary":"new"}]`},
		{Round: 2, FindingsJSON: `[]`},
	}
	if lf := lastFindings(runs); len(lf) != 1 || lf[0].Summary != "new" {
		t.Fatalf("lastFindings = %+v, want only the round-2 finding", lf)
	}
	views := runViews([]wfRunRow{{StepID: "build", Round: 1, Role: "coder", AgentID: "a", State: "done", Verdict: "pass"},
		{StepID: "review", Round: 1, Role: "reviewer", State: "done"}})
	if views[0]["verdict"] != "pass" || len(views[1]) != 5 {
		t.Fatalf("runViews = %v, want verdict only where set", views)
	}
}

func TestOnStoryReadyForReviewRelayRules(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	story, err := s.Items.Get(ctx, "STORY-1")
	if err != nil {
		t.Fatal(err)
	}
	fire := func() {
		t.Helper()
		if err := s.tx(ctx, func(tx *sql.Tx) error { return s.OnStoryReadyForReview(ctx, tx, story) }); err != nil {
			t.Fatal(err)
		}
	}
	relays := func(agentID string) int {
		return countEvent(relayEvents(t, s, agentID), "story_ready_for_review")
	}

	// No orchestrator for the root yet: nothing to relay to, and no error.
	fire()
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("relays with no orchestrator = %d err=%v, want 0", n, err)
	}

	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	// A story whose workflow already ran (or is running) is not re-announced.
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, round, extra_rounds,
		context_json, worktrees_json, created_at, updated_at) VALUES ('wf_x', ?, ?, ?, 'running', 1, 0, '[]', '[]', 1, 1)`,
		story.ID, story.RootID, orch.ID); err != nil {
		t.Fatal(err)
	}
	fire()
	if got := relays(orch.ID); got != 0 {
		t.Fatalf("relays while a workflow runs = %d, want 0", got)
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM workflows WHERE id = 'wf_x'`); err != nil {
		t.Fatal(err)
	}

	// An ended orchestrator still receives it (the inbox outlives the session),
	// and a repeat fire is deduplicated.
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, orch.ID); err != nil {
		t.Fatal(err)
	}
	fire()
	fire()
	if got := relays(orch.ID); got != 1 {
		t.Fatalf("relays to the finished orchestrator = %d, want exactly 1", got)
	}
}
