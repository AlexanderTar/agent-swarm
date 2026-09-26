//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Epic approval lane (docs/specs/2026-09-26-epic-approval-lane.md E1, E7):
// the accept_epic row is routed to the live orchestrator with a request_open
// relay; a board change request reaches it as approval_result and sends the
// epic back to work; a fresh integration re-opens and re-relays; approval
// reaches it too and finishes the epic. The setup mirrors scenario 29.
// ponytail: setup duplicated from staleaccept_test.go; extract a harness
// helper if a third scenario needs an in-review epic.
func TestScenarioEpicApprovalLane(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
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
	h.mustTool(t, orch, "swarm_items", map[string]any{
		"op": "update", "key": task, "status": "done", "revision": h.itemRevision(t, task),
	})
	integrated := func() {
		h.mustTool(t, orch, "swarm_checkpoint", map[string]any{
			"kind": "integrated", "summary": "integrated",
			"git":          []map[string]any{{"repo": "chat", "branch": "main", "sha": h.headSHA(t)}},
			"verification": []map[string]any{{"cmd": "go test ./...", "phase": "green", "ok": true}},
		})
	}
	count := func(kind, reqID string) int {
		var n int
		h.db(t).QueryRow(`SELECT COUNT(*) FROM messages m JOIN agents a ON a.id = m.to_agent_id
			WHERE a.name = ? AND m.kind = ? AND m.request_id = ?`, orch, kind, reqID).Scan(&n)
		return n
	}

	since := time.Now()
	integrated()
	first := h.waitForRequestFull(t, epic, "accept_epic", 20*time.Second)
	firstID := first["id"].(string)
	if first["agent_name"] != orch {
		t.Fatalf("accept_epic agent_name = %v, want %s", first["agent_name"], orch)
	}
	if !h.waitForRelay(t, orch, "request_open", since, 10*time.Second) || count("relay", firstID) != 1 {
		t.Fatalf("no request_open relay for %s", firstID)
	}

	// E7: request changes on the board reaches the orchestrator and reopens work.
	h.doT(t, http.MethodPost, "/api/requests/"+firstID+"/request-changes",
		map[string]any{"comment": "Rename the flag.", "via": "board"}, nil)
	if count("approval_result", firstID) != 1 {
		t.Fatalf("no approval_result for the change request")
	}
	if got := h.itemStatus(t, epic); got != "in_progress" {
		t.Fatalf("epic status = %s, want in_progress", got)
	}

	// Re-integration re-opens a fresh row and re-relays it.
	integrated()
	deadline := time.Now().Add(20 * time.Second)
	var second map[string]any
	for {
		if id := h.openRequest(t, epic, "accept_epic"); id != "" && id != firstID {
			second = h.requestByKind(t, epic, "accept_epic")
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no fresh accept_epic after re-integration")
		}
		time.Sleep(200 * time.Millisecond)
	}
	secondID := second["id"].(string)
	if count("relay", secondID) != 1 {
		t.Fatalf("no request_open relay for the re-opened %s", secondID)
	}

	h.doT(t, http.MethodPost, "/api/requests/"+secondID+"/approve",
		map[string]any{"binding": second["binding"], "via": "board"}, nil)
	if count("approval_result", secondID) != 1 {
		t.Fatalf("no approval_result for the approval")
	}
	if got := h.itemStatus(t, epic); got != "done" {
		t.Fatalf("epic status = %s, want done", got)
	}

	// Root finish (docs/specs/2026-09-26-root-finish-and-cancel-cascade.md
	// R1, R3): the daemon wrote the orchestrator's completed on its own item,
	// a late handoff is refused, and once the pane goes (killPane stands in
	// for the 60 s reaper) the session ends completed.
	var daemonCompleted int
	h.db(t).QueryRow(`SELECT COUNT(*) FROM checkpoints c JOIN agents a ON a.id = c.agent_id
		WHERE a.name = ? AND c.item_id = a.item_id AND c.kind = 'completed' AND c.daemon_written = 1`,
		orch).Scan(&daemonCompleted)
	if daemonCompleted != 1 {
		t.Fatalf("%d daemon completed checkpoints for %s, want 1", daemonCompleted, orch)
	}
	err := h.tool(t, orch, "swarm_checkpoint", map[string]any{"kind": "handoff", "summary": "saving my place"})
	if err == nil || !strings.Contains(err.Error(), "Root accepted; write completed.") {
		t.Fatalf("late handoff err = %v, want the root-accepted refusal", err)
	}
	h.killPane(t, orch)
	if !h.waitForSessionState(t, orch, "completed", 30*time.Second) {
		t.Fatalf("orchestrator session = %s, want completed", h.sessionState(t, orch))
	}
	if got := h.agentState(t, orch); got != "finished" {
		t.Fatalf("orchestrator agent = %s, want finished", got)
	}
}
