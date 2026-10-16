package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
)

// newGrokStore is newStore with the fake adapter registered as grok and a grok
// catalog row, so an agent of kind grok can start.
func newGrokStore(t *testing.T) (*Store, *adapter.Fake) {
	t.Helper()
	s, _, fa := newStore(t)
	s.Adapters[Grok] = fa
	seedCatalog(t, s, "grok", "1.0.60", `[{"id":"grok-4.7","label":"Grok 4.7","efforts":["low","high"],"default_effort":"high","effort_encoding":"flag","advisor_capable":false}]`, "grok-4.7")
	setEnabled(t, s, Grok)
	return s, fa
}

func TestLoginCommandNamesEveryKindsOwnLogin(t *testing.T) {
	for k, want := range map[AgentKind]string{
		Claude: "claude /login", Codex: "codex login", Agy: "agy login", Cursor: "cursor-agent login",
		Muse: "muse login", Grok: "grok login",
	} {
		if got := loginCommand(k); got != want {
			t.Errorf("loginCommand(%s) = %q, want %q", k, got, want)
		}
	}
}

// Grok's resume keeps the kickoff (probed 2026-10-16), so unlike muse the
// low-token guidance rides the kickoff and no assignment_update note is queued.
func TestGrokResumeKeepsLowTokenBlockOnTheKickoff(t *testing.T) {
	s, fa := newGrokStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Grok, Model: "grok-4.7"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
	a, err := s.AgentByID(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.startSession(ctx, a, 1, 5, true, "prov", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fa.LastSpec.Kickoff, "## Low-token mode is on") || !strings.Contains(fa.LastSpec.Kickoff, KindKickoffNote(Grok)) {
		t.Fatalf("grok resume kickoff:\n%s", fa.LastSpec.Kickoff)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'
		AND payload_json LIKE '%Low-token mode is now on%'`, orch.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d low-token notes queued; grok's kickoff already carries the block", n)
	}
}

// Muse reports no context window, so its launch writes the cap as one. Grok's
// signals.json carries the real window: a grok launch leaves it unset until a
// sample arrives.
func TestGrokLowTokenLaunchSetsCapButNotTheWindow(t *testing.T) {
	s, fa := newGrokStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Grok, Model: "grok-4.7"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
	a, _ := s.AgentByID(ctx, orch.ID)
	ses, err := s.startSession(ctx, a, 1, 6, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if fa.LastSpec.LowTokenCap != NativeCapTokens("grok") {
		t.Fatalf("LowTokenCap = %d, want %d", fa.LastSpec.LowTokenCap, NativeCapTokens("grok"))
	}
	var window *int
	if err := s.DB.QueryRowContext(ctx, `SELECT context_window FROM sessions WHERE id = ?`, ses.ID).Scan(&window); err != nil {
		t.Fatal(err)
	}
	if window != nil {
		t.Fatalf("context_window = %d, want unset", *window)
	}
}
