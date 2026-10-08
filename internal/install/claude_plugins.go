package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// claudePluginEntry is one element of installed_plugins.json v2's
// plugins["<name>@<marketplace>"] list.
type claudePluginEntry struct {
	Scope       string `json:"scope"`
	InstallPath string `json:"installPath"`
}

// ClaudePluginDirs returns the --plugin-dir values for spawned claude
// sessions: user-scope entries of ~/.claude/plugins/installed_plugins.json
// whose marketplace is swarm's own (MarketplaceName), whose name is
// supported by every agent kind (derived from pluginSupport), whose name is
// not in SkipAlways, and which are not explicitly disabled
// (enabledPlugins[key] == false in ~/.claude/settings.json).
//
// Missing or malformed files yield no dirs and no error: a plugin lookup
// must never fail a launch. A missing or unparsable settings.json disables
// nothing (absence from enabledPlugins means enabled). An entry whose
// installPath is not an existing directory is skipped: a stale
// installed_plugins.json entry must never yield --plugin-dir <missing>.
func ClaudePluginDirs(userHome string) []string {
	if userHome == "" {
		return nil
	}
	body, err := os.ReadFile(filepath.Join(userHome, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Plugins map[string][]claudePluginEntry `json:"plugins"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil
	}
	disabled := claudeDisabledPlugins(userHome)
	seen := map[string]bool{}
	var out []string
	for key, entries := range doc.Plugins {
		name, marketplace := splitPluginKey(key)
		if marketplace != MarketplaceName {
			continue
		}
		if _, never := SkipAlways[name]; never {
			continue
		}
		if !supportedByAllKinds(name) {
			continue
		}
		if disabled[key] {
			continue
		}
		for _, e := range entries {
			if e.Scope != "user" || e.InstallPath == "" {
				continue
			}
			fi, err := os.Stat(e.InstallPath)
			if err != nil || !fi.IsDir() {
				continue
			}
			if !seen[e.InstallPath] {
				seen[e.InstallPath] = true
				out = append(out, e.InstallPath)
			}
		}
	}
	sort.Strings(out)
	return out
}

// supportedByAllKinds reports whether name is supported by every agent kind
// in Kinds, derived from the pluginSupport matrix rather than a hardcoded
// name list. A plugin unknown to the matrix is claude-only (see
// SupportedBy) and never qualifies.
func supportedByAllKinds(name string) bool {
	kinds, known := pluginSupport[name]
	if !known {
		return false
	}
	have := make(map[Kind]bool, len(kinds))
	for _, k := range kinds {
		have[k] = true
	}
	for _, k := range Kinds {
		if !have[k] {
			return false
		}
	}
	return true
}

// splitPluginKey cuts "<name>@<marketplace>" on its last "@".
func splitPluginKey(key string) (name, marketplace string) {
	i := strings.LastIndex(key, "@")
	if i < 0 {
		return key, ""
	}
	return key[:i], key[i+1:]
}

// claudeDisabledPlugins returns the set of plugin keys with
// enabledPlugins[key] == false. Any read or parse failure yields an empty
// set rather than an error.
func claudeDisabledPlugins(userHome string) map[string]bool {
	out := map[string]bool{}
	body, err := os.ReadFile(filepath.Join(userHome, ".claude", "settings.json"))
	if err != nil {
		return out
	}
	var doc struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return out
	}
	for k, enabled := range doc.EnabledPlugins {
		if !enabled {
			out[k] = true
		}
	}
	return out
}
