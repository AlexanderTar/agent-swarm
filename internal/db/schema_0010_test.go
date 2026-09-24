package db

import (
	"reflect"
	"strings"
	"testing"
)

// TestMigration0010PreservesRowsAndWidensChecks pins down 0010's SQLite
// table-rebuild (agents, artifacts have no ALTER TABLE ... ALTER CONSTRAINT):
// every existing row -- and every row referencing agents/artifacts -- must
// survive untouched, byte for byte and column for column, every index and
// foreign key must still be there, and the widened CHECKs must accept the
// new values ('designer' role, 'design'/'research' kinds) while still
// rejecting bogus ones.
//
// Row counts and single-column spot checks alone aren't enough: they pass
// even when a rebuild drops an index, or silently transposes two columns'
// values (INSERT INTO t_new SELECT * FROM t copies positionally, so
// reordering two same-type columns in t_new's declaration relabels their
// values without changing anything positional). The checks below compare
// rows by column name and diff the index and foreign key lists directly, to
// catch exactly that.
func TestMigration0010PreservesRowsAndWidensChecks(t *testing.T) {
	raw := openFixtureAtVersion(t, 9) // pre-0010: today's schema, before this migration exists

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(query, args...); err != nil {
			t.Fatalf("fixture insert failed: %v\n%s", err, query)
		}
	}

	// Two items so agents' item_id and root_item_id are genuinely distinct
	// values, not the same string doing double duty.
	exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_root', 'EPIC-1', 'epic', 'itm_root', 'root epic', 'ready', 1, 1)`)
	exec(`INSERT INTO items (id, key, type, parent_id, root_id, title, status, created_at, updated_at)
		VALUES ('itm_task', 'TASK-1', 'task', 'itm_root', 'itm_root', 'test task', 'ready', 2, 2)`)

	// Every agents column gets a distinct, non-NULL value (where the CHECK
	// constraints allow one), including a second row whose parent_agent_id
	// points at the first -- so a rebuild that drops or corrupts the
	// self-referential FK, or transposes any two same-type columns (e.g.
	// advisor_kind/advisor_model, item_id/root_item_id), is detectable.
	exec(`INSERT INTO agents (id, name, kind, model, effort, role, item_id, root_item_id,
			parent_agent_id, advisor_kind, advisor_model, advisor_effort, advisor_mode,
			brief, state, preflight_error, created_at, finished_at, role_overrides)
		VALUES ('agt_1', 'agent-one', 'claude', 'claude-3-opus', 'high', 'coder', 'itm_task', 'itm_root',
			NULL, 'codex', 'gpt-5', 'low', 'native',
			'brief one', 'active', 'preflight one', 100, 200, '{"a":1}')`)
	exec(`INSERT INTO agents (id, name, kind, model, effort, role, item_id, root_item_id,
			parent_agent_id, advisor_kind, advisor_model, advisor_effort, advisor_mode,
			brief, state, preflight_error, created_at, finished_at, role_overrides)
		VALUES ('agt_2', 'agent-two', 'codex', 'gpt-5-codex', 'medium', 'reviewer', 'itm_task', 'itm_root',
			'agt_1', 'cursor', 'gpt-4o', 'high', 'simulated',
			'brief two', 'finished', 'preflight two', 150, 250, '{"b":2}')`)

	// Every artifacts column gets a distinct, non-NULL value too.
	exec(`INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_1', 'itm_task', 'note', 'path-one', 1, 'agt_1', 300)`)
	exec(`INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_2', 'itm_root', 'spec', 'path-two', 2, 'agt_2', 400)`)

	// Rows referencing agents/artifacts, to prove the rebuild doesn't just
	// preserve the rebuilt tables' own rows but also what points at them.
	exec(`INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES ('ses_1', 'agt_1', 1, 1, 'tok_1', 'tmux_1', '/tmp', 'running', 'neutral', 1)`)
	exec(`INSERT INTO artifact_revisions (artifact_id, revision, sha256, content, sections_json, created_at)
		VALUES ('art_1', 1, 'sha', 'content', '[]', 1)`)

	beforeCounts := map[string]int{
		"items":              tableRowCount(t, raw, "items"),
		"agents":             tableRowCount(t, raw, "agents"),
		"artifacts":          tableRowCount(t, raw, "artifacts"),
		"sessions":           tableRowCount(t, raw, "sessions"),
		"artifact_revisions": tableRowCount(t, raw, "artifact_revisions"),
	}
	beforeAgents := namedRowSnapshot(t, raw, `SELECT * FROM agents ORDER BY id`)
	beforeArtifacts := namedRowSnapshot(t, raw, `SELECT * FROM artifacts ORDER BY id`)
	beforeAgentsIdx := indexList(t, raw, "agents")
	beforeArtifactsIdx := indexList(t, raw, "artifacts")
	beforeAgentsFK := foreignKeyList(t, raw, "agents")
	beforeArtifactsFK := foreignKeyList(t, raw, "artifacts")
	beforeArtifactRevisionsFK := foreignKeyList(t, raw, "artifact_revisions")
	beforeRequestsFK := foreignKeyList(t, raw, "requests")
	beforeSessionsFK := foreignKeyList(t, raw, "sessions")
	beforeCheckpointsFK := foreignKeyList(t, raw, "checkpoints")
	// Guard the FK comparison below against a typo'd or missing table name:
	// PRAGMA foreign_key_list on a table that doesn't exist (or has no FKs)
	// returns zero rows, which would make an empty-vs-empty comparison pass
	// without actually checking anything.
	for name, fks := range map[string][]fkRow{
		"agents":             beforeAgentsFK,
		"artifacts":          beforeArtifactsFK,
		"artifact_revisions": beforeArtifactRevisionsFK,
		"requests":           beforeRequestsFK,
		"sessions":           beforeSessionsFK,
		"checkpoints":        beforeCheckpointsFK,
	} {
		if len(fks) == 0 {
			t.Fatalf("%s: foreign_key_list returned no rows before migrating; the table name is probably wrong", name)
		}
	}

	continueMigratingTo(t, raw, 9, 10) // applies 0010, and only 0010

	// Row counts are unchanged.
	for table, want := range beforeCounts {
		if got := tableRowCount(t, raw, table); got != want {
			t.Errorf("%s row count = %d, want %d (rebuild must preserve rows)", table, got, want)
		}
	}

	// Every row is byte-for-byte identical, column name for column name.
	// SELECT * copies by position, so a column reorder in the rebuild
	// relabels values under the wrong name -- comparing by name (via
	// namedRowSnapshot) catches that; comparing by position would not.
	afterAgents := namedRowSnapshot(t, raw, `SELECT * FROM agents ORDER BY id`)
	afterArtifacts := namedRowSnapshot(t, raw, `SELECT * FROM artifacts ORDER BY id`)
	if !reflect.DeepEqual(beforeAgents, afterAgents) {
		t.Errorf("agents rows changed across the rebuild:\nbefore: %v\nafter:  %v", beforeAgents, afterAgents)
	}
	if !reflect.DeepEqual(beforeArtifacts, afterArtifacts) {
		t.Errorf("artifacts rows changed across the rebuild:\nbefore: %v\nafter:  %v", beforeArtifacts, afterArtifacts)
	}

	// The self-referential FK still resolves to the same original row.
	var parentName string
	if err := raw.QueryRow(`SELECT p.name FROM agents c JOIN agents p ON p.id = c.parent_agent_id WHERE c.id = 'agt_2'`).Scan(&parentName); err != nil {
		t.Fatalf("agents self-reference FK broken: %v", err)
	}
	if parentName != "agent-one" {
		t.Fatalf("parent agent name = %q, want agent-one", parentName)
	}

	// Every index on agents/artifacts is unchanged -- a rebuild that forgets
	// to recreate an index after the rename (or gets one wrong) shows up
	// here, not in row counts or row contents.
	if !reflect.DeepEqual(beforeAgentsIdx, indexList(t, raw, "agents")) {
		t.Errorf("agents indexes changed:\nbefore: %+v\nafter:  %+v", beforeAgentsIdx, indexList(t, raw, "agents"))
	}
	if !reflect.DeepEqual(beforeArtifactsIdx, indexList(t, raw, "artifacts")) {
		t.Errorf("artifacts indexes changed:\nbefore: %+v\nafter:  %+v", beforeArtifactsIdx, indexList(t, raw, "artifacts"))
	}
	haveAgentsIdx := map[string]bool{}
	for _, idx := range indexList(t, raw, "agents") {
		haveAgentsIdx[idx.Name] = true
	}
	if !haveAgentsIdx["agents_parent"] || !haveAgentsIdx["agents_root"] {
		t.Fatalf("agents must keep the agents_parent and agents_root indexes, got %+v", indexList(t, raw, "agents"))
	}

	// Every foreign key on the rebuilt tables, and on the tables that
	// reference them, is unchanged. "requests" is the HITL requests table
	// (0004_hitl_requests.sql): there is no separate table literally named
	// hitl_requests.
	fkChecks := []struct {
		table  string
		before []fkRow
	}{
		{"agents", beforeAgentsFK},
		{"artifacts", beforeArtifactsFK},
		{"artifact_revisions", beforeArtifactRevisionsFK},
		{"requests", beforeRequestsFK},
		{"sessions", beforeSessionsFK},
		{"checkpoints", beforeCheckpointsFK},
	}
	for _, c := range fkChecks {
		after := foreignKeyList(t, raw, c.table)
		if !reflect.DeepEqual(c.before, after) {
			t.Errorf("%s foreign keys changed:\nbefore: %+v\nafter:  %+v", c.table, c.before, after)
		}
	}

	// The database-wide foreign key consistency check finds nothing broken.
	if n := foreignKeyCheckViolations(t, raw); n != 0 {
		t.Errorf("PRAGMA foreign_key_check reported %d violation(s), want 0", n)
	}

	// The widened CHECKs accept the new values...
	if _, err := raw.Exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_designer', 'agent-designer', 'claude', 'claude-3', 'designer', 'itm_task', 'itm_root', 'b', 'active', 1)`); err != nil {
		t.Fatalf("role 'designer' should now be allowed: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_design', 'itm_task', 'design', 'p2', 1, 'agt_1', 1)`); err != nil {
		t.Fatalf("kind 'design' should now be allowed: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_research', 'itm_task', 'research', 'p3', 1, 'agt_1', 1)`); err != nil {
		t.Fatalf("kind 'research' should now be allowed: %v", err)
	}

	// ...and an invalid role/kind still fails.
	if _, err := raw.Exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_bogus', 'agent-bogus', 'claude', 'claude-3', 'bogus', 'itm_task', 'itm_root', 'b', 'active', 1)`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("role 'bogus' should still violate the CHECK constraint, err = %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_bogus', 'itm_task', 'bogus', 'p4', 1, 'agt_1', 1)`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("kind 'bogus' should still violate the CHECK constraint, err = %v", err)
	}
}
