package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
	_ "modernc.org/sqlite"
)

var ctx = context.Background()

func TestFreshDBMigratesOnceAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.db")
	d, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var v int
	d.QueryRow("PRAGMA user_version").Scan(&v)
	if v != db.SchemaVersion {
		t.Fatalf("user_version = %d", v)
	}
	d.Close()
	d, err = db.Open(ctx, path) // second run is a no-op
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d.Close()
	var n int
	d.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='items'`).Scan(&n)
	if n != 1 {
		t.Fatal("items table missing")
	}
}

// Live incident (2026-09-20): failure_text was first added by editing
// 0001_init.sql in place, so a database that had already run migration 1
// never got the column. This proves both halves of the real fix: a fresh
// database ends up with the column, and one already parked at version 1
// (this repo's actual production shape until today) picks it up on its next
// open, without a manual ALTER TABLE.
func TestExistingDatabaseGainsColumnsAddedByLaterMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.db")
	d, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	assertHasFailureText := func() {
		t.Helper()
		var n int
		if err := d.QueryRow(`SELECT failure_text FROM sessions LIMIT 0`).Scan(&n); err != sql.ErrNoRows {
			t.Fatalf("sessions.failure_text: %v", err)
		}
	}
	assertHasRoleOverrides := func() {
		t.Helper()
		var n int
		if err := d.QueryRow(`SELECT role_overrides FROM agents LIMIT 0`).Scan(&n); err != sql.ErrNoRows {
			t.Fatalf("agents.role_overrides: %v", err)
		}
	}
	assertHasWorkflowColumns := func() {
		t.Helper()
		var n int
		if err := d.QueryRow(`SELECT workflow_json, steps_json, units_json, solo, verify_json FROM items LIMIT 0`).Scan(&n, &n, &n, &n, &n); err != sql.ErrNoRows {
			t.Fatalf("items workflow columns: %v", err)
		}
		if err := d.QueryRow(`SELECT verdict, findings_json FROM checkpoints LIMIT 0`).Scan(&n, &n); err != sql.ErrNoRows {
			t.Fatalf("checkpoints.verdict/findings_json: %v", err)
		}
	}
	assertHasFailureText()
	assertHasRoleOverrides()
	assertHasWorkflowColumns()
	d.Close()

	// Simulate a database that only ever ran migration 1 (pre-2026-09-20 production).
	// This must strip every column later migrations added by ALTER TABLE, not
	// just the two the incident was about: 0012_workflows.sql adds columns to
	// items and checkpoints without rebuilding either table, so a real v1
	// database's items/checkpoints tables never had them either -- leaving
	// them in place here would make 0003_add_chore.sql's items rebuild (which
	// expects exactly the v1 column set) fail with a column-count mismatch.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`ALTER TABLE sessions DROP COLUMN failure_text`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`ALTER TABLE agents DROP COLUMN role_overrides`); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"workflow_json", "steps_json", "units_json", "solo", "verify_json"} {
		if _, err := raw.Exec(`ALTER TABLE items DROP COLUMN ` + col); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`ALTER TABLE artifact_revisions DROP COLUMN warnings_json`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`ALTER TABLE agents DROP COLUMN auto_restart`); err != nil {
		t.Fatal(err)
	}
	// 0016_agent_kind_reason.sql adds agents.kind_reason with plain ALTER
	// TABLE, and 0008's agents rebuild expects the v1 column set.
	if _, err := raw.Exec(`ALTER TABLE agents DROP COLUMN kind_reason`); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"verdict", "findings_json"} {
		if _, err := raw.Exec(`ALTER TABLE checkpoints DROP COLUMN ` + col); err != nil {
			t.Fatal(err)
		}
	}
	// 0012_workflows.sql also creates two new tables (plain CREATE TABLE, not
	// IF NOT EXISTS), so replaying it against this "v1" database must not
	// find them already there.
	if _, err := raw.Exec(`DROP TABLE workflow_runs`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE workflows`); err != nil {
		t.Fatal(err)
	}
	// 0015_recovery_preservation.sql adds columns to agent_operations and
	// sessions with plain ALTER TABLE, so a simulated v1 database must not
	// still carry them when the migrations replay (same precedent as 0014).
	for _, col := range []string{"manifest_path", "manifest_hash", "checkpoint_id"} {
		if _, err := raw.Exec(`ALTER TABLE agent_operations DROP COLUMN ` + col); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`ALTER TABLE sessions DROP COLUMN first_sync_at`); err != nil {
		t.Fatal(err)
	}
	// 0014_agent_continuity.sql likewise creates its tables with plain
	// CREATE TABLE, so replaying it must not find them already there.
	if _, err := raw.Exec(`DROP TABLE agent_operations`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE agent_lineage`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err = db.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen must apply the pending migration, not fail: %v", err)
	}
	defer d.Close()
	assertHasFailureText()
	assertHasRoleOverrides()
	assertHasWorkflowColumns()
	if _, err := d.Exec(`SELECT kind_reason FROM agents LIMIT 1`); err != nil {
		t.Fatalf("agents.kind_reason missing after catching up: %v", err)
	}
	var v int
	d.QueryRow("PRAGMA user_version").Scan(&v)
	if v != db.SchemaVersion {
		t.Fatalf("user_version = %d, want %d after catching up", v, db.SchemaVersion)
	}
}

// Live incident (2026-09-23): muse was wired in at the application layer
// (kinds.AgentKinds, the adapter, catalog, usage source, menubar) across
// several merges, but agents.kind's own CHECK constraint was never
// migrated -- every attempt to spawn a real muse agent failed with "CHECK
// constraint failed: kind IN ('claude','codex','agy','cursor','fake')",
// silently, at the SQL layer, well below anything a Go-level kind check
// could catch. This pins the fix and proves the other four kinds -- and a
// genuinely invalid one -- still behave exactly as before.
func TestAgentsKindAllowsMuse(t *testing.T) {
	d := dbtest.Open(t)
	if _, err := d.Exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'TASK-1', 'task', 'itm_1', 'test item', 'ready', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	insertAgent := func(id, kind string) error {
		_, err := d.Exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
			VALUES (?, ?, ?, 'm', 'coder', 'itm_1', 'itm_1', 'b', 'queued', 0)`, id, id, kind)
		return err
	}
	// kinds.AgentKinds is the real, installable kinds (kinds.Fake is
	// deliberately excluded from it -- test-only, not a kind a user selects
	// -- so it's asserted separately below). Iterating the real slice, not a
	// literal copy of it, is what makes this test rot loudly the next time a
	// kind is added at the Go layer without a matching schema migration --
	// exactly the gap that caused this incident.
	for _, kind := range kinds.AgentKinds {
		if err := insertAgent("agt_"+string(kind), string(kind)); err != nil {
			t.Errorf("kind %q: %v", kind, err)
		}
	}
	if err := insertAgent("agt_fake", string(kinds.Fake)); err != nil {
		t.Errorf("kind %q: %v", kinds.Fake, err)
	}
	if err := insertAgent("agt_bogus", "bogus"); err == nil {
		t.Fatal("kind 'bogus' should still violate the CHECK constraint")
	}
}

func TestNewerDatabaseIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.db")
	d, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	d.Exec(fmt.Sprintf("PRAGMA user_version = %d", db.SchemaVersion+1))
	d.Close()
	_, err = db.Open(ctx, path)
	if !errors.Is(err, db.ErrTooNew) || err.Error() != "database is newer than this build" {
		t.Fatalf("err = %v", err)
	}
}

func TestLegacyDatabaseIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.db")
	raw, _ := sql.Open("sqlite", "file:"+path)
	raw.Exec(`CREATE TABLE schema_meta (version INTEGER)`)
	raw.Close()
	_, err := db.Open(ctx, path)
	if !errors.Is(err, db.ErrLegacy) || err.Error() != "Agent Swarm 1.x data found. Run `swarm migrate` first." {
		t.Fatalf("err = %v", err)
	}
}

func TestPragmasApplyOnEveryConnection(t *testing.T) {
	d := dbtest.Open(t)
	d.SetMaxOpenConns(3)
	for range 3 {
		var mode string
		var fk, busy, syncMode int
		d.QueryRow("PRAGMA journal_mode").Scan(&mode)
		d.QueryRow("PRAGMA foreign_keys").Scan(&fk)
		d.QueryRow("PRAGMA busy_timeout").Scan(&busy)
		d.QueryRow("PRAGMA synchronous").Scan(&syncMode)
		if mode != "wal" || fk != 1 || busy != 5000 || syncMode != 1 {
			t.Fatalf("pragmas = %s %d %d %d", mode, fk, busy, syncMode)
		}
	}
}

func insertItem(d *db.DB, id, key, typ, title, brief, status string) error {
	_, err := d.Exec(`INSERT INTO items (id, key, type, root_id, title, brief, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1)`, id, key, typ, id, title, brief, status)
	return err
}

func TestCheckConstraints(t *testing.T) {
	d := dbtest.Open(t)
	cases := map[string]error{
		"bad type":    insertItem(d, "itm_1", "X-1", "saga", "t", "", "ready"),
		"bad status":  insertItem(d, "itm_2", "EPIC-2", "epic", "t", "", "doing"),
		"empty title": insertItem(d, "itm_3", "EPIC-3", "epic", "", "", "ready"),
		"long title":  insertItem(d, "itm_4", "EPIC-4", "epic", strings.Repeat("t", 201), "", "ready"),
	}
	for name, err := range cases {
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := insertItem(d, "itm_ok", "EPIC-9", "epic", strings.Repeat("t", 200), strings.Repeat("b", 600), "ready"); err != nil {
		t.Fatalf("boundary values rejected: %v", err)
	}
	if err := insertItem(d, "itm_long_brief", "EPIC-10", "epic", "Title", strings.Repeat("b", 2000), "ready"); err != nil {
		t.Fatalf("long brief rejected: %v", err)
	}
	_, err := d.Exec(`INSERT INTO item_deps (item_id, blocked_by_id, created_at) VALUES ('itm_ok', 'itm_ok', 1)`)
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Errorf("self dependency: err = %v", err)
	}
	_, err = d.Exec(`INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, created_at)
		VALUES ('ckp_1', 'ses_x', 'agt_x', 'itm_ok', 'progress', 1, ?, 1)`, strings.Repeat("s", 501))
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Errorf("long summary: err = %v", err)
	}
	_, err = d.Exec(`INSERT INTO repos (id, path, name, source, created_at, updated_at) VALUES ('repo_1', '/p', 'p', 'guess', 1, 1)`)
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Errorf("bad repo source: err = %v", err)
	}
	_, err = d.Exec(`INSERT INTO requests (id, kind, item_id, prompt, state, responded_via, created_at)
		VALUES ('req_bad_via', 'question', 'itm_ok', 'prompt', 'open', 'pigeon', 1)`)
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Errorf("bad responded_via: err = %v", err)
	}
	_, err = d.Exec(`INSERT INTO requests (id, kind, item_id, prompt, state, responded_via, created_at)
		VALUES ('req_terminal', 'question', 'itm_ok', 'prompt', 'answered', 'terminal', 1)`)
	if err != nil {
		t.Fatalf("terminal responded_via rejected: %v", err)
	}
}

func ftsCount(t *testing.T, d *db.DB, q string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM items_fts WHERE items_fts MATCH ?`, q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestItemsFTSStaysInSync(t *testing.T) {
	d := dbtest.Open(t)
	if err := insertItem(d, "itm_a", "EPIC-1", "epic", "Authentication overhaul", "login flow", "draft"); err != nil {
		t.Fatal(err)
	}
	if ftsCount(t, d, "authentication") != 1 || ftsCount(t, d, `"EPIC-1"`) != 1 {
		t.Fatal("insert not indexed")
	}
	d.Exec(`UPDATE items SET title = 'Payments rewrite' WHERE id = 'itm_a'`)
	if ftsCount(t, d, "authentication") != 0 || ftsCount(t, d, "payments") != 1 {
		t.Fatal("update not indexed")
	}
	d.Exec(`DELETE FROM items WHERE id = 'itm_a'`)
	if ftsCount(t, d, "payments") != 0 {
		t.Fatal("delete not indexed")
	}
}

func TestTxCommitsAndRollsBack(t *testing.T) {
	d := dbtest.Open(t)
	boom := errors.New("boom")
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		tx.Exec(`INSERT INTO settings (key, value_json, updated_at) VALUES ('a', '1', 1)`)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if err := d.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings (key, value_json, updated_at) VALUES ('b', '1', 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	d.QueryRow(`SELECT count(*) FROM settings`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want only the committed one", n)
	}
}

func TestMillisRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 123_000_000, time.UTC)
	if got := db.FromMillis(db.Millis(now)); !got.Equal(now) {
		t.Fatalf("got %v", got)
	}
	if db.Millis(time.Time{}) != 0 || !db.FromMillis(0).IsZero() {
		t.Fatal("zero time must map to 0 and back")
	}
}

func TestNativeAdviceIsUniquePerSourceRequest(t *testing.T) {
	d := dbtest.Open(t)
	d.SetMaxOpenConns(1)                // the pragma below is per connection
	d.Exec("PRAGMA foreign_keys = OFF") // only the index is under test
	ins := func(id string, src any) error {
		_, err := d.Exec(`INSERT INTO advice (id, session_id, item_id, advisor_kind, advisor_model, question, state, source_request_id, created_at)
			VALUES (?, 'ses_1', 'itm_1', 'claude', 'opus', 'q', 'answered', ?, 1)`, id, src)
		return err
	}
	for _, id := range []string{"adv_1", "adv_2"} {
		if err := ins(id, nil); err != nil {
			t.Fatalf("null source_request_id must not collide: %v", err)
		}
	}
	if err := ins("adv_3", "req#0"); err != nil {
		t.Fatal(err)
	}
	if err := ins("adv_4", "req#0"); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate native advice: err = %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT needs_compaction_notice FROM sessions LIMIT 0`).Scan(&n); err != sql.ErrNoRows {
		t.Fatalf("sessions.needs_compaction_notice: %v", err)
	}
}

/* NOTE: TestMigration0010LowercasesGitVerifyKeys lived here until the
   main-branch merge added migrations after 0010: its downgrade-user_version
   replay trick only works when 0010 is the last migration, so it moved to
   schema_0010_lowercase_test.go (same name, same assertions) built on the
   migration-helper fixture instead. */
