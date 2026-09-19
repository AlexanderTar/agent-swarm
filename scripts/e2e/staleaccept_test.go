//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

// Scenario 29: stale acceptance. An epic reaches in_review and opens its
// accept_epic request; before the user gets to it, the orchestrator writes a
// second `integrated` checkpoint (more work landed, e.g. a fixup commit).
// reconcileRoot (internal/items/transition.go) notices the open request's
// binding no longer matches the current integration and stales it, then
// opens a fresh accept_epic bound to the new checkpoint. Approving the stale
// request must 409; approving the new one must succeed and mark the epic
// done.
func TestScenario29StaleAcceptance(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)

	// the epic itself starts Draft (materializedEpic only readies its story
	// and task, matching materialize.go's own createTree); ready it so the
	// orchestrator's first "accepted" checkpoint can move it to in_progress.
	h.doT(t, http.MethodPatch, "/api/items/"+epic,
		map[string]any{"status": "ready", "revision": h.itemRevision(t, epic)}, nil)

	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting the epic"})

	task := h.firstTask(t, epic)
	worker := h.spawn(t, orch, task, "coder")
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
		t.Fatalf("task status = %s, want in_review", got)
	}

	// the orchestrator marks the task done (§10.1: in_review -> done is the
	// orchestrator's own call for a task, not the daemon's); the story
	// derives to done behind it, but the epic needs its own integrated
	// checkpoint before it can leave in_progress.
	h.mustTool(t, orch, "swarm_items", map[string]any{
		"op": "update", "key": task, "status": "done", "revision": h.itemRevision(t, task),
	})
	if got := h.itemStatus(t, task); got != "done" {
		t.Fatalf("task status = %s, want done", got)
	}

	integrated := func(sha string) {
		h.mustTool(t, orch, "swarm_checkpoint", map[string]any{
			"kind": "integrated", "summary": "integrated",
			"git":          []map[string]any{{"repo": "chat", "branch": "main", "sha": sha}},
			"verification": []map[string]any{{"cmd": "go test ./...", "phase": "green", "ok": true}},
		})
	}
	integrated(h.headSHA(t))

	oldReq := h.waitForRequestFull(t, epic, "accept_epic", 20*time.Second)
	if got := h.itemStatus(t, epic); got != "in_review" {
		t.Fatalf("epic status = %s, want in_review", got)
	}

	// more work landed: a second integrated checkpoint stales the open
	// request and opens a fresh one bound to the new integration.
	integrated(h.headSHA(t))

	deadline := time.Now().Add(20 * time.Second)
	for {
		if h.requestState(t, oldReq["id"].(string)) == "stale" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("old accept_epic request never went stale (state = %q)", h.requestState(t, oldReq["id"].(string)))
		}
		time.Sleep(200 * time.Millisecond)
	}

	// approving the stale request 409s
	status, raw, err := h.do(http.MethodPost, "/api/requests/"+oldReq["id"].(string)+"/approve",
		map[string]any{"binding": oldReq["binding"], "via": "board"})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusConflict {
		t.Fatalf("approving the stale accept_epic request = %d %s, want 409", status, raw)
	}

	newReq := h.requestByKind(t, epic, "accept_epic")
	if newReq["id"].(string) == oldReq["id"].(string) {
		t.Fatalf("expected a fresh accept_epic request, got the same id back")
	}

	h.doT(t, http.MethodPost, "/api/requests/"+newReq["id"].(string)+"/approve",
		map[string]any{"binding": newReq["binding"], "via": "board"}, nil)

	if got := h.itemStatus(t, epic); got != "done" {
		t.Fatalf("epic status = %s, want done", got)
	}
}
