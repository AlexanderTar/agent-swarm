package runtime

import (
	"context"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/adapter"
	"github.com/AlexanderTar/swarmrun/internal/kinds"
)

// museObserverFake is a Muse-kind adapter whose ObserveModel returns canned
// answers and counts calls.
type museObserverFake struct {
	*adapter.Fake
	model, effort string
	ok            bool
	gotID         string
	calls         int
}

func (m *museObserverFake) ObserveModel(_, providerSessionID string) (string, string, bool) {
	m.calls++
	m.gotID = providerSessionID
	return m.model, m.effort, m.ok
}

func TestReconcileRecordsMuseObservedModel(t *testing.T) {
	reconcileRecordsObservedModel(t, kinds.Muse, "muse-old", "muse-new")
}

// Grok sessions write their model to the grok home's summary.json and send no
// model in any hook, so the reconcile tick observes them like Muse's.
func TestReconcileRecordsGrokObservedModel(t *testing.T) {
	reconcileRecordsObservedModel(t, kinds.Grok, "grok-4.6", "grok-4.7")
}

func reconcileRecordsObservedModel(t *testing.T, kind kinds.AgentKind, oldModel, newModel string) {
	t.Helper()
	s, tm, fa := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "MuseObs", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = ?, model = ? WHERE id = ?`, kind, oldModel, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET provider_session_id = 'prov-1', state = 'running' WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}
	obs := &museObserverFake{Fake: fa, model: newModel, effort: "low", ok: true}
	s.Adapters[kind] = obs
	panes(tm, Pane{Session: ses.TmuxName})
	row := func() (string, string) {
		var m, e string
		if err := s.DB.QueryRowContext(ctx, `SELECT model, COALESCE(effort,'') FROM agents WHERE id = ?`, a.ID).Scan(&m, &e); err != nil {
			t.Fatal(err)
		}
		return m, e
	}
	changes := func() int {
		evs, _ := s.Events.After(ctx, 0, 100000)
		n := 0
		for _, e := range evs {
			if e.Type == "agent.changed" {
				n++
			}
		}
		return n
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if m, e := row(); m != newModel || e != "low" {
		t.Fatalf("row = %q/%q, want %s/low", m, e, newModel)
	}
	if obs.gotID != "prov-1" {
		t.Fatalf("observer got provider session %q, want prov-1", obs.gotID)
	}
	n := changes()
	// unchanged observation: no further writes or events
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := changes(); got != n {
		t.Fatalf("unchanged observation published events: %d -> %d", n, got)
	}
	// a miss never overwrites
	obs.ok = false
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if m, e := row(); m != newModel || e != "low" {
		t.Fatalf("row after miss = %q/%q", m, e)
	}
}
