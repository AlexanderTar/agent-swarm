package install

import (
	"context"
	"os"
	"path/filepath"

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
// The v1 symlink and the v2 skills folder share the same path
// (c.Claude("skills", "swarm")): if the v1 link is still there, WriteSkills'
// MkdirAll/WriteFile/Rename would transparently follow it into the v1 release
// target instead of creating a real v2 directory. RemoveLegacyClaude must run
// first so WriteSkills always lands on a real path.
func WriteClaude(ctx context.Context, c Config, run execx.Runner) ([]string, error) {
	if _, err := RemoveLegacyClaude(c); err != nil {
		return nil, err
	}
	_, _ = run(ctx, "claude", "mcp", "remove", "swarm", "-s", "user")
	if _, err := run(ctx, "claude", "mcp", "add", "swarm", "-s", "user", "--", c.Bin, "mcp"); err != nil {
		return nil, err
	}
	return WriteSkills(c, KindClaude)
}

// RemoveLegacyClaude removes the v1 symlink ~/.claude/skills/swarm. It is removed
// as a link, never followed (§20). A real directory there is v2's own skill folder,
// so it is left alone.
func RemoveLegacyClaude(c Config) ([]string, error) {
	link := c.Claude("skills", "swarm")
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
	root := c.SkillsDir(KindClaude)
	for _, name := range SkillNames() {
		p := filepath.Join(root, name, "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			return []Check{{"Claude skills", false, "Missing " + p + ". Run swarm install."}}
		}
	}
	return []Check{{"Claude skills", true, root}}
}
