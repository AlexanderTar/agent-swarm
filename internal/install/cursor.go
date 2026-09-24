package install

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// cursorEvents is §11.1's list. Cursor's event names are lowerCamelCase, unlike
// codex's and agy's, and it runs hooks through /bin/zsh, so a command is a string.
var cursorEvents = []string{"sessionStart", "beforeSubmitPrompt", "preToolUse", "postToolUse", "preCompact", "stop"}

// CursorMCP is the "swarm" entry for ~/.cursor/mcp.json. Cursor passes no
// environment to MCP servers, so every variable is mapped with ${env:NAME} (L16;
// the shim treats a ${env: value as unset). SWARM_AGENT_KIND is a literal because
// it never varies for cursor.
func CursorMCP(c Config) map[string]any {
	return map[string]any{
		"type":    "stdio",
		"command": c.Bin,
		"args":    []string{"mcp"},
		"env": map[string]string{
			"SWARM_URL":        "${env:SWARM_URL}",
			"SWARM_SESSION":    "${env:SWARM_SESSION}",
			"SWARM_TOKEN_FILE": "${env:SWARM_TOKEN_FILE}",
			"SWARM_AGENT_KIND": "cursor",
		},
	}
}

// CursorHooks is the whole ~/.cursor/hooks.json swarm produces, for tests and doctor.
func CursorHooks(c Config) []byte {
	hooks := map[string]any{}
	for _, ev := range cursorEvents {
		hooks[ev] = []any{map[string]any{"command": c.Bin + " hook cursor " + ev}}
	}
	body, err := json.MarshalIndent(map[string]any{"version": 1, "hooks": hooks}, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(body, '\n')
}

func WriteCursor(c Config) ([]string, error) {
	var changed []string

	mcpPath := c.Cursor("mcp.json")
	wrote, err := EditJSON(mcpPath, true, func(m map[string]any) error {
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
			m["mcpServers"] = servers
		}
		entry, err := toAny(CursorMCP(c))
		if err != nil {
			return err
		}
		servers["swarm"] = entry
		return nil
	})
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, mcpPath)
	}

	hooksPath := c.Cursor("hooks.json")
	wrote, err = EditJSON(hooksPath, true, func(m map[string]any) error {
		m["version"] = float64(1)
		hooks, _ := m["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
			m["hooks"] = hooks
		}
		for _, ev := range cursorEvents {
			var kept []any
			if list, ok := hooks[ev].([]any); ok {
				for _, entry := range list {
					obj, _ := entry.(map[string]any)
					cmd, _ := obj["command"].(string)
					if !isSwarmHookCommand(cmd) {
						kept = append(kept, entry)
					}
				}
			}
			hooks[ev] = append(kept, map[string]any{"command": c.Bin + " hook cursor " + ev})
		}
		return nil
	})
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, hooksPath)
	}

	// L23: cursor co-authors by default, and only the global file accepts the key.
	cliPath := c.Cursor("cli-config.json")
	wrote, err = EditJSON(cliPath, true, func(m map[string]any) error {
		attr, _ := m["attribution"].(map[string]any)
		if attr == nil {
			attr = map[string]any{}
			m["attribution"] = attr
		}
		attr["attributeCommitsToAgent"] = false
		attr["attributePRsToAgent"] = false
		return nil
	})
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, cliPath)
	}

	skills, _, err := WriteSkills(c, KindCursor)
	return append(changed, skills...), err
}

// RemoveLegacyCursor removes v1's mcp.json entry, its hooks and the v1 local-plugin
// link. It leaves every other local plugin alone (§12.4: uninstall keeps plugins).
func RemoveLegacyCursor(c Config) ([]string, error) {
	var changed []string

	mcpPath := c.Cursor("mcp.json")
	wrote, err := EditJSON(mcpPath, false, dropSwarmMCPServer)
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, mcpPath)
	}

	hooksPath := c.Cursor("hooks.json")
	wrote, err = EditJSON(hooksPath, false, func(m map[string]any) error {
		hooks, _ := m["hooks"].(map[string]any)
		if hooks == nil {
			return nil
		}
		for ev, v := range hooks {
			list, ok := v.([]any)
			if !ok {
				continue
			}
			var kept []any
			for _, entry := range list {
				obj, _ := entry.(map[string]any)
				cmd, _ := obj["command"].(string)
				if !isSwarmHookCommand(cmd) {
					kept = append(kept, entry)
				}
			}
			if len(kept) == 0 {
				delete(hooks, ev)
				continue
			}
			hooks[ev] = kept
		}
		return nil
	})
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, hooksPath)
	}

	link := c.Cursor("plugins", "local", "swarm")
	removed, realDir, err := RemoveLink(link)
	if err != nil {
		return changed, err
	}
	if removed {
		changed = append(changed, link)
	} else if realDir {
		// A real v1 folder (not what this machine has, but possible elsewhere):
		// cursor has no uninstall command, so the folder is removed directly.
		if err := os.RemoveAll(link); err != nil {
			return changed, err
		}
		changed = append(changed, link)
	}
	return changed, nil
}

func CheckCursor(ctx context.Context, c Config, run execx.Runner) []Check {
	return []Check{cursorMCPCheck(c), cursorHookCheck(c), cursorAttributionCheck(c), CheckSkills(c, KindCursor)}
}

func cursorMCPCheck(c Config) Check {
	p := c.Cursor("mcp.json")
	body, err := os.ReadFile(p)
	if err != nil {
		return Check{"Cursor MCP", false, "Not installed. Run swarm install."}
	}
	var f struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return Check{"Cursor MCP", false, "Invalid " + p + ". Run swarm install."}
	}
	entry, ok := f.MCPServers["swarm"]
	if !ok {
		return Check{"Cursor MCP", false, "The swarm MCP server is missing from " + p + ". Run swarm install."}
	}
	if entry.Command != c.Bin {
		return Check{"Cursor MCP", false, "The swarm MCP server points at " + entry.Command + ". Run swarm install."}
	}
	for _, k := range []string{"SWARM_URL", "SWARM_SESSION", "SWARM_TOKEN_FILE"} {
		if entry.Env[k] != "${env:"+k+"}" {
			return Check{"Cursor MCP", false, k + " is not mapped with ${env:…} in " + p + ". Run swarm install."}
		}
	}
	if entry.Env["SWARM_AGENT_KIND"] != "cursor" {
		return Check{"Cursor MCP", false, "SWARM_AGENT_KIND is not \"cursor\" in " + p + ". Run swarm install."}
	}
	return Check{"Cursor MCP", true, p}
}

func cursorHookCheck(c Config) Check {
	p := c.Cursor("hooks.json")
	body, err := os.ReadFile(p)
	if err != nil {
		return Check{"Cursor hooks", false, "Not installed. Run swarm install."}
	}
	var f struct {
		Hooks map[string][]struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return Check{"Cursor hooks", false, "Invalid " + p + ". Run swarm install."}
	}
	var missing []string
	for _, ev := range cursorEvents {
		found := false
		for _, h := range f.Hooks[ev] {
			found = found || h.Command == c.Bin+" hook cursor "+ev
		}
		if !found {
			missing = append(missing, ev)
		}
	}
	if len(missing) > 0 {
		return Check{"Cursor hooks", false, "Missing or stale: " + strings.Join(missing, ", ") + ". Run swarm install."}
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return Check{"Cursor hooks", false, "The hook binary is missing: " + c.Bin + ". Run make install."}
	}
	return Check{"Cursor hooks", true, p}
}

// cursorAttributionCheck is L23's "swarm doctor checks it" and the precondition for
// §23.4 item 8.
func cursorAttributionCheck(c Config) Check {
	p := c.Cursor("cli-config.json")
	body, err := os.ReadFile(p)
	if err != nil {
		return Check{"Cursor attribution", false, "Not set. Run swarm install."}
	}
	var f struct {
		Attribution struct {
			Commits *bool `json:"attributeCommitsToAgent"`
			PRs     *bool `json:"attributePRsToAgent"`
		} `json:"attribution"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return Check{"Cursor attribution", false, "Invalid " + p + ". Run swarm install."}
	}
	on := func(b *bool) bool { return b == nil || *b }
	if on(f.Attribution.Commits) || on(f.Attribution.PRs) {
		return Check{"Cursor attribution", false,
			"Cursor still attributes commits or PRs to the agent in " + p + ". Run swarm install."}
	}
	return Check{"Cursor attribution", true, "Cursor adds no agent attribution."}
}
