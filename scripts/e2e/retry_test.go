//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// Scenario 26: review and retry. A reviewer's finding (represented here by
// the orchestrator's own retry note, rather than a full second reviewer
// agent — the mechanic under test is the attempt boundary, not review
// itself) leads to swarm_control retry; the second attempt shows red then
// green and completes; the first attempt's completion is not counted after
// the retry (internal/items/transition.go's completedCurrent joins on
// MAX(attempt), so a fresh attempt with no completed checkpoint of its own
// makes the task un-completable again until it earns one).
func TestScenario26ReviewAndRetry(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	task := h.firstTask(t, epic)
	worker := h.spawn(t, orch, task, "coder")

	// attempt 1: completes cleanly
	h.mustTool(t, worker, "swarm_checkpoint", map[string]any{
		"kind": "progress", "summary": "red",
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "red", "ok": false}},
	})
	h.mustTool(t, worker, "swarm_checkpoint", map[string]any{
		"kind": "completed", "summary": "attempt 1",
		"git":          []map[string]any{{"repo": "chat", "branch": "task/one", "sha": h.headSHA(t)}},
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "green", "ok": true}},
	})
	if got := h.itemStatus(t, task); got != "in_review" {
		t.Fatalf("task status = %s, want in_review", got)
	}

	// a reviewer's finding sends it back for another attempt. Retry always
	// launches a fresh tmux session under the agent's own name (internal/
	// runtime/agents.go's startSession -> Tmux.Start, unconditionally, no
	// prior kill) rather than resuming in place, so the harness ends the
	// first attempt's pane first — exactly what a real agent process exiting
	// on its own would leave behind.
	h.killPane(t, worker)
	// Retry now refuses a session that isn't completed/failed/crashed/
	// interrupted (Task 41's state guard, matching Pause/Resume's own
	// convention), so this must wait for the daemon's reconciler to observe
	// the dead pane and land the session on "completed" (the attempt's own
	// completed checkpoint makes that the terminal state here) before
	// retrying it -- the same wait every other pane-killing scenario in this
	// suite already does before acting on the result.
	if !h.waitForSessionState(t, worker, "completed", 6*time.Second) {
		t.Fatal("worker session never reached completed after its pane died")
	}
	h.mustTool(t, orch, "swarm_control", map[string]any{
		"target": worker, "action": "retry", "note": "reviewer: missing an edge case",
	})

	// completedCurrent (internal/items/transition.go) joins on MAX(attempt)
	// FROM CHECKPOINTS for this item, not the session's own attempt counter —
	// so attempt 1's completion still counts until attempt 2 writes a
	// checkpoint of its own. The moment it does (red, below), MAX(attempt)
	// becomes 2 and attempt 1's completed checkpoint stops matching it:
	// marking the task done in between is refused again, exactly the window
	// this scenario is about.
	h.mustTool(t, worker, "swarm_checkpoint", map[string]any{
		"kind": "progress", "summary": "red, attempt 2",
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "red", "ok": false}},
	})
	err := h.tool(t, orch, "swarm_items", map[string]any{
		"op": "update", "key": task, "status": "done", "revision": h.itemRevision(t, task),
	})
	if err == nil {
		t.Fatal("marking the task done mid-attempt-2, before it completes, should be refused")
	}

	h.mustTool(t, worker, "swarm_checkpoint", map[string]any{
		"kind": "completed", "summary": "attempt 2",
		"git":          []map[string]any{{"repo": "chat", "branch": "task/one", "sha": h.headSHA(t)}},
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "green", "ok": true}},
	})
	if got := h.itemStatus(t, task); got != "in_review" {
		t.Fatalf("task status = %s, want in_review", got)
	}

	// now the orchestrator's own mark-done succeeds, against the new attempt
	h.mustTool(t, orch, "swarm_items", map[string]any{
		"op": "update", "key": task, "status": "done", "revision": h.itemRevision(t, task),
	})
	if got := h.itemStatus(t, task); got != "done" {
		t.Fatalf("task status = %s, want done", got)
	}
}
