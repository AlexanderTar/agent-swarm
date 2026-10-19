package install_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Research and design note paths hang off SWARM_HOME, which can point
// anywhere, so skills must not hardcode ~/.swarm/research or ~/.swarm/designs;
// they point at $SWARM_HOME and the brief's resolved directory instead.
func TestSkillsDoNotHardcodeResearchOrDesignDirs(t *testing.T) {
	files, err := filepath.Glob("../../skills/*/SKILL.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("skills glob = %v, %v", files, err)
	}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"~/.swarm/research", "~/.swarm/designs"} {
			if strings.Contains(string(body), bad) {
				t.Errorf("%s hardcodes %s; use $SWARM_HOME/<dir>/", f, bad)
			}
		}
	}
}
