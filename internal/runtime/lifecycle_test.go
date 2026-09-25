package runtime

// Batch 4 worker-lifecycle contract tests (F11 relay revision, F3/F4/F5/F14
// lifecycle guards). The Batch 1-2 harnesses (worker, newStore) are reused
// additively; no existing lifecycle behavior is redefined here.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// TestRelayCarriesFinalItemRevision (F11): the parent relay for a
// checkpoint carries the item's final revision after cascading
// transitions — the same revision WriteCheckpoint reports — so the
// observing parent sees current state without a revision:latest shortcut.
func TestRelayCarriesFinalItemRevision(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)

	res, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "done", Verification: []Verify{{Cmd: "go test ./..."}},
		Git: []GitRef{{Repo: "proj", Branch: "task/task-1", SHA: "abc1234"}}})
	if err != nil {
		t.Fatal(err)
	}
	final, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ItemRevision != final.Revision {
		t.Fatalf("result revision = %d, current = %d", res.ItemRevision, final.Revision)
	}
	var payload string
	if err := s.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' ORDER BY seq DESC LIMIT 1`, orch.ID).Scan(&payload); err != nil {
		t.Fatalf("parent got no checkpoint relay: %v", err)
	}
	var body struct {
		Event        string `json:"event"`
		ItemRevision *int   `json:"item_revision"`
	}
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatal(err)
	}
	if body.Event != string(CompletedCkp) {
		t.Fatalf("relay event = %q, want completed", body.Event)
	}
	if body.ItemRevision == nil {
		t.Fatal("relay carries no item_revision")
	}
	if *body.ItemRevision != final.Revision {
		t.Fatalf("relay item_revision = %d, want final %d", *body.ItemRevision, final.Revision)
	}
	if final.Status != items.InReview {
		t.Fatalf("status = %s, want in_review", final.Status)
	}
}
