package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

func reservationReleased(t *testing.T, s *Store, wtID, agentID string) bool {
	t.Helper()
	var n int
	if err := s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM worktree_reservations
		WHERE worktree_id = ? AND agent_id = ? AND released_at IS NOT NULL`, wtID, agentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// A finished reviewer with no live session must not pin a worktree; a live
// one still does. Reclaim releases the stale reservation.
func TestReclaimIgnoresFinishedReviewersReservation(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	repoID := seedRepo(t, s, "proj")
	repoPath := repoPathFor(t, s, repoID)
	seedReclaimAgent(t, s, "owner_a", ep.ID, "")
	seedReclaimAgent(t, s, "owner_b", ep.ID, "")
	seedReclaimAgent(t, s, "rev_done", ep.ID, "")
	seedReclaimAgent(t, s, "rev_live", ep.ID, "")
	for _, id := range []string{"owner_a", "owner_b", "rev_done"} {
		finishReclaimAgent(t, s, id, s.Now())
	}
	seedReclaimSession(t, s, "ses_live", "rev_live", Running)
	wtA := seedReclaimWorktree(t, s, repoID, repoPath, "task/res-a", "owner_a", ep.ID)
	wtB := seedReclaimWorktree(t, s, repoID, repoPath, "task/res-b", "owner_b", ep.ID)
	for _, r := range [][2]string{{wtA.ID, "rev_done"}, {wtB.ID, "rev_live"}} {
		mustExec(t, s.DB, `INSERT INTO worktree_reservations (worktree_id, agent_id, mode, created_at) VALUES (?, ?, 'ro', ?)`,
			r[0], r[1], db.Millis(s.Now()))
	}
	at.Advance(2 * time.Hour)
	if err := s.ReclaimWorktrees(ctx); err != nil {
		t.Fatal(err)
	}
	if state, _ := reclaimWorktreeState(t, s, wtA.ID); state != "removed" {
		t.Fatalf("worktree held by a finished reviewer: state = %s, want removed", state)
	}
	if !reservationReleased(t, s, wtA.ID, "rev_done") {
		t.Fatal("the finished reviewer's reservation must be released")
	}
	if state, _ := reclaimWorktreeState(t, s, wtB.ID); state != "active" {
		t.Fatalf("worktree held by a live reviewer: state = %s, want active", state)
	}
	if reservationReleased(t, s, wtB.ID, "rev_live") {
		t.Fatal("a live reviewer's reservation must stay")
	}
}
