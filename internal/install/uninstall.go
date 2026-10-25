package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Uninstall removes the daemon's service and Swarm's own entries in each agent's
// configuration. It keeps ~/.swarm (data), the superpowers plugins (§12.4) and the
// cursor attribution keys (L23).
func Uninstall(ctx context.Context, o AgentsOpts) error {
	svc := o.Cfg.Service()
	if o.Cfg.Isolated {
		if o.Out != nil {
			fmt.Fprintf(o.Out, "%s is not the default swarm home, so the %s and the agents' own configuration under %s were left alone. Pass --live to remove them anyway.\n", o.Cfg.Home, svc.Noun(), o.Cfg.UserHome)
		}
		return nil
	}
	if err := svc.Stop(ctx, o.Run); err != nil {
		return err
	}
	unit := svc.Path()
	if err := os.Remove(unit); err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil {
		o.report("removed", []string{unit})
		if err := svc.Removed(ctx, o.Run); err != nil {
			return err
		}
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
		{o.Cfg.Muse("settings.json"), dropSwarmMCPServer},
	} {
		wrote, err := EditJSON(step.path, false, step.edit)
		if err != nil {
			return err
		}
		if wrote {
			o.report("removed", []string{step.path})
		}
	}
	// grok's config.toml and auth links, only Swarm's own parts.
	grokRemoved, grokSkipped, err := UninstallGrok(o.Cfg)
	if err != nil {
		return err
	}
	o.report("removed", grokRemoved)
	o.report("skipped", grokSkipped)
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

	// Every registered skill, from every kind's root — but only swarm's own
	// entries (A1): a same-named skill the user made themselves is left alone.
	skillsHome, err := SkillsHome(o.Cfg.Home)
	if err != nil {
		return err
	}
	for _, k := range Kinds {
		for _, name := range SkillNames() {
			dir := filepath.Join(o.Cfg.SkillsDir(k), name)
			owned, err := isSwarmOwned(dir, skillsHome, true) // explicit `swarm uninstall`: adopt
			if err != nil {
				return err
			}
			if !owned {
				continue
			}
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
	// The graphify CLI stays, like the plugins (§12.4): say how to remove it.
	if o.Out != nil {
		fmt.Fprintln(o.Out, "graphify was left installed. Remove it with: uv tool uninstall graphifyy")
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
