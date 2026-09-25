package runtime

import (
	"context"
	"testing"
)

// TestSyncRecoverySurvivesAckedAssignment pins the successor-recovery
// contract: every generation's first sync carries the durable assignment
// plus the recovery bundle (operation, predecessor session), independent of
// acked inbox rows. A later sync of the same session carries the assignment
// but no recovery bundle.
func TestSyncRecoverySurvivesAckedAssignment(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}

	// Generation 1 never replaced: assignment present, no recovery bundle.
	gen1, err := s.SyncRecovery(ctx, wSes.ID)
	if err != nil {
		t.Fatalf("gen1 SyncRecovery err = %v", err)
	}
	if gen1.Assignment.ItemKey != "TASK-1" {
		t.Fatalf("gen1 assignment item = %q, want TASK-1", gen1.Assignment.ItemKey)
	}
	if gen1.Recovery != nil {
		t.Fatalf("gen1 recovery = %+v, want nil (never replaced)", gen1.Recovery)
	}
	if !gen1.FirstSync {
		t.Fatal("gen1 FirstSync = false, want true")
	}

	// Ack every inbox row, then replace: the successor's first sync must
	// still carry both assignment and recovery.
	syncRes, err := s.Sync(ctx, wSes.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ack []string
	for _, m := range syncRes.Messages {
		ack = append(ack, m.MsgID)
	}
	if _, err := s.Sync(ctx, wSes.ID, ack, 0); err != nil {
		t.Fatal(err)
	}

	panes(tm, Pane{Session: wSes.TmuxName})
	op, err := s.RequestReplacement(ctx, w.ID, ModeRecover, "rec1", "")
	if err != nil {
		t.Fatalf("request err = %v", err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatalf("resume err = %v", err)
	}
	succ, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.Generation != wSes.Generation+1 {
		t.Fatalf("successor generation = %d, want %d", succ.Generation, wSes.Generation+1)
	}

	first, err := s.SyncRecovery(ctx, succ.ID)
	if err != nil {
		t.Fatalf("successor SyncRecovery err = %v", err)
	}
	if !first.FirstSync {
		t.Fatal("successor FirstSync = false, want true")
	}
	if first.Assignment.ItemKey != "TASK-1" {
		t.Fatalf("successor assignment item = %q, want TASK-1", first.Assignment.ItemKey)
	}
	if first.Assignment.Brief == "" {
		t.Fatal("successor assignment brief empty, want the durable brief")
	}
	if first.Recovery == nil {
		t.Fatal("successor recovery nil, want the recovery bundle")
	} else {
		if first.Recovery.OperationID != op.ID {
			t.Fatalf("recovery op = %q, want %q", first.Recovery.OperationID, op.ID)
		}
		if first.Recovery.PredecessorSessionID != wSes.ID {
			t.Fatalf("recovery predecessor = %q, want %q", first.Recovery.PredecessorSessionID, wSes.ID)
		}
	}

	second, err := s.SyncRecovery(ctx, succ.ID)
	if err != nil {
		t.Fatalf("second SyncRecovery err = %v", err)
	}
	if second.FirstSync {
		t.Fatal("second FirstSync = true, want false")
	}
	if second.Assignment.ItemKey != "TASK-1" {
		t.Fatalf("second assignment item = %q, want TASK-1 (survives acks)", second.Assignment.ItemKey)
	}
	if second.Recovery != nil {
		t.Fatalf("second recovery = %+v, want nil", second.Recovery)
	}
}
