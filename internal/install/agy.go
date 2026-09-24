package install

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// The agy hook file is keyed by hook NAME, and the shapes differ per event
// (P0-2, §11.1): the two tool events take a matcher wrapper, the other three are
// flat handlers. A nested PreInvocation fails to parse in agy (§21.4 row 1).
const agyHookName = "swarm"

var (
	agyMatcherEvents = []string{"PreToolUse", "PostToolUse"}
	agyFlatEvents    = []string{"PreInvocation", "PostInvocation", "Stop"}
)

type agyFlatHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// AgyHooks is the swarm block of ~/.gemini/config/hooks.json.
func AgyHooks(c Config) []byte {
	body, err := json.MarshalIndent(map[string]any{agyHookName: agySwarmBlock(c)}, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(body, '\n')
}

func agySwarmBlock(c Config) map[string]any {
	block := map[string]any{}
	for _, ev := range agyMatcherEvents {
		block[ev] = []any{map[string]any{
			"matcher": "*",
			"hooks":   []any{agyHook(c, ev)},
		}}
	}
	for _, ev := range agyFlatEvents {
		block[ev] = []any{agyHook(c, ev)}
	}
	return block
}

func agyHook(c Config, event string) map[string]any {
	return map[string]any{"type": "command", "command": c.Bin + " hook agy " + event, "timeout": agyHookTimeout}
}

const agyHookTimeout = 3

// WriteAgy installs agy's hooks, MCP server and skills.
func WriteAgy(ctx context.Context, c Config, run execx.Runner) ([]string, error) {
	var changed []string
	p := c.Gemini("config", "hooks.json")
	wrote, err := EditJSON(p, true, func(m map[string]any) error {
		// Replace only our own hook name; the file merges with plugin files.
		block, err := toAny(agySwarmBlock(c))
		if err != nil {
			return err
		}
		m[agyHookName] = block
		return nil
	})
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, p)
	}

	// §11.1: the MCP server is global and registered once by swarm install (M3).
	// agy stores it in mcp_config.json itself, so this is idempotent on agy's side.
	if _, err := run(ctx, "agy", "mcp", "add", "--type", "stdio", "swarm", c.Bin, "mcp"); err != nil {
		return changed, err
	}

	skills, _, err := WriteSkills(c, KindAgy)
	return append(changed, skills...), err
}

// RemoveLegacyAgy removes the v1 agy integration: the GEMINI.md block, the
// mcp_config.json entry and the v1 plugin (a symlink on this machine, so it is
// removed as a link and never followed, §20).
func RemoveLegacyAgy(ctx context.Context, c Config, run execx.Runner) ([]string, error) {
	var changed []string

	md := c.Gemini("GEMINI.md")
	if old, err := os.ReadFile(md); err == nil {
		if next, removed := RemoveMarkedSpan(string(old), "<!-- swarm:start -->", "<!-- swarm:end -->"); removed {
			if _, err := WriteIfChanged(md, []byte(next), 0o644); err != nil {
				return changed, err
			}
			changed = append(changed, md)
		}
	} else if !os.IsNotExist(err) {
		return changed, err
	}

	mcp := c.Gemini("antigravity", "mcp_config.json")
	wrote, err := EditJSON(mcp, false, dropSwarmMCPServer)
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, mcp)
	}

	plugin := c.Gemini("config", "plugins", "swarm")
	removedLink, realDir, err := RemoveLink(plugin)
	if err != nil {
		return changed, err
	}
	switch {
	case removedLink:
		changed = append(changed, plugin)
	case realDir:
		// §20: agy plugin uninstall exists (P0-8), but its argument form is not
		// verified, so a failure falls back to removing the folder directly.
		if _, err := run(ctx, "agy", "plugin", "uninstall", "swarm"); err != nil {
			if err := os.RemoveAll(plugin); err != nil {
				return changed, err
			}
		}
		if _, err := os.Stat(plugin); err == nil {
			if err := os.RemoveAll(plugin); err != nil {
				return changed, err
			}
		}
		changed = append(changed, plugin)
	}
	return changed, nil
}

// dropSwarmMCPServer removes the "swarm" entry from an mcpServers object, keeping
// every other server. Shared with the cursor removal.
func dropSwarmMCPServer(m map[string]any) error {
	servers, _ := m["mcpServers"].(map[string]any)
	if servers == nil {
		return nil
	}
	delete(servers, "swarm")
	return nil
}

// CheckAgy is doctor's agy block (§21.4 row 1, plus A1's skills check).
func CheckAgy(ctx context.Context, c Config, run execx.Runner) []Check {
	return []Check{agyHooksCheck(c), CheckSkills(c, KindAgy)}
}

func agyHooksCheck(c Config) Check {
	p := c.Gemini("config", "hooks.json")
	body, err := os.ReadFile(p)
	if err != nil {
		return Check{"agy hooks", false, "Not installed. Run swarm install."}
	}
	if strings.Contains(string(body), "${PLUGIN_ROOT}") {
		return Check{"agy hooks", false, p + " still uses ${PLUGIN_ROOT}. Run swarm install."}
	}
	var f map[string]map[string][]map[string]any
	if err := json.Unmarshal(body, &f); err != nil {
		return Check{"agy hooks", false, "Invalid " + p + ". Run swarm install."}
	}
	block, ok := f[agyHookName]
	if !ok {
		return Check{"agy hooks", false, "The swarm hook block is missing from " + p + ". Run swarm install."}
	}
	for _, ev := range agyFlatEvents {
		entries := block[ev]
		if len(entries) == 0 {
			return Check{"agy hooks", false, ev + " is missing from " + p + ". Run swarm install."}
		}
		cmd, _ := entries[0]["command"].(string)
		if cmd == "" {
			return Check{"agy hooks", false,
				ev + " must be a flat { command } handler; a nested hooks array fails to parse in agy. Run swarm install."}
		}
		if cmd != c.Bin+" hook agy "+ev {
			return Check{"agy hooks", false, ev + " points somewhere else: " + cmd + ". Run swarm install."}
		}
	}
	for _, ev := range agyMatcherEvents {
		entries := block[ev]
		if len(entries) == 0 {
			return Check{"agy hooks", false, ev + " is missing from " + p + ". Run swarm install."}
		}
		if m, _ := entries[0]["matcher"].(string); m != "*" {
			return Check{"agy hooks", false, ev + " needs a matcher wrapper. Run swarm install."}
		}
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return Check{"agy hooks", false, "The hook binary is missing: " + c.Bin + ". Run make install."}
	}
	return Check{"agy hooks", true, p}
}
