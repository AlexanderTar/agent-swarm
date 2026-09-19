package advisor

import (
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// §11.6: every row of the mode table.
func TestMode(t *testing.T) {
	cases := []struct {
		session runtime.AgentKind
		advisor runtime.AgentKind
		model   string
		capable bool
		want    string
	}{
		{runtime.Claude, runtime.Claude, "claude-fable-5-1", true, "native"},
		{runtime.Claude, runtime.Claude, "claude-opus-5", true, "native"},
		{runtime.Claude, runtime.Claude, "claude-haiku-4-5", false, "simulated"},
		{runtime.Claude, runtime.Codex, "gpt-6-astra", false, "simulated"},
		{runtime.Codex, runtime.Claude, "claude-fable-5-1", true, "simulated"},
		{runtime.Agy, runtime.Claude, "claude-opus-5", true, "simulated"},
		{runtime.Cursor, runtime.Cursor, "auto", false, "simulated"},
		{runtime.Claude, runtime.Claude, "none", false, ""},
		{runtime.Codex, runtime.Codex, "none", false, ""},
	}
	for _, c := range cases {
		if got := Mode(c.session, c.advisor, c.model, c.capable); got != c.want {
			t.Errorf("Mode(%s, %s, %s, %v) = %q, want %q",
				c.session, c.advisor, c.model, c.capable, got, c.want)
		}
	}
}
