package db

import (
	"strings"
	"testing"
)

// TestAgentContinuityMigrationPreservesRows pins down migration 0014: every
// pre-migration row survives byte-identical (agents.id stays canonical -- no
// row is deleted, renamed or merged), and the new continuity tables arrive
// with the exact modes, phases and the one-nonterminal-operation-per-agent
// partial unique index the replacement protocol depends on.
func TestAgentContinuityMigrationPreservesRows(t *testing.T) {
	raw := openFixtureAtVersion(t, 13) // post-0013, pre-0014

	exec := func(query string, args ...any) error {
		t.Helper()
		_, err := raw.Exec(query, args...)
		return err
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if err := exec(query, args...); err != nil {
			t.Fatalf("fixture insert failed: %v\n%s", err, query)
		}
	}

	mustExec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'epic one', 'in_progress', 1, 2)`)
	mustExec(`INSERT INTO items (id, key, type, parent_id, root_id, title, status, created_at, updated_at)
		VALUES ('itm_2', 'TASK-1', 'task', 'itm_1', 'itm_1', 'task one', 'in_progress', 3, 4)`)
	mustExec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_1', 'epic-one-orchestrator', 'fake', 'fake-1', 'orchestrator', 'itm_1', 'itm_1', 'b', 'active', 5)`)
	mustExec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, parent_agent_id, brief, state, created_at)
		VALUES ('agt_2', 'task-one-coder', 'fake', 'fake-1', 'coder', 'itm_2', 'itm_1', 'agt_1', 'b', 'active', 6)`)
	mustExec(`INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES ('ses_1', 'agt_2', 1, 1, 'tok_1', 'task-one-coder', '/tmp', 'running', 'neutral', 7)`)
	mustExec(`INSERT INTO messages (id, seq, kind, origin, to_agent_id, root_item_id, item_id, payload_json, state, created_at)
		VALUES ('msg_1', 1, 'assignment', 'daemon', 'agt_2', 'itm_1', 'itm_2', '{}', 'pending', 8)`)
	mustExec(`INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, created_at)
		VALUES ('ckp_1', 'ses_1', 'agt_2', 'itm_2', 'accepted', 1, 'starting', 9)`)
	mustExec(`INSERT INTO requests (id, kind, agent_id, session_id, item_id, prompt, options_json, state, created_at)
		VALUES ('req_1', 'question', 'agt_2', 'ses_1', 'itm_2', 'which way?', '[]', 'open', 10)`)

	before := map[string][]map[string]string{}
	for _, q := range []struct{ name, query string }{
		{"agents", `SELECT * FROM agents ORDER BY id`},
		{"sessions", `SELECT * FROM sessions ORDER BY id`},
		{"items", `SELECT * FROM items ORDER BY id`},
		{"messages", `SELECT * FROM messages ORDER BY id`},
		{"checkpoints", `SELECT * FROM checkpoints ORDER BY id`},
		{"requests", `SELECT * FROM requests ORDER BY id`},
	} {
		before[q.name] = namedRowSnapshot(t, raw, q.query)
	}

	continueMigratingTo(t, raw, 13, 14) // applies exactly 0014

	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 14 {
		t.Fatalf("user_version = %d, want 14", version)
	}

	// Every pre-migration row survives unchanged: same count, same values
	// under the same column names (a positional rebuild that relabels
	// columns would show up here via namedRowSnapshot).
	for _, q := range []struct{ name, query string }{
		{"agents", `SELECT * FROM agents ORDER BY id`},
		{"sessions", `SELECT * FROM sessions ORDER BY id`},
		{"items", `SELECT * FROM items ORDER BY id`},
		{"messages", `SELECT * FROM messages ORDER BY id`},
		{"checkpoints", `SELECT * FROM checkpoints ORDER BY id`},
		{"requests", `SELECT * FROM requests ORDER BY id`},
	} {
		after := namedRowSnapshot(t, raw, q.query)
		if len(after) != len(before[q.name]) {
			t.Fatalf("%s row count changed: %d -> %d", q.name, len(before[q.name]), len(after))
		}
		for i := range after {
			for col, want := range before[q.name][i] {
				if after[i][col] != want {
					t.Fatalf("%s row %d col %s changed: %q -> %q", q.name, i, col, want, after[i][col])
				}
			}
		}
	}

	// agents gains auto_restart, defaulting to 1 (restart enabled) on the
	// preserved rows: user Cancel is what flips it to 0, never the migration.
	var auto int
	if err := raw.QueryRow(`SELECT auto_restart FROM agents WHERE id = 'agt_1'`).Scan(&auto); err != nil {
		t.Fatalf("agents.auto_restart: %v", err)
	}
	if auto != 1 {
		t.Fatalf("agents.auto_restart = %d, want 1", auto)
	}

	// agent_operations: the exact mode and phase enums.
	mustExec(`INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, created_at, updated_at)
		VALUES ('op_1', 'agt_2', 'recover', 'requested', 'k1', 11, 11)`)
	if err := exec(`INSERT INTO agent_operations (id, agent_id, mode, phase, created_at, updated_at)
		VALUES ('op_bad_mode', 'agt_2', 'restart', 'requested', 11, 11)`); err == nil ||
		!strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("agent_operations.mode should reject 'restart', err = %v", err)
	}
	if err := exec(`INSERT INTO agent_operations (id, agent_id, mode, phase, created_at, updated_at)
		VALUES ('op_bad_phase', 'agt_2', 'recover', 'flying', 11, 11)`); err == nil ||
		!strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("agent_operations.phase should reject 'flying', err = %v", err)
	}

	// One nonterminal operation per agent: a second in-flight row for the
	// same agent fails, while a second terminal row does not.
	if err := exec(`INSERT INTO agent_operations (id, agent_id, mode, phase, created_at, updated_at)
		VALUES ('op_2', 'agt_2', 'pause', 'queued', 12, 12)`); err == nil ||
		!strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("second nonterminal operation should fail unique, err = %v", err)
	}
	mustExec(`UPDATE agent_operations SET phase = 'succeeded' WHERE id = 'op_1'`)
	mustExec(`INSERT INTO agent_operations (id, agent_id, mode, phase, created_at, updated_at)
		VALUES ('op_2', 'agt_2', 'pause', 'queued', 12, 12)`)

	// Request identity is durable per canonical agent + request key.
	if err := exec(`INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, created_at, updated_at)
		VALUES ('op_3', 'agt_2', 'handoff', 'requested', 'k1', 13, 13)`); err == nil ||
		!strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate (agent_id, request_key) should fail unique, err = %v", err)
	}

	// agent_lineage backfill: every pre-migration agent is the root of its
	// own chain, with history preserved and nothing renamed or deleted.
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM agent_lineage`).Scan(&n); err != nil {
		t.Fatalf("agent_lineage count: %v", err)
	}
	if n != 2 {
		t.Fatalf("agent_lineage rows = %d, want 2 (one per pre-migration agent)", n)
	}
	rows := namedRowSnapshot(t, raw, `SELECT agent_id, predecessor_agent_id, root_item_id, item_id, role FROM agent_lineage ORDER BY agent_id`)
	if rows[0]["agent_id"] != "agt_1" || rows[0]["predecessor_agent_id"] != "<nil>" ||
		rows[0]["root_item_id"] != "itm_1" || rows[0]["item_id"] != "itm_1" || rows[0]["role"] != "orchestrator" {
		t.Fatalf("lineage backfill agt_1 = %v", rows[0])
	}
	if rows[1]["agent_id"] != "agt_2" || rows[1]["item_id"] != "itm_2" || rows[1]["role"] != "coder" {
		t.Fatalf("lineage backfill agt_2 = %v", rows[1])
	}
	var name string
	if err := raw.QueryRow(`SELECT name FROM agents WHERE id = 'agt_2'`).Scan(&name); err != nil || name != "task-one-coder" {
		t.Fatalf("agent renamed by migration: name = %q, err = %v", name, err)
	}

	if v := foreignKeyCheckViolations(t, raw); v != 0 {
		t.Fatalf("foreign_key_check violations = %d", v)
	}
}
