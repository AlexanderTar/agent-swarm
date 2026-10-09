package runtime

import (
	"context"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
)

// renameStore is newStore with the Fake adapter's session-rename command on.
func renameStore(t *testing.T) (*Store, *fakeTmux, *adapter.Fake) {
	t.Helper()
	s, tm, fa := newStore(t)
	fa.NoRename = false
	return s, tm, fa
}

func pendingName(t *testing.T, s *Store, sesID string) string {
	t.Helper()
	var n string
	if err := s.DB.QueryRowContext(context.Background(),
		`SELECT COALESCE(pending_name, '') FROM sessions WHERE id = ?`, sesID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TASK-769: a RenameCommand kind's session carries its agent name as pending
// at launch and at resume; the hook-titled kind (claude) never does.
func TestLaunchAndResumeSetPendingSessionName(t *testing.T) {
	s, _, _ := renameStore(t)
	ctx := context.Background()
	a, ses, _ := titlePendingSpike(t, s)
	if got := pendingName(t, s, ses.ID); got != a.Name {
		t.Fatalf("launch pending_name = %q, want %q", got, a.Name)
	}
	ses2, err := s.startSession(ctx, a, ses.Attempt, ses.Generation+1, true, "prov", "resume")
	if err != nil {
		t.Fatal(err)
	}
	if got := pendingName(t, s, ses2.ID); got != a.Name {
		t.Fatalf("resume pending_name = %q, want %q", got, a.Name)
	}
}

func TestHookTitledKindNeverGetsAPendingName(t *testing.T) {
	s, _, _ := renameStore(t)
	ctx := context.Background()
	a, ses, _ := titlePendingSpike(t, s)
	ad, err := adapter.New(Claude, adapter.Deps{Home: s.Home, UserHome: t.TempDir(), Bin: "/usr/local/bin/swarm",
		Log: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	s.Adapters[Claude] = ad
	a.Kind = Claude
	// The launch itself may fail without a real binary; the row is inserted first.
	_, _ = s.startSession(ctx, a, ses.Attempt, ses.Generation+1, false, "", "")
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ? AND generation = ?`,
		a.ID, ses.Generation+1).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected one new session row, got %d (%v)", n, err)
	}
	var pending string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(pending_name,'') FROM sessions WHERE agent_id = ? AND generation = ?`,
		a.ID, ses.Generation+1).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != "" {
		t.Fatalf("pending_name = %q, want empty", pending)
	}
}

func TestRenameSetsPendingNameOnTheLiveSession(t *testing.T) {
	s, _, _ := renameStore(t)
	ctx := context.Background()
	_, ses, _ := titlePendingSpike(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET pending_name = NULL WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "starting",
		Title: "Fix login redirect loop"}); err != nil {
		t.Fatal(err)
	}
	if got := pendingName(t, s, ses.ID); got != "fix-login-redirect-loop-orchestrator" {
		t.Fatalf("pending_name after rename = %q", got)
	}
}
