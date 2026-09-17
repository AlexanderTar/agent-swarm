// Package catalog resolves each agent's models and effort levels at run time (§6.7, L26, L27).
package catalog

import "slices"

// EffortLevels are Claude's effort levels, low → max.
var EffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// DefaultLevel is the effort key for a bare slug that has suffixed siblings (cursor `gpt-5.3-codex`).
const DefaultLevel = "default"

type CatalogModel struct {
	ID             string            `json:"id"`
	Label          string            `json:"label"`
	Aliases        []string          `json:"aliases,omitempty"`
	Efforts        []string          `json:"efforts"`
	DefaultEffort  string            `json:"default_effort"`
	EffortEncoding string            `json:"effort_encoding"`      // flag | slug
	LaunchIDs      map[string]string `json:"launch_ids,omitempty"` // slug encoding: level → exact catalog id
	AdvisorCapable bool              `json:"advisor_capable"`
	Hidden         bool              `json:"hidden,omitempty"`
	IsDefault      bool              `json:"is_default,omitempty"`
}

// Find matches an id or an alias.
func Find(models []CatalogModel, id string) (CatalogModel, bool) {
	for _, m := range models {
		if m.ID == id || slices.Contains(m.Aliases, id) {
			return m, true
		}
	}
	return CatalogModel{}, false
}

// SupportsEffort reports whether level can be chosen; "" (the default) always can.
func (m CatalogModel) SupportsEffort(level string) bool {
	return level == "" || slices.Contains(m.Efforts, level)
}

// LaunchModel is the value passed to --model. For slug agents, "" means DefaultEffort.
func (m CatalogModel) LaunchModel(effort string) string {
	if m.EffortEncoding != "slug" {
		return m.ID
	}
	if effort == "" {
		effort = m.DefaultEffort
	}
	if id, ok := m.LaunchIDs[effort]; ok {
		return id
	}
	if effort == "" || effort == DefaultLevel {
		return m.ID
	}
	return m.ID + "-" + effort
}
