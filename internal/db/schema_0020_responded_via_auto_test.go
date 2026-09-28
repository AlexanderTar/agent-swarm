package db

import (
	"reflect"
	"testing"
)

// TestRespondedViaAutoMigrationAllowsAutoOnceMigrated is
// docs/specs/2026-09-28-empty-section-auto-approve.md's DB models section:
// requests.responded_via's CHECK -- last rebuilt by 0006_hitl_terminal_via.sql
// at ('menubar','board','cli','terminal') -- has no 'auto' value for the new
// daemon-issued auto-approval. This proves a database parked at version 19
// rejects 'auto' pre-migration and accepts it once it catches up to 20,
// while still rejecting an unrelated bad value.
func TestRespondedViaAutoMigrationAllowsAutoOnceMigrated(t *testing.T) {
	raw := openFixtureAtVersion(t, 19) // pre-0020 shape, CHECK has no 'auto'
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(stmt, args...); err != nil {
			t.Fatalf("fixture insert failed: %v\n%s", err, stmt)
		}
	}
	exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'SPIKE-1', 'spike', 'itm_1', 'test spike', 'in_progress', 0, 0)`)

	if _, err := raw.Exec(`INSERT INTO requests (id, kind, item_id, prompt, state, responded_via, created_at)
		VALUES ('req_1', 'approve_section', 'itm_1', 'a summary', 'approved', 'auto', 0)`); err == nil {
		t.Fatal("responded_via = 'auto' must still be refused before the migration (CHECK has no 'auto')")
	}

	continueMigratingTo(t, raw, 19, 20)

	exec(`INSERT INTO requests (id, kind, item_id, prompt, state, responded_via, created_at)
		VALUES ('req_1', 'approve_section', 'itm_1', 'a summary', 'approved', 'auto', 0)`)

	var got string
	if err := raw.QueryRow(`SELECT responded_via FROM requests WHERE id = 'req_1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "auto" {
		t.Fatalf("responded_via = %q, want %q", got, "auto")
	}

	if _, err := raw.Exec(`INSERT INTO requests (id, kind, item_id, prompt, state, responded_via, created_at)
		VALUES ('req_2', 'approve_section', 'itm_1', 'a summary', 'approved', 'bogus', 0)`); err == nil {
		t.Fatal("an unrelated bad responded_via value must still be refused after the migration")
	}
}

// TestMigration0020PreservesRequestsAndReferencingRows mirrors
// TestMigration0019PreservesRequestsAndReferencingRows: 0020's rebuild does
// DROP TABLE requests (via requests_new / RENAME), and messages.request_id /
// notifications.request_id both reference requests(id). A pre-existing
// requests row plus rows in both referencing tables must all survive the
// rebuild untouched, and SQLite's own foreign_key_check must report nothing
// broken.
func TestMigration0020PreservesRequestsAndReferencingRows(t *testing.T) {
	raw := openFixtureAtVersion(t, 19)
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(stmt, args...); err != nil {
			t.Fatalf("fixture insert failed: %v\n%s", err, stmt)
		}
	}
	exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'SPIKE-1', 'spike', 'itm_1', 'test spike', 'in_progress', 0, 0)`)
	exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_1', 'agent-one', 'claude', 'm', 'orchestrator', 'itm_1', 'itm_1', 'b', 'active', 0)`)
	exec(`INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES ('ses_1', 'agt_1', 1, 1, 'tok', 'tm', '/tmp', 'running', 'neutral', 0)`)
	exec(`INSERT INTO requests (id, kind, agent_id, session_id, item_id, prompt, options_json, state, responded_via, binding_json, created_at)
		VALUES ('req_1', 'approve_plan', 'agt_1', 'ses_1', 'itm_1', 'a stored summary', '["Approve"]', 'approved', 'terminal', '{"ref":"req_1"}', 5)`)
	exec(`INSERT INTO messages (id, seq, kind, wake_class, priority, origin, to_agent_id, root_item_id, request_id, payload_json, state, created_at)
		VALUES ('msg_1', 1, 'relay', 'immediate', 1, 'daemon', 'agt_1', 'itm_1', 'req_1', '{"a":1}', 'pending', 6)`)
	exec(`INSERT INTO notifications (id, level, kind, title, body, agent_id, item_id, request_id, dedup_key, created_at)
		VALUES ('ntf_1', 'action', 'request.approve_plan', 't', 'b', 'agt_1', 'itm_1', 'req_1', 'dk_1', 7)`)

	beforeRequests := namedRowSnapshot(t, raw, `SELECT * FROM requests ORDER BY id`)
	beforeRequestsFK := foreignKeyList(t, raw, "requests")

	continueMigratingTo(t, raw, 19, 20)

	afterRequests := namedRowSnapshot(t, raw, `SELECT * FROM requests ORDER BY id`)
	if !reflect.DeepEqual(beforeRequests, afterRequests) {
		t.Fatalf("requests rows changed:\nbefore=%+v\nafter=%+v", beforeRequests, afterRequests)
	}
	if diff := cmpFKLists(beforeRequestsFK, foreignKeyList(t, raw, "requests")); diff != "" {
		t.Fatalf("requests foreign keys changed: %s", diff)
	}
	var msgRequestID, ntfRequestID string
	if err := raw.QueryRow(`SELECT request_id FROM messages WHERE id = 'msg_1'`).Scan(&msgRequestID); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT request_id FROM notifications WHERE id = 'ntf_1'`).Scan(&ntfRequestID); err != nil {
		t.Fatal(err)
	}
	if msgRequestID != "req_1" || ntfRequestID != "req_1" {
		t.Fatalf("referencing rows lost their request_id: messages=%q notifications=%q", msgRequestID, ntfRequestID)
	}
	if n := foreignKeyCheckViolations(t, raw); n != 0 {
		t.Fatalf("foreign_key_check reported %d violation(s) after the rebuild", n)
	}
}
