package runtime

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// claudeOrch starts a Claude orchestrator on EPIC-1 with role overrides set and
// a running, pane-backed session, ready for a handoff walk.
func claudeOrch(t *testing.T, s *Store, tm *fakeTmux) (Agent, Session) {
	t.Helper()
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Claude, Model: "claude-sonnet-5",
		Roles: map[Role]settings.RoleDefault{RoleCoder: {Agent: Claude, Model: "claude-sonnet-5"}}})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: ses.TmuxName})
	tm.env[ses.TmuxName] = map[string]string{"SWARM_SESSION": ses.ID}
	tm.killed = nil
	return orch, ses
}

// driveHandoff walks a preserving handoff to its end: checkpoint, stop, start.
func driveHandoff(t *testing.T, s *Store, tm *fakeTmux, ses Session) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Handoff, Summary: "saved"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
}

func switchRow(t *testing.T, s *Store, id string) (kind, model, effort, advKind, advMode, advEffort, kindReason string, overrides sql.NullString) {
	t.Helper()
	if err := s.DB.QueryRow(`SELECT kind, model, COALESCE(effort,''), COALESCE(advisor_kind,''), COALESCE(advisor_mode,''),
		COALESCE(advisor_effort,''), COALESCE(kind_reason,''), role_overrides FROM agents WHERE id = ?`, id).
		Scan(&kind, &model, &effort, &advKind, &advMode, &advEffort, &kindReason, &overrides); err != nil {
		t.Fatal(err)
	}
	return
}

func TestHandoffSwitchAppliesAtSuccessor(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	ctx := context.Background()
	orch, ses := claudeOrch(t, s, tm)
	op, err := s.RequestHandoffTo(ctx, orch.ID, "sw1", AgentSwitch{Kind: Codex, Model: "gpt-6-astra", Effort: "high",
		Advisor: &AdvisorChoice{None: true}})
	if err != nil {
		t.Fatal(err)
	}
	if k, _, _, _, _, _, _, _ := switchRow(t, s, orch.ID); k != "claude" {
		t.Fatalf("kind switched at request time (%s); the switch applies at PhaseStarting", k)
	}
	driveHandoff(t, s, tm, ses)
	got, _ := s.getOperation(ctx, op.ID)
	if got.Phase != PhaseSucceeded {
		t.Fatalf("phase = %s (%s), want succeeded", got.Phase, got.Error)
	}
	kind, model, effort, advKind, _, _, reason, overrides := switchRow(t, s, orch.ID)
	if kind != "codex" || model != "gpt-6-astra" || effort != "high" || advKind != "" || reason != "" || overrides.Valid {
		t.Fatalf("row = %s/%s/%s adv=%q reason=%q overrides=%v", kind, model, effort, advKind, reason, overrides)
	}
	fa := s.Adapters[Codex].(*adapter.Fake)
	if fa.LastSpec.ProviderSessionID != "" {
		t.Fatalf("successor resumed provider session %q, want a fresh one", fa.LastSpec.ProviderSessionID)
	}
	if !strings.Contains(fa.LastSpec.Kickoff, "continuing in a fresh session after handoff") {
		t.Fatalf("kickoff = %q, want the handoff successor kickoff", fa.LastSpec.Kickoff)
	}
}

func TestHandoffSwitchReplayReturnsSameOp(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	ctx := context.Background()
	orch, _ := claudeOrch(t, s, tm)
	sw := AgentSwitch{Kind: Codex, Model: "gpt-6-astra"}
	a, err := s.RequestHandoffTo(ctx, orch.ID, "same", sw)
	if err != nil {
		t.Fatal(err)
	}
	sw.Model = "not-a-model" // replay skips validation
	b, err := s.RequestHandoffTo(ctx, orch.ID, "same", sw)
	if err != nil || a.ID != b.ID {
		t.Fatalf("replay = %s/%v, want %s", b.ID, err, a.ID)
	}
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, orch.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("operations = %d, want 1", n)
	}
}

func TestHandoffSwitchRejects(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, s *Store, orch Agent) string // returns agent id to hand off
		sw   AgentSwitch
		want string
	}{
		{"non-orchestrator", func(t *testing.T, s *Store, orch Agent) string {
			w, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Claude, Model: "claude-sonnet-5",
				ParentAgentID: orch.ID, Brief: BriefInput{Objective: "x"}})
			if err != nil {
				t.Fatal(err)
			}
			return w.ID
		}, AgentSwitch{Kind: Codex, Model: "gpt-6-astra"}, errSwitchNotOrchestrator},
		{"finished", func(t *testing.T, s *Store, orch Agent) string {
			s.DB.Exec(`UPDATE agents SET state = 'finished' WHERE id = ?`, orch.ID)
			return orch.ID
		}, AgentSwitch{Kind: Codex, Model: "gpt-6-astra"}, errSwitchNotLive},
		{"bad model", func(t *testing.T, s *Store, orch Agent) string { return orch.ID },
			AgentSwitch{Kind: Codex, Model: "nope"}, "model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, tm := newStoreWithFallback(t)
			orch, _ := claudeOrch(t, s, tm)
			id := tc.prep(t, s, orch)
			_, err := s.RequestHandoffTo(ctx, id, "k", tc.sw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			var n int
			s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, id).Scan(&n)
			if n != 0 {
				t.Fatalf("operations = %d, want none recorded", n)
			}
		})
	}
}

func TestHandoffSwitchPreflightFailsAtStartBlocks(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	ctx := context.Background()
	orch, ses := claudeOrch(t, s, tm)
	op, err := s.RequestHandoffTo(ctx, orch.ID, "k", AgentSwitch{Kind: Codex, Model: "gpt-6-astra"})
	if err != nil {
		t.Fatal(err)
	}
	setEnabled(t, s, Claude) // Codex disabled after the request was accepted
	driveHandoff(t, s, tm, ses)
	got, _ := s.getOperation(ctx, op.ID)
	if got.Phase != PhaseBlocked || got.Error == "" {
		t.Fatalf("phase = %s (%q), want blocked with the Preflight text", got.Phase, got.Error)
	}
	if k, _, _, _, _, _, _, ov := switchRow(t, s, orch.ID); k != "claude" || !ov.Valid {
		t.Fatalf("row changed on a blocked switch: kind=%s overrides=%v", k, ov)
	}
	if got := s.Notify.(*fakeNotifier).kinds(); !slices.Contains(got, "agent.preflight_failed") {
		t.Fatalf("raised %v, want agent.preflight_failed", got)
	}
}

func TestHandoffSwitchNativeAdvisorReResolved(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Codex, Model: "gpt-6-astra"})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, orch.ID)
	s.SetSessionState(ctx, ses.ID, Running)
	panes(tm, Pane{Session: ses.TmuxName})
	tm.env[ses.TmuxName] = map[string]string{"SWARM_SESSION": ses.ID}
	s.Advisor = fakeAdvisor{mode: "native"} // a Claude session with a Claude advisor runs it natively
	if _, err := s.RequestHandoffTo(ctx, orch.ID, "k", AgentSwitch{Kind: Claude, Model: "claude-sonnet-5",
		Advisor: &AdvisorChoice{Kind: Claude, Model: "claude-fable-5-1", Effort: "high"}}); err != nil {
		t.Fatal(err)
	}
	driveHandoff(t, s, tm, ses)
	_, _, _, advKind, advMode, advEffort, _, _ := switchRow(t, s, orch.ID)
	if advKind != "claude" || advMode != "native" || advEffort != "" {
		t.Fatalf("advisor = %s/%s/%q, want claude/native/''", advKind, advMode, advEffort)
	}
}

func TestPlainHandoffLeavesSwitchEmpty(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	ctx := context.Background()
	orch, ses := claudeOrch(t, s, tm)
	op, err := s.RequestReplacement(ctx, orch.ID, ModeHandoff, "plain", "")
	if err != nil {
		t.Fatal(err)
	}
	driveHandoff(t, s, tm, ses)
	var sw string
	s.DB.QueryRow(`SELECT switch_json FROM agent_operations WHERE id = ?`, op.ID).Scan(&sw)
	if k, _, _, _, _, _, _, ov := switchRow(t, s, orch.ID); sw != "" || k != "claude" || !ov.Valid {
		t.Fatalf("plain handoff: switch_json=%q kind=%s overrides=%v", sw, k, ov)
	}
}

func cancelledClaudeOrch(t *testing.T, s *Store) Agent {
	t.Helper()
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Claude, Model: "claude-sonnet-5",
		Roles: map[Role]settings.RoleDefault{RoleCoder: {Agent: Claude, Model: "claude-sonnet-5"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(ctx, orch.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	return orch
}

func TestStartAfterCancelAppliesPicks(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	ctx := context.Background()
	orch := cancelledClaudeOrch(t, s)
	again, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Codex, Model: "gpt-6-astra", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != orch.ID || again.Name != orch.Name {
		t.Fatalf("restart = %s/%s, want same %s/%s", again.ID, again.Name, orch.ID, orch.Name)
	}
	kind, model, effort, _, _, _, reason, ov := switchRow(t, s, orch.ID)
	if kind != "codex" || model != "gpt-6-astra" || effort != "high" || reason != "" || ov.Valid {
		t.Fatalf("row = %s/%s/%s reason=%q overrides=%v", kind, model, effort, reason, ov)
	}
	var mode, phase string
	s.DB.QueryRow(`SELECT mode, phase FROM agent_operations WHERE agent_id = ? ORDER BY created_at DESC LIMIT 1`, orch.ID).Scan(&mode, &phase)
	if mode != "recover" || phase != "succeeded" {
		t.Fatalf("op = %s/%s, want recover/succeeded", mode, phase)
	}
}

func TestStartAfterCancelWithoutPicksKeepsKind(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	ctx := context.Background()
	orch := cancelledClaudeOrch(t, s)
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1"}); err != nil {
		t.Fatal(err)
	}
	var sw string
	s.DB.QueryRow(`SELECT switch_json FROM agent_operations WHERE agent_id = ? ORDER BY created_at DESC LIMIT 1`, orch.ID).Scan(&sw)
	if k, _, _, _, _, _, _, ov := switchRow(t, s, orch.ID); k != "claude" || !ov.Valid || sw != "" {
		t.Fatalf("kind=%s overrides=%v switch_json=%q, want unchanged", k, ov, sw)
	}
}

func TestStartAfterCancelBadPicksIsPreflightError(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	ctx := context.Background()
	orch := cancelledClaudeOrch(t, s)
	var before int
	s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, orch.ID).Scan(&before)
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Codex, Model: "nope"}); err == nil {
		t.Fatal("want a Preflight error for an unknown model")
	}
	var after int
	s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, orch.ID).Scan(&after)
	if k, _, _, _, _, _, _, _ := switchRow(t, s, orch.ID); after != before || k != "claude" {
		t.Fatalf("ops %d→%d kind=%s, want nothing recorded, row unchanged", before, after, k)
	}
}
