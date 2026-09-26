//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

// Cancel cascade (docs/specs/2026-09-26-root-finish-and-cancel-cascade.md
// C1, C3, C4): cancelling the epic on the board cancels its story and task,
// the reconciler cancels the worker and the orchestrator, and reopening the
// epic leaves the children cancelled.
func TestScenarioCancelCascade(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	h.doT(t, http.MethodPatch, "/api/items/"+epic,
		map[string]any{"status": "ready", "revision": h.itemRevision(t, epic)}, nil)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting the epic"})
	task := h.firstTask(t, epic)
	worker := h.spawn(t, orch, task, "coder")

	h.doT(t, http.MethodPatch, "/api/items/"+epic,
		map[string]any{"status": "cancelled", "revision": h.itemRevision(t, epic)}, nil)
	if got := h.itemStatus(t, task); got != "cancelled" {
		t.Fatalf("task status = %s, want cancelled", got)
	}
	for _, name := range []string{worker, orch} {
		if !h.waitForAgentState(t, name, "finished", 20*time.Second) {
			t.Fatalf("%s agent = %s, want finished", name, h.agentState(t, name))
		}
		if got := h.sessionState(t, name); got != "cancelled" {
			t.Fatalf("%s session = %s, want cancelled", name, got)
		}
	}

	h.doT(t, http.MethodPatch, "/api/items/"+epic,
		map[string]any{"status": "ready", "revision": h.itemRevision(t, epic)}, nil)
	if got := h.itemStatus(t, epic); got != "ready" {
		t.Fatalf("epic status = %s, want ready", got)
	}
	if got := h.itemStatus(t, task); got != "cancelled" {
		t.Fatalf("task status after reopen = %s, want cancelled", got)
	}
}
