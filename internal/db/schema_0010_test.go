package db

import (
	"strings"
	"testing"
)

// TestMigration0010PreservesRowsAndWidensChecks pins down 0010's SQLite
// table-rebuild (agents, artifacts have no ALTER TABLE ... ALTER CONSTRAINT):
// every existing row -- and every row referencing agents/artifacts -- must
// survive untouched, and the widened CHECKs must accept the new values
// ('designer' role, 'design'/'research' kinds) while still rejecting bogus
// ones.
func TestMigration0010PreservesRowsAndWidensChecks(t *testing.T) {
	raw := openFixtureAtVersion(t, 9) // pre-0010: today's schema, before this migration exists

	// Representative rows at the pre-0010 schema: an item, an agent on it, an
	// artifact on it, plus rows that reference the agent and the artifact
	// (sessions.agent_id, artifact_revisions.artifact_id) to prove the
	// rebuild doesn't just preserve the rebuilt tables' own rows but also
	// what points at them.
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(query, args...); err != nil {
			t.Fatalf("fixture insert failed: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'TASK-1', 'task', 'itm_1', 'test item', 'ready', 1, 1)`)
	exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_1', 'agent-1', 'claude', 'claude-3', 'coder', 'itm_1', 'itm_1', 'b', 'active', 1)`)
	exec(`INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_1', 'itm_1', 'note', 'p', 1, 'agt_1', 1)`)
	exec(`INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES ('ses_1', 'agt_1', 1, 1, 'tok_1', 'tmux_1', '/tmp', 'running', 'neutral', 1)`)
	exec(`INSERT INTO artifact_revisions (artifact_id, revision, sha256, content, sections_json, created_at)
		VALUES ('art_1', 1, 'sha', 'content', '[]', 1)`)

	before := map[string]int{
		"items":              tableRowCount(t, raw, "items"),
		"agents":             tableRowCount(t, raw, "agents"),
		"artifacts":          tableRowCount(t, raw, "artifacts"),
		"sessions":           tableRowCount(t, raw, "sessions"),
		"artifact_revisions": tableRowCount(t, raw, "artifact_revisions"),
	}

	continueMigrating(t, raw, 9) // applies 0010 (and anything after it)

	for table, want := range before {
		if got := tableRowCount(t, raw, table); got != want {
			t.Errorf("%s row count = %d, want %d (rebuild must preserve rows)", table, got, want)
		}
	}
	// The FK from sessions to the rebuilt agents row, and from
	// artifact_revisions to the rebuilt artifacts row, must still resolve to
	// the exact same original rows.
	var agentName, artifactPath string
	if err := raw.QueryRow(`SELECT a.name FROM sessions s JOIN agents a ON a.id = s.agent_id WHERE s.id = 'ses_1'`).Scan(&agentName); err != nil {
		t.Fatalf("session->agent FK broken: %v", err)
	}
	if agentName != "agent-1" {
		t.Fatalf("agent name = %q, want agent-1", agentName)
	}
	if err := raw.QueryRow(`SELECT ar.path FROM artifact_revisions r JOIN artifacts ar ON ar.id = r.artifact_id WHERE r.artifact_id = 'art_1'`).Scan(&artifactPath); err != nil {
		t.Fatalf("artifact_revisions->artifacts FK broken: %v", err)
	}
	if artifactPath != "p" {
		t.Fatalf("artifact path = %q, want p", artifactPath)
	}

	// The widened CHECKs accept the new values...
	if _, err := raw.Exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_designer', 'agent-designer', 'claude', 'claude-3', 'designer', 'itm_1', 'itm_1', 'b', 'active', 1)`); err != nil {
		t.Fatalf("role 'designer' should now be allowed: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_design', 'itm_1', 'design', 'p2', 1, 'agt_1', 1)`); err != nil {
		t.Fatalf("kind 'design' should now be allowed: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_research', 'itm_1', 'research', 'p3', 1, 'agt_1', 1)`); err != nil {
		t.Fatalf("kind 'research' should now be allowed: %v", err)
	}

	// ...and an invalid role/kind still fails.
	if _, err := raw.Exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_bogus', 'agent-bogus', 'claude', 'claude-3', 'bogus', 'itm_1', 'itm_1', 'b', 'active', 1)`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("role 'bogus' should still violate the CHECK constraint, err = %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_bogus', 'itm_1', 'bogus', 'p4', 1, 'agt_1', 1)`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("kind 'bogus' should still violate the CHECK constraint, err = %v", err)
	}
}
