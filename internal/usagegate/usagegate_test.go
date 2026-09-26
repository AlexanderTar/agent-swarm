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

// TestNonHeadlineMeterAtCapDoesNotCount was originally written the other way
// (asserting that any meter at 100% -- headline or not -- counts as
// exhausted). That was wrong for a real source's shape: internal/usage's
// Claude source (claude.go) emits per-model weekly meters like
// seven_day_opus and seven_day_<model> alongside the all-models headline;
// hitting one of those does not block every other model on that same agent
// kind. Rewritten (not deleted -- see CLAUDE.md's test discipline) to assert
// the corrected contract: only the headline meter's reading counts, so a
// per-model cap being hit while the headline reads low must NOT be treated
// as the whole agent kind being exhausted, or a spawn that would actually
// succeed (e.g. a Sonnet-based role while only the Opus weekly cap is hit)
// gets wrongly refused or redirected.
func TestNonHeadlineMeterAtCapDoesNotCount(t *testing.T) {
	snap := usage.Snapshot{
		HeadlineID: "five_hour",
		Meters: []usage.Meter{
			{ID: "five_hour", UsedPct: 20},
			{ID: "seven_day_opus", UsedPct: 100},
		},
	}
	if Exhausted(snap, now) {
		t.Fatal("a non-headline (per-model) meter at 100%% must not count as the kind being exhausted")
	}
}

// TestExhaustedWhenTheHeadlineMeterIsAtCap is the positive counterpart: the
// headline meter itself at 100%% does count, regardless of other meters.
func TestExhaustedWhenTheHeadlineMeterIsAtCap(t *testing.T) {
	snap := usage.Snapshot{
		HeadlineID: "five_hour",
		Meters: []usage.Meter{
			{ID: "five_hour", UsedPct: 100},
			{ID: "seven_day_opus", UsedPct: 10},
		},
	}
	if !Exhausted(snap, now) {
		t.Fatal("the headline meter at 100%% must count as exhausted")
	}
}

// TestHeadlineIDMissingFallsBackToFirstMeter mirrors the menubar's own
// UsageSnapshot.headline convention: an empty or non-matching HeadlineID
// falls back to the first meter, rather than being treated as "no headline
// at all".
func TestHeadlineIDMissingFallsBackToFirstMeter(t *testing.T) {
	snap := usage.Snapshot{Meters: []usage.Meter{{ID: "only", UsedPct: 100}}}
	if !Exhausted(snap, now) {
		t.Fatal("a single meter with no HeadlineID set must still be read as the headline")
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

// agy's Claude & GPT quota at 100 % must not mark agy exhausted while its
// native Gemini quota is barely used (live 2026-09-26: a researcher fell
// back to Claude for exactly this reason).
func TestAgyNotExhaustedByExtraModels(t *testing.T) {
	meters, headline, err := usage.ParseAgyQuota([]byte(`{"groups":[
	  {"displayName":"Gemini Models","buckets":[
	    {"window":"5h","remainingFraction":0.94,"resetTime":"2026-09-19T15:00:00Z"}]},
	  {"displayName":"Claude and GPT models","buckets":[
	    {"window":"5h","remainingFraction":0,"resetTime":"2026-09-19T14:00:00Z"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	snap := usage.Snapshot{Agent: kinds.Agy, Meters: meters, HeadlineID: headline}
	if Exhausted(snap, now) {
		t.Fatalf("agy exhausted by its extra-model meter: %+v headline %q", meters, headline)
	}
}
