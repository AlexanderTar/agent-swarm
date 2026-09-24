package install

import (
	"context"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// Claude's own MCP config, hooks and --settings for a SPAWNED session are still
// built per launch by the adapter (§11.1). Separately, a global "swarm" MCP
// server is registered via `claude mcp add -s user` (never by hand-editing
// ~/.claude.json, which Claude Code rewrites on its own and Swarm never edits
// directly, §11.5), so an interactive, non-swarm-spawned session also gets
// swarm's tools. Remove-then-add makes this idempotent and self-healing against
// a stale binary path from an older build.
//
// The v1 symlink and v2's own skill symlink (A1) share the same path
// (c.Claude("skills", "swarm")): a leftover v1 link points outside
// ~/.swarm/skills, at the old release's plugin folder, so it must be replaced
// rather than mistaken for v2's own link. RemoveLegacyClaude runs first and
// only removes a link that is not already swarm-owned.
func WriteClaude(ctx context.Context, c Config, run execx.Runner) ([]string, error) {
	if _, err := RemoveLegacyClaude(c); err != nil {
		return nil, err
	}
	_, _ = run(ctx, "claude", "mcp", "remove", "swarm", "-s", "user")
	if _, err := run(ctx, "claude", "mcp", "add", "swarm", "-s", "user", "--", c.Bin, "mcp"); err != nil {
		return nil, err
	}
	changed, _, err := WriteSkills(c, KindClaude)
	return changed, err
}

// RemoveLegacyClaude removes the v1 symlink ~/.claude/skills/swarm. It is removed
// as a link, never followed (§20). A1: v2 legitimately symlinks this same path
// into ~/.swarm/skills/swarm, so only a link that does not resolve there counts
// as legacy; v2's own link (or a real v2-managed directory) is left alone.
func RemoveLegacyClaude(c Config) ([]string, error) {
	link := c.Claude("skills", "swarm")
	skillsHome, err := SkillsHome(c.Home)
	if err != nil {
		return nil, err
	}
	owned, err := isSwarmOwned(link, skillsHome)
	if err != nil {
		return nil, err
	}
	if owned {
		return nil, nil
	}
	removed, _, err := RemoveLink(link)
	if err != nil {
		return nil, err
	}
	if removed {
		return []string{link}, nil
	}
	return nil, nil
}

func CheckClaude(ctx context.Context, c Config, run execx.Runner) []Check {
	return []Check{CheckSkills(c, KindClaude)}
}
