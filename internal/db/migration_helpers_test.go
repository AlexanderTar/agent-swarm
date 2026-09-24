package db

// Test helpers shared by the schema migration tests (schema_0010_test.go,
// schema_0011_test.go). They drive the real migration runner (migrationFiles
// + applyMigrations, the same functions (*DB).migrate uses) instead of
// duplicating its logic, so a fixture built here is exactly what a database
// left at an older schema version actually looks like.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// openFixtureAtVersion opens a fresh sqlite database and applies exactly the
// first `version` embedded migrations (schema file 000N corresponds to
// PRAGMA user_version N), leaving it in the shape of a database that has
// never run any later migration. Pass the resulting *sql.DB to
// continueMigrating to apply the rest.
func openFixtureAtVersion(t *testing.T, version int) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "swarm.db")
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)&_txlock=immediate"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })

	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if version > len(files) {
		t.Fatalf("version %d exceeds %d embedded migrations", version, len(files))
	}

	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := applyMigrations(ctx, conn, files, 0, version); err != nil {
		t.Fatal(err)
	}
	return raw
}

// continueMigrating applies every embedded migration from `from` onward to
// an already-open database (typically one from openFixtureAtVersion),
// mirroring what (*DB).migrate does on a normal reopen.
func continueMigrating(t *testing.T, raw *sql.DB, from int) {
	t.Helper()
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := applyMigrations(ctx, conn, files, from, len(files)); err != nil {
		t.Fatal(err)
	}
}

func tableRowCount(t *testing.T, raw *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := raw.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count(%s): %v", table, err)
	}
	return n
}
