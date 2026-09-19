//go:build e2e

package e2e

import (
	"net/http"
	"testing"
)

// Scenario 5: status changes are refused where they should be. TASK-2 is
// blocked by TASK-1: swarm_read (here, the plain item GET, which carries the
// same blocked_by array) shows it; moving the story to done while TASK-2
// isn't done is refused; a cycle is refused with 409.
func TestScenario05BlockedDependency(t *testing.T) {
	h := newHarness(t)
	var epic, story map[string]any
	h.doT(t, http.MethodPost, "/api/items", map[string]any{"type": "epic", "title": "E2E deps " + unique()}, &epic)
	h.doT(t, http.MethodPost, "/api/items",
		map[string]any{"type": "story", "title": "Story", "parent_key": epic["key"]}, &story)
	storyKey, _ := story["key"].(string)
	var task1, task2 map[string]any
	h.doT(t, http.MethodPost, "/api/items", map[string]any{"type": "task", "title": "One", "parent_key": storyKey}, &task1)
	h.doT(t, http.MethodPost, "/api/items", map[string]any{"type": "task", "title": "Two", "parent_key": storyKey}, &task2)
	task1Key, _ := task1["key"].(string)
	task2Key, _ := task2["key"].(string)

	if status, raw, err := h.do(http.MethodPost, "/api/items/"+task2Key+"/deps",
		map[string]any{"blocked_by": task1Key}); err != nil || status != http.StatusNoContent {
		t.Fatalf("add dep: %d %s %v", status, raw, err)
	}

	var detail struct {
		Item map[string]any `json:"item"`
	}
	h.doT(t, http.MethodGet, "/api/items/"+task2Key, nil, &detail)
	blocked, _ := detail.Item["blocked_by"].([]any)
	if len(blocked) != 1 || blocked[0] != task1Key {
		t.Fatalf("blocked_by = %v, want [%s]", blocked, task1Key)
	}

	// moving the story to done while TASK-2 isn't done is refused
	rev, _ := story["revision"].(float64)
	status, raw, err := h.do(http.MethodPatch, "/api/items/"+storyKey,
		map[string]any{"status": "done", "revision": int(rev)})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("story -> done = %d %s, want 422 transition_denied", status, raw)
	}

	// a cycle is refused with 409
	status, raw, err = h.do(http.MethodPost, "/api/items/"+task1Key+"/deps", map[string]any{"blocked_by": task2Key})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusConflict {
		t.Fatalf("cycle = %d %s, want 409", status, raw)
	}
}
