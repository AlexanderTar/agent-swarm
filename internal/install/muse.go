package install

import (
	"context"
	"encoding/json"
	"os"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// WriteMuse merges the swarm MCP server into muse's settings.json and installs
// the swarm skills into the probed user-skills dir ($CONFIG_DIR/skills). There
// is deliberately no hooks step, and settings.json is the only file the CLI
// reads MCP config from — writing a parallel mcp_config.json like agy's would
// be an unread file, not a wiring.
//
// REVERTED (code review, 2026-09-26): an earlier version of this function also
// wrote a .muse-plugin/plugin.json (PreToolUse/PostToolUse/UserPromptSubmit,
// each running `swarm hook muse <event>`) and ran `muse plugins install` +
// `muse plugins approve` against the real, shared, user-wide muse plugin
// store. Two live probes found that path dead on arrival and unsafe to ship:
// (1) muse runs a hook subprocess with only a fixed env allowlist (HOME, PATH,
// USER, LANG, TERM, SHELL, PWD, LOGNAME, SHLVL, plus muse's own PLUGIN_*/
// MUSE_PLUGIN_* vars) -- confirmed live with `muse plugins hook test ...
// --fixture` after an explicit `SWARM_SESSION=x` in the invoking env never
// reached the hook script. SWARM_SESSION/SWARM_TOKEN_FILE never arrive, so
// `swarm hook muse <event>` no-ops on every call (client.go's "no
// SWARM_SESSION" fast path) -- the plugin is unreachable end to end. (2) the
// install target is muse's shared, user-wide store, so once reachable it
// would also fire on every tool call in the user's own muse sessions outside
// Swarm, not just Swarm-launched ones. swarm_ask kind:"question" stays the
// load-bearing fallback for muse (Task 9) until a wrapper carries the
// session's identity through some channel other than env (e.g. a per-cwd
// file, since cwd is in every hook's stdin payload) and the install step is
// scoped so it only ever touches Swarm-launched sessions.
//
// No "env" key is written here (P0-1): muse takes only literal env values,
// never ${VAR} passthrough (confirmed 2026-09-23, see adapter/muse.go's
// museMCPEnv doc), and SWARM_SESSION/SWARM_TOKEN_FILE don't exist yet at
// install time — startSession mints them fresh per launch. The real env
// wiring happens per-launch in adapter/muse.go's setupEnv, which isolates
// XDG_CONFIG_HOME and overwrites the swarm entry there with that launch's
// concrete values. This install-time entry is only what a manually-run
// `muse` outside the daemon sees, and stays a harmless no-auth optional
// server for that case.
func WriteMuse(ctx context.Context, c Config, _ execx.Runner) ([]string, error) {
	_ = ctx
	var changed []string
	p := c.Muse("settings.json")
	wrote, err := EditJSON(p, true, func(m map[string]any) error {
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers["swarm"] = map[string]any{
			"mode":    "optional",
			"command": c.Bin,
			"args":    []string{"mcp"},
		}
		m["mcpServers"] = servers
		return nil
	})
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, p)
	}
	skills, _, err := WriteSkills(c, KindMuse)
	return append(changed, skills...), err
}

// CheckMuse is doctor's muse block (plus A1's skills check). Muse has no hooks
// step (see WriteMuse), so the only thing to verify is the swarm MCP entry in
// settings.json plus the hook binary it points at -- the same shape and size
// as agy's single check.
func CheckMuse(ctx context.Context, c Config, run execx.Runner) []Check {
	return []Check{museMCPCheck(c), CheckSkills(c, KindMuse)}
}

func museMCPCheck(c Config) Check {
	p := c.Muse("settings.json")
	body, err := os.ReadFile(p)
	if err != nil {
		return Check{"muse MCP", false, "Not installed. Run swarm install."}
	}
	var f struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return Check{"muse MCP", false, "Invalid " + p + ". Run swarm install."}
	}
	entry, ok := f.MCPServers["swarm"]
	if !ok {
		return Check{"muse MCP", false, "The swarm MCP server is missing from " + p + ". Run swarm install."}
	}
	if entry.Command != c.Bin {
		return Check{"muse MCP", false, "The swarm MCP server points at " + entry.Command + ". Run swarm install."}
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return Check{"muse MCP", false, "The swarm binary is missing: " + c.Bin + ". Run make install."}
	}
	return Check{"muse MCP", true, p}
}
