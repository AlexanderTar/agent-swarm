//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/hook"
)

// Scenario 15: a hook running outside a Swarm-spawned pane (no SWARM_SESSION)
// is a silent, fast no-op — S6's fail-safe, so running `swarm hook` by hand
// never blocks a real editor/CLI action.
func TestScenario15HooksOutsideSwarm(t *testing.T) {
	os.Unsetenv("SWARM_SESSION")
	os.Unsetenv("SWARM_TOKEN_FILE")
	env := func(k string) string {
		if k == "SWARM_SESSION" || k == "SWARM_TOKEN_FILE" {
			return ""
		}
		return os.Getenv(k)
	}
	var out bytes.Buffer
	start := time.Now()
	code := hook.Run([]string{"claude", "Stop"}, strings.NewReader(`{}`), &out, env, nil)
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("took %v, want under 50ms", d)
	}
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want none", out.String())
	}
}
