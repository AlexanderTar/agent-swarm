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
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

const NoAdvisor = "none"

type RoleDefault struct {
	Agent  runtime.AgentKind `json:"agent"`
	Model  string            `json:"model"`
	Effort string            `json:"effort,omitempty"` // "" = agent default (L27)
}

type NotifyPref struct {
	Center bool `json:"center"`
	Sound  bool `json:"sound"`
}

type Settings struct {
	EnabledAgents    []runtime.AgentKind          `json:"enabled_agents"`
	Roles            map[runtime.Role]RoleDefault `json:"roles"`
	Notifications    map[string]NotifyPref        `json:"notifications"`
	MaxOrchestrators int                          `json:"max_orchestrators"`
	MaxAgents        int                          `json:"max_agents"`
	MaxAgentsPerRoot int                          `json:"max_agents_per_root"`
	ScanExcludes     []string                     `json:"scan_excludes"`
	ScanIntervalSec  int                          `json:"scan_interval_sec"`
	MenubarCompact   bool                         `json:"menubar_compact"`
	UsagePollSec     int                          `json:"usage_poll_sec"`
	PauseDeadlineSec int                          `json:"pause_deadline_sec"`
}

// roleDefaults is §2.1 A3; "default" effort is "".
var roleDefaults = map[runtime.Role]RoleDefault{
	runtime.RoleOrchestrator: {runtime.Claude, "opus", ""},
	runtime.RoleCoder:        {runtime.Claude, "sonnet", ""},
	runtime.RoleReviewer:     {runtime.Claude, "opus", ""},
	runtime.RoleUIReviewer:   {runtime.Claude, "opus", ""},
	runtime.RoleResearcher:   {runtime.Claude, "sonnet", ""},
	runtime.RoleDebugger:     {runtime.Claude, "opus", ""},
	runtime.RoleMechanical:   {runtime.Claude, "haiku", ""},
	runtime.RoleAdvisor:      {runtime.Claude, "fable", ""},
}

// Defaults: Claude plus any installed agent, in settings order.
func Defaults(installed []runtime.AgentKind) Settings {
	enabled := []runtime.AgentKind{runtime.Claude}
	for _, k := range runtime.AgentKinds {
		if k != runtime.Claude && slices.Contains(installed, k) {
			enabled = append(enabled, k)
		}
	}
	on := NotifyPref{Center: true, Sound: true}
	return Settings{
		EnabledAgents:    enabled,
		Roles:            maps.Clone(roleDefaults),
		Notifications:    map[string]NotifyPref{"info": on, "attention": on, "action": on},
		MaxOrchestrators: 3,
		MaxAgents:        8,
		MaxAgentsPerRoot: 4,
		ScanExcludes:     []string{"~/Library", "~/.Trash", "~/Downloads"},
		ScanIntervalSec:  21600,
		UsagePollSec:     300,
		PauseDeadlineSec: 120,
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
	ModelsFor func(context.Context, runtime.AgentKind) ([]catalog.CatalogModel, string, error)
	Installed func(context.Context) []runtime.AgentKind
}

func (s *Store) Get(ctx context.Context) (Settings, error) {
	var installed []runtime.AgentKind
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
		s.Roles = map[runtime.Role]RoleDefault{}
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

// switchDisabled implements I18 for agents this update disables.
func (s *Store) switchDisabled(ctx context.Context, prev Settings, next *Settings) error {
	if len(next.EnabledAgents) == 0 {
		return invalid("At least one agent must stay enabled.")
	}
	first := next.EnabledAgents[0]
	for role, rd := range next.Roles {
		if rd.Model == NoAdvisor && role == runtime.RoleAdvisor {
			continue
		}
		disabledNow := slices.Contains(prev.EnabledAgents, rd.Agent) && !slices.Contains(next.EnabledAgents, rd.Agent)
		if !disabledNow {
			continue
		}
		if first == runtime.Claude {
			next.Roles[role] = roleDefaults[role]
			continue
		}
		models, def, err := s.ModelsFor(ctx, first)
		if err != nil {
			return err
		}
		if def == "" && len(models) > 0 {
			def = models[0].ID
		}
		next.Roles[role] = RoleDefault{Agent: first, Model: def}
	}
	return nil
}

func (s *Store) validate(ctx context.Context, prev, next Settings) error {
	for _, k := range next.EnabledAgents {
		if !slices.Contains(runtime.AgentKinds, k) {
			return invalid("Unknown agent %s.", k)
		}
	}
	for role := range next.Roles {
		if !slices.Contains(runtime.SettingsRoles, role) {
			return invalid("Unknown role %s.", role)
		}
	}
	for _, role := range runtime.SettingsRoles {
		rd := next.Roles[role]
		if role == runtime.RoleAdvisor && rd.Model == NoAdvisor {
			continue
		}
		if !slices.Contains(next.EnabledAgents, rd.Agent) {
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
			continue
		}
		m, ok := catalog.Find(models, rd.Model)
		if !ok {
			if old := prev.Roles[role]; old.Agent == rd.Agent && old.Model == rd.Model {
				return invalid("%s is no longer offered by %s.", rd.Model, rd.Agent.Display())
			}
			return invalid("Choose a model available for this agent.")
		}
		if role == runtime.RoleAdvisor && rd.Agent == runtime.Claude && !m.AdvisorCapable {
			return invalid("Choose a model available for this agent.")
		}
		if !m.SupportsEffort(rd.Effort) {
			return invalid("%s isn't available for %s.", rd.Effort, m.Label)
		}
	}
	switch {
	case next.MaxOrchestrators < 1 || next.MaxOrchestrators > 8:
		return invalid("Maximum concurrent orchestrators must be between 1 and 8.")
	case next.MaxAgents < 1 || next.MaxAgents > 32:
		return invalid("Maximum concurrent agents must be between 1 and 32.")
	case next.MaxAgentsPerRoot < 1 || next.MaxAgentsPerRoot > 16:
		return invalid("Maximum concurrent agents per item must be between 1 and 16.")
	case next.PauseDeadlineSec < 30 || next.PauseDeadlineSec > 600:
		return invalid("Pause deadline must be between 30 and 600 seconds.")
	case next.ScanIntervalSec < 3600:
		return invalid("Repository scans must be at least 3600 seconds apart.")
	case next.UsagePollSec < 60:
		return invalid("Usage polling must be at least 60 seconds apart.")
	}
	return nil
}
