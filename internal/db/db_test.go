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
	assertHasFailureText()
	d.Close()

	// Simulate a database that only ever ran migration 1 (pre-2026-09-20 production).
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
	raw.Close()

	d, err = db.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen must apply the pending migration, not fail: %v", err)
	}
	defer d.Close()
	assertHasFailureText()
	var v int
	d.QueryRow("PRAGMA user_version").Scan(&v)
	if v != db.SchemaVersion {
		t.Fatalf("user_version = %d, want %d after catching up", v, db.SchemaVersion)
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
