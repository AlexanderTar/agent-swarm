package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/migrate"
	_ "modernc.org/sqlite"
)

// newV1DB builds a real v4 database in t.TempDir() from the schema fixture.
// S-5: it is never the operator's ~/.swarm/swarm.db.
func newV1DB(t *testing.T, rows ...[]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "swarm.db")
	d, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(migrate.V1SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := d.Exec(`INSERT INTO tasks (key, title, status, repo_path, initial_context,
			handoff_note, parent_task_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, (SELECT id FROM tasks WHERE key = ?), ?, ?)`, r...); err != nil {
			t.Fatalf("insert %v: %v", r[0], err)
		}
	}
	return path
}

// row is one v1 task, in the order newV1DB's INSERT expects.
func row(key, title, status, repo, ctx, handoff, parentKey, created, updated string) []any {
	var p any
	if parentKey != "" {
		p = parentKey
	}
	var rp any
	if repo != "" {
		rp = repo
	}
	return []any{key, title, status, rp, ctx, handoff, p, created, updated}
}

// S-8, and the sole guarding assertion that always runs regardless of SQLite build
// behavior: a real immutable=1 read of the live database on 2026-09-18 reported 14
// children for SW-673 where the correct count (mode=ro, WAL-aware) was 36.
// TestOpenV1ReadsTheWALAndNeverUsesImmutable below exercises the WAL-visibility
// scenario end to end, but it may skip depending on whether the local
// modernc.org/sqlite build checkpoints on Close (it does, on every build tested so
// far, making that skip the norm rather than a local anomaly) — a skip there must
// never silence this DSN check, so it lives in its own test with no skip path.
func TestV1DSNIsReadOnlyAndNeverImmutable(t *testing.T) {
	dsn := migrate.V1DSN(filepath.Join(t.TempDir(), "swarm.db"))
	if strings.Contains(dsn, "immutable") {
		t.Errorf("DSN = %q; must never contain immutable (S-8)", dsn)
	}
	if !strings.Contains(dsn, "mode=ro") {
		t.Errorf("DSN = %q; must contain mode=ro so the -wal file is read (S-8)", dsn)
	}
}

// S-8: immutable=1 skipped the WAL and undercounted SW-673's children on
// 2026-09-18 (14 instead of 36). The reader must read the WAL. This test exercises
// that scenario end to end; TestV1DSNIsReadOnlyAndNeverImmutable above is what
// actually guards the DSN itself when this one skips.
func TestOpenV1ReadsTheWALAndNeverUsesImmutable(t *testing.T) {
	path := newV1DB(t, row("SW-1", "One", "ready", "", "c", "", "", "2026-09-01 10:00:00", "2026-09-01 10:00:00"))
	// Write a second row through a separate handle and do not checkpoint, so the new
	// row lives only in the -wal file.
	w, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO tasks (key, title, status, created_at, updated_at)
		VALUES ('SW-2','Two','ready','2026-09-01 10:00:00','2026-09-01 10:00:00')`); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Skip("this SQLite build checkpointed on close; the WAL case cannot be exercised here")
	}

	d, err := migrate.OpenV1(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, err := migrate.ReadV1(context.Background(), d, []string{"SW-1", "SW-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d rows, want 2; a WAL-only row was missed (S-8)", len(got))
	}
}

func TestV1VersionReadsSchemaMeta(t *testing.T) {
	d, err := migrate.OpenV1(newV1DB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	v, err := migrate.V1Version(context.Background(), d)
	if err != nil || v != 4 {
		t.Fatalf("= %d, %v, want 4", v, err)
	}
}

func TestReadV1ParsesTimestampsAndOptionalColumns(t *testing.T) {
	path := newV1DB(t,
		row("SW-100", "Parent", "ready", "/fake/repo", "context text", "", "", "2026-09-01 10:11:12", "2026-09-02 13:14:15"),
		row("SW-101", "Child", "in_progress", "", "", "handoff text", "SW-100", "2026-09-01 10:11:13", "2026-09-02 13:14:16"),
	)
	d, err := migrate.OpenV1(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, err := migrate.ReadV1(context.Background(), d, []string{"SW-100", "SW-101"})
	if err != nil {
		t.Fatal(err)
	}
	p := got["SW-100"]
	if p.Title != "Parent" || p.Status != "ready" || p.RepoPath != "/fake/repo" || p.InitialContext != "context text" {
		t.Fatalf("SW-100 = %+v", p)
	}
	if p.CreatedAt.UTC().Format("2006-01-02 15:04:05") != "2026-09-01 10:11:12" {
		t.Errorf("CreatedAt = %v", p.CreatedAt)
	}
	if p.UpdatedAt.UTC().Format("2006-01-02 15:04:05") != "2026-09-02 13:14:15" {
		t.Errorf("UpdatedAt = %v", p.UpdatedAt)
	}
	c := got["SW-101"]
	if c.ParentKey != "SW-100" {
		t.Errorf("ParentKey = %q, want SW-100", c.ParentKey)
	}
	if c.RepoPath != "" {
		t.Errorf("RepoPath = %q, want empty for NULL", c.RepoPath)
	}
	if c.HandoffNote != "handoff text" {
		t.Errorf("HandoffNote = %q", c.HandoffNote)
	}
}

// A key §20 names that is not in the database must be reported, not skipped: the
// migration builds a tree the spec dictates, and a hole in it is a hard failure.
func TestReadV1ReportsMissingKeys(t *testing.T) {
	d, err := migrate.OpenV1(newV1DB(t, row("SW-1", "One", "ready", "", "", "", "", "2026-09-01 10:00:00", "2026-09-01 10:00:00")))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, err = migrate.ReadV1(context.Background(), d, []string{"SW-1", "SW-999", "SW-998"})
	if err == nil {
		t.Fatal("want an error naming the missing keys")
	}
	for _, want := range []string{"SW-998", "SW-999"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// §20's status table, exactly. review and archived are not in it.
func TestMapStatusFollowsTheSpecTableAndRefusesTheRest(t *testing.T) {
	for v1, want := range map[string]string{
		"ready":       "ready",
		"in_progress": "ready", // A7: nothing is in progress after the cutover
		"blocked":     "blocked",
		"backlog":     "draft",
		"done":        "done",
	} {
		got, err := migrate.MapStatus(v1)
		if err != nil || got != want {
			t.Errorf("MapStatus(%q) = %q, %v, want %q", v1, got, err, want)
		}
	}
	for _, v1 := range []string{"review", "archived", "", "nonsense"} {
		if _, err := migrate.MapStatus(v1); err == nil {
			t.Errorf("MapStatus(%q) must fail rather than guess (§20 lists five statuses)", v1)
		}
	}
}

// §20: "The brief is the first 600 characters of initial_context, or of
// handoff_note if the context is empty."
func TestBriefPrefersContextThenHandoffAndCapsAtSixHundredCharacters(t *testing.T) {
	if got := migrate.Brief("ctx", "handoff"); got != "ctx" {
		t.Errorf("= %q, want the context", got)
	}
	if got := migrate.Brief("   ", "handoff"); got != "handoff" {
		t.Errorf("= %q, want the handoff when the context is blank", got)
	}
	if got := migrate.Brief("", ""); got != "" {
		t.Errorf("= %q, want empty", got)
	}
	// items.brief has CHECK (length(brief) <= 600), and SQLite length() counts
	// characters, so the cap is in runes, not bytes.
	long := strings.Repeat("é", 700)
	got := migrate.Brief(long, "")
	if n := len([]rune(got)); n != 600 {
		t.Fatalf("runes = %d, want 600", n)
	}
	if len(got) == 600 {
		t.Error("the cap was applied in bytes; items.brief's CHECK counts characters")
	}
}
