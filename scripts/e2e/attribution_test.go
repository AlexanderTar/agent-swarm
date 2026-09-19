//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/hook"
)

// Scenario 20: attribution gate. A `git commit` with a Co-Authored-By
// trailer is denied by the PreToolUse hook; a clean, signed commit goes
// through. The repo signs with a throwaway GPG key created in the test's
// own GNUPGHOME, never the user's key.
//
// R10: the commit a real agent would run happens inside a tmux pane the
// daemon spawned, whose whole environment is spawn.BaseEnv plus the spawn's
// own Launch.Env — a GNUPGHOME set only in the Go test process is invisible
// to it. Without proving the daemon's own GNUPGHOME reached that pane, "a
// signed commit goes through" could just as well have signed with the
// user's real secret key. This scenario proves it two ways: paneEnv reads
// GNUPGHOME straight off the real pane the daemon spawned for a coder (not
// the harness's own env), and gitRepo's commit — made in the Go test
// process, which shares that same GNUPGHOME because scripts/e2e.sh exports
// it once to the shell that starts both the daemon and `go test` — is
// checked against the throwaway key's own id and identity, not merely
// "some" signature.
func TestScenario20AttributionGate(t *testing.T) {
	h := newHarness(t)
	gnupgHome, keyID := throwawayGPGKey(t)

	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	task := h.firstTask(t, epic)
	worker := h.spawn(t, orch, task, "coder")

	// R10: the pane's own environment, not the test process's.
	if got := h.paneEnv(t, worker, "GNUPGHOME"); got != gnupgHome {
		t.Fatalf("the coder's tmux pane has GNUPGHOME=%q, want the throwaway keyring %q", got, gnupgHome)
	}

	// a commit with an attribution trailer is denied
	out := h.hookPost(t, worker, "PreToolUse", map[string]any{
		"command": "git commit -m \"fix: x\n\nCo-Authored-By: Someone <someone@example.com>\"",
	})
	spec, _ := out["hookSpecificOutput"].(map[string]any)
	if spec == nil || spec["permissionDecision"] != "deny" {
		t.Fatalf("hookPost = %+v, want a deny decision", out)
	}
	if reason, _ := spec["permissionDecisionReason"].(string); reason != hook.AttrReason {
		t.Fatalf("reason = %q, want %q", reason, hook.AttrReason)
	}

	// disabling signing is denied too (§17.3's other reason)
	out = h.hookPost(t, worker, "PreToolUse", map[string]any{"command": "git commit -m ok --no-gpg-sign"})
	spec, _ = out["hookSpecificOutput"].(map[string]any)
	if reason, _ := spec["permissionDecisionReason"].(string); reason != hook.SignReason {
		t.Fatalf("reason = %q, want %q", reason, hook.SignReason)
	}

	// a clean commit is allowed (no block, so an empty body)
	if out := h.hookPost(t, worker, "PreToolUse", map[string]any{"command": "git commit -m \"fix: x\""}); out != nil {
		t.Fatalf("hookPost = %+v, want no block", out)
	}

	// the actual signed commit: proof R10 exists for. Without GNUPGHOME in
	// BaseEnv this would sign with (or hang waiting for) the user's own key.
	repo := gitRepo(t, true, keyID)
	signer := strings.TrimSpace(gitOut(t, repo, "log", "-1", "--format=%GK"))
	if !strings.HasSuffix(signer, keyID[len(keyID)-16:]) {
		t.Fatalf("the commit was signed by %q, want the throwaway key %q", signer, keyID)
	}
	if identity := gitOut(t, repo, "log", "-1", "--format=%GS"); !strings.Contains(identity, "Swarm E2E") {
		t.Fatalf("signer = %q, want the throwaway identity", identity)
	}
}
