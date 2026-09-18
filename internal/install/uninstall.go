package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Uninstall removes the launchd job and Swarm's own entries in each agent's
// configuration. It keeps ~/.swarm (data), the superpowers plugins (§12.4) and the
// cursor attribution keys (L23).
func Uninstall(ctx context.Context, o AgentsOpts) error {
	target := fmt.Sprintf("gui/%d/%s", o.Cfg.UID, Label)
	if _, err := o.Run(ctx, "launchctl", "bootout", target); err != nil && !notLoaded(err) {
		return fmt.Errorf("launchctl bootout %s: %w", Label, err)
	}
	plist := PlistPath(o.Cfg)
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil {
		o.report("removed", []string{plist})
	}

	// Hook and MCP entries. EditJSON with create=false never creates a file.
	for _, step := range []struct {
		path string
		edit func(m map[string]any) error
	}{
		{o.Cfg.Codex("hooks.json"), dropSwarmHookEntries},
		{o.Cfg.Cursor("hooks.json"), dropSwarmCursorHookEntries},
		{o.Cfg.Cursor("mcp.json"), dropSwarmMCPServer},
		{o.Cfg.Gemini("config", "hooks.json"), dropAgySwarmBlock},
	} {
		wrote, err := EditJSON(step.path, false, step.edit)
		if err != nil {
			return err
		}
		if wrote {
			o.report("removed", []string{step.path})
		}
	}
	// agy registered its MCP server through its own CLI, so remove it the same way.
	// A failure is not fatal: the agent may be gone, and the entry is inert.
	if _, err := os.Stat(o.Cfg.Gemini("antigravity", "mcp_config.json")); err == nil {
		if _, err := o.Run(ctx, "agy", "mcp", "remove", "swarm"); err != nil {
			wrote, err2 := EditJSON(o.Cfg.Gemini("antigravity", "mcp_config.json"), false, dropSwarmMCPServer)
			if err2 != nil {
				return err2
			}
			if wrote {
				o.report("removed", []string{o.Cfg.Gemini("antigravity", "mcp_config.json")})
			}
		}
	}

	// Both skills, from all four roots.
	for _, k := range Kinds {
		for _, name := range SkillNames {
			dir := filepath.Join(o.Cfg.SkillsDir(k), name)
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		}
	}

	// ~/.local/bin/swarm, only if it is ours.
	link := o.Cfg.LocalBin()
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if cur, err := os.Readlink(link); err == nil && cur == o.Cfg.Bin {
			if err := os.Remove(link); err != nil {
				return err
			}
			o.report("removed", []string{link})
		}
	}
	return nil
}

// dropSwarmCursorHookEntries removes swarm's commands from a cursor hooks.json.
func dropSwarmCursorHookEntries(m map[string]any) error {
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
}

// dropAgySwarmBlock removes the whole "swarm" hook name, leaving other plugins'.
func dropAgySwarmBlock(m map[string]any) error {
	delete(m, agyHookName)
	return nil
}
