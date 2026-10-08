package runtime

import (
	"context"
	"testing"
)

func TestLowTokenOverrideSurvivesHandoff(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	panes(tm)
	if _, err := s.RequestReplacement(ctx, orch.ID, ModeHandoff, "lt-handoff", ""); err != nil {
		t.Fatal(err)
	}
	succ, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.ID == ses.ID {
		t.Fatal("no successor session started")
	}
	got, err := s.AgentByID(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LowToken == nil || !*got.LowToken {
		t.Fatalf("low_token lost on handoff: %v", got.LowToken)
	}
}
