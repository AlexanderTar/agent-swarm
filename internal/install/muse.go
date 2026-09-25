package install

import (
	"context"
	"encoding/json"
	"os"

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
