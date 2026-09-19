package usagegate

import (
	"context"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/usage"
)

var now = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func TestExhaustedAtCap(t *testing.T) {
	later := now.Add(time.Hour)
	snap := usage.Snapshot{Meters: []usage.Meter{{ID: "five_hour", UsedPct: 100, ResetsAt: &later}}}
	if !Exhausted(snap, now) {
		t.Fatal("want exhausted at UsedPct 100")
	}
}

func TestNotExhaustedBelowCap(t *testing.T) {
	snap := usage.Snapshot{Meters: []usage.Meter{{ID: "five_hour", UsedPct: 99.9}}}
	if Exhausted(snap, now) {
		t.Fatal("99.9%% must not count as exhausted")
	}
}

func TestNotExhaustedWhenStale(t *testing.T) {
	snap := usage.Snapshot{Stale: true, Meters: []usage.Meter{{ID: "five_hour", UsedPct: 100}}}
	if Exhausted(snap, now) {
		t.Fatal("a stale snapshot must never count as exhausted")
	}
}

func TestNotExhaustedOnFetchError(t *testing.T) {
	snap := usage.Snapshot{Error: "network blip", Meters: []usage.Meter{{ID: "five_hour", UsedPct: 100}}}
	if Exhausted(snap, now) {
		t.Fatal("a fetch error must never count as exhausted, even alongside a 100%% meter")
	}
}

func TestNotExhaustedNoMeters(t *testing.T) {
	if Exhausted(usage.Snapshot{}, now) {
		t.Fatal("no meters at all must not count as exhausted")
	}
}

func TestNotExhaustedResetAlreadyPassed(t *testing.T) {
	past := now.Add(-time.Hour)
	snap := usage.Snapshot{Meters: []usage.Meter{{ID: "five_hour", UsedPct: 100, ResetsAt: &past}}}
	if Exhausted(snap, now) {
		t.Fatal("a 100%% meter whose reset time has already passed must not count as a live block")
	}
}

// TestExhaustedOnNonHeadlineMeter proves the heuristic checks every meter,
// not just HeadlineID's display meter: a weekly cap can be hit while the 5h
// headline still reads low, and that must still block further work.
func TestExhaustedOnNonHeadlineMeter(t *testing.T) {
	snap := usage.Snapshot{
		HeadlineID: "five_hour",
		Meters: []usage.Meter{
			{ID: "five_hour", UsedPct: 20},
			{ID: "seven_day_sonnet", UsedPct: 100},
		},
	}
	if !Exhausted(snap, now) {
		t.Fatal("a non-headline meter at 100%% must still count as exhausted")
	}
}

// ---- Gate, against a real Poller ----

// stubModelsFor answers for every default role (opus/sonnet/haiku/fable):
// settings.Store.Put's own validate() calls it for every role default, even
// ones this test never touches.
func stubModelsFor(context.Context, kinds.AgentKind) ([]catalog.CatalogModel, string, error) {
	return []catalog.CatalogModel{
		{ID: "opus", Label: "Opus", Efforts: []string{}, AdvisorCapable: true},
		{ID: "sonnet", Label: "Sonnet", Efforts: []string{}, AdvisorCapable: true},
		{ID: "haiku", Label: "Haiku", Efforts: []string{}, AdvisorCapable: false},
		{ID: "fable", Label: "Fable", Efforts: []string{}, AdvisorCapable: true},
	}, "opus", nil
}

func newSettings(t *testing.T, d *db.DB, enabled ...kinds.AgentKind) *settings.Store {
	t.Helper()
	st := &settings.Store{DB: d, Events: events.New(d, time.Now), Now: time.Now, ModelsFor: stubModelsFor}
	cfg, err := st.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnabledAgents = enabled
	if _, err := st.Put(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestGateExhaustedAfterRefresh(t *testing.T) {
	d := dbtest.Open(t)
	st := newSettings(t, d, kinds.Claude)
	src := usage.Source{Agent: runtime.Claude, Fetch: func(context.Context) ([]usage.Meter, string, error) {
		return []usage.Meter{{ID: "five_hour", UsedPct: 100}}, "five_hour", nil
	}}
	p := &usage.Poller{DB: d, Events: events.New(d, time.Now), Settings: st, Now: time.Now, Sources: []usage.Source{src}}
	if err := p.RefreshOne(context.Background(), runtime.Claude); err != nil {
		t.Fatal(err)
	}
	g := &Gate{Poller: p}
	if !g.Exhausted(context.Background(), runtime.Claude) {
		t.Fatal("want exhausted after a 100%% refresh")
	}
}

func TestGateFalseWhenNeverPolled(t *testing.T) {
	d := dbtest.Open(t)
	st := newSettings(t, d, kinds.Claude)
	p := &usage.Poller{DB: d, Events: events.New(d, time.Now), Settings: st, Now: time.Now}
	g := &Gate{Poller: p}
	if g.Exhausted(context.Background(), runtime.Codex) {
		t.Fatal("a kind that was never polled must not count as exhausted")
	}
}
