package runtime

import (
	"context"
	"slices"
	"strings"
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

func countAgents(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ---- StartOrchestrator ----

func TestStartOrchestratorSubstitutesExhaustedFallback(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	seedEpicWithTask(t, s)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	s.Usage = fakeUsage{Claude: true}
	a, queued, err := s.StartOrchestrator(context.Background(), OrchestratorInput{ItemKey: "EPIC-1",
		Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("not queued")
	}
	if a.Kind != Codex || a.Model != "gpt-6-astra" {
		t.Fatalf("agent = %+v, want substituted", a)
	}
	if len(tm.started) != 1 {
		t.Fatalf("started = %v", tm.started)
	}
	n := notified(t, s, "agent.fallback_used")
	if n.Args["name"] != a.Name || n.Args["agent"] != "Codex" || n.Args["from"] != "Claude" {
		t.Fatalf("fallback_used args = %+v", n.Args)
	}
}

func TestStartOrchestratorBothExhaustedReturnsErrorNoRow(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	seedEpicWithTask(t, s)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	s.Usage = fakeUsage{Claude: true, Codex: true}
	before := countAgents(t, s)
	_, _, err := s.StartOrchestrator(context.Background(), OrchestratorInput{ItemKey: "EPIC-1",
		Kind: Claude, Model: "claude-sonnet-5"})
	if err == nil {
		t.Fatal("want an error when both the agent and its fallback are exhausted")
	}
	if got := countAgents(t, s); got != before {
		t.Fatalf("agents = %d, want unchanged at %d (no row on this refusal)", got, before)
	}
	if len(tm.started) != 0 {
		t.Fatalf("started = %v, want no session", tm.started)
	}
}

func TestStartOrchestratorNilUsageNeverSubstitutes(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	seedEpicWithTask(t, s)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	a, _, err := s.StartOrchestrator(context.Background(), OrchestratorInput{ItemKey: "EPIC-1",
		Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Claude {
		t.Fatalf("agent = %+v, want unaffected", a)
	}
}

// ---- Spawn ----

func TestSpawnSubstitutesExhaustedFallback(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	seedEpicWithTask(t, s)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	s.Usage = fakeUsage{Claude: true}
	a, queued, err := s.Spawn(context.Background(), SpawnInput{ItemKey: "TASK-1", Role: RoleCoder,
		Kind: Claude, Model: "claude-sonnet-5", Brief: BriefInput{Objective: "do it"}})
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("not queued")
	}
	if a.Kind != Codex || a.Model != "gpt-6-astra" {
		t.Fatalf("agent = %+v, want substituted", a)
	}
	if len(tm.started) != 1 {
		t.Fatalf("started = %v", tm.started)
	}
	notified(t, s, "agent.fallback_used")
}

func TestSpawnBothExhaustedReturnsErrorNoRow(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	seedEpicWithTask(t, s)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	s.Usage = fakeUsage{Claude: true, Codex: true}
	before := countAgents(t, s)
	_, _, err := s.Spawn(context.Background(), SpawnInput{ItemKey: "TASK-1", Role: RoleCoder,
		Kind: Claude, Model: "claude-sonnet-5", Brief: BriefInput{Objective: "do it"}})
	if err == nil {
		t.Fatal("want an error when both the agent and its fallback are exhausted")
	}
	if got := countAgents(t, s); got != before {
		t.Fatalf("agents = %d, want unchanged at %d", got, before)
	}
}

func TestSpawnNilUsageNeverSubstitutes(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	seedEpicWithTask(t, s)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	a, _, err := s.Spawn(context.Background(), SpawnInput{ItemKey: "TASK-1", Role: RoleCoder,
		Kind: Claude, Model: "claude-sonnet-5", Brief: BriefInput{Objective: "do it"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Claude {
		t.Fatalf("agent = %+v, want unaffected", a)
	}
}

// TestSpawnFallbackReplayDoesNotDoubleNotify proves the idempotent-replay
// guard: retrying the same SessionID/RequestID after a substitution must not
// raise agent.fallback_used a second time.
func TestSpawnFallbackReplayDoesNotDoubleNotify(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	seedEpicWithTask(t, s)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	s.Usage = fakeUsage{Claude: true}
	in := SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Claude, Model: "claude-sonnet-5",
		Brief: BriefInput{Objective: "do it"}, SessionID: "ses_orch", RequestID: "req-1"}
	if _, _, err := s.Spawn(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Spawn(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.fallback_used"); n != 1 {
		t.Fatalf("agent.fallback_used raised %d times, want 1", n)
	}
}

// ---- Retry ----

// crashLatestSession puts a's current session into a retryable state.
func crashLatestSession(t *testing.T, s *Store, a Agent) {
	t.Helper()
	ses, err := s.LatestSession(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE sessions SET state = 'crashed' WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRetrySubstitutesExhaustedFallbackAndPersistsIt(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Retry me", Intent: "feature", Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	crashLatestSession(t, s, a)
	s.Usage = fakeUsage{Claude: true}
	out, err := s.Retry(ctx, a.Name, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Codex || out.Model != "gpt-6-astra" {
		t.Fatalf("agent = %+v, want substituted", out)
	}
	if row := agentRow(t, s, a.Name); row.Kind != Codex || row.Model != "gpt-6-astra" {
		t.Fatalf("persisted row = %+v, want substituted kind/model", row)
	}
	n := notified(t, s, "agent.fallback_used")
	if n.Args["agent"] != "Codex" || n.Args["from"] != "Claude" {
		t.Fatalf("fallback_used args = %+v", n.Args)
	}
	if len(tm.started) != 2 { // one from StartSpike, one from Retry
		t.Fatalf("started = %v", tm.started)
	}
}

func TestRetryBothExhaustedReturnsErrorNoNewSession(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Retry me", Intent: "feature", Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	crashLatestSession(t, s, a)
	s.Usage = fakeUsage{Claude: true, Codex: true}
	startedBefore := len(tm.started)
	if _, err := s.Retry(ctx, a.Name, "", "", ""); err == nil {
		t.Fatal("want an error when both the agent and its fallback are exhausted")
	}
	if row := agentRow(t, s, a.Name); row.Kind != Claude {
		t.Fatalf("row kind = %s, want unchanged", row.Kind)
	}
	if len(tm.started) != startedBefore {
		t.Fatalf("started = %v, want no new session", tm.started)
	}
}

// TestRetrySubstitutedFallbackFailsItsOwnPreflight covers a substituted
// fallback that is itself broken (not installed): Retry must return that
// error rather than spawn a session doomed to fail, and must not persist
// the broken substitute onto the agent row.
func TestRetrySubstitutedFallbackFailsItsOwnPreflight(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Retry me", Intent: "feature", Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	crashLatestSession(t, s, a)
	s.Usage = fakeUsage{Claude: true}
	delete(s.Adapters, Codex) // the fallback agent isn't actually installed
	if _, err := s.Retry(ctx, a.Name, "", "", ""); err == nil {
		t.Fatal("want an error when the substituted fallback fails its own Preflight")
	}
	if row := agentRow(t, s, a.Name); row.Kind != Claude {
		t.Fatalf("row kind = %s, want unchanged (never persist a broken substitute)", row.Kind)
	}
}

func TestRetryNilUsageNeverSubstitutes(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Retry me", Intent: "feature", Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	crashLatestSession(t, s, a)
	out, err := s.Retry(ctx, a.Name, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Claude {
		t.Fatalf("agent = %+v, want unaffected", out)
	}
}

// ---- DrainQueue / startQueued ----

func TestDrainQueueSubstitutesExhaustedFallback(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	setLimits(t, s, 3, 1, 4)
	seedEpicWithTwoTasks(t, s)
	first, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Claude,
		Model: "claude-sonnet-5", Brief: BriefInput{Objective: "one"}})
	if err != nil || queued {
		t.Fatalf("first = %v, queued = %v, err = %v", first.Name, queued, err)
	}
	second, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Claude,
		Model: "claude-sonnet-5", Brief: BriefInput{Objective: "two"}})
	if err != nil || !queued {
		t.Fatalf("second = %v, queued = %v, err = %v", second.Name, queued, err)
	}
	// free the slot, then mark Claude exhausted before the drain runs the
	// decision -- exactly the "quota changed between spawn and drain" case
	// this call site exists for.
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, first.ID)
	s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, first.ID)
	s.Usage = fakeUsage{Claude: true}
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	drained := agentRow(t, s, second.Name)
	if drained.State != AgentActive {
		t.Fatalf("state = %s, want active", drained.State)
	}
	if drained.Kind != Codex || drained.Model != "gpt-6-astra" {
		t.Fatalf("drained = %+v, want substituted to codex/gpt-6-astra", drained)
	}
	if len(tm.started) != 2 { // TASK-1's session plus the drained TASK-2 one
		t.Fatalf("started = %v", tm.started)
	}
	n := notified(t, s, "agent.fallback_used")
	if n.Args["agent"] != "Codex" || n.Args["from"] != "Claude" {
		t.Fatalf("fallback_used args = %+v", n.Args)
	}
}

func TestDrainQueueBothExhaustedRelaysToParent(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	setLimits(t, s, 3, 1, 4)
	seedEpicWithTwoTasks(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	first, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Claude,
		Model: "claude-sonnet-5", Brief: BriefInput{Objective: "one"}, ParentAgentID: orch.ID})
	if err != nil || queued {
		t.Fatalf("first = %v, queued = %v, err = %v", first.Name, queued, err)
	}
	second, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Claude,
		Model: "claude-sonnet-5", Brief: BriefInput{Objective: "two"}, ParentAgentID: orch.ID})
	if err != nil || !queued {
		t.Fatalf("second = %v, queued = %v, err = %v", second.Name, queued, err)
	}
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, first.ID)
	s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, first.ID)
	s.Usage = fakeUsage{Claude: true, Codex: true}
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	drained, err := s.Agent(ctx, second.Name)
	if err != nil {
		t.Fatal(err)
	}
	if drained.State != AgentActive {
		t.Fatalf("state = %s, want active with a failed session", drained.State)
	}
	if drained.Kind != Claude {
		t.Fatalf("kind = %s, want unchanged (no usable fallback)", drained.Kind)
	}
	ses, err := s.LatestSession(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != Failed {
		t.Fatalf("session state = %s, want failed", ses.State)
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'`, orch.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("relay message count = %d, err = %v", count, err)
	}
}

// ---- Resume: explicitly excluded (spec Locked Decision 2) ----

// TestResumeIgnoresUsageExhaustion proves Resume never substitutes even when
// Store.Usage reports the agent's kind exhausted: Resume continues an
// existing provider session, and switching agent kind mid-resume cannot
// continue a Claude conversation on Codex.
func TestResumeIgnoresUsageExhaustion(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Resume me", Intent: "feature", Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused', provider_session_id = 'p1' WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}
	s.Usage = fakeUsage{Claude: true}
	out, err := s.Resume(ctx, a.Name, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Claude {
		t.Fatalf("agent = %+v, want Resume to never substitute", out)
	}
	if row := agentRow(t, s, a.Name); row.Kind != Claude {
		t.Fatalf("row kind = %s, want unchanged", row.Kind)
	}
	if slices.Contains(s.Notify.(*fakeNotifier).kinds(), "agent.fallback_used") {
		t.Fatal("Resume must never raise agent.fallback_used")
	}
	if !strings.Contains(tm.started[len(tm.started)-1], "--resume") {
		t.Fatal("want the resumed launch, not a fresh one")
	}
}
