package runtime

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// P2 T35's P32: two one-liners over things that already exist, plus the two
// httpapi accessors T31 added to Store.

func TestTmuxBinAndTmuxSocketReadTheStoreFields(t *testing.T) {
	s := &Store{TmuxPath: "/usr/bin/tmux", TmuxSocketName: "swarm-test-1"}
	if s.TmuxBin() != "/usr/bin/tmux" || s.TmuxSocket() != "swarm-test-1" {
		t.Fatalf("TmuxBin/TmuxSocket = %q, %q", s.TmuxBin(), s.TmuxSocket())
	}
}

func TestDeliverAdvicePutsAnAdviceMessageInTheInbox(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "E"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverAdvice(ctx, ses.ID, Advice{Question: "Q?", Answer: "A.", State: "answered"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'advice' AND to_agent_id = ?`,
		a.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("advice messages = %d", n)
	}
}

func TestOnWorktreeRetainedRaisesTheSeventeenFiveNotification(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		return s.OnWorktreeRetained(ctx, tx, worktree.Worktree{Path: "/tmp/wt-x", RetainedReason: "unmerged"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Notify.(*fakeNotifier).kinds(); !slices.Contains(got, "worktree.retained") {
		t.Fatalf("kinds = %v", got)
	}
}

func TestRequestWireByIDMatchesTheTxVariant(t *testing.T) {
	s, _, _ := newStore(t)
	req, _, _ := seedSectionApproval(t, s)
	w, err := s.RequestWireByID(context.Background(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.ID != req.ID || w.Kind != "approve_section" {
		t.Fatalf("wire = %+v", w)
	}
}
