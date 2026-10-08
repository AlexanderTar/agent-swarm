package runtime

import (
	"context"
	"testing"
)

// BUG-60: a tree the owner removed but Swarm retained (unmerged) is still the
// owner's tree, so Start accepts it and the step's share reactivates it.
func TestStartWorkflowAcceptsARetainedWorktreeYouOwn(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
	if _, err := s.DB.ExecContext(ctx, `UPDATE worktrees SET state = 'retained', retained_reason = 'unmerged'
		WHERE id = ?`, wtID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}}); err != nil {
		t.Fatalf("StartWorkflow with retained tree = %v, want nil", err)
	}
	var state, owner string
	if err := s.DB.QueryRowContext(ctx, `SELECT state, owner_agent_id FROM worktrees WHERE id = ?`, wtID).
		Scan(&state, &owner); err != nil {
		t.Fatal(err)
	}
	if state != "active" || owner != orch.ID {
		t.Fatalf("worktree state, owner = %s, %s; want active, %s", state, owner, orch.ID)
	}
}
