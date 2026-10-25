package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
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

func requireNothingToFinish(t *testing.T, s *Store, ses, why string) {
	t.Helper()
	_, err := finishCustom(s, ses, CheckpointInput{Kept: []KeptRepo{
		{Repo: "proj", Note: "branch stays"}, {Repo: "docs", Note: "same"}}})
	if err == nil || !strings.Contains(err.Error(), "Nothing to finish") {
		t.Fatalf("%s: finishing err = %v, want Nothing to finish", why, err)
	}
}

// What still costs the approval, on a root an earlier accepted already pulled out of review.
func TestStuckRootStaysUnfinishableAfterRealChanges(t *testing.T) {
	ctx := context.Background()
	stick := func(s *Store, key string) {
		mustExec(t, s.DB, `UPDATE items SET status = 'in_progress' WHERE id = ?`, mustItemID(t, s, key))
	}
	t.Run("newer integrated checkpoint", func(t *testing.T) {
		s, ses, key := customFinishFixture(t)
		stick(s, key)
		if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Integrated, Summary: "again",
			Git:          []GitRef{{Repo: "proj", Branch: "swarm/chore-1", SHA: "4a0d1bc0000"}},
			Verification: []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}}}); err != nil {
			t.Fatal(err)
		}
		requireNothingToFinish(t, s, ses, "newer integration")
	})
	t.Run("reopened child", func(t *testing.T) {
		s, ses, key := customFinishFixture(t)
		child, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: key, Title: "More work"}, items.User("board"))
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, s.DB, `UPDATE items SET status = 'in_progress', updated_at = 9999999999999 WHERE id = ?`, child.ID)
		stick(s, key)
		requireNothingToFinish(t, s, ses, "reopened child")
		if st := itemStatus(t, s, key); st != items.InProgress {
			t.Fatalf("status = %s, want in_progress", st)
		}
	})
	t.Run("changes requested", func(t *testing.T) {
		s, ses, key := customFinishFixture(t)
		mustExec(t, s.DB, `UPDATE requests SET state = 'changes_requested' WHERE item_id = ?`, mustItemID(t, s, key))
		stick(s, key)
		requireNothingToFinish(t, s, ses, "changes requested")
	})
	t.Run("explicit status update", func(t *testing.T) {
		s, ses, key := customFinishFixture(t)
		id := mustItemID(t, s, key)
		if _, err := s.Items.Transition(ctx, key, items.InProgress, items.Orchestrator("agt_x", id)); err != nil {
			t.Fatal(err)
		}
		if st := itemStatus(t, s, key); st != items.InProgress {
			t.Fatalf("status = %s, want in_progress", st)
		}
		requireNothingToFinish(t, s, ses, "explicit status update")
	})
	t.Run("pr closed", func(t *testing.T) {
		s, orch, key := watchFixture(t)
		tick(t, s, map[string]execx.Result{prView(prURL): {Out: ghPRJSON("CLOSED", `[]`)}})
		if err := s.Items.Reconcile(ctx, key); err != nil {
			t.Fatal(err)
		}
		if st := itemStatus(t, s, key); st != items.InProgress {
			t.Fatalf("status = %s, want in_progress", st)
		}
		_ = orch
	})
}

// A fix-round coder's accepted still reopens a task that reached review.
func TestAcceptedStillReopensAnInReviewTask(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	w, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Brief: BriefInput{Objective: "fix round"}})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE items SET status = 'in_review' WHERE key = 'TASK-1'`)
	if _, err := s.WriteCheckpoint(ctx, mustSessionID(t, s, w.ID), CheckpointInput{Kind: Accepted, Summary: "fixing"}); err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, "TASK-1"); st != items.InProgress {
		t.Fatalf("task status = %s, want in_progress", st)
	}
}
