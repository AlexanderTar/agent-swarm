package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
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
		{"id":"claude-sonnet-5","aliases":["sonnet"],"label":"Claude Sonnet 5","efforts":["low","medium","high","xhigh","max"],"default_effort":"","effort_encoding":"flag","advisor_capable":true},
		{"id":"claude-haiku-4-5","aliases":["haiku"],"label":"Claude Haiku 4.5","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":false}
	]`, "claude-sonnet-5")
	// A real effort list (not empty), so a test can prove effort is
	// substituted along with kind/model rather than carried over from the
	// original agent (docs/specs/2026-09-19-usage-fallback-agent.md).
	seedCatalog(t, s, "codex", "codex-1",
		`[{"id":"gpt-6-astra","label":"GPT-6-Astra","efforts":["low","medium","high"],"default_effort":"medium","effort_encoding":"flag","advisor_capable":false}]`,
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

// TestStartSpikeSubstitutionDropsAnEffortTheFallbackCannotSupport is the
// regression this fix addresses: the original agent's effort must not be
// carried over onto the fallback's model when the fallback doesn't support
// it (Claude's "xhigh" isn't in codex's efforts list) -- before the fix,
// this refused the spawn outright (Preflight rejected the substituted
// model/effort pair), defeating the entire feature the moment a role used
// a non-default effort.
func TestStartSpikeSubstitutionDropsAnEffortTheFallbackCannotSupport(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"}) // no Effort configured
	s.Usage = fakeUsage{Claude: true}
	ctx := context.Background()
	_, a, queued, err := s.StartSpike(ctx, SpikeInput{Name: "Investigate crash", Intent: "debug",
		Kind: Claude, Model: "claude-sonnet-5", Effort: "xhigh"})
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("not queued")
	}
	if a.PreflightError != "" {
		t.Fatalf("agent = %+v, want the spawn to succeed (effort must not carry over)", a)
	}
	if a.Kind != Codex || a.Model != "gpt-6-astra" || a.Effort != "" {
		t.Fatalf("agent = %+v, want substituted with effort dropped to the fallback's default", a)
	}
	if len(tm.started) != 1 {
		t.Fatalf("started = %v, want exactly one session", tm.started)
	}
}

// TestStartSpikeSubstitutionUsesTheConfiguredFallbackEffort proves a
// configured FallbackDefault.Effort is actually used, not silently dropped
// to "" unconditionally.
func TestStartSpikeSubstitutionUsesTheConfiguredFallbackEffort(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra", Effort: "high"})
	s.Usage = fakeUsage{Claude: true}
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Investigate crash", Intent: "debug",
		Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Effort != "high" {
		t.Fatalf("agent = %+v, want the configured fallback effort", a)
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

// TestStartSpikeDefaultFallbackSameAsExhaustedKindRefuses covers the branch
// every fresh install actually hits: the shipped default FallbackDefault is
// {Claude, "sonnet"} (the same agent every role defaults to), so when
// Claude itself is exhausted, fb.Agent == kind and there is nothing to
// substitute -- the terminal path, not a silent no-op success. A user must
// point the fallback at a second agent to get an escape from Claude
// exhaustion; this test is what makes that consequence visible rather than
// discovered.
func TestStartSpikeDefaultFallbackSameAsExhaustedKindRefuses(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	// FallbackDefault is left at its shipped default (Claude/sonnet, set by
	// newStoreWithFallback's underlying newStore/Settings.Defaults) -- no
	// setFallback call here, unlike every other test in this file.
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
	if a.PreflightError == "" {
		t.Fatalf("agent = %+v, want a PreflightError set (default fallback == the exhausted kind)", a)
	}
	if len(tm.started) != 0 {
		t.Fatalf("started = %v, want no session", tm.started)
	}
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

// TestRetrySubstitutionDropsAnEffortTheFallbackCannotSupport is Retry's own
// coverage of the same effort-carryover regression StartSpike's own test
// covers: Retry re-Preflights the substituted fallback with its own effort,
// not the original agent's.
func TestRetrySubstitutionDropsAnEffortTheFallbackCannotSupport(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"}) // no Effort configured
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Retry me", Intent: "feature",
		Kind: Claude, Model: "claude-sonnet-5", Effort: "xhigh"})
	if err != nil {
		t.Fatal(err)
	}
	crashLatestSession(t, s, a)
	s.Usage = fakeUsage{Claude: true}
	out, err := s.Retry(ctx, a.Name, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Codex || out.Effort != "" {
		t.Fatalf("agent = %+v, want substituted with effort dropped to the fallback's default", out)
	}
	if row := agentRow(t, s, a.Name); row.Effort != "" {
		t.Fatalf("persisted row = %+v, want the effort column updated too", row)
	}
}

// kindSensitiveAdvisor mirrors internal/advisor.Mode's real rule (native
// only for a Claude session with a Claude advisor) instead of the fixed
// fakeAdvisor used elsewhere -- needed here because the whole point of these
// tests is that mode must be *recomputed* for the post-fallback sessionKind,
// not just returned constant.
type kindSensitiveAdvisor struct{}

func (kindSensitiveAdvisor) Mode(sessionKind, advisorKind AgentKind, model string, capable bool) string {
	if model == "" || model == "none" {
		return ""
	}
	if sessionKind == Claude && advisorKind == Claude && capable {
		return "native"
	}
	return "simulated"
}

func (kindSensitiveAdvisor) Ask(context.Context, string, string, []string, time.Duration) (Advice, error) {
	return Advice{}, errors.New("not used in this test")
}

// TestRetryReResolvesAdvisorModeOnKindSwap is a regression test: a usage
// fallback swap inside Retry must re-run resolveAdvisor for the new
// sessionKind, or a Claude agent's "native" mode survives onto a substituted
// Codex session that has neither the native advisor tool nor swarm_advise
// (mcpserver only adds swarm_advise for mode == "simulated").
func TestRetryReResolvesAdvisorModeOnKindSwap(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	s.Advisor = kindSensitiveAdvisor{}
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Retry me", Intent: "feature", Kind: Claude, Model: "claude-sonnet-5",
		Advisor: &AdvisorChoice{Kind: Claude, Model: "claude-fable-5-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, mode := advisorCols(t, s, a.ID); mode != "native" {
		t.Fatalf("advisor mode before retry = %q, want native", mode)
	}
	crashLatestSession(t, s, a)
	s.Usage = fakeUsage{Claude: true}
	out, err := s.Retry(ctx, a.Name, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Codex {
		t.Fatalf("agent kind = %s, want substituted to Codex", out.Kind)
	}
	if out.AdvisorMode != "simulated" {
		t.Fatalf("out.AdvisorMode = %q, want simulated (recomputed for the Codex session)", out.AdvisorMode)
	}
	if _, _, _, mode := advisorCols(t, s, a.ID); mode != "simulated" {
		t.Fatalf("persisted advisor mode = %q, want simulated", mode)
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
	setLimits(t, s, 1, 4)
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

// TestDrainQueueReResolvesAdvisorModeOnKindSwap is startQueued's counterpart
// to TestRetryReResolvesAdvisorModeOnKindSwap: a queued agent admitted after
// its usage fallback substitutes Claude for Codex must have its advisor mode
// recomputed too, not left at the stale "native" from spawn time.
func TestDrainQueueReResolvesAdvisorModeOnKindSwap(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	s.Advisor = kindSensitiveAdvisor{}
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	setLimits(t, s, 1, 4)
	seedEpicWithTwoTasks(t, s)
	first, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Claude,
		Model: "claude-sonnet-5", Advisor: &AdvisorChoice{Kind: Claude, Model: "claude-fable-5-1"},
		Brief: BriefInput{Objective: "one"}})
	if err != nil || queued {
		t.Fatalf("first = %v, queued = %v, err = %v", first.Name, queued, err)
	}
	second, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Claude,
		Model: "claude-sonnet-5", Advisor: &AdvisorChoice{Kind: Claude, Model: "claude-fable-5-1"},
		Brief: BriefInput{Objective: "two"}})
	if err != nil || !queued {
		t.Fatalf("second = %v, queued = %v, err = %v", second.Name, queued, err)
	}
	if _, _, _, mode := advisorCols(t, s, second.ID); mode != "native" {
		t.Fatalf("advisor mode before drain = %q, want native", mode)
	}
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, first.ID)
	s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, first.ID)
	s.Usage = fakeUsage{Claude: true}
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	drained := agentRow(t, s, second.Name)
	if drained.Kind != Codex {
		t.Fatalf("drained kind = %s, want substituted to Codex", drained.Kind)
	}
	if drained.AdvisorMode != "simulated" {
		t.Fatalf("drained.AdvisorMode = %q, want simulated (recomputed for the Codex session)", drained.AdvisorMode)
	}
	if _, _, _, mode := advisorCols(t, s, second.ID); mode != "simulated" {
		t.Fatalf("persisted advisor mode = %q, want simulated", mode)
	}
}

func TestDrainQueueBothExhaustedRelaysToParent(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	// agents=2: the orchestrator and the first child share the pool now.
	setLimits(t, s, 2, 4)
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

// TestDrainQueueSubstitutedFallbackFailsItsOwnPreflightPersistsConsistently
// covers substituted=true where the fallback itself then fails Preflight
// (e.g. not installed): the persisted row's kind/model and the
// preflight_failed reason must agree on which agent Preflight actually
// ran against (the fallback), not the originally configured one.
func TestDrainQueueSubstitutedFallbackFailsItsOwnPreflightPersistsConsistently(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	setLimits(t, s, 1, 4)
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
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, first.ID)
	s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, first.ID)
	s.Usage = fakeUsage{Claude: true}
	// A broken-but-registered adapter (Preflight's own Installed check fails),
	// not a missing map entry (startQueued's own "no adapter for %s" guard,
	// a wiring gap distinct from a real preflight failure) -- this exercises
	// the preflightErr path this test is actually about.
	s.Adapters[Codex] = &adapter.Fake{NotInstalled: true}
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	drained := agentRow(t, s, second.Name)
	if drained.Kind != Codex || drained.Model != "gpt-6-astra" {
		t.Fatalf("drained = %+v, want the row to persist the substituted (failed) fallback", drained)
	}
	n := notified(t, s, "agent.preflight_failed")
	if !strings.Contains(n.Args["reason"], "Codex") {
		t.Fatalf("reason = %q, want it to name the fallback the row now shows", n.Args["reason"])
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
