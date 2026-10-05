package catalog

import "testing"

func launchIDModels() []CatalogModel {
	return []CatalogModel{
		{ID: "auto", IsDefault: true, EffortEncoding: "slug"},
		{ID: "gemini-3.8-flash", EffortEncoding: "slug", Efforts: []string{"low", "medium", "high"},
			LaunchIDs: map[string]string{"low": "gemini-3.8-flash-low", "medium": "gemini-3.8-flash-medium", "high": "gemini-3.8-flash-high"}},
		{ID: "gpt-5.3-codex", EffortEncoding: "slug", Efforts: []string{"default", "low"},
			LaunchIDs: map[string]string{"default": "gpt-5.3-codex", "low": "gpt-5.3-codex-low"}},
		{ID: "claude-opus-5-5", Aliases: []string{"opus", "claude-opus-5-5-20260101", "opus[1m]"}, EffortEncoding: "flag", Efforts: EffortLevels},
	}
}

func TestFromLaunchID(t *testing.T) {
	ms := launchIDModels()
	cases := []struct {
		id, wantID, wantEffort string
		ok                     bool
	}{
		{"gemini-3.8-flash-low", "gemini-3.8-flash", "low", true},
		{"gpt-5.3-codex", "gpt-5.3-codex", "default", true},
		{"gpt-5.3-codex-low", "gpt-5.3-codex", "low", true},
		{"default", "auto", "", true},
		{"auto", "auto", "", true},
		{"opus", "claude-opus-5-5", "", true},
		{"claude-opus-5-5-20260101", "claude-opus-5-5", "", true},
		{"opus[1m]", "claude-opus-5-5", "", true},
		{"claude-opus-5-5", "claude-opus-5-5", "", true},
		{"nope", "", "", false},
	}
	for _, c := range cases {
		m, eff, ok := FromLaunchID(ms, c.id)
		if ok != c.ok || m.ID != c.wantID || eff != c.wantEffort {
			t.Errorf("FromLaunchID(%q) = (%q, %q, %v), want (%q, %q, %v)", c.id, m.ID, eff, ok, c.wantID, c.wantEffort, c.ok)
		}
	}
}
