package settings

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

var ctx = context.Background()

var all5 = []string{"low", "medium", "high", "xhigh", "max"}

var catalogs = map[runtime.AgentKind][]catalog.CatalogModel{
	runtime.Claude: {
		{ID: "claude-fable-5-1", Aliases: []string{"fable"}, Label: "Claude Fable 5.1", Efforts: all5, AdvisorCapable: true},
		{ID: "claude-opus-5", Aliases: []string{"opus"}, Label: "Claude Opus 5", Efforts: all5, AdvisorCapable: true},
		{ID: "claude-sonnet-5", Aliases: []string{"sonnet"}, Label: "Claude Sonnet 5", Efforts: all5, AdvisorCapable: true},
		{ID: "claude-haiku-4-5-20251001", Aliases: []string{"haiku"}, Label: "Claude Haiku 4.5", Efforts: []string{}},
	},
	runtime.Codex: {{ID: "gpt-6-astra", Label: "GPT-6-Astra", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"}},
}

func newStore(t *testing.T, installed ...runtime.AgentKind) *Store {
	d := dbtest.Open(t)
	now := func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }
	return &Store{DB: d, Events: events.New(d, now), Now: now,
		ModelsFor: func(_ context.Context, k runtime.AgentKind) ([]catalog.CatalogModel, string, error) {
			def := ""
			if k == runtime.Codex {
				def = "gpt-6-astra"
			}
			return catalogs[k], def, nil
		},
		Installed: func(context.Context) []runtime.AgentKind { return installed },
	}
}

func verr(t *testing.T, err error, msg string) {
	t.Helper()
	var v *ValidationError
	if !errors.As(err, &v) || v.Message != msg {
		t.Fatalf("err = %v, want %q", err, msg)
	}
}

func TestDefaults(t *testing.T) {
	d := Defaults(nil)
	if !slices.Equal(d.EnabledAgents, []runtime.AgentKind{runtime.Claude}) {
		t.Errorf("enabled = %v", d.EnabledAgents)
	}
	d = Defaults([]runtime.AgentKind{runtime.Agy, runtime.Claude, runtime.Codex})
	if !slices.Equal(d.EnabledAgents, []runtime.AgentKind{runtime.Claude, runtime.Codex, runtime.Agy}) {
		t.Errorf("enabled = %v", d.EnabledAgents)
	}
	want := map[runtime.Role]RoleDefault{
		runtime.RoleOrchestrator: {runtime.Claude, "opus", ""},
		runtime.RoleCoder:        {runtime.Claude, "sonnet", ""},
		runtime.RoleReviewer:     {runtime.Claude, "opus", ""},
		runtime.RoleUIReviewer:   {runtime.Claude, "opus", ""},
		runtime.RoleResearcher:   {runtime.Claude, "sonnet", ""},
		runtime.RoleDebugger:     {runtime.Claude, "opus", ""},
		runtime.RoleMechanical:   {runtime.Claude, "haiku", ""},
		runtime.RoleAdvisor:      {runtime.Claude, "fable", ""},
	}
	if len(d.Roles) != 8 {
		t.Fatalf("roles = %v", d.Roles)
	}
	for r, w := range want {
		if d.Roles[r] != w {
			t.Errorf("%s = %+v", r, d.Roles[r])
		}
	}
	for _, lvl := range []string{"info", "attention", "action"} {
		if d.Notifications[lvl] != (NotifyPref{Center: true, Sound: true}) {
			t.Errorf("notifications[%s] = %+v", lvl, d.Notifications[lvl])
		}
	}
	if d.MaxOrchestrators != 3 || d.MaxAgents != 8 || d.MaxAgentsPerRoot != 4 || d.ScanIntervalSec != 21600 ||
		d.UsagePollSec != 300 || d.PauseDeadlineSec != 120 || d.MenubarCompact ||
		!slices.Equal(d.ScanExcludes, []string{"~/Library", "~/.Trash", "~/Downloads"}) {
		t.Errorf("defaults = %+v", d)
	}
}

func TestGetPutRoundTrip(t *testing.T) {
	s := newStore(t, runtime.Claude, runtime.Codex)
	got, err := s.Get(ctx)
	if err != nil || !slices.Equal(got.EnabledAgents, []runtime.AgentKind{runtime.Claude, runtime.Codex}) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	got.MaxAgents = 12
	got.Roles[runtime.RoleCoder] = RoleDefault{runtime.Codex, "gpt-6-astra", "high"}
	got.Roles[runtime.RoleAdvisor] = RoleDefault{Model: NoAdvisor}
	got.ScanExcludes = []string{"~/Downloads", "~/Movies"}
	saved, err := s.Put(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := s.Get(ctx)
	if again.MaxAgents != 12 || again.Roles[runtime.RoleCoder] != saved.Roles[runtime.RoleCoder] ||
		again.Roles[runtime.RoleAdvisor].Model != NoAdvisor || !slices.Equal(again.ScanExcludes, []string{"~/Downloads", "~/Movies"}) {
		t.Fatalf("after put = %+v", again)
	}
	evs, _ := s.Events.After(ctx, 0, 10)
	if len(evs) != 1 || evs[0].Type != events.SettingsChanged {
		t.Fatalf("events = %+v", evs)
	}
	// a partial table still yields defaults for the rest
	s.DB.Exec(`DELETE FROM settings WHERE key <> 'max_agents'`)
	partial, _ := s.Get(ctx)
	if partial.MaxAgents != 12 || partial.MaxOrchestrators != 3 || partial.Roles[runtime.RoleCoder].Agent != runtime.Claude {
		t.Fatalf("partial = %+v", partial)
	}
}

func TestPutValidation(t *testing.T) {
	s := newStore(t, runtime.Claude, runtime.Codex)
	base, _ := s.Get(ctx)
	clone := func(edit func(*Settings)) Settings {
		c := base
		c.Roles = map[runtime.Role]RoleDefault{}
		for k, v := range base.Roles {
			c.Roles[k] = v
		}
		c.EnabledAgents = slices.Clone(base.EnabledAgents)
		edit(&c)
		return c
	}
	cases := []struct {
		edit func(*Settings)
		msg  string
	}{
		{func(c *Settings) { c.Roles[runtime.RoleCoder] = RoleDefault{runtime.Claude, "claude-9", ""} }, "Choose a model available for this agent."},
		{func(c *Settings) { c.Roles[runtime.RoleCoder] = RoleDefault{runtime.Claude, "sonnet", "ultra"} }, "ultra isn't available for Claude Sonnet 5."},
		{func(c *Settings) { c.Roles[runtime.RoleMechanical] = RoleDefault{runtime.Claude, "haiku", "low"} }, "low isn't available for Claude Haiku 4.5."},
		{func(c *Settings) { c.Roles[runtime.RoleAdvisor] = RoleDefault{runtime.Claude, "haiku", ""} }, "Choose a model available for this agent."},
		{func(c *Settings) { c.Roles[runtime.RoleCoder] = RoleDefault{runtime.Agy, "gemini", ""} }, "agy isn't enabled. Choose an enabled agent."},
		{func(c *Settings) { c.EnabledAgents = append(c.EnabledAgents, "opencode") }, "Unknown agent opencode."},
		{func(c *Settings) { c.EnabledAgents = nil }, "At least one agent must stay enabled."},
		{func(c *Settings) { c.Roles["janitor"] = RoleDefault{runtime.Claude, "opus", ""} }, "Unknown role janitor."},
		{func(c *Settings) { c.MaxOrchestrators = 9 }, "Maximum concurrent orchestrators must be between 1 and 8."},
		{func(c *Settings) { c.MaxOrchestrators = 0 }, "Maximum concurrent orchestrators must be between 1 and 8."},
		{func(c *Settings) { c.MaxAgents = 33 }, "Maximum concurrent agents must be between 1 and 32."},
		{func(c *Settings) { c.MaxAgentsPerRoot = 17 }, "Maximum concurrent agents per item must be between 1 and 16."},
		{func(c *Settings) { c.PauseDeadlineSec = 29 }, "Pause deadline must be between 30 and 600 seconds."},
		{func(c *Settings) { c.PauseDeadlineSec = 601 }, "Pause deadline must be between 30 and 600 seconds."},
		{func(c *Settings) { c.ScanIntervalSec = 3599 }, "Repository scans must be at least 3600 seconds apart."},
		{func(c *Settings) { c.UsagePollSec = 59 }, "Usage polling must be at least 60 seconds apart."},
	}
	for _, c := range cases {
		_, err := s.Put(ctx, clone(c.edit))
		verr(t, err, c.msg)
	}
	if n := countRows(t, s); n != 0 {
		t.Fatalf("failed puts wrote %d rows", n)
	}
	ok := clone(func(c *Settings) {
		c.Roles[runtime.RoleCoder] = RoleDefault{runtime.Claude, "claude-sonnet-5", "xhigh"}
		c.Roles[runtime.RoleAdvisor] = RoleDefault{runtime.Claude, "opus", ""}
		c.MaxOrchestrators, c.MaxAgents, c.MaxAgentsPerRoot, c.PauseDeadlineSec = 1, 32, 16, 600
	})
	if _, err := s.Put(ctx, ok); err != nil {
		t.Fatalf("boundary values: %v", err)
	}
}

func countRows(t *testing.T, s *Store) int {
	var n int
	s.DB.QueryRow(`SELECT count(*) FROM settings`).Scan(&n)
	return n
}

func TestModelGoneFromCatalog(t *testing.T) {
	s := newStore(t, runtime.Claude)
	cur, _ := s.Get(ctx)
	cur.Roles[runtime.RoleCoder] = RoleDefault{runtime.Claude, "claude-sonnet-5", ""}
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatal(err)
	}
	saved := catalogs[runtime.Claude]
	catalogs[runtime.Claude] = saved[:2] // sonnet and haiku disappear
	defer func() { catalogs[runtime.Claude] = saved }()
	cur, _ = s.Get(ctx)
	cur.MaxAgents = 9
	_, err := s.Put(ctx, cur)
	verr(t, err, "claude-sonnet-5 is no longer offered by Claude.")
}

func TestDisablingAnAgentSwitchesDefaults(t *testing.T) {
	s := newStore(t, runtime.Claude, runtime.Codex)
	cur, _ := s.Get(ctx)
	cur.Roles[runtime.RoleCoder] = RoleDefault{runtime.Codex, "gpt-6-astra", "high"}
	cur, err := s.Put(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	cur.EnabledAgents = []runtime.AgentKind{runtime.Claude}
	got, err := s.Put(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if got.Roles[runtime.RoleCoder] != (RoleDefault{runtime.Claude, "sonnet", ""}) {
		t.Fatalf("coder = %+v", got.Roles[runtime.RoleCoder])
	}

	got.EnabledAgents = []runtime.AgentKind{runtime.Codex, runtime.Claude}
	got, _ = s.Put(ctx, got)
	got.EnabledAgents = []runtime.AgentKind{runtime.Codex}
	got, err = s.Put(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runtime.SettingsRoles {
		want := RoleDefault{runtime.Codex, "gpt-6-astra", ""}
		if got.Roles[r] != want {
			t.Errorf("%s = %+v, want %+v", r, got.Roles[r], want)
		}
	}
}

func TestEmptyCatalogSkipsModelChecks(t *testing.T) {
	s := newStore(t, runtime.Claude, runtime.Agy)
	cur, _ := s.Get(ctx)
	cur.Roles[runtime.RoleResearcher] = RoleDefault{runtime.Agy, "gemini-3.8-flash", "high"}
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatalf("agy has no cached models yet: %v", err)
	}
	cur.Roles[runtime.RoleResearcher] = RoleDefault{runtime.Agy, "", ""}
	_, err := s.Put(ctx, cur)
	verr(t, err, "Choose a model available for this agent.")
}

func TestPutDoesNotMutateCallerMaps(t *testing.T) {
	s := newStore(t, runtime.Claude)
	cur, _ := s.Get(ctx)
	delete(cur.Roles, runtime.RoleDebugger)
	delete(cur.Notifications, "info")
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if _, ok := cur.Roles[runtime.RoleDebugger]; ok {
		t.Error("Put wrote into the caller's roles map")
	}
	if _, ok := cur.Notifications["info"]; ok {
		t.Error("Put wrote into the caller's notifications map")
	}
}
