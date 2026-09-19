package runtime

import (
	"context"
	"slices"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// fakeUsage implements UsageReader for tests: f[kind] is whether kind is
// confirmed exhausted. A kind absent from the map is available.
type fakeUsage map[AgentKind]bool

func (f fakeUsage) Exhausted(_ context.Context, k AgentKind) bool { return f[k] }

// seedCatalog inserts one model_catalog row, the way agents_test.go's own
// seedFakeCatalog does for "fake".
func seedCatalog(t *testing.T, s *Store, kind, version, modelsJSON, defaultModel string) {
	t.Helper()
	_, err := s.DB.ExecContext(context.Background(), `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES (?, ?, ?, ?, 'test', 1, 1)`, kind, version, modelsJSON, defaultModel)
	if err != nil {
		t.Fatal(err)
	}
}

// newStoreWithFallback extends newStore(t): registers the fake adapter under
// Claude and Codex too (Fake doesn't care which kind key it's launched
// under), seeds a real model_catalog row for each so Preflight/catalog.Find
// succeed, enables both, and returns the Store ready for a
// setFallback/s.Usage-driven fallback test.
func newStoreWithFallback(t *testing.T) (*Store, *fakeTmux) {
	t.Helper()
	s, tm, fa := newStore(t)
	s.Adapters[Claude] = fa
	s.Adapters[Codex] = fa
	// Every alias every default role might use, so Settings.Put's own
	// validate() (which now sees a real, non-empty Claude catalog once this
	// row exists) doesn't reject an unrelated role's untouched default.
	seedCatalog(t, s, "claude", "claude-1", `[
		{"id":"claude-fable-5-1","aliases":["fable"],"label":"Claude Fable 5.1","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":true},
		{"id":"claude-opus-5","aliases":["opus"],"label":"Claude Opus 5","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":true},
		{"id":"claude-sonnet-5","aliases":["sonnet"],"label":"Claude Sonnet 5","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":true},
		{"id":"claude-haiku-4-5","aliases":["haiku"],"label":"Claude Haiku 4.5","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":false}
	]`, "claude-sonnet-5")
	seedCatalog(t, s, "codex", "codex-1",
		`[{"id":"gpt-6-astra","label":"GPT-6-Astra","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":false}]`,
		"gpt-6-astra")
	setEnabled(t, s, Claude, Codex)
	return s, tm
}

func setEnabled(t *testing.T, s *Store, kinds ...AgentKind) {
	t.Helper()
	cfg, err := s.Settings.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnabledAgents = kinds
	if _, err := s.Settings.Put(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func setFallback(t *testing.T, s *Store, fb settings.RoleDefault) {
	t.Helper()
	cfg, err := s.Settings.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.FallbackDefault = fb
	if _, err := s.Settings.Put(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func agentRow(t *testing.T, s *Store, name string) Agent {
	t.Helper()
	a, err := s.Agent(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// ---- StartSpike ----

func TestStartSpikeSubstitutesExhaustedFallback(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	s.Usage = fakeUsage{Claude: true}
	ctx := context.Background()
	_, a, queued, err := s.StartSpike(ctx, SpikeInput{Name: "Investigate crash", Intent: "debug",
		Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("not queued")
	}
	if a.Kind != Codex || a.Model != "gpt-6-astra" {
		t.Fatalf("agent = %+v, want substituted to codex/gpt-6-astra", a)
	}
	if len(tm.started) != 1 {
		t.Fatalf("started = %v, want exactly one session", tm.started)
	}
	n := notified(t, s, "agent.fallback_used")
	if n.Args["name"] != a.Name || n.Args["agent"] != "Codex" || n.Args["from"] != "Claude" {
		t.Fatalf("fallback_used args = %+v", n.Args)
	}
}

func TestStartSpikeNotExhaustedNoSubstitution(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	s.Usage = fakeUsage{} // nothing exhausted
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Investigate crash", Intent: "debug",
		Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Claude || a.Model != "claude-sonnet-5" {
		t.Fatalf("agent = %+v, want unchanged", a)
	}
	if len(tm.started) != 1 {
		t.Fatalf("started = %v", tm.started)
	}
	if got := s.Notify.(*fakeNotifier).kinds(); slices.Contains(got, "agent.fallback_used") {
		t.Fatalf("raised %v, want no fallback notification", got)
	}
}

func TestStartSpikeBothExhaustedRefuses(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	s.Usage = fakeUsage{Claude: true, Codex: true}
	ctx := context.Background()
	_, a, queued, err := s.StartSpike(context.Background(), SpikeInput{Name: "Investigate crash", Intent: "debug",
		Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("not queued")
	}
	if a.PreflightError == "" {
		t.Fatalf("agent = %+v, want a PreflightError set", a)
	}
	if n := notified(t, s, "agent.preflight_failed"); n.AgentName != a.Name {
		t.Fatalf("preflight_failed args = %+v", n)
	}
	if len(tm.started) != 0 {
		t.Fatalf("started = %v, want no session (both exhausted)", tm.started)
	}
	_ = ctx
}

func TestStartSpikeNilUsageNeverSubstitutes(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	// s.Usage stays nil, as newStore already leaves it.
	_, a, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Investigate crash", Intent: "debug",
		Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Claude {
		t.Fatalf("agent = %+v, want unaffected by a nil Store.Usage", a)
	}
	if len(tm.started) != 1 {
		t.Fatalf("started = %v", tm.started)
	}
}

var _ = agentRow // used by later steps' tests in this file
