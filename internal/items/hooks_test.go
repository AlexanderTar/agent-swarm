package items_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// R5: with RequestPayload set, request.* carries whatever the hook returns.
func TestResolveStaleUsesRequestPayload(t *testing.T) {
	st := newStore(t)
	st.RequestPayload = func(ctx context.Context, tx *sql.Tx, id string) (any, error) {
		return map[string]string{"id": id, "kind": "accept_epic", "state": "stale", "prompt": "p"}, nil
	}
	it := mk(t, st, items.Epic, "", "Accept me")
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, item_id, prompt, state, binding_json, created_at)
		VALUES ('req_x', 'accept_epic', ?, 'p', 'open', '{}', 1)`, it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Transition(ctx, it.Key, items.Cancelled, user); err != nil {
		t.Fatal(err)
	}
	evs, err := st.Events.After(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range evs {
		if e.Type != events.RequestResolved {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			t.Fatal(err)
		}
		if m["prompt"] == "p" && m["state"] == "stale" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no request.resolved with the hook payload in %v", evs)
	}
}

// The spike approval kinds must also go stale (P1 carry).
func TestStaleAcceptsCoversApprovalKinds(t *testing.T) {
	st := newStore(t)
	sp := mk(t, st, items.Spike, "", "Look into it") // mk sets SpikeIntent: "feature"
	for i, kind := range []string{"approve_section", "approve_plan", "approve_report"} {
		if _, err := st.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, item_id, prompt, state, created_at)
			VALUES (?, ?, ?, 'p', 'open', ?)`, "req_"+string(rune('a'+i)), kind, sp.ID, i+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Transition(ctx, sp.Key, items.Cancelled, user); err != nil {
		t.Fatal(err)
	}
	var open int
	if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE item_id = ? AND state = 'open'`, sp.ID).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("%d approval requests still open after cancel", open)
	}
}

// RequestOpened fires for the accept request the reconciler opens.
func TestReconcileRootCallsRequestOpened(t *testing.T) {
	st := newStore(t)
	var opened []string
	st.RequestOpened = func(ctx context.Context, tx *sql.Tx, id string) error {
		opened = append(opened, id)
		return nil
	}
	ep := mk(t, st, items.Epic, "", "Ship it")
	story := mk(t, st, items.Story, ep.Key, "One")
	// reconcileRoot only reaches step 4 from in_progress or in_review, and the
	// Ready->InProgress hop needs an *accepted* checkpoint (acceptedSince), which
	// this test is not about. Put the epic in in_progress directly; the raw UPDATE
	// leaves `revision` alone, so the binding the request records still matches.
	exec(t, st.DB, `UPDATE items SET status = 'in_progress' WHERE id = ?`, ep.ID)
	exec(t, st.DB, `UPDATE items SET status = 'done' WHERE id = ?`, story.ID)
	insertIntegrated(t, st, ep.ID)
	if err := st.Reconcile(ctx, ep.Key); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 {
		t.Fatalf("RequestOpened called %d times, want 1", len(opened))
	}
	var kind, state string
	if err := st.DB.QueryRowContext(ctx, `SELECT kind, state FROM requests WHERE id = ?`, opened[0]).Scan(&kind, &state); err != nil {
		t.Fatal(err)
	}
	if kind != "accept_epic" || state != "open" {
		t.Fatalf("hook got %s/%s, want accept_epic/open", kind, state)
	}
}
