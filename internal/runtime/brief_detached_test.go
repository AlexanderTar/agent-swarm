package runtime

import (
	"context"
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
