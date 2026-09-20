// Package settings loads, validates and saves user settings (§6.5).
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

const NoAdvisor = "none"

type RoleDefault struct {
	Agent  kinds.AgentKind `json:"agent"`
	Model  string          `json:"model"`
	Effort string          `json:"effort,omitempty"` // "" = agent default (L27)
}

type NotifyPref struct {
	Center bool `json:"center"`
	Sound  bool `json:"sound"`
}

type Settings struct {
	EnabledAgents []kinds.AgentKind          `json:"enabled_agents"`
	Roles         map[kinds.Role]RoleDefault `json:"roles"`
	// FallbackDefault is the agent+model substituted in place of a role's
	// configured agent when that agent is confirmed out of usage (see
	// docs/specs/2026-09-19-usage-fallback-agent.md). Validated and
	// reassigned on a disabled agent the same way a role default is.
	FallbackDefault  RoleDefault           `json:"fallback_default"`
	Notifications    map[string]NotifyPref `json:"notifications"`
	MaxOrchestrators int                   `json:"max_orchestrators"`
	MaxAgents        int                   `json:"max_agents"`
	MaxAgentsPerRoot       int                   `json:"max_agents_per_root"`
	MaxConcurrentSubagents int                   `json:"max_concurrent_subagents"`
	ScanExcludes           []string              `json:"scan_excludes"`
	ScanIntervalSec        int                   `json:"scan_interval_sec"`
	MenubarCompact         bool                  `json:"menubar_compact"`
	UsagePollSec           int                   `json:"usage_poll_sec"`
	PauseDeadlineSec       int                   `json:"pause_deadline_sec"`
}

// roleDefaults is §2.1 A3; "default" effort is "".
var roleDefaults = map[kinds.Role]RoleDefault{
	kinds.RoleOrchestrator: {kinds.Claude, "opus", ""},
	kinds.RoleCoder:        {kinds.Claude, "sonnet", ""},
	kinds.RoleReviewer:     {kinds.Claude, "opus", ""},
	kinds.RoleUIReviewer:   {kinds.Claude, "opus", ""},
	kinds.RoleResearcher:   {kinds.Claude, "sonnet", ""},
	kinds.RoleDebugger:     {kinds.Claude, "opus", ""},
	kinds.RoleMechanical:   {kinds.Claude, "haiku", ""},
	kinds.RoleAdvisor:      {kinds.Claude, "fable", ""},
}

// Defaults: Claude plus any installed agent, in settings order.
func Defaults(installed []kinds.AgentKind) Settings {
	enabled := []kinds.AgentKind{kinds.Claude}
	for _, k := range kinds.AgentKinds {
		if k != kinds.Claude && slices.Contains(installed, k) {
			enabled = append(enabled, k)
		}
	}
	on := NotifyPref{Center: true, Sound: true}
	return Settings{
		EnabledAgents:          enabled,
		Roles:                  maps.Clone(roleDefaults),
		FallbackDefault:        RoleDefault{Agent: kinds.Claude, Model: "sonnet"},
		Notifications:          map[string]NotifyPref{"info": on, "attention": on, "action": on},
		MaxOrchestrators:       3,
		MaxAgents:              8,
		MaxAgentsPerRoot:       4,
		MaxConcurrentSubagents: 3,
		ScanExcludes:           []string{"~/Library", "~/.Trash", "~/Downloads"},
		ScanIntervalSec:        21600,
		UsagePollSec:           300,
		PauseDeadlineSec:       120,
	}
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

type Store struct {
	DB        *db.DB
	Events    *events.Store
	Now       func() time.Time
	ModelsFor func(context.Context, kinds.AgentKind) ([]catalog.CatalogModel, string, error)
	Installed func(context.Context) []kinds.AgentKind
}

func (s *Store) Get(ctx context.Context) (Settings, error) {
	var installed []kinds.AgentKind
	if s.Installed != nil {
		installed = s.Installed(ctx)
	}
	def := Defaults(installed)
	raw, _ := json.Marshal(def)
	fields := map[string]json.RawMessage{}
	json.Unmarshal(raw, &fields)
	rows, err := s.DB.QueryContext(ctx, `SELECT key, value_json FROM settings`)
	if err != nil {
		return Settings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return Settings{}, err
		}
		if _, known := fields[k]; known {
			fields[k] = json.RawMessage(v)
		}
	}
	if err := rows.Err(); err != nil {
		return Settings{}, err
	}
	merged, _ := json.Marshal(fields)
	var out Settings
	if err := json.Unmarshal(merged, &out); err != nil {
		return Settings{}, err
	}
	fillMissing(&out, def)
	return out, nil
}

func fillMissing(s *Settings, def Settings) {
	if s.Roles == nil {
		s.Roles = map[kinds.Role]RoleDefault{}
	}
	for r, d := range def.Roles {
		if _, ok := s.Roles[r]; !ok {
			s.Roles[r] = d
		}
	}
	if s.Notifications == nil {
		s.Notifications = map[string]NotifyPref{}
	}
	for k, v := range def.Notifications {
		if _, ok := s.Notifications[k]; !ok {
			s.Notifications[k] = v
		}
	}
	if s.ScanExcludes == nil {
		s.ScanExcludes = []string{}
	}
}

func (s *Store) Put(ctx context.Context, next Settings) (Settings, error) {
	prev, err := s.Get(ctx)
	if err != nil {
		return Settings{}, err
	}
	next.Roles, next.Notifications = maps.Clone(next.Roles), maps.Clone(next.Notifications)
	fillMissing(&next, Defaults(nil))
	if err := s.switchDisabled(ctx, prev, &next); err != nil {
		return prev, err
	}
	if err := s.validate(ctx, prev, next); err != nil {
		return prev, err
	}
	raw, _ := json.Marshal(next)
	fields := map[string]json.RawMessage{}
	json.Unmarshal(raw, &fields)
	now := db.Millis(s.Now())
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		for k, v := range fields {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
				k, string(v), now); err != nil {
				return err
			}
		}
		_, err := s.Events.Append(ctx, tx, events.SettingsChanged, next)
		return err
	})
	if err != nil {
		return prev, err
	}
	s.Events.Notify()
	return next, nil
}

// reassignDefault is I18's own per-default logic: what a RoleDefault (a
// role, or FallbackDefault) becomes when the agent it names gets disabled.
// fallback (a zero value for every real role) is the roleDefaults entry to
// fall back to when first is Claude; FallbackDefault has no such
// pre-canned entry, so its caller passes the shipped {Claude, "sonnet"} one.
func (s *Store) reassignDefault(ctx context.Context, first kinds.AgentKind, claudeFallback RoleDefault) (RoleDefault, error) {
	if first == kinds.Claude {
		return claudeFallback, nil
	}
	models, def, err := s.ModelsFor(ctx, first)
	if err != nil {
		return RoleDefault{}, err
	}
	if def == "" && len(models) > 0 {
		def = models[0].ID
	}
	return RoleDefault{Agent: first, Model: def}, nil
}

// switchDisabled implements I18 for agents this update disables.
func (s *Store) switchDisabled(ctx context.Context, prev Settings, next *Settings) error {
	if len(next.EnabledAgents) == 0 {
		return invalid("At least one agent must stay enabled.")
	}
	first := next.EnabledAgents[0]
	disabledNow := func(agent kinds.AgentKind) bool {
		return slices.Contains(prev.EnabledAgents, agent) && !slices.Contains(next.EnabledAgents, agent)
	}
	for role, rd := range next.Roles {
		if rd.Model == NoAdvisor && role == kinds.RoleAdvisor {
			continue
		}
		if !disabledNow(rd.Agent) {
			continue
		}
		reassigned, err := s.reassignDefault(ctx, first, roleDefaults[role])
		if err != nil {
			return err
		}
		next.Roles[role] = reassigned
	}
	if disabledNow(next.FallbackDefault.Agent) {
		reassigned, err := s.reassignDefault(ctx, first, RoleDefault{Agent: kinds.Claude, Model: "sonnet"})
		if err != nil {
			return err
		}
		next.FallbackDefault = reassigned
	}
	return nil
}

// validateDefault checks one RoleDefault (a role, or FallbackDefault)
// against enabled and the real catalog, in the exact order this codebase
// has always checked a default: enabled, then catalog/model, then (via
// extra, when non-nil — RoleAdvisor's own AdvisorCapable rule) any
// role-specific rule on the resolved model, then effort. oldRD is the
// previously-saved value for the same slot, so a model that just
// disappeared from the catalog gets its own clearer message.
func (s *Store) validateDefault(ctx context.Context, oldRD, rd RoleDefault, enabled []kinds.AgentKind, extra func(catalog.CatalogModel) error) error {
	if !slices.Contains(enabled, rd.Agent) {
		return invalid("%s isn't enabled. Choose an enabled agent.", rd.Agent.Display())
	}
	models, _, err := s.ModelsFor(ctx, rd.Agent)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		if rd.Model == "" {
			return invalid("Choose a model available for this agent.")
		}
		return nil
	}
	m, ok := catalog.Find(models, rd.Model)
	if !ok {
		if oldRD.Agent == rd.Agent && oldRD.Model == rd.Model {
			return invalid("%s is no longer offered by %s.", rd.Model, rd.Agent.Display())
		}
		return invalid("Choose a model available for this agent.")
	}
	if extra != nil {
		if err := extra(m); err != nil {
			return err
		}
	}
	if !m.SupportsEffort(rd.Effort) {
		return invalid("%s isn't available for %s.", rd.Effort, m.Label)
	}
	return nil
}

func (s *Store) validate(ctx context.Context, prev, next Settings) error {
	for _, k := range next.EnabledAgents {
		if !slices.Contains(kinds.AgentKinds, k) {
			return invalid("Unknown agent %s.", k)
		}
	}
	for role := range next.Roles {
		if !slices.Contains(kinds.SettingsRoles, role) {
			return invalid("Unknown role %s.", role)
		}
	}
	for _, role := range kinds.SettingsRoles {
		rd := next.Roles[role]
		if role == kinds.RoleAdvisor && rd.Model == NoAdvisor {
			continue
		}
		var advisorCapable func(catalog.CatalogModel) error
		if role == kinds.RoleAdvisor {
			advisorCapable = func(m catalog.CatalogModel) error {
				if rd.Agent == kinds.Claude && !m.AdvisorCapable {
					return invalid("Choose a model available for this agent.")
				}
				return nil
			}
		}
		if err := s.validateDefault(ctx, prev.Roles[role], rd, next.EnabledAgents, advisorCapable); err != nil {
			return err
		}
	}
	// FallbackDefault has no "none"/advisor-capable carve-out: it must
	// always resolve to a real, enabled agent and model, or the feature it
	// backs is defeated.
	if err := s.validateDefault(ctx, prev.FallbackDefault, next.FallbackDefault, next.EnabledAgents, nil); err != nil {
		return err
	}
	switch {
	case next.MaxOrchestrators < 1 || next.MaxOrchestrators > 8:
		return invalid("Maximum concurrent orchestrators must be between 1 and 8.")
	case next.MaxAgents < 1 || next.MaxAgents > 32:
		return invalid("Maximum concurrent agents must be between 1 and 32.")
	case next.MaxAgentsPerRoot < 1 || next.MaxAgentsPerRoot > 16:
		return invalid("Maximum concurrent agents per item must be between 1 and 16.")
	case next.MaxConcurrentSubagents < 1 || next.MaxConcurrentSubagents > 16:
		return invalid("Maximum concurrent subagents per parent must be between 1 and 16.")
	case next.PauseDeadlineSec < 30 || next.PauseDeadlineSec > 600:
		return invalid("Pause deadline must be between 30 and 600 seconds.")
	case next.ScanIntervalSec < 3600:
		return invalid("Repository scans must be at least 3600 seconds apart.")
	case next.UsagePollSec < 60:
		return invalid("Usage polling must be at least 60 seconds apart.")
	}
	return nil
}
