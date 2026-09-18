package install

import (
	"context"
	"os"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// Claude needs no global configuration: its MCP config, hooks and --settings are
// built per launch by the adapter (§11.1), and its trust state lives in
// ~/.claude.json, which Claude Code rewrites itself and Swarm never edits (§11.5).
// So install writes the two skills and nothing else.
func WriteClaude(c Config) ([]string, error) {
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
	for _, name := range SkillNames {
		p := filepath.Join(root, name, "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			return []Check{{"Claude skills", false, "Missing " + p + ". Run swarm install."}}
		}
	}
	return []Check{{"Claude skills", true, root}}
}
