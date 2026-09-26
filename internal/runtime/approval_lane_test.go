package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Spec E1 copy: the two accept kinds' native prompts.
func TestNativePromptForAcceptKinds(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	bug, err := s.Items.Create(ctx, items.CreateInput{Type: items.Bug, Title: "Login loop"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		got, err := s.nativePromptFor(ctx, tx, Request{ID: "req_e1", Kind: KindAcceptEpic, ItemID: ep.ID}, "", nil)
		if err != nil {
			return err
		}
		want := NativePrompt{Header: "Accept epic", Question: `Accept EPIC-1 "Build it" as done? ⟦swarm:req_e1⟧`, Options: approveOptions}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("accept_epic prompt = %+v, want %+v", got, want)
		}
		got, err = s.nativePromptFor(ctx, tx, Request{ID: "req_f1", Kind: KindAcceptFix, ItemID: bug.ID}, "", nil)
		if err != nil {
			return err
		}
		want = NativePrompt{Header: "Accept fix",
			Question: fmt.Sprintf(`Accept the fix for %s "Login loop" as done? ⟦swarm:req_f1⟧`, bug.Key), Options: approveOptions}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("accept_fix prompt = %+v, want %+v", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// openAcceptRow inserts an accept row exactly as reconcileRoot does (no agent,
// no session) and fires the RequestOpened hook in the same tx.
func openAcceptRow(t *testing.T, s *Store, id, kind, itemKey string) {
	t.Helper()
	ctx := context.Background()
	it, err := s.Items.Get(ctx, itemKey)
	if err != nil {
		t.Fatal(err)
	}
	binding := fmt.Sprintf(`{"item_revision":%d,"integrated_checkpoint":"ckp_x","git":[]}`, it.Revision)
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, item_id, prompt, state, binding_json, created_at)
			VALUES (?, ?, ?, 'Review completed work and accept the epic.', 'open', ?, 1)`, id, kind, it.ID, binding); err != nil {
			return err
		}
		return s.OnRequestOpened(ctx, tx, id)
	}); err != nil {
		t.Fatal(err)
	}
}

// relayFor returns the newest request_open relay payload for reqID and how many exist.
func relayFor(t *testing.T, s *Store, toAgentID, reqID string) (map[string]any, int) {
	t.Helper()
	rows, err := s.DB.QueryContext(context.Background(), `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' AND request_id = ? ORDER BY seq`, toAgentID, reqID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var last map[string]any
	n := 0
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		last = map[string]any{}
		if err := json.Unmarshal([]byte(p), &last); err != nil {
			t.Fatal(err)
		}
		n++
	}
	return last, n
}

func decodeNP(t *testing.T, p map[string]any) NativePrompt {
	t.Helper()
	raw, _ := json.Marshal(p["native_prompt"])
	var np NativePrompt
	if err := json.Unmarshal(raw, &np); err != nil {
		t.Fatal(err)
	}
	return np
}

// Spec E1.
func TestAcceptRowRoutesToLiveRootOrchestrator(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")

	req, err := s.RequestByID(ctx, "req_accept")
	if err != nil {
		t.Fatal(err)
	}
	if req.AgentID != orch.ID || req.SessionID != orchSes {
		t.Fatalf("bound to (%q, %q), want (%q, %q)", req.AgentID, req.SessionID, orch.ID, orchSes)
	}
	p, n := relayFor(t, s, orch.ID, "req_accept")
	if n != 1 {
		t.Fatalf("%d request_open relays, want 1", n)
	}
	if p["event"] != "request_open" || p["kind"] != "accept_epic" || p["item"] != "EPIC-1" || p["request_id"] != "req_accept" {
		t.Fatalf("relay payload = %v", p)
	}
	np := decodeNP(t, p)
	if np.Header != "Accept epic" || np.Question != `Accept EPIC-1 "Build it" as done? ⟦swarm:req_accept⟧` {
		t.Fatalf("native_prompt = %+v", np)
	}
	if p["question"] != np.Question || p["next"] != NativePromptNextStep("req_accept") {
		t.Fatalf("question/next = %v / %v", p["question"], p["next"])
	}
}

// Spec E4.
func TestAcceptRowStaysAgentlessWithoutLiveOrchestrator(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE agent_id = ?`, orch.ID); err != nil {
		t.Fatal(err)
	}
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")
	req, err := s.RequestByID(ctx, "req_accept")
	if err != nil {
		t.Fatal(err)
	}
	if req.AgentID != "" || req.SessionID != "" {
		t.Fatalf("bound to (%q, %q) with no live orchestrator", req.AgentID, req.SessionID)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE request_id = 'req_accept'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d messages for an unrouted accept row", n)
	}
}

// Spec E11 (exhaust): the relay keeps the native prompt even while the kind is out of usage.
func TestAcceptRelayIsNotHeldWhileExhausted(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	s.Usage = fakeUsage{Fake: true}
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")
	if _, n := relayFor(t, s, orch.ID, "req_accept"); n != 1 {
		t.Fatalf("%d relays while exhausted, want 1", n)
	}
	var held int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppressed_relays`).Scan(&held)
	if held != 0 {
		t.Fatalf("%d suppressed_relays rows, want 0", held)
	}
}
