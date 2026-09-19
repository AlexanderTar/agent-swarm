//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// countActiveAgents is the same global count internal/runtime's Admit uses
// (role <> orchestrator, state = active) — needed because this suite shares
// one daemon across every scenario and never tears an agent down, so a
// literal max_agents=1 would already be over budget by the time scenario 11
// runs. Set the ceiling relative to what's already there instead of to a
// fixed number.
func (h *harness) countActiveAgents(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.db(t).QueryRow(`SELECT COUNT(*) FROM agents WHERE role <> 'orchestrator' AND state = 'active'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Scenario 11: with exactly one admission slot free, the second spawn gets
// queued:true and an agent.queued notification, and starts when the first
// completes.
func TestScenario11ConcurrencyQueue(t *testing.T) {
	h := newHarness(t)
	before := h.countActiveAgents(t)
	h.setMaxAgents(t, before+1)
	t.Cleanup(func() { h.setMaxAgents(t, 200) })

	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	task1 := h.firstTask(t, epic)

	// a second task, sibling of the first, under the same story
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

	worker1 := h.spawn(t, orch, task1, "coder")

	since := time.Now()
	out := h.mustTool(t, orch, "swarm_spawn", map[string]any{
		"item": task2Key, "role": "coder", "agent": "fake", "model": "fake-1",
	})
	if out["queued"] != true {
		t.Fatalf("second spawn = %+v, want queued:true", out)
	}
	worker2, _ := out["agent"].(string)
	if worker2 == "" {
		t.Fatalf("swarm_spawn returned no agent name: %+v", out)
	}
	if !h.waitForNotification(t, "agent.queued", since, 5*time.Second) {
		t.Fatal("no agent.queued notification")
	}

	// completing worker1 (red then green, the TDD gate from scenario 19)
	// frees a slot; DrainQueue should start worker2.
	h.mustTool(t, worker1, "swarm_checkpoint", map[string]any{
		"kind": "progress", "summary": "red",
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "red", "ok": false}},
	})
	h.mustTool(t, worker1, "swarm_checkpoint", map[string]any{
		"kind": "completed", "summary": "green",
		"git":          []map[string]any{{"repo": "chat", "branch": "task/one", "sha": h.headSHA(t)}},
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "green", "ok": true}},
	})
	// A "completed" checkpoint alone frees nothing: Admit only counts
	// agents.state, and only the reconciler's resolveDead flips that to
	// "finished", which only happens once it sees worker1's pane actually
	// gone — the harness wrote the checkpoint directly over MCP, so nothing
	// made the real fake-agent process (still idling in _default.json) exit.
	h.killPane(t, worker1)

	// DrainQueue only runs from the 5s reconcile tick (internal/runtime/
	// reconcile.go:148 calls it, nothing calls it synchronously after a
	// checkpoint completes), so the wait needs more than one tick's worth of
	// slack regardless of where in the cycle the slot actually freed.
	deadline := time.Now().Add(20 * time.Second)
	for {
		var state string
		if err := h.db(t).QueryRow(`SELECT state FROM agents WHERE name = ?`, worker2).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "active" {
			var hasSession int
			h.db(t).QueryRow(`SELECT COUNT(*) FROM sessions s JOIN agents a ON a.id = s.agent_id WHERE a.name = ?`, worker2).Scan(&hasSession)
			if hasSession > 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker2 (%s) never started after the queue drained", worker2)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
