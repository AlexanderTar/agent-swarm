package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

// isolatingKindFixture describes, for one isolating agent kind, where its
// SuperpowersInstalled() check looks on the real UserHome and where its
// setupEnv's isolated environment is expected to make that same content
// reachable. A kind landing here with a mismatch means SuperpowersInstalled
// could report "installed" for a spawn that will not actually see the
// plugin -- exactly the bug fixed in Tasks 1 and 2.
type isolatingKindFixture struct {
	name         string
	realRel      []string // path segments under UserHome holding the plugin fixture
	isolatedRel  []string // path segments under the isolated home the fixture must appear at
	launch       func(t *testing.T, d Deps) (Launch, error)
	isolatedHome func(l Launch) string
}

func superpowersParityFixtures() []isolatingKindFixture {
	return []isolatingKindFixture{
		{
			name:         "codex",
			realRel:      []string{".codex", "plugins", "cache", "obra", "superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md"},
			isolatedRel:  []string{"plugins", "cache", "obra", "superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md"},
			launch:       func(t *testing.T, d Deps) (Launch, error) { return newCodex(d).Launch(codexSpec(t)) },
			isolatedHome: func(l Launch) string { return l.Env["CODEX_HOME"] },
		},
		{
			name:         "agy",
			realRel:      []string{".gemini", "config", "plugins", "superpowers", "skills", "brainstorming", "SKILL.md"},
			isolatedRel:  []string{".gemini", "config", "plugins", "superpowers", "skills", "brainstorming", "SKILL.md"},
			launch:       func(t *testing.T, d Deps) (Launch, error) { return newAgy(d).Launch(agySpec(t)) },
			isolatedHome: func(l Launch) string { return l.Env["HOME"] },
		},
	}
}

func TestSuperpowersPluginReachesEveryIsolatedHome(t *testing.T) {
	for _, f := range superpowersParityFixtures() {
		t.Run(f.name, func(t *testing.T) {
			d := testDeps(t)
			real := append([]string{d.UserHome}, f.realRel...)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(real...)), 0o755); err != nil {
				t.Fatal(err)
			}
			const body = "# Brainstorming\n..."
			if err := os.WriteFile(filepath.Join(real...), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			l, err := f.launch(t, d)
			if err != nil {
				t.Fatal(err)
			}
			isolated := append([]string{f.isolatedHome(l)}, f.isolatedRel...)
			got, err := os.ReadFile(filepath.Join(isolated...))
			if err != nil {
				t.Fatalf("%s: superpowers plugin not reachable in isolated home: %v", f.name, err)
			}
			if string(got) != body {
				t.Errorf("%s: plugin body = %q, want %q", f.name, got, body)
			}
		})
	}
}
