package adapter

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

// TASK-1206 (BUG-105): spawn.Spawner moves a long argv element into a launch
// file, element by element. Every kind must therefore hand its kickoff over as
// one whole argv element, never spliced into a longer string, or a long
// kickoff would stay on tmux's command line.
func TestEveryAdapterPassesTheKickoffAsOneArgvElement(t *testing.T) {
	kickoff := "You are swarm agent a.\nCall swarm_sync. " + strings.Repeat("long ", 4000)
	for _, kind := range []string{"claude", "codex", "cursor", "agy", "grok", "muse", "fake"} {
		t.Run(kind, func(t *testing.T) {
			d := testDeps(t)
			seedSkillsHome(t, d.Home)
			a, err := New(kinds.AgentKind(kind), d)
			if err != nil {
				t.Fatal(err)
			}
			cwd := filepath.Join(t.TempDir(), "work", "agent")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			l, err := a.Launch(Spec{AgentName: "agent", AgentID: "ag_01", SessionID: "ses_01", Token: "tok",
				TokenFile: filepath.Join(d.Home, "run", "tokens", "ses_01"),
				DaemonURL: "http://127.0.0.1:17778", Model: "m", Cwd: cwd, Kickoff: kickoff,
				Bin: "/usr/local/bin/swarm", ProviderSessionID: "11111111-2222-4333-8444-555555555555"})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(l.Argv, kickoff) {
				t.Fatalf("argv has no element equal to the kickoff: %d elements", len(l.Argv))
			}
		})
	}
}
