package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/workflow"
	"github.com/AlexanderTar/swarmrun/internal/worktree"
)

// TASK-1492 unit 1: a detached review tree must render its head sha after
// '@' with a detached marker, never an empty branch and never its own head
// as the base.
func TestReviewBriefLineDetachedHonest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _ := seedWorkflowTask(t, s, buildReviewSpec(t))
	_, repoID, dir, head := seedOwnedRepoWorktree(t, s, orch)
	rev, err := s.Worktree.Review(ctx, worktree.CreateInput{RepoID: repoID, RepoPath: dir,
		OwnerAgentID: orch.ID, RootItemID: orch.RootItemID}, head)
	if err != nil {
		t.Fatal(err)
	}
	wts, err := s.BriefWorktrees(ctx, []WorkflowWorktree{{WorktreeID: rev.ID, Mode: "ro"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := RenderBrief(BriefInput{Key: "TASK-1", Title: "t", Name: "n", Role: RoleReviewer,
		RootKey: "TASK-1", Objective: "do it", Worktrees: wts})
	if err != nil {
		t.Fatal(err)
	}
	head7 := workflow.SHA7(head)
	t.Logf("rendered brief:\n%s", got)
	if !strings.Contains(got, "@ "+head7+" (detached, ro)") {
		t.Errorf("brief has no honest detached line '@ %s (detached, ro)':\n%s", head7, got)
	}
	if strings.Contains(got, "@  (") {
		t.Errorf("brief prints an empty '@  (' line:\n%s", got)
	}
	if strings.Contains(got, "base "+head7) {
		t.Errorf("brief prints the review head %s as its own base:\n%s", head7, got)
	}
}

// TASK-1492 unit 2: the workflow review step's brief shows the reviewed head
// after '@' and, as base, the base_sha of the workflow's rw worktree for the
// same repo -- not the reviewed head.
func TestReviewBriefLineCarriesCoderBase(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, repoID, dir, base := seedOwnedRepoWorktree(t, s, orch)
	commitFile(t, dir, "b.txt", "more")
	head := strings.TrimSpace(gitOutput(t, dir, "rev-parse", "HEAD"))
	if _, err := s.DB.ExecContext(ctx, `UPDATE worktrees SET base_sha = ?, base_ref = 'main' WHERE id = ?`,
		base, wtID); err != nil {
		t.Fatal(err)
	}
	rev, err := s.Worktree.Review(ctx, worktree.CreateInput{RepoID: repoID, RepoPath: dir,
		OwnerAgentID: orch.ID, RootItemID: orch.RootItemID}, head)
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	wtJSON, _ := json.Marshal([]WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}})
	wf := wfRow{OwnerAgentID: orch.ID, WorktreesJSON: string(wtJSON), ContextJSON: "[]"}
	brief, _, err := s.stepBrief(ctx, wf, it, wfRunRow{StepID: "review", Role: "reviewer", Round: 1,
		ReviewWorktreeID: rev.ID})
	if err != nil {
		t.Fatal(err)
	}
	name, err := defaultName(RoleReviewer, it.Title)
	if err != nil {
		t.Fatal(err)
	}
	brief.Key, brief.Title, brief.Name, brief.Role = it.Key, it.Title, name, RoleReviewer
	brief.RootKey, brief.ParentName = it.RootKey, orch.Name
	got, err := RenderBrief(brief)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rendered brief:\n%s", got)
	head7, base7 := workflow.SHA7(head), workflow.SHA7(base)
	if !strings.Contains(got, "@ "+head7+" (detached, base "+base7+", ro)") {
		t.Errorf("brief has no '@ %s (detached, base %s, ro)' line:\n%s", head7, base7, got)
	}
	if strings.Contains(got, "@  (") {
		t.Errorf("brief prints an empty '@  (' line:\n%s", got)
	}
	if strings.Contains(got, "base "+head7) {
		t.Errorf("brief prints the review head %s as its own base:\n%s", head7, got)
	}
}
