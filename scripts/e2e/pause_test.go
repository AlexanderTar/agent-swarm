//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// Scenario 6: pause with handoff. The coder is paused (session scope); its
// next PreToolUse (a non-swarm tool) is denied with the control notice; it
// writes a handoff checkpoint and its process exits (simulated the same way
// scenario 26 ends an attempt: h.killPane, "exactly what a real agent
// process exiting on its own would leave behind" — its own doc comment).
// That single reconcile tick (≤5s) is what proves the two "within 6s"
// checks: resolveDead's Stopping branch needs only the pane to already be
// gone, not TickPause's separate killAfterHandoff/kill-then-detect path,
// which is a slower fallback for an agent that does NOT exit on its own
// (scenario 9 exercises that path deliberately).
func TestScenario06PauseWithHandoff(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	task := h.firstTask(t, epic)
	coder := h.spawn(t, orch, task, "coder")
	// pause is only allowed once the session reaches "running" (§10.7's
	// table) — watchStartup gets there once its real pane draws the fake
	// adapter's idle prompt, a background race this harness has to wait out
	// rather than assume already won by the time swarm_spawn returns.
	if !h.waitForSessionState(t, coder, "running", 5*time.Second) {
		t.Fatalf("coder session = %s, never reached running", h.sessionState(t, coder))
	}

	since := time.Now()
	h.pause(t, coder, "session")

	out := h.hookPost(t, coder, "PreToolUse", map[string]any{"command": "echo hi"})
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "deny" {
		t.Fatalf("PreToolUse decision = %+v, want deny", out)
	}
	reason, _ := hso["permissionDecisionReason"].(string)
	if reason == "" {
		t.Fatalf("PreToolUse denial has no reason: %+v", out)
	}

	h.mustTool(t, coder, "swarm_checkpoint", map[string]any{"kind": "handoff", "summary": "pausing, handing off"})
	h.killPane(t, coder)

	if !h.waitForSessionState(t, coder, "paused", 6*time.Second) {
		t.Fatalf("coder session = %s, want paused within 6s", h.sessionState(t, coder))
	}
	if !h.waitForNotification(t, "agent.paused", since, 2*time.Second) {
		t.Fatal("no agent.paused notification")
	}
	if !h.waitForRelay(t, orch, "paused", since, 2*time.Second) {
		t.Fatal("no relay paused message to the parent")
	}
}
