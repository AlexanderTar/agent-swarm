package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/items"
	"github.com/AlexanderTar/swarmrun/internal/workflow"
	"github.com/AlexanderTar/swarmrun/internal/worktree"
)

// BUG-77: a story review was started with a ro review tree that had already
// been removed, and its reviewer's brief named the missing path. Start must
// refuse a worktree that is no longer on disk.
func TestStartWorkflowRefusesRemovedWorktree(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	orch, _ := seedWorkflowTask(t, s, buildReviewSpec(t))
	setItemWorkflow(t, s, "STORY-1", workflow.Spec{AfterTasks: &workflow.Step{ID: "story-review", Review: []string{"reviewer"}}})
	wtID, repoID, dir, head := seedOwnedRepoWorktree(t, s, orch)
	rev, err := s.Worktree.Review(ctx, worktree.CreateInput{RepoID: repoID, RepoPath: dir,
		OwnerAgentID: orch.ID, RootItemID: orch.RootItemID}, head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Worktree.Remove(ctx, rev.ID, orch.ID); err != nil {
		t.Fatal(err)
	}

	_, err = s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: "STORY-1",
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "ro"}, {WorktreeID: rev.ID, Mode: "ro"}}})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest || !strings.Contains(ie.Message, rev.Path) {
		t.Fatalf("err = %v, want bad_request naming the removed %s", err, rev.Path)
	}
}

// BUG-77: a review run keeps its review_worktree_id across a retry. If the
// tree was reclaimed in between, the retried reviewer's brief must name a tree
// that exists (a fresh one at the same sha), not the removed path.
func TestReviewRetryNeverNamesRemovedWorktree(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coderSes := agentSessionForStep(t, s, st.ID, "build")
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head, Dirty: false}}}); err != nil {
		t.Fatal(err)
	}

	// The reviewer's run is queued again (a retry) after its tree was reclaimed.
	var runID, oldWT, oldAgent, oldPath string
	if err := s.DB.QueryRowContext(ctx, `SELECT r.id, r.review_worktree_id, r.agent_id, w.path FROM workflow_runs r
		JOIN worktrees w ON w.id = r.review_worktree_id
		WHERE r.workflow_id = ? AND r.step_id = 'review'`, st.ID).Scan(&runID, &oldWT, &oldAgent, &oldPath); err != nil {
		t.Fatal(err)
	}
	if err := s.Worktree.Release(ctx, oldWT, oldAgent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Worktree.Remove(ctx, oldWT, orch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); err == nil {
		t.Fatalf("setup: %s still on disk", oldPath)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'waiting', agent_id = NULL WHERE id = ?`, runID); err != nil {
		t.Fatal(err)
	}

	if err := s.fillWaitingRuns(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	var brief, newWT string
	if err := s.DB.QueryRowContext(ctx, `SELECT a.brief, r.review_worktree_id FROM workflow_runs r
		JOIN agents a ON a.id = r.agent_id WHERE r.id = ?`, runID).Scan(&brief, &newWT); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(brief, oldPath+" ") {
		t.Fatalf("brief names the removed worktree %s:\n%s", oldPath, brief)
	}
	var newPath string
	if err := s.DB.QueryRowContext(ctx, `SELECT path FROM worktrees WHERE id = ? AND state = 'active'`, newWT).Scan(&newPath); err != nil {
		t.Fatalf("run's review worktree %s is not an active worktree: %v", newWT, err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("review worktree %s missing on disk: %v", newPath, err)
	}
	if !strings.Contains(brief, newPath+" ") {
		t.Fatalf("brief does not name the live review worktree %s:\n%s", newPath, brief)
	}
}

// BUG-77: a story review's trees are named from the workflow's own worktree
// list. If one is gone by spawn time the run fails with a clear, fatal error
// (escalating to the orchestrator) instead of erroring the advance forever or
// briefing the dead path.
func TestStoryReviewSpawnFailsRunOnRemovedWorktree(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	orch, _ := seedWorkflowTask(t, s, buildReviewSpec(t))
	setItemWorkflow(t, s, "STORY-1", workflow.Spec{AfterTasks: &workflow.Step{ID: "story-review", Review: []string{"reviewer"}}})
	wtID, repoID, dir, head := seedOwnedRepoWorktree(t, s, orch)
	rev, err := s.Worktree.Review(ctx, worktree.CreateInput{RepoID: repoID, RepoPath: dir,
		OwnerAgentID: orch.ID, RootItemID: orch.RootItemID}, head)
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: "STORY-1",
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "ro"}, {WorktreeID: rev.ID, Mode: "ro"}}})
	if err != nil {
		t.Fatal(err)
	}
	// Back to queued, as after a crash, then the tree is reclaimed.
	if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'waiting', agent_id = NULL WHERE workflow_id = ?`, st.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Worktree.Remove(ctx, rev.ID, orch.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.fillWaitingRuns(ctx, st.ID); err != nil {
		t.Fatalf("fillWaitingRuns = %v, want the run failed in place, not an advance error", err)
	}
	var state, runErr string
	var fatal bool
	if err := s.DB.QueryRowContext(ctx, `SELECT state, COALESCE(error, ''), error_fatal FROM workflow_runs WHERE workflow_id = ?`,
		st.ID).Scan(&state, &runErr, &fatal); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || !fatal || !strings.Contains(runErr, "is gone") || !strings.Contains(runErr, rev.Path) {
		t.Fatalf("run = state %q fatal %v error %q, want failed, fatal, naming the gone %s", state, fatal, runErr, rev.Path)
	}
}
