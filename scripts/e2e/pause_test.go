//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
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

// Scenario 7: pause timeout. The fake agent ignores the pause outright (no
// hook call, no checkpoint) — TickPause has to notice the deadline itself,
// send the interrupt keys, and kill the pane on its own 10s timer
// (killAfterInterrupt in pause.go), which nothing here can shorten without a
// production change. The daemon's reconcile tick is a fixed 5s
// (cmd/swarm/daemon.go), so the only production-side seam available is the
// deadline itself (settings.pause_deadline_sec) — shortened to 3s here, per
// the spec text, via setPauseDeadlineSec rather than waiting out the real
// 120s default. No explicit outer bound is given for this scenario (unlike
// 6/10's "within 6s"), so the poll timeout below is sized generously for the
// deadline + interrupt + kill-then-detect chain, not tightly.
func TestScenario07PauseTimeout(t *testing.T) {
	h := newHarness(t)
	h.setPauseDeadlineSec(t, 3)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	task := h.firstTask(t, epic)
	coder := h.spawn(t, orch, task, "coder")
	if !h.waitForSessionState(t, coder, "running", 5*time.Second) {
		t.Fatalf("coder session = %s, never reached running", h.sessionState(t, coder))
	}

	since := time.Now()
	h.pause(t, coder, "session")

	// Ignore it entirely: no hookPost, no swarm_checkpoint. Reaching
	// "interrupted" is only possible via getInterrupted != nil in
	// reconcile.go's resolveDead — that IS the proof the interrupt keys were
	// sent; there is no DB-visible record of the send itself (pause.go's own
	// comment: "no column exists for it, and none is needed").
	if !h.waitForSessionState(t, coder, "interrupted", 40*time.Second) {
		t.Fatalf("coder session = %s, want interrupted", h.sessionState(t, coder))
	}
	if !h.waitForNotification(t, "agent.interrupted", since, 2*time.Second) {
		t.Fatal("no agent.interrupted notification")
	}
}

// handOffAndKill drives an already-pausing agent to "paused" the way
// scenario 6 does: a handoff checkpoint, then the pane exiting on its own
// (h.killPane) rather than waiting out TickPause's slower kill-then-detect
// fallback. It does not itself request the pause — allowedActions
// (internal/httpapi/spawn.go) only permits the "pause" action from session
// state Running, so a session pause-all already moved to pause_requested
// would 409 on a second POST /pause; the caller is expected to have
// requested the pause already (directly, or via pause-all).
func handOffAndKill(t *testing.T, h *harness, agentName string) {
	t.Helper()
	h.mustTool(t, agentName, "swarm_checkpoint", map[string]any{"kind": "handoff", "summary": "pausing"})
	h.killPane(t, agentName)
	if !h.waitForSessionState(t, agentName, "paused", 6*time.Second) {
		t.Fatalf("%s session = %s, want paused within 6s", agentName, h.sessionState(t, agentName))
	}
}

// Scenario 8: pause all and resume. PauseAll (internal/runtime/pause.go)
// requests a plain session-scope pause on every live root and worker alike —
// TestPauseAllCountsEverySession (internal/runtime/pause_test.go) pins this
// as deliberate, not the hierarchical subtree pause pauseSubtree/
// promotePendingSubtreePauses implement — so nothing in PauseAll itself
// orders children ahead of orchestrators or writes a daemon-combined
// checkpoint (that machinery is scenario 9's, gated on pause_scope =
// 'subtree', which pause-all never sets). "Children pause before
// orchestrators" here is enforced by the harness's own call order below —
// pause-all requests all four pauses in the same instant, and each of the
// four agents only actually reaches "paused" once something drives its own
// PreToolUse-denial-then-handoff cycle, exactly as scenario 6 does — so
// which one pauses first is a property of when its handoff was written, not
// of any daemon-side ordering.
func TestScenario08PauseAllAndResume(t *testing.T) {
	h := newHarness(t)
	epic1 := h.materializedEpic(t)
	orch1 := h.startOrchestrator(t, epic1)
	h.mustTool(t, orch1, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	child1 := h.spawn(t, orch1, h.firstTask(t, epic1), "coder")

	epic2 := h.materializedEpic(t)
	orch2 := h.startOrchestrator(t, epic2)
	h.mustTool(t, orch2, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	child2 := h.spawn(t, orch2, h.firstTask(t, epic2), "coder")

	for _, name := range []string{orch1, child1, orch2, child2} {
		if !h.waitForSessionState(t, name, "running", 5*time.Second) {
			t.Fatalf("%s session = %s, never reached running", name, h.sessionState(t, name))
		}
	}

	if n := h.pauseAll(t); n < 4 {
		t.Fatalf("pause-all requested = %d, want at least the 4 agents just spawned", n)
	}

	// Children first, on both roots, before either orchestrator: the
	// deterministic order this scenario is asserting is that pause-all's
	// uniform session-scope pause permits (never forbids) a child finishing
	// before its own orchestrator.
	handOffAndKill(t, h, child1)
	handOffAndKill(t, h, child2)
	handOffAndKill(t, h, orch1)
	handOffAndKill(t, h, orch2)

	var childEndedAt, orchEndedAt int64
	if err := h.db(t).QueryRow(`SELECT s.ended_at FROM sessions s JOIN agents a ON a.id = s.agent_id
		WHERE a.name = ? ORDER BY s.generation DESC LIMIT 1`, child1).Scan(&childEndedAt); err != nil {
		t.Fatal(err)
	}
	if err := h.db(t).QueryRow(`SELECT s.ended_at FROM sessions s JOIN agents a ON a.id = s.agent_id
		WHERE a.name = ? ORDER BY s.generation DESC LIMIT 1`, orch1).Scan(&orchEndedAt); err != nil {
		t.Fatal(err)
	}
	if childEndedAt > orchEndedAt {
		t.Fatalf("child1 paused at %d, after orch1 at %d, want child first", childEndedAt, orchEndedAt)
	}

	// Resume: orch1 gets generation 2 and a new token; the generation-1
	// token sessionAuth already rejects (internal/httpapi/server.go, proven
	// at the unit level by TestMCPRejectsAStaleGenerationToken) — this is
	// the same mechanism exercised end to end.
	oldToken := h.sessionToken(t, orch1)
	h.resume(t, orch1)
	var gen int
	var newToken string
	if err := h.db(t).QueryRow(`SELECT generation FROM sessions WHERE agent_id =
		(SELECT id FROM agents WHERE name = ?) ORDER BY generation DESC LIMIT 1`, orch1).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	if gen != 2 {
		t.Fatalf("orch1 generation = %d, want 2", gen)
	}
	newToken = h.sessionToken(t, orch1)
	if newToken == oldToken {
		t.Fatal("resume kept the same token")
	}

	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	req, err := http.NewRequest(http.MethodPost, h.url+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+oldToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old-generation token on /mcp = %d, want 401", resp.StatusCode)
	}
}
