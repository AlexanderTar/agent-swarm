package runtime

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// A kind a pane showed a usage-limit line for is exhausted until its reset,
// to every gate caller, with no usage poller at all (BUG-110: muse's weekly
// cap read 99% in the poller while the pane said "Usage limit reached").
func TestNotedUsageLimitDivertsSpawnsUntilReset(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	if s.kindExhausted(ctx, Claude) {
		t.Fatal("nothing noted yet")
	}
	reset := s.Now().Add(time.Hour)
	s.noteUsageLimit(Claude, reset)
	if !s.kindExhausted(ctx, Claude) || s.kindExhausted(ctx, Codex) {
		t.Fatal("only the noted kind reads exhausted")
	}
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Investigate crash", Intent: "debug",
		Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Codex || len(tm.started) != 1 {
		t.Fatalf("agent = %+v, started = %v, want substituted to codex", a, tm.started)
	}
	// Past the reset the kind is usable again.
	tm.clk.Advance(2 * time.Hour)
	if s.kindExhausted(ctx, Claude) {
		t.Fatal("exhausted after the reset time passed")
	}
}

// limitStore is newStoreWithFallback with the fake kind enabled too (Settings
// validation knows only real kinds, so it is written directly), so the limited
// sessions are fake and the fallback, when withFallback, is codex.
func limitStore(t *testing.T, withFallback bool) (*Store, *fakeTmux) {
	t.Helper()
	s, tm := newStoreWithFallback(t)
	if withFallback {
		setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	}
	setEnabledKinds(t, s, `["fake","claude","codex"]`)
	return s, tm
}

func setEnabledKinds(t *testing.T, s *Store, kindsJSON string) {
	t.Helper()
	if _, err := s.DB.ExecContext(context.Background(),
		`UPDATE settings SET value_json = ? WHERE key = 'enabled_agents'`, kindsJSON); err != nil {
		t.Fatal(err)
	}
	s.Events.Notify()
}

const fakeLimitLine = "Fake usage limit reached, wait for usage to reset"

// limitedWorker spawns (under a claude orchestrator: relays to a parent of the
// exhausted kind itself are held until the reset) a worker whose pane already shows the kind's usage-limit
// line above an idle prompt, with the fake kind's limit resetting in three hours.
func limitedWorker(t *testing.T, s *Store, tm *fakeTmux, noFallback bool) (orch, w Agent, reset time.Time) {
	t.Helper()
	ctx := context.Background()
	reset = s.Now().Add(3 * time.Hour)
	fa := s.Adapters[Fake].(*adapter.Fake)
	fa.UsageLimitText, fa.UsageLimitReset = fakeLimitLine, reset
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if noFallback {
		setEnabledKinds(t, s, `["fake"]`) // the configured fallback is no longer enabled
	}
	tm.captures["limited-coder"] = []string{"◆ " + fakeLimitLine + "\n─────\n❯ \n─────\n"}
	w, _, err = s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Name: "limited-coder", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "build it"}})
	if err != nil {
		t.Fatal(err)
	}
	return orch, w, reset
}

// failedRelay is the one "failed" relay the parent got, decoded.
func failedRelay(t *testing.T, s *Store, parentID string) map[string]any {
	t.Helper()
	rows, err := s.DB.QueryContext(context.Background(), `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' ORDER BY seq`, parentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		if p["event"] == "failed" {
			out = append(out, p)
		}
	}
	if len(out) != 1 {
		t.Fatalf("failed relays = %v, want exactly one", out)
	}
	return out[0]
}

// BUG-110: a kind's pane shows its usage-limit line, then an ordinary idle
// prompt, before the first checkpoint. watchStartup used to read that as a
// healthy start and the parent heard only no_ack.
func TestStartupUsageLimitEndsTheSessionAndMarksTheKind(t *testing.T) {
	s, tm := limitStore(t, true)
	ctx := context.Background()
	orch, w, reset := limitedWorker(t, s, tm, false)
	ses, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != Failed {
		t.Fatalf("session state = %s, want failed: a usage-limited session must end, not run", ses.State)
	}
	if !slices.Contains(tm.killed, "limited-coder") {
		t.Fatalf("killed = %v, want the limited pane killed", tm.killed)
	}
	if !s.kindExhausted(ctx, Fake) {
		t.Fatal("the kind should read exhausted until its reset")
	}
	tm.clk.Advance(reset.Sub(s.Now()) + time.Minute)
	if s.kindExhausted(ctx, Fake) {
		t.Fatal("the kind should be usable again after the reset")
	}
	p := failedRelay(t, s, orch.ID)
	reason, _ := p["reason"].(string)
	for _, want := range []string{"usage limit", "Codex"} {
		if !strings.Contains(reason, want) {
			t.Errorf("relay reason = %q, want it to mention %q", reason, want)
		}
	}
}

// Without a usable fallback the relay still names the usage limit, and says no
// other agent is available.
func TestStartupUsageLimitWithoutFallbackRelaysTheLimit(t *testing.T) {
	s, tm := limitStore(t, false)
	ctx := context.Background()
	orch, _, _ := limitedWorker(t, s, tm, true)
	reason, _ := failedRelay(t, s, orch.ID)["reason"].(string)
	for _, want := range []string{"usage limit", "no other agent is available"} {
		if !strings.Contains(reason, want) {
			t.Errorf("relay reason = %q, want it to mention %q", reason, want)
		}
	}
	for _, e := range relayEvents(t, s, orch.ID) {
		if e == "no_ack" {
			t.Errorf("relays include no_ack: the limit must replace the silent no-ack")
		}
	}
	if !s.kindExhausted(ctx, Fake) {
		t.Fatal("the kind should read exhausted")
	}
}

// reconcileLimited makes worker's pane show the limit line and runs one tick.
func reconcileLimited(t *testing.T, s *Store, tm *fakeTmux) (Agent, Session) {
	t.Helper()
	s.Adapters[Fake].(*adapter.Fake).UsageLimitText = fakeLimitLine
	_, w, wSes := worker(t, s)
	if wSes.State != Running {
		t.Fatalf("setup: session state = %s, want running", wSes.State)
	}
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.captures[w.Name] = []string{"◆ " + fakeLimitLine + "\n─────\n❯ \n─────\n"}
	return w, wSes
}

// The limit line can draw after the session was already marked running: the
// reconcile tick ends it while it has no checkpoint.
func TestReconcileEndsARunningSessionThatShowsTheUsageLimitBeforeItsFirstCheckpoint(t *testing.T) {
	s, tm := limitStore(t, false)
	ctx := context.Background()
	w, _ := reconcileLimited(t, s, tm)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if ses, _ := s.LatestSession(ctx, w.ID); ses.State != Failed {
		t.Fatalf("session state = %s, want failed", ses.State)
	}
	if !s.kindExhausted(ctx, Fake) {
		t.Fatal("the kind should read exhausted")
	}
}

// A session that has checkpointed may merely print the words (quoting a log, a
// test fixture): it is left alone.
func TestReconcileLeavesACheckpointedSessionAloneWhenItPrintsTheUsageLimitText(t *testing.T) {
	s, tm := limitStore(t, false)
	ctx := context.Background()
	w, wSes := reconcileLimited(t, s, tm)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if ses, _ := s.LatestSession(ctx, w.ID); ses.State != Running || s.kindExhausted(ctx, Fake) {
		t.Fatalf("session state = %s, exhausted = %v, want it left running", ses.State, s.kindExhausted(ctx, Fake))
	}
}

// The workflow path end to end: a coder whose pane shows the limit line is
// ended, the run is auto-retried, and Retry re-resolves the kind through the
// usage fallback because the limited kind is still exhausted.
func TestWorkflowStepRespawnsOnTheFallbackAfterAUsageLimit(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	setEnabledKinds(t, s, `["fake","codex"]`)
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coderAgentID := agentIDForStep(t, s, st.ID, "build")
	coder, err := s.agentByID(ctx, coderAgentID)
	if err != nil {
		t.Fatal(err)
	}
	coderSes := agentSessionForStep(t, s, st.ID, "build")
	s.Adapters[Fake].(*adapter.Fake).UsageLimitText = fakeLimitLine
	panes(tm, Pane{Session: coder.Name, Command: "swarm-fake-agent"})
	tm.env[coder.Name] = map[string]string{"SWARM_SESSION": coderSes.ID}
	tm.captures[coder.Name] = []string{"◆ " + fakeLimitLine + "\n─────\n❯ \n─────\n"}

	// The limited pane dies with its kill; the retry's fresh pane is healthy
	// (the fake adapter is shared by every kind, so the text is cleared too).
	tm.onKill = func() {
		delete(tm.captures, coder.Name)
		s.Adapters[Fake].(*adapter.Fake).UsageLimitText = ""
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var oldState string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM sessions WHERE id = ?`, coderSes.ID).Scan(&oldState); err != nil || oldState != "failed" {
		t.Fatalf("limited session state = %q (%v), want failed", oldState, err)
	}
	got, err := s.agentByID(ctx, coderAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != Codex || !strings.Contains(got.KindReason, "usage") {
		t.Fatalf("coder after the limit = kind %s reason %q, want respawned on codex via the usage fallback", got.Kind, got.KindReason)
	}
	var state string
	var autoRetries int
	if err := s.DB.QueryRowContext(ctx, `SELECT state, auto_retries FROM workflow_runs
		WHERE workflow_id = ? AND step_id = 'build'`, st.ID).Scan(&state, &autoRetries); err != nil {
		t.Fatal(err)
	}
	if state != "active" || autoRetries != 1 {
		t.Fatalf("build run = %s with %d auto-retries, want active with 1", state, autoRetries)
	}
	if ses, _ := s.LatestSession(ctx, coderAgentID); ses.ID == coderSes.ID || !ses.State.Live() {
		t.Fatalf("latest session = %+v, want a new live one", ses)
	}
}
