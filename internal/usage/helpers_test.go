package usage

import (
	"context"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// stubModelsFor is settings.Store.ModelsFor's minimal stand-in: Put's own
// validate() calls it for every role default, even ones this package's tests
// never touch, so it has to answer for any agent kind with something that
// satisfies every default role (opus/sonnet/haiku/fable).
func stubModelsFor(context.Context, kinds.AgentKind) ([]catalog.CatalogModel, string, error) {
	return []catalog.CatalogModel{
		{ID: "opus", Label: "Opus", Efforts: []string{}, AdvisorCapable: true},
		{ID: "sonnet", Label: "Sonnet", Efforts: []string{}, AdvisorCapable: true},
		{ID: "haiku", Label: "Haiku", Efforts: []string{}, AdvisorCapable: false},
		{ID: "fable", Label: "Fable", Efforts: []string{}, AdvisorCapable: true},
	}, "opus", nil
}

// clk is the movable clock every test in this package uses.
type clk struct{ t time.Time }

func newClk() *clk { return &clk{t: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)} }

func (c *clk) Now() time.Time          { return c.t }
func (c *clk) Advance(d time.Duration) { c.t = c.t.Add(d) }

// stubSource is a Source that records how often it was asked and always succeeds.
func stubSource(kind runtime.AgentKind, calls map[runtime.AgentKind]int) Source {
	return Source{Agent: kind, Fetch: func(ctx context.Context) ([]Meter, string, error) {
		calls[kind]++
		return []Meter{{ID: string(kind) + "_5h", Label: "5h", Window: "5h", UsedPct: 10}},
			string(kind) + "_5h", nil
	}}
}

// failingSource always fails, so the poller's keep-the-old-meters rule is tested.
func failingSource(kind runtime.AgentKind, err error) Source {
	return Source{Agent: kind, Fetch: func(ctx context.Context) ([]Meter, string, error) {
		return nil, "", err
	}}
}

// settingsWith opens a settings store with usage_poll_sec set.
//
// The brief's own note said to check settings' exact method name before
// relying on it: origin/main has Put, not Update.
func settingsWith(t *testing.T, d *db.DB, pollSec int) *settings.Store {
	t.Helper()
	st := &settings.Store{DB: d, Events: events.New(d, time.Now), Now: time.Now, ModelsFor: stubModelsFor}
	cfg, err := st.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.UsagePollSec = pollSec
	if _, err := st.Put(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return st
}

// settingsWithEnabled is settingsWith(300) plus an explicit enabled_agents list.
func settingsWithEnabled(t *testing.T, d *db.DB, kinds ...runtime.AgentKind) *settings.Store {
	t.Helper()
	st := settingsWith(t, d, 300)
	setEnabled(t, st, kinds...)
	return st
}

func setEnabled(t *testing.T, st *settings.Store, kinds ...runtime.AgentKind) {
	t.Helper()
	cfg, err := st.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnabledAgents = kinds
	if _, err := st.Put(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}
