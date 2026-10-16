package grokhome_test

import (
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
)

func TestDirIsUnderTheSwarmHome(t *testing.T) {
	if got, want := grokhome.Dir("/h/.swarm"), filepath.Join("/h/.swarm", "grok"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
	if grokhome.Bin != "grok" {
		t.Errorf("Bin = %q", grokhome.Bin)
	}
}

// Every grok subprocess gets GROK_HOME and all 18 external-compat switches off
// (3 foreign agents x 6 surfaces, probed in the grok binary), so Claude, Cursor
// and Codex config never leaks into a Swarm grok session.
func TestEnvSetsHomeAndCompatOff(t *testing.T) {
	env := grokhome.Env("/h/.swarm")
	if env["GROK_HOME"] != filepath.Join("/h/.swarm", "grok") {
		t.Errorf("GROK_HOME = %q", env["GROK_HOME"])
	}
	n := 0
	for _, agent := range []string{"CLAUDE", "CURSOR", "CODEX"} {
		for _, surface := range []string{"SKILLS", "RULES", "AGENTS", "MCPS", "HOOKS", "SESSIONS"} {
			key := "GROK_" + agent + "_" + surface + "_ENABLED"
			if v, ok := env[key]; !ok || v != "0" {
				t.Errorf("%s = %q (present %v), want \"0\"", key, v, ok)
			}
			n++
		}
	}
	if len(env) != n+1 {
		t.Errorf("Env has %d keys, want GROK_HOME plus %d switches: %v", len(env), n, env)
	}
	// ask_user_question stays on: Swarm turns the card into a question row.
	if _, ok := env["GROK_ASK_USER_QUESTION"]; ok {
		t.Error("GROK_ASK_USER_QUESTION must be absent")
	}
}

func TestEnvReturnsAFreshMap(t *testing.T) {
	a := grokhome.Env("/h")
	a["GROK_HOME"] = "changed"
	if grokhome.Env("/h")["GROK_HOME"] == "changed" {
		t.Error("Env shares state between calls")
	}
}
