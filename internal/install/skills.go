package install

import (
	"embed"
	"fmt"
	"path/filepath"
)

// Both skills ship inside the binary so swarm install works from any directory
// (§18: they are installed for every agent, at four fixed roots).
//
//go:embed skills/swarm/SKILL.md skills/swarm-orchestrator/SKILL.md
var skillFS embed.FS

// SkillNames is §18's list, in kickoff order: every agent loads `swarm`, and
// orchestrators also load `swarm-orchestrator`.
var SkillNames = []string{"swarm", "swarm-orchestrator"}

// SkillBody is the embedded SKILL.md for name. It panics on an unknown name: a
// typo must fail at the call site, not install an empty skill.
func SkillBody(name string) []byte {
	body, err := skillFS.ReadFile("skills/" + name + "/SKILL.md")
	if err != nil {
		panic(fmt.Sprintf("install: no embedded skill %q", name))
	}
	return body
}

// WriteSkills installs both skills for k and returns the paths it changed.
func WriteSkills(c Config, k Kind) ([]string, error) {
	root := c.SkillsDir(k)
	if root == "" {
		return nil, fmt.Errorf("install: no skills folder for agent %q", k)
	}
	var changed []string
	for _, name := range SkillNames {
		p := filepath.Join(root, name, "SKILL.md")
		wrote, err := WriteIfChanged(p, SkillBody(name), 0o644)
		if err != nil {
			return changed, err
		}
		if wrote {
			changed = append(changed, p)
		}
	}
	return changed, nil
}
