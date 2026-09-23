package install

import (
	"context"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// WriteMuse merges the swarm MCP server into muse's settings.json and installs
// the swarm skills into the probed user-skills dir ($CONFIG_DIR/skills). There
// is no hooks step: muse's hook surface is still unprobed (adapter muse.go),
// and settings.json is the only file the CLI reads MCP config from — writing a
// parallel mcp_config.json like agy's would be an unread file, not a wiring.
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
	skills, err := WriteSkills(c, KindMuse)
	return append(changed, skills...), err
}
