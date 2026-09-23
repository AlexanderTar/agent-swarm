//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
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
	if want := runtime.ControlNotice(coder, task); reason != want {
		t.Fatalf("PreToolUse denial reason = %q, want %q", reason, want)
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

// assertChildrenBeforeOrchestrator drives one root's subtree-scoped pause
// (already requested via pause-all) to completion, and proves the ordering
// is daemon-imposed rather than merely permitted: the orchestrator's own
// session must still be "running" immediately after pause-all (pauseSubtree
// keeps a root running until every descendant is done, §10.5), it must
// promote to "pause_requested" only once its child reaches "paused", and the
// control message that promotion sends must carry the real child checkpoint
// promotePendingSubtreePauses computed from live DB state (agent name +
// checkpoint summary) — not anything this harness wrote into the message
// itself, since the harness never touches the pending control message at
// all.
func assertChildrenBeforeOrchestrator(t *testing.T, h *harness, orch, child string) {
	t.Helper()
	if got := h.sessionState(t, orch); got != "running" {
		t.Fatalf("%s session = %s immediately after pause-all, want still running until its child finishes", orch, got)
	}
	handOffAndKill(t, h, child)
	if !h.waitForSessionState(t, orch, "pause_requested", 6*time.Second) {
		t.Fatalf("%s session = %s, want pause_requested once its child is done (children-first, daemon-imposed)",
			orch, h.sessionState(t, orch))
	}
	var payload string
	if err := h.db(t).QueryRow(`SELECT m.payload_json FROM messages m JOIN agents a ON a.id = m.to_agent_id
		WHERE m.kind = 'control' AND a.name = ? ORDER BY m.created_at DESC LIMIT 1`, orch).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, child) || !strings.Contains(payload, "pausing") {
		t.Fatalf("%s's promoted pause payload does not carry the real child checkpoint: %s", orch, payload)
	}
	handOffAndKill(t, h, orch)
}

// Scenario 8: pause all and resume. PauseAll (internal/runtime/pause.go)
// routes a root with any live descendant through pauseSubtree, the same
// mechanism a direct POST /agents/{name}/pause {scope:"subtree"} call uses
// (§10.5's own heading names "Pause all" as subtree pause's other entry
// point; §23.2 scenario 8 tests pause-all specifically for children-first
// ordering and combined handoffs) — a root with no live descendant (a
// standalone spike) gets plain session scope instead, since there is
// nothing to cascade onto. assertChildrenBeforeOrchestrator below proves
// the resulting ordering is the daemon's own, not the harness's.
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

	// Both roots have a live child, so both must have been routed through
	// subtree scope (never session) by PauseAll.
	for _, orch := range []string{orch1, orch2} {
		var scope string
		if err := h.db(t).QueryRow(`SELECT COALESCE(pause_scope, '') FROM sessions
			WHERE agent_id = (SELECT id FROM agents WHERE name = ?) ORDER BY generation DESC LIMIT 1`,
			orch).Scan(&scope); err != nil {
			t.Fatal(err)
		}
		if scope != "subtree" {
			t.Fatalf("%s pause_scope = %q after pause-all, want subtree", orch, scope)
		}
	}

	assertChildrenBeforeOrchestrator(t, h, orch1, child1)
	assertChildrenBeforeOrchestrator(t, h, orch2, child2)

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

// Scenario 8's missing assertion (Fix R-1, final review): a queued spawn
// stays queued through a pause-all, and thaws once the pause resolves. The
// gap PauseAll had was invisible whenever a target still had a live
// descendant at the moment it ran (liveDescendants(ds) > 0 already picked
// subtree scope for the right reason) — it only showed up when the ONLY
// descendant left was a queued one, which liveDescendants cannot see at all
// (it has no session row). This drives exactly that shape end to end: one
// admitted child that gets cancelled to free the slot, one queued sibling,
// then pause-all.
func TestScenario08bPauseAllFreezesAQueuedSpawn(t *testing.T) {
	h := newHarness(t)
	// +2, not +1: the orchestrator started below now shares the same pool as
	// the worker it spawns (unify-agent-limits), so both need headroom before
	// the second child's spawn is the one that queues.
	before := h.countActiveAgents(t)
	h.setMaxConcurrentAgents(t, before+2)
	t.Cleanup(func() { h.setMaxConcurrentAgents(t, 200) })

	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	task1 := h.firstTask(t, epic)

	// a second task, sibling of the first, under the same story (scenario 11's
	// own pattern for a guaranteed-to-queue sibling)
	storyKey := ""
	{
		var list struct {
			Items []map[string]any `json:"items"`
		}
		h.doT(t, "GET", "/api/items?view=flat&root="+epic, nil, &list)
		for _, it := range list.Items {
			if it["type"] == "story" {
				storyKey, _ = it["key"].(string)
			}
		}
	}
	var task2Resp map[string]any
	h.doT(t, "POST", "/api/items", map[string]any{"type": "task", "title": "Two", "parent_key": storyKey}, &task2Resp)
	task2Key, _ := task2Resp["key"].(string)
	rev, _ := task2Resp["revision"].(float64)
	h.doT(t, "PATCH", "/api/items/"+task2Key, map[string]any{"status": "ready", "revision": int(rev)}, nil)

	first := h.spawn(t, orch, task1, "coder")
	if !h.waitForSessionState(t, first, "running", 5*time.Second) {
		t.Fatalf("%s session = %s, never reached running", first, h.sessionState(t, first))
	}

	out := h.mustTool(t, orch, "swarm_spawn", map[string]any{
		"item": task2Key, "role": "coder", "agent": "fake", "model": "fake-1",
	})
	if out["queued"] != true {
		t.Fatalf("second spawn = %+v, want queued:true", out)
	}
	second, _ := out["agent"].(string)
	if second == "" {
		t.Fatalf("swarm_spawn returned no agent name: %+v", out)
	}

	// Free first's slot before pause-all runs, so the queued sibling is the
	// orchestrator's only descendant left at that moment. Cancel's HTTP
	// response only comes back after RT.Cancel has already committed
	// state='finished', so there is nothing to wait for here -- waiting would
	// only widen the window in which the shared daemon's own 5s reconcile
	// tick could drain the now-free slot on its own, before pause-all runs.
	h.cancel(t, first)

	n := h.pauseAll(t)
	if h.agentState(t, second) != "queued" {
		t.Fatalf("%s was admitted before pause-all ran (a reconcile tick raced the cancel); rerun", second)
	}
	if n < 1 {
		t.Fatalf("pause-all requested = %d, want at least 1 (the orchestrator)", n)
	}

	var scope string
	if err := h.db(t).QueryRow(`SELECT COALESCE(pause_scope, '') FROM sessions
		WHERE agent_id = (SELECT id FROM agents WHERE name = ?) ORDER BY generation DESC LIMIT 1`,
		orch).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	if scope != "subtree" {
		t.Fatalf("%s pause_scope = %q after pause-all, want subtree -- it still has a queued descendant", orch, scope)
	}

	// The orchestrator has nothing live left to wait for, so it promotes to
	// pause_requested on the very next reconcile tick -- proving the freeze
	// really is live (rootHasLiveSubtreePause), not just a scope label.
	if !h.waitForSessionState(t, orch, "pause_requested", 6*time.Second) {
		t.Fatalf("%s session = %s, want pause_requested", orch, h.sessionState(t, orch))
	}
	if h.agentState(t, second) != "queued" {
		t.Fatalf("%s agent state = %s, want still queued while the pause is live", second, h.agentState(t, second))
	}

	// Resolve the orchestrator's own pause the way scenario 6 does (handoff,
	// then the pane exiting on its own) -- once it does, the freeze thaws.
	handOffAndKill(t, h, orch)
	if !h.waitForAgentState(t, second, "active", 6*time.Second) {
		t.Fatalf("%s agent state = %s, want active -- the pause resolved, so the queue must thaw and admit it", second, h.agentState(t, second))
	}
}

// Scenario 9: unresponsive orchestrator during a subtree pause. Neither the
// orchestrator nor its child ever responds — both are left on _default.json,
// which only ever syncs once then sleeps. pauseSubtree (pause.go) pauses the
// child first with the same deadline as the orchestrator's own; the child
// times out through the same interrupt-then-kill path scenario 7 exercises,
// which is what makes it "a child without a handoff" once it's no longer
// live — promotePendingSubtreePauses then promotes the orchestrator's own
// session to pause_requested, whose deadline is already long past, so
// writeUnresponsiveOrchestratorCheckpoints fires in that same reconcile
// tick. No bound is given in the spec text for this one (unlike 6/10's
// "within 6s"), so the timeout below is sized for the full deadline +
// interrupt + kill-then-detect + promote chain, not tight.
func TestScenario09UnresponsiveOrchestrator(t *testing.T) {
	h := newHarness(t)
	h.setPauseDeadlineSec(t, 3)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	child := h.spawn(t, orch, h.firstTask(t, epic), "coder")
	if !h.waitForSessionState(t, orch, "running", 5*time.Second) {
		t.Fatalf("orch session = %s, never reached running", h.sessionState(t, orch))
	}
	if !h.waitForSessionState(t, child, "running", 5*time.Second) {
		t.Fatalf("child session = %s, never reached running", h.sessionState(t, child))
	}

	h.pause(t, orch, "subtree")

	// Neither agent is ever touched again: both ignore the pause.
	deadline := time.Now().Add(60 * time.Second)
	for {
		var daemonWritten int
		var summary, blockersJSON string
		err := h.db(t).QueryRow(`SELECT daemon_written, summary, blockers_json FROM checkpoints
			WHERE agent_id = (SELECT id FROM agents WHERE name = ?) AND kind = 'handoff'
			ORDER BY created_at DESC LIMIT 1`, orch).Scan(&daemonWritten, &summary, &blockersJSON)
		if err == nil {
			if daemonWritten != 1 {
				t.Fatalf("orchestrator's own handoff daemon_written = %d, want 1", daemonWritten)
			}
			if summary != "Paused by daemon; orchestrator did not respond." {
				t.Fatalf("summary = %q", summary)
			}
			if !bytes.Contains([]byte(blockersJSON), []byte(child)) {
				t.Fatalf("blockers = %s, want to list %s (no handoff)", blockersJSON, child)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no daemon-written checkpoint for %s within %s: %v", orch, 60*time.Second, err)
		}
		time.Sleep(300 * time.Millisecond)
	}

	var childHandoffs int
	if err := h.db(t).QueryRow(`SELECT COUNT(*) FROM checkpoints WHERE agent_id =
		(SELECT id FROM agents WHERE name = ?) AND kind = 'handoff'`, child).Scan(&childHandoffs); err != nil {
		t.Fatal(err)
	}
	if childHandoffs != 0 {
		t.Fatalf("child wrote %d handoff checkpoints, want 0 (it never responded)", childHandoffs)
	}
}

// Scenario 10: crash. Killing the fake agent's pane out of the blue (no
// handoff, no pause requested at all) marks the session crashed within 6s —
// this needs only the reconcile loop's normal dead-pane detection, the same
// one reconcile tick scenario 6 relies on, so no timing seam is needed here
// either. Acknowledging then moves it to "history": isFinishedChild
// (internal/httpapi/runtime.go) buckets an acknowledged agent under
// "finished" rather than "children" in the agent tree.
func TestScenario10Crash(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	task := h.firstTask(t, epic)
	coder := h.spawn(t, orch, task, "coder")
	if !h.waitForSessionState(t, coder, "running", 5*time.Second) {
		t.Fatalf("coder session = %s, never reached running", h.sessionState(t, coder))
	}

	statusBefore := h.itemStatus(t, task)
	since := time.Now()
	h.killPane(t, coder)

	if !h.waitForSessionState(t, coder, "crashed", 6*time.Second) {
		t.Fatalf("coder session = %s, want crashed within 6s", h.sessionState(t, coder))
	}
	if !h.waitForNotification(t, "agent.crashed", since, 2*time.Second) {
		t.Fatal("no agent.crashed notification")
	}
	if !h.waitForRelay(t, orch, "crashed", since, 2*time.Second) {
		t.Fatal("no relay crashed message to the parent")
	}
	if got := h.itemStatus(t, task); got != statusBefore {
		t.Fatalf("item status changed from %q to %q on a crash", statusBefore, got)
	}

	h.doT(t, http.MethodPost, "/api/agents/"+coder+"/ack", nil, nil)

	var tree []map[string]any
	h.doT(t, http.MethodGet, "/api/agents?root="+epic+"&state=all", nil, &tree)
	var root map[string]any
	for _, n := range tree {
		if n["name"] == orch {
			root = n
			break
		}
	}
	if root == nil {
		t.Fatalf("orchestrator %s not found in %+v", orch, tree)
	}
	found := false
	for _, f := range root["finished"].([]any) {
		if fm, ok := f.(map[string]any); ok && fm["name"] == coder {
			found = true
		}
	}
	if !found {
		t.Fatalf("acked coder not under \"finished\": %+v", root)
	}
	for _, c := range root["children"].([]any) {
		if cm, ok := c.(map[string]any); ok && cm["name"] == coder {
			t.Fatalf("acked coder still under \"children\": %+v", root)
		}
	}
}
