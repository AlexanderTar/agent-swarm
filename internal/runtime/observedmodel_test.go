package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/events"
)

const observedAgyCatalog = `[
 {"id":"gemini-3.8-flash","label":"Flash","efforts":["medium","high"],"default_effort":"high","effort_encoding":"slug","launch_ids":{"medium":"gemini-3.8-flash-medium","high":"gemini-3.8-flash-high"},"advisor_capable":false},
 {"id":"gemini-3.9-pro","label":"Pro","efforts":["low","high"],"default_effort":"high","effort_encoding":"slug","launch_ids":{"low":"gemini-3.9-pro-low","high":"gemini-3.9-pro-high"},"advisor_capable":false}]`

func seedObservedAgy(t *testing.T, s *Store) (Agent, Session) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('agy','agy-1',?,'gemini-3.8-flash','test',1,1)`, observedAgyCatalog); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE settings SET value_json = '["claude", "fake", "agy"]' WHERE key = 'enabled_agents'`); err != nil {
		t.Fatal(err)
	}
	s.Adapters[Agy] = s.Adapters[Fake]
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Observed", Intent: "feature",
		Kind: Agy, Model: "gemini-3.8-flash", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a, ses
}

func countAgentChangedEvents(t *testing.T, s *Store) int {
	t.Helper()
	evs, _ := s.Events.After(context.Background(), 0, 10000)
	n := 0
	for _, e := range evs {
		if e.Type == events.AgentChanged {
			n++
		}
	}
	return n
}

func TestRecordObservedModelUpdatesRowReasonAndPublishesOnce(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	a, ses := seedObservedAgy(t, s)
	before := countAgentChangedEvents(t, s)

	changed, err := s.RecordObservedModel(ctx, ses.ID, "gemini-3.8-flash-medium", "", "hook")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want true,nil", changed, err)
	}
	got, _ := s.agentByID(ctx, a.ID)
	if got.Model != "gemini-3.8-flash" || got.Effort != "medium" {
		t.Errorf("row = %q/%q, want gemini-3.8-flash/medium", got.Model, got.Effort)
	}
	if !strings.Contains(got.KindReason, "changed in session (hook)") {
		t.Errorf("kind_reason = %q", got.KindReason)
	}
	if n := countAgentChangedEvents(t, s); n != before+1 {
		t.Errorf("agent.changed = %d, want %d", n, before+1)
	}

	// a new model with a slug-encoded effort
	if changed, err = s.RecordObservedModel(ctx, ses.ID, "gemini-3.9-pro-low", "", "hook"); err != nil || !changed {
		t.Fatalf("second change: %v %v", changed, err)
	}
	got, _ = s.agentByID(ctx, a.ID)
	if got.Model != "gemini-3.9-pro" || got.Effort != "low" {
		t.Errorf("row = %q/%q, want gemini-3.9-pro/low", got.Model, got.Effort)
	}
}

func TestRecordObservedModelIsIdempotent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	a, ses := seedObservedAgy(t, s)
	before := countAgentChangedEvents(t, s)
	row0, _ := s.agentByID(ctx, a.ID)

	// current model: base id, launch slug of the current effort, and the
	// empty-effort form must all be no-ops
	for _, m := range []string{"gemini-3.8-flash", "gemini-3.8-flash-high", ""} {
		changed, err := s.RecordObservedModel(ctx, ses.ID, m, "", "hook")
		if err != nil || changed {
			t.Fatalf("model %q: changed=%v err=%v, want false,nil", m, changed, err)
		}
	}
	// default-effort row (effort "") observed as its default slug
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET effort = NULL WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if changed, _ := s.RecordObservedModel(ctx, ses.ID, "gemini-3.8-flash-high", "", "hook"); changed {
		t.Error("default-effort slug of an unset-effort row must be a no-op")
	}
	if n := countAgentChangedEvents(t, s); n != before {
		t.Errorf("agent.changed = %d, want %d", n, before)
	}
	row1, _ := s.agentByID(ctx, a.ID)
	if row1.KindReason != row0.KindReason || row1.Model != row0.Model {
		t.Errorf("row mutated: %+v -> %+v", row0, row1)
	}

	// the same observed change twice publishes once
	s.RecordObservedModel(ctx, ses.ID, "gemini-3.8-flash-medium", "", "hook")
	mid := countAgentChangedEvents(t, s)
	if changed, _ := s.RecordObservedModel(ctx, ses.ID, "gemini-3.8-flash-medium", "", "hook"); changed {
		t.Error("repeat of the same observation must be a no-op")
	}
	if n := countAgentChangedEvents(t, s); n != mid {
		t.Errorf("repeat published: %d -> %d", mid, n)
	}
}

func TestRecordObservedModelEmptyEffortKeepsStoredEffort(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	a, ses := seedObservedAgy(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET effort = 'medium' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	// a bare base id carries no effort: model changes, effort stays
	if changed, err := s.RecordObservedModel(ctx, ses.ID, "gemini-3.9-pro", "", "hook"); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := s.agentByID(ctx, a.ID)
	if got.Model != "gemini-3.9-pro" || got.Effort != "medium" {
		t.Errorf("row = %q/%q, want gemini-3.9-pro/medium", got.Model, got.Effort)
	}
}

func TestRecordObservedModelCursorDefaultIsModelDefaultWithNoEffort(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	a, ses := seedObservedAgy(t, s)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('cursor','cursor-1','[{"id":"auto","label":"Auto","efforts":[],"default_effort":"","effort_encoding":"slug","is_default":true,"advisor_capable":false},{"id":"gpt-5.3-codex","label":"Codex","efforts":["low","high"],"default_effort":"high","effort_encoding":"slug","launch_ids":{"low":"gpt-5.3-codex-low","high":"gpt-5.3-codex-high"},"advisor_capable":false}]','auto','test',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind='cursor', model='gpt-5.3-codex', effort='low' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.RecordObservedModel(ctx, ses.ID, "default", "", "hook"); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := s.agentByID(ctx, a.ID)
	if got.Model != "default" || got.Effort != "" {
		t.Errorf("row = %q/%q, want default/empty", got.Model, got.Effort)
	}
	// "default" and "auto" are the same model: no further writes
	if changed, _ := s.RecordObservedModel(ctx, ses.ID, "default", "", "hook"); changed {
		t.Error("repeat of default must be a no-op")
	}
	if changed, _ := s.RecordObservedModel(ctx, ses.ID, "auto", "", "hook"); changed {
		t.Error("auto equals default; must be a no-op")
	}
}

// After an observed change, the wake relaunch (resolveLaunchModel via the
// agents row) must use the observed model and effort, not the spawn-time pair.
func TestWakeUsesObservedModelAndEffort(t *testing.T) {
	s, tm, fa := newStore(t)
	fa.WakeOK = true
	ctx := context.Background()
	a, ses := seedObservedAgy(t, s)
	if _, err := s.RecordObservedModel(ctx, ses.ID, "gemini-3.9-pro-low", "", "hook"); err != nil {
		t.Fatal(err)
	}
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "agy"})
	tm.clk.Advance(25 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if fa.LastWakeTarget.Model != "gemini-3.9-pro-low" {
		t.Fatalf("WakeTarget.Model = %q, want the observed launch id gemini-3.9-pro-low", fa.LastWakeTarget.Model)
	}
}
