//go:build e2e

package e2e

import (
	"testing"
)

func TestScenario19TDDGate(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	task := h.firstTask(t, epic)
	worker := h.spawn(t, orch, task, "coder")

	// a completed checkpoint with no red run is refused
	err := h.tool(t, worker, "swarm_checkpoint", map[string]any{
		"kind": "completed", "summary": "done",
		"git":          []map[string]any{{"repo": "chat", "branch": "task/one", "sha": h.headSHA(t)}},
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "green", "ok": true}},
	})
	if err == nil || err.Error() != "TDD evidence missing: record the failing test run (phase: red) before completing." {
		t.Fatalf("err = %v", err)
	}
	if got := h.itemStatus(t, task); got != "in_progress" {
		t.Fatalf("the task must stay in progress: %s", got)
	}

	// red then green is accepted
	h.mustTool(t, worker, "swarm_checkpoint", map[string]any{
		"kind": "progress", "summary": "wrote the failing test",
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "red", "ok": false}},
	})
	h.mustTool(t, worker, "swarm_checkpoint", map[string]any{
		"kind": "completed", "summary": "made it pass",
		"git":          []map[string]any{{"repo": "chat", "branch": "task/one", "sha": h.headSHA(t)}},
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "green", "ok": true}},
	})
	if got := h.itemStatus(t, task); got != "in_review" {
		t.Fatalf("status = %s, want in_review", got)
	}
}
