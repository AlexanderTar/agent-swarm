package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

// The daemon deletes ~/.swarm/work/<agent> once an agent finishes and
// MkdirAlls an empty one on Retry (runtime.ReclaimWorkDirs), so every adapter
// must rewrite whatever it needs in the cwd on every Launch/Resume.
func TestEveryAdapterLaunchesAndResumesIntoAFreshEmptyCwd(t *testing.T) {
	cases := map[string]struct {
		make  func(Deps) Adapter
		files []string
	}{
		"claude": {func(d Deps) Adapter { return newClaude(d) },
			[]string{".mcp.json", filepath.Join(".claude", "skills", "swarm", "SKILL.md")}},
		"codex":  {func(d Deps) Adapter { return newCodex(d) }, nil},
		"cursor": {func(d Deps) Adapter { return newCursor(d) }, []string{"AGENTS.md"}},
		"agy":    {func(d Deps) Adapter { return newAgy(d) }, nil},
		"muse":   {func(d Deps) Adapter { return newMuse(d) }, []string{"AGENTS.md"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := testDeps(t)
			seedSkillsHome(t, d.Home)
			a := tc.make(d)
			for _, resume := range []bool{false, true} {
				cwd := filepath.Join(t.TempDir(), "work", "agent")
				if err := os.MkdirAll(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				s := Spec{AgentName: "agent", AgentID: "ag_01", SessionID: "ses_01", Token: "tok",
					TokenFile: filepath.Join(d.Home, "run", "tokens", "ses_01"),
					DaemonURL: "http://127.0.0.1:17778", Model: "m", Cwd: cwd, Kickoff: "kick",
					Instructions: "do it", Bin: "/usr/local/bin/swarm",
					ProviderSessionID: "11111111-2222-4333-8444-555555555555"}
				var err error
				if resume {
					_, err = a.Resume(s)
				} else {
					_, err = a.Launch(s)
				}
				if err != nil {
					t.Fatalf("resume=%v: %v", resume, err)
				}
				for _, f := range tc.files {
					if _, err := os.Stat(filepath.Join(cwd, f)); err != nil {
						t.Errorf("resume=%v: %s missing after launch into a fresh cwd: %v", resume, f, err)
					}
				}
			}
		})
	}
}
