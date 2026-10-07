package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// seedReviewTree creates a detached review worktree owned by a live root
// orchestrator that has a live descendant, plus one reviewer reservation.
func seedReviewTree(t *testing.T, s *Store, owner string, reviewers ...string) (worktree.Worktree, string) {
	t.Helper()
	ep := seedEpicWithTask(t, s)
	repoID := seedRepo(t, s, "proj")
	repoPath := repoPathFor(t, s, repoID)
	seedReclaimAgent(t, s, owner, ep.ID, "")
	seedReclaimSession(t, s, "ses_"+owner, owner, Running)
	seedReclaimAgent(t, s, owner+"_kid", ep.ID, owner) // live descendant
	sha := strings.TrimSpace(gitOutput(t, repoPath, "rev-parse", "HEAD"))
	wt, err := s.Worktree.Review(context.Background(), worktree.CreateInput{RepoID: repoID, RepoPath: repoPath,
		OwnerAgentID: owner, RootItemID: ep.ID}, sha)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reviewers {
		seedReclaimAgent(t, s, r, ep.ID, owner)
		mustExec(t, s.DB, `INSERT INTO worktree_reservations (worktree_id, agent_id, mode, created_at) VALUES (?, ?, 'ro', ?)`,
			wt.ID, r, db.Millis(s.Now()))
	}
	return wt, ep.ID
}

func TestReclaimRemovesReviewTreeOnceReviewersFinishedWithLiveOwner(t *testing.T) {
	s, _, at := clockStore(t)
	wt, _ := seedReviewTree(t, s, "root_o", "rev_1")
	finishReclaimAgent(t, s, "rev_1", s.Now())
	at.Advance(2 * time.Hour)
	if err := s.ReclaimWorktrees(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _ := reclaimWorktreeState(t, s, wt.ID); state != "removed" {
		t.Fatalf("state = %s, want removed", state)
	}
}

func TestReclaimKeepsReviewTreeWithLiveReviewer(t *testing.T) {
	s, _, at := clockStore(t)
	wt, _ := seedReviewTree(t, s, "root_o", "rev_1")
	seedReclaimSession(t, s, "ses_rev", "rev_1", Running)
	at.Advance(2 * time.Hour)
	if err := s.ReclaimWorktrees(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _ := reclaimWorktreeState(t, s, wt.ID); state != "active" {
		t.Fatalf("state = %s, want active", state)
	}
}

func TestReclaimKeepsReviewTreeWithinGraceOfReviewerFinish(t *testing.T) {
	s, _, at := clockStore(t)
	wt, _ := seedReviewTree(t, s, "root_o", "rev_1")
	at.Advance(2 * time.Hour)
	finishReclaimAgent(t, s, "rev_1", s.Now().Add(-10*time.Minute))
	if err := s.ReclaimWorktrees(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _ := reclaimWorktreeState(t, s, wt.ID); state != "active" {
		t.Fatalf("state = %s, want active", state)
	}
}

func TestReclaimKeepsFreshReviewTreeWithNoReservation(t *testing.T) {
	s, _, at := clockStore(t)
	wt, _ := seedReviewTree(t, s, "root_o")
	at.Advance(10 * time.Minute)
	if err := s.ReclaimWorktrees(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _ := reclaimWorktreeState(t, s, wt.ID); state != "active" {
		t.Fatalf("state = %s, want active", state)
	}
	// Control: an old tree nobody ever reserved is reclaimed.
	at.Advance(2 * time.Hour)
	if err := s.ReclaimWorktrees(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _ := reclaimWorktreeState(t, s, wt.ID); state != "removed" {
		t.Fatalf("state = %s, want removed once past grace", state)
	}
}

func TestReclaimKeepsReviewTreeReferencedByANonTerminalRun(t *testing.T) {
	s, _, at := clockStore(t)
	wt, root := seedReviewTree(t, s, "root_o", "rev_1")
	finishReclaimAgent(t, s, "rev_1", s.Now())
	mustExec(t, s.DB, `INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, worktrees_json, created_at, updated_at)
		VALUES ('wf_x', ?, ?, 'root_o', 'running', '[]', 1, 1)`, root, root)
	mustExec(t, s.DB, `INSERT INTO workflow_runs (id, workflow_id, step_id, round, role, state, review_worktree_id, created_at)
		VALUES ('run_x', 'wf_x', 'review', 1, 'reviewer', 'waiting', ?, 1)`, wt.ID)
	at.Advance(2 * time.Hour)
	if err := s.ReclaimWorktrees(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _ := reclaimWorktreeState(t, s, wt.ID); state != "active" {
		t.Fatalf("state = %s, want active", state)
	}
	mustExec(t, s.DB, `UPDATE workflow_runs SET state = 'completed' WHERE id = 'run_x'`)
	if err := s.ReclaimWorktrees(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _ := reclaimWorktreeState(t, s, wt.ID); state != "removed" {
		t.Fatalf("state = %s, want removed once the run is terminal", state)
	}
}

// A sibling run's review_worktree_id that points at a removed worktree must
// not be reused: the reviewer would be launched into a deleted directory.
func TestReviewWorktreeForSkipsARemovedSiblingTree(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, task := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: task, Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	wf, ok, err := s.workflowRowByID(ctx, st.ID)
	if err != nil || !ok {
		t.Fatalf("workflow row: ok=%v err=%v", ok, err)
	}
	first, err := s.reviewWorktreeFor(ctx, wf, "review", 1, head)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `INSERT INTO workflow_runs (id, workflow_id, step_id, round, role, state, review_worktree_id, created_at)
		VALUES ('run_sib', ?, 'review', 1, 'sibling', 'completed', ?, 1)`, wf.ID, first)
	if again, err := s.reviewWorktreeFor(ctx, wf, "review", 1, head); err != nil || again != first {
		t.Fatalf("active sibling tree: got %q err=%v, want reuse of %q", again, err, first)
	}
	mustExec(t, s.DB, `UPDATE worktrees SET state = 'removed' WHERE id = ?`, first)
	fresh, err := s.reviewWorktreeFor(ctx, wf, "review", 1, head)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == first {
		t.Fatalf("reviewWorktreeFor returned the removed worktree %s", first)
	}
}
