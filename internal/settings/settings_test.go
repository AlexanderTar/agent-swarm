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
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

var ctx = context.Background()

var all5 = []string{"low", "medium", "high", "xhigh", "max"}

var catalogs = map[kinds.AgentKind][]catalog.CatalogModel{
	kinds.Claude: {
		{ID: "claude-fable-5-1", Aliases: []string{"fable"}, Label: "Claude Fable 5.1", Efforts: all5, AdvisorCapable: true},
		{ID: "claude-opus-5", Aliases: []string{"opus"}, Label: "Claude Opus 5", Efforts: all5, AdvisorCapable: true},
		{ID: "claude-sonnet-5", Aliases: []string{"sonnet"}, Label: "Claude Sonnet 5", Efforts: all5, AdvisorCapable: true},
		{ID: "claude-haiku-4-5-20251001", Aliases: []string{"haiku"}, Label: "Claude Haiku 4.5", Efforts: []string{}},
	},
	kinds.Codex: {{ID: "gpt-6-astra", Label: "GPT-6-Astra", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"}},
}

func newStore(t *testing.T, installed ...kinds.AgentKind) *Store {
	d := dbtest.Open(t)
	now := func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }
	return &Store{DB: d, Events: events.New(d, now), Now: now,
		ModelsFor: func(_ context.Context, k kinds.AgentKind) ([]catalog.CatalogModel, string, error) {
			def := ""
			if k == kinds.Codex {
				def = "gpt-6-astra"
			}
			return catalogs[k], def, nil
		},
		Installed: func(context.Context) []kinds.AgentKind { return installed },
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
	if !slices.Equal(d.EnabledAgents, []kinds.AgentKind{kinds.Claude}) {
		t.Errorf("enabled = %v", d.EnabledAgents)
	}
	d = Defaults([]kinds.AgentKind{kinds.Agy, kinds.Claude, kinds.Codex})
	if !slices.Equal(d.EnabledAgents, []kinds.AgentKind{kinds.Claude, kinds.Codex, kinds.Agy}) {
		t.Errorf("enabled = %v", d.EnabledAgents)
	}
	want := map[kinds.Role]RoleDefault{
		kinds.RoleOrchestrator: {kinds.Claude, "opus", "", ""},
		kinds.RoleCoder:        {kinds.Claude, "sonnet", "", ""},
		kinds.RoleReviewer:     {kinds.Claude, "opus", "", ""},
		kinds.RoleUIReviewer:   {kinds.Claude, "opus", "", ""},
		kinds.RoleResearcher:   {kinds.Claude, "sonnet", "", ""},
		kinds.RoleDebugger:     {kinds.Claude, "opus", "", ""},
		kinds.RoleMechanical:   {kinds.Claude, "haiku", "", ""},
		kinds.RoleAdvisor:      {kinds.Claude, "fable", "", ""},
		kinds.RoleDesigner:     {kinds.Claude, "opus", "", ""},
	}
	if len(d.Roles) != 9 {
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
	if d.MaxConcurrentAgents != 4 || d.MaxAgentsPerRoot != 4 || d.ScanIntervalSec != 21600 ||
		d.UsagePollSec != 300 || d.PauseDeadlineSec != 120 || d.MenubarCompact ||
		!slices.Equal(d.ScanExcludes, []string{"~/Library", "~/.Trash", "~/Downloads", "~/Music", "~/Pictures", "~/Movies"}) {
		t.Errorf("defaults = %+v", d)
	}
	if want := (RoleDefault{kinds.Claude, "sonnet", "", ""}); d.FallbackDefault != want {
		t.Errorf("fallback default = %+v, want %+v", d.FallbackDefault, want)
	}
}

// TestSettingsDefaultsIncludeDesigner is spec A5: designer defaults to
// claude/opus like reviewer/ui_reviewer/debugger.
func TestSettingsDefaultsIncludeDesigner(t *testing.T) {
	d := Defaults(nil)
	want := RoleDefault{kinds.Claude, "opus", "", ""}
	if d.Roles[kinds.RoleDesigner] != want {
		t.Errorf("designer default = %+v, want %+v", d.Roles[kinds.RoleDesigner], want)
	}
}

func TestGetPutRoundTrip(t *testing.T) {
	s := newStore(t, kinds.Claude, kinds.Codex)
	got, err := s.Get(ctx)
	if err != nil || !slices.Equal(got.EnabledAgents, []kinds.AgentKind{kinds.Claude, kinds.Codex}) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	got.MaxConcurrentAgents = 12
	got.Roles[kinds.RoleCoder] = RoleDefault{kinds.Codex, "gpt-6-astra", "high", ""}
	got.Roles[kinds.RoleAdvisor] = RoleDefault{Model: NoAdvisor}
	got.ScanExcludes = []string{"~/Downloads", "~/Movies"}
	saved, err := s.Put(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := s.Get(ctx)
	if again.MaxConcurrentAgents != 12 || again.Roles[kinds.RoleCoder] != saved.Roles[kinds.RoleCoder] ||
		again.Roles[kinds.RoleAdvisor].Model != NoAdvisor || !slices.Equal(again.ScanExcludes, []string{"~/Downloads", "~/Movies"}) {
		t.Fatalf("after put = %+v", again)
	}
	evs, _ := s.Events.After(ctx, 0, 10)
	if len(evs) != 1 || evs[0].Type != events.SettingsChanged {
		t.Fatalf("events = %+v", evs)
	}
	// a partial table still yields defaults for the rest
	s.DB.Exec(`DELETE FROM settings WHERE key <> 'max_concurrent_agents'`)
	partial, _ := s.Get(ctx)
	if partial.MaxConcurrentAgents != 12 || partial.MaxAgentsPerRoot != 4 || partial.Roles[kinds.RoleCoder].Agent != kinds.Claude {
		t.Fatalf("partial = %+v", partial)
	}
}

func TestPutValidation(t *testing.T) {
	s := newStore(t, kinds.Claude, kinds.Codex)
	base, _ := s.Get(ctx)
	clone := func(edit func(*Settings)) Settings {
		c := base
		c.Roles = map[kinds.Role]RoleDefault{}
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
		{func(c *Settings) { c.Roles[kinds.RoleCoder] = RoleDefault{kinds.Claude, "claude-9", "", ""} }, "Choose a model available for this agent."},
		{func(c *Settings) { c.Roles[kinds.RoleCoder] = RoleDefault{kinds.Claude, "sonnet", "ultra", ""} }, "ultra isn't available for Claude Sonnet 5."},
		{func(c *Settings) { c.Roles[kinds.RoleMechanical] = RoleDefault{kinds.Claude, "haiku", "low", ""} }, "low isn't available for Claude Haiku 4.5."},
		{func(c *Settings) { c.Roles[kinds.RoleAdvisor] = RoleDefault{kinds.Claude, "haiku", "", ""} }, "Choose a model available for this agent."},
		{func(c *Settings) { c.Roles[kinds.RoleCoder] = RoleDefault{kinds.Agy, "gemini", "", ""} }, "agy isn't enabled. Choose an enabled agent."},
		{func(c *Settings) { c.EnabledAgents = append(c.EnabledAgents, "opencode") }, "Unknown agent opencode."},
		{func(c *Settings) { c.EnabledAgents = nil }, "At least one agent must stay enabled."},
		{func(c *Settings) { c.Roles["janitor"] = RoleDefault{kinds.Claude, "opus", "", ""} }, "Unknown role janitor."},
		{func(c *Settings) { c.FallbackDefault = RoleDefault{kinds.Agy, "gemini", "", ""} }, "agy isn't enabled. Choose an enabled agent."},
		{func(c *Settings) { c.FallbackDefault = RoleDefault{kinds.Claude, "claude-9", "", ""} }, "Choose a model available for this agent."},
		{func(c *Settings) { c.FallbackDefault = RoleDefault{kinds.Claude, "sonnet", "ultra", ""} }, "ultra isn't available for Claude Sonnet 5."},
		{func(c *Settings) { c.MaxConcurrentAgents = 33 }, "Maximum concurrent agents must be between 1 and 32."},
		{func(c *Settings) { c.MaxConcurrentAgents = 0 }, "Maximum concurrent agents must be between 1 and 32."},
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
		c.Roles[kinds.RoleCoder] = RoleDefault{kinds.Claude, "claude-sonnet-5", "xhigh", ""}
		c.Roles[kinds.RoleAdvisor] = RoleDefault{kinds.Claude, "opus", "", ""}
		c.MaxConcurrentAgents, c.MaxAgentsPerRoot, c.PauseDeadlineSec = 32, 16, 600
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
	s := newStore(t, kinds.Claude)
	cur, _ := s.Get(ctx)
	cur.Roles[kinds.RoleCoder] = RoleDefault{kinds.Claude, "claude-sonnet-5", "", ""}
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatal(err)
	}
	saved := catalogs[kinds.Claude]
	catalogs[kinds.Claude] = saved[:2] // sonnet and haiku disappear
	defer func() { catalogs[kinds.Claude] = saved }()
	cur, _ = s.Get(ctx)
	cur.MaxConcurrentAgents = 9
	_, err := s.Put(ctx, cur)
	verr(t, err, "claude-sonnet-5 is no longer offered by Claude.")
}

func TestFallbackModelGoneFromCatalog(t *testing.T) {
	s := newStore(t, kinds.Claude)
	cur, _ := s.Get(ctx)
	// Repoint every role that defaults to "sonnet" or "haiku" at the
	// surviving "opus" model first, so the roles loop (which validate()
	// runs before FallbackDefault) can't fail on one of them instead once
	// sonnet and haiku disappear below -- isolating the assertion to
	// FallbackDefault's own pinned model, the same way TestModelGoneFromCatalog
	// isolates it to RoleCoder's.
	cur.Roles[kinds.RoleCoder] = RoleDefault{kinds.Claude, "opus", "", ""}
	cur.Roles[kinds.RoleResearcher] = RoleDefault{kinds.Claude, "opus", "", ""}
	cur.Roles[kinds.RoleMechanical] = RoleDefault{kinds.Claude, "opus", "", ""}
	cur.FallbackDefault = RoleDefault{kinds.Claude, "claude-sonnet-5", "", ""}
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatal(err)
	}
	saved := catalogs[kinds.Claude]
	catalogs[kinds.Claude] = saved[:2] // sonnet and haiku disappear
	defer func() { catalogs[kinds.Claude] = saved }()
	cur, _ = s.Get(ctx)
	cur.MaxConcurrentAgents = 9
	_, err := s.Put(ctx, cur)
	verr(t, err, "claude-sonnet-5 is no longer offered by Claude.")
}

func TestDisablingAnAgentSwitchesDefaults(t *testing.T) {
	s := newStore(t, kinds.Claude, kinds.Codex)
	cur, _ := s.Get(ctx)
	cur.Roles[kinds.RoleCoder] = RoleDefault{kinds.Codex, "gpt-6-astra", "high", ""}
	cur.FallbackDefault = RoleDefault{kinds.Codex, "gpt-6-astra", "", ""}
	cur, err := s.Put(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	cur.EnabledAgents = []kinds.AgentKind{kinds.Claude}
	got, err := s.Put(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if got.Roles[kinds.RoleCoder] != (RoleDefault{kinds.Claude, "sonnet", "", ""}) {
		t.Fatalf("coder = %+v", got.Roles[kinds.RoleCoder])
	}
	// I18 parity: FallbackDefault is reassigned the same way a role is when
	// its agent gets disabled.
	if got.FallbackDefault != (RoleDefault{kinds.Claude, "sonnet", "", ""}) {
		t.Fatalf("fallback default = %+v", got.FallbackDefault)
	}

	got.EnabledAgents = []kinds.AgentKind{kinds.Codex, kinds.Claude}
	got, _ = s.Put(ctx, got)
	got.EnabledAgents = []kinds.AgentKind{kinds.Codex}
	got, err = s.Put(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range kinds.SettingsRoles {
		want := RoleDefault{kinds.Codex, "gpt-6-astra", "", ""}
		if got.Roles[r] != want {
			t.Errorf("%s = %+v, want %+v", r, got.Roles[r], want)
		}
	}
	if want := (RoleDefault{kinds.Codex, "gpt-6-astra", "", ""}); got.FallbackDefault != want {
		t.Errorf("fallback default = %+v, want %+v", got.FallbackDefault, want)
	}
}

func TestEmptyCatalogSkipsModelChecks(t *testing.T) {
	s := newStore(t, kinds.Claude, kinds.Agy)
	cur, _ := s.Get(ctx)
	cur.Roles[kinds.RoleResearcher] = RoleDefault{kinds.Agy, "gemini-3.8-flash", "high", ""}
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatalf("agy has no cached models yet: %v", err)
	}
	cur.Roles[kinds.RoleResearcher] = RoleDefault{kinds.Agy, "", "", ""}
	_, err := s.Put(ctx, cur)
	verr(t, err, "Choose a model available for this agent.")
}

func TestPutDoesNotMutateCallerMaps(t *testing.T) {
	s := newStore(t, kinds.Claude)
	cur, _ := s.Get(ctx)
	delete(cur.Roles, kinds.RoleDebugger)
	delete(cur.Notifications, "info")
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if _, ok := cur.Roles[kinds.RoleDebugger]; ok {
		t.Error("Put wrote into the caller's roles map")
	}
	if _, ok := cur.Notifications["info"]; ok {
		t.Error("Put wrote into the caller's notifications map")
	}
}

func newTestStore(t *testing.T) *Store {
	return newStore(t)
}

func TestSettingsInstructionsRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	s, err := st.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Instructions != "" {
		t.Fatalf("expected empty default instructions, got %q", s.Instructions)
	}

	s.Instructions = "# Custom Swarm Rules\n1. Standard library first."
	saved, err := st.Put(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Instructions != s.Instructions {
		t.Fatalf("Put returned instructions %q, want %q", saved.Instructions, s.Instructions)
	}

	reloaded, err := st.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Instructions != s.Instructions {
		t.Fatalf("Get returned instructions %q, want %q", reloaded.Instructions, s.Instructions)
	}
}
