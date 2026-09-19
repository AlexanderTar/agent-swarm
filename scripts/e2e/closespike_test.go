//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

// Scenario 27: a "completed" checkpoint with resolution:"no_change" opens a
// close_spike request; the user approves it through POST
// /api/requests/{id}/close-spike; the spike ends up Done with no new item
// created under it.
func TestScenario27CloseSpike(t *testing.T) {
	h := newHarness(t)
	var spikeResp map[string]any
	h.doT(t, http.MethodPost, "/api/spikes", map[string]any{
		"request_id": "req-" + unique(), "name": "Nothing to build " + unique(), "intent": "debug",
		"agent": "fake", "model": "fake-1",
	}, &spikeResp)
	item, _ := spikeResp["item"].(map[string]any)
	agent, _ := spikeResp["agent"].(map[string]any)
	spikeKey, _ := item["key"].(string)
	orch, _ := agent["name"].(string)

	// reconcileSpike (internal/items/transition.go) only looks at close_spike
	// once the spike has left Draft/Ready, which happens on an accepted
	// checkpoint (like any other assignment) — a spike that goes straight to
	// "completed" with no "accepted" first would just sit at Draft forever.
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{
		"kind": "completed", "summary": "nothing here", "resolution": "no_change",
	})

	reqID := h.waitForRequest(t, spikeKey, "close_spike", 5*time.Second)
	var wire map[string]any
	h.doT(t, http.MethodPost, "/api/requests/"+reqID+"/close-spike", map[string]any{"via": "board"}, &wire)
	if wire["state"] != "approved" {
		t.Fatalf("close-spike state = %v, want approved", wire["state"])
	}

	if got := h.itemStatus(t, spikeKey); got != "done" {
		t.Fatalf("spike status = %s, want done", got)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	h.doT(t, http.MethodGet, "/api/items?view=flat&root="+spikeKey, nil, &list)
	if len(list.Items) != 1 {
		t.Fatalf("closing a spike must create no new item: %+v", list.Items)
	}
}
