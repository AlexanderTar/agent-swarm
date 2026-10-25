package runtime

import (
	"context"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// finishKept finishes key by keeping every repo's branch, the way a custom option does.
func finishKept(t *testing.T, s *Store, ses string) {
	t.Helper()
	if _, err := finishCustom(s, ses, CheckpointInput{Kept: []KeptRepo{
		{Repo: "proj", Note: "branch stays"}, {Repo: "docs", Note: "same"}}}); err != nil {
		t.Fatalf("finishing: %v", err)
	}
}

// BUG-116: an orchestrator resumed after a pause writes accepted again; the approved root stays in review.
func TestAcceptedOnInReviewRootKeepsApprovedFinish(t *testing.T) {
	s, ses, key := customFinishFixture(t)
	if _, err := s.WriteCheckpoint(context.Background(), ses, CheckpointInput{Kind: Accepted, Summary: "resumed"}); err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, key); st != items.InReview {
		t.Fatalf("status after accepted = %s, want in_review", st)
	}
	finishKept(t, s, ses)
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status after finishing = %s, want done", st)
	}
}

// BUG-119: a reviewer spawned on the root writes accepted and completed; the approved root stays in review.
func TestReviewerOnInReviewRootKeepsApprovedFinish(t *testing.T) {
	s, ses, key := customFinishFixture(t)
	ctx := context.Background()
	var orchID string
	if err := s.DB.QueryRowContext(ctx, `SELECT agent_id FROM sessions WHERE id = ?`, ses).Scan(&orchID); err != nil {
		t.Fatal(err)
	}
	rev, _, err := s.Spawn(ctx, SpawnInput{ItemKey: key, Role: RoleReviewer, Kind: Fake, Model: "fake-1",
		ParentAgentID: orchID, Brief: BriefInput{Objective: "review"}})
	if err != nil {
		t.Fatal(err)
	}
	rSes := mustSessionID(t, s, rev.ID)
	for _, k := range []CheckpointKind{Accepted, CompletedCkp} {
		if _, err := s.WriteCheckpoint(ctx, rSes, CheckpointInput{Kind: k, Summary: "reviewed"}); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if st := itemStatus(t, s, key); st != items.InReview {
			t.Fatalf("status after reviewer %s = %s, want in_review", k, st)
		}
	}
	finishKept(t, s, ses)
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status after finishing = %s, want done", st)
	}
}

// A root an earlier accepted checkpoint already pulled out of review is finishable again while its approval
// still binds the newest integration and no child changed since.
func TestInProgressRootWithCurrentApprovalIsFinishable(t *testing.T) {
	s, ses, key := customFinishFixture(t)
	mustExec(t, s.DB, `UPDATE items SET status = 'in_progress' WHERE id = ?`, mustItemID(t, s, key))
	finishKept(t, s, ses)
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status after finishing = %s, want done", st)
	}
}
