package db

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestSummary2000MigrationAllowsA1500CharacterPrompt is the Opus-review fix:
// 2026-09-28-summary-in-native-question raised the application-level
// section/plan/report approval summary cap from 1000 to 2000 characters
// (askApproval), but requests.prompt's CHECK constraint -- last rebuilt by
// 0006_hitl_terminal_via.sql at <= 1000 -- was never migrated, so a summary
// over 1000 characters passed Go validation only to fail the INSERT. This
// proves a database parked at version 18 accepts a 1500-character prompt
// once it catches up to 19, and still rejects one over 2000.
func TestSummary2000MigrationAllowsA1500CharacterPrompt(t *testing.T) {
	raw := openFixtureAtVersion(t, 18) // pre-0019 shape, CHECK <= 1000
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(stmt, args...); err != nil {
			t.Fatalf("fixture insert failed: %v\n%s", err, stmt)
		}
	}
	exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'SPIKE-1', 'spike', 'itm_1', 'test spike', 'in_progress', 0, 0)`)

	long1500 := strings.Repeat("s", 1500)
	if _, err := raw.Exec(`INSERT INTO requests (id, kind, item_id, prompt, state, created_at)
		VALUES ('req_1', 'approve_section', 'itm_1', ?, 'open', 0)`, long1500); err == nil {
		t.Fatal("a 1500-character prompt must still be refused before the migration (CHECK <= 1000)")
	}

	continueMigratingTo(t, raw, 18, 19)

	exec(`INSERT INTO requests (id, kind, item_id, prompt, state, created_at)
		VALUES ('req_1', 'approve_section', 'itm_1', ?, 'open', 0)`, long1500)

	var got string
	if err := raw.QueryRow(`SELECT prompt FROM requests WHERE id = 'req_1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != long1500 {
		t.Fatalf("stored prompt length = %d, want %d", len(got), len(long1500))
	}

	long2001 := strings.Repeat("s", 2001)
	if _, err := raw.Exec(`INSERT INTO requests (id, kind, item_id, prompt, state, created_at)
		VALUES ('req_2', 'approve_section', 'itm_1', ?, 'open', 0)`, long2001); err == nil {
		t.Fatal("a 2001-character prompt must still be refused after the migration (CHECK <= 2000)")
	}
}

// TestMigration0019PreservesRequestsAndReferencingRows is the advisor's
// review fix: 0019's rebuild does `DROP TABLE requests` (via requests_new /
// RENAME), and messages.request_id / notifications.request_id both
// reference requests(id). A pre-existing requests row plus rows in both
// referencing tables must all survive the rebuild untouched, and SQLite's
// own foreign_key_check must report nothing broken (applyMigrations runs
// migrations with `PRAGMA foreign_keys = OFF`, so a real violation would
// otherwise pass silently here rather than failing loudly at insert time).
func TestMigration0019PreservesRequestsAndReferencingRows(t *testing.T) {
	raw := openFixtureAtVersion(t, 18)
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
	exec(`INSERT INTO requests (id, kind, agent_id, session_id, item_id, prompt, options_json, state, binding_json, created_at)
		VALUES ('req_1', 'approve_plan', 'agt_1', 'ses_1', 'itm_1', 'a stored summary', '["Approve"]', 'open', '{"ref":"req_1"}', 5)`)
	exec(`INSERT INTO messages (id, seq, kind, wake_class, priority, origin, to_agent_id, root_item_id, request_id, payload_json, state, created_at)
		VALUES ('msg_1', 1, 'relay', 'immediate', 1, 'daemon', 'agt_1', 'itm_1', 'req_1', '{"a":1}', 'pending', 6)`)
	exec(`INSERT INTO notifications (id, level, kind, title, body, agent_id, item_id, request_id, dedup_key, created_at)
		VALUES ('ntf_1', 'action', 'request.approve_plan', 't', 'b', 'agt_1', 'itm_1', 'req_1', 'dk_1', 7)`)

	beforeRequests := namedRowSnapshot(t, raw, `SELECT * FROM requests ORDER BY id`)
	beforeRequestsFK := foreignKeyList(t, raw, "requests")

	continueMigratingTo(t, raw, 18, 19)

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

// cmpFKLists is a tiny diff for two foreignKeyList results, used only to
// produce a readable failure message (reflect.DeepEqual would just say
// "not equal").
func cmpFKLists(before, after []fkRow) string {
	if reflect.DeepEqual(before, after) {
		return ""
	}
	return fmt.Sprintf("before=%+v after=%+v", before, after)
}
