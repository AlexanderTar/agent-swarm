package db

// Test helpers shared by the schema migration tests (schema_0010_test.go,
// schema_0011_test.go). They drive the real migration runner (migrationFiles
// + applyMigrations, the same functions (*DB).migrate uses) instead of
// duplicating its logic, so a fixture built here is exactly what a database
// left at an older schema version actually looks like.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
)

// openFixtureAtVersion opens a fresh sqlite database and applies exactly the
// first `version` embedded migrations (schema file 000N corresponds to
// PRAGMA user_version N), leaving it in the shape of a database that has
// never run any later migration. Pass the resulting *sql.DB to
// continueMigrating (or continueMigratingTo) to apply the rest.
func openFixtureAtVersion(t *testing.T, version int) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "swarm.db")
	raw, err := sql.Open("sqlite", "file:"+path+dsnPragmas)
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
	continueMigratingTo(t, raw, from, len(files))
}

// continueMigratingTo applies embedded migrations [from, to) to an
// already-open database. Tests that pin down a single migration use this
// with to = from+1 so a later migration can't paper over what the one under
// test got wrong.
func continueMigratingTo(t *testing.T, raw *sql.DB, from, to int) {
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
	if err := applyMigrations(ctx, conn, files, from, to); err != nil {
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

// namedRowSnapshot captures every row of a query keyed by column NAME rather
// than position, because rows.Columns() reflects the executing table's
// *current* declared column order. That is exactly what makes this catch a
// migration bug that reorders a table's columns in its rebuild: an
// `INSERT INTO t_new SELECT * FROM t` copies by position, so swapping two
// same-type columns' declaration order in t_new silently relabels their
// values (the physical bytes end up under the other column's name) without
// changing anything a purely positional comparison would notice.
func namedRowSnapshot(t *testing.T, raw *sql.DB, query string) []map[string]string {
	t.Helper()
	rows, err := raw.Query(query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := make(map[string]string, len(cols))
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[c] = fmt.Sprintf("%v", v)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// indexRow is one row of PRAGMA index_list, minus `seq` (an ordinal that a
// table rebuild is free to reassign even when the same indexes exist).
type indexRow struct {
	Name    string
	Unique  int
	Origin  string
	Partial int
}

// indexList returns table's indexes (explicit and autoindexes alike, e.g. the
// implicit index behind a TEXT PRIMARY KEY or a UNIQUE column), sorted by
// name for stable comparison across a rebuild.
func indexList(t *testing.T, raw *sql.DB, table string) []indexRow {
	t.Helper()
	rows, err := raw.Query(fmt.Sprintf("PRAGMA index_list(%s)", table))
	if err != nil {
		t.Fatalf("index_list(%s): %v", table, err)
	}
	defer rows.Close()
	var out []indexRow
	for rows.Next() {
		var seq int
		var r indexRow
		if err := rows.Scan(&seq, &r.Name, &r.Unique, &r.Origin, &r.Partial); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// fkRow is one row of PRAGMA foreign_key_list, minus `id`/`seq` (ordinals a
// rebuild is free to reassign even when the same foreign keys exist).
type fkRow struct {
	RefTable string
	From     string
	To       string
}

// foreignKeyList returns table's foreign keys, sorted for stable comparison
// across a rebuild.
func foreignKeyList(t *testing.T, raw *sql.DB, table string) []fkRow {
	t.Helper()
	rows, err := raw.Query(fmt.Sprintf("PRAGMA foreign_key_list(%s)", table))
	if err != nil {
		t.Fatalf("foreign_key_list(%s): %v", table, err)
	}
	defer rows.Close()
	var out []fkRow
	for rows.Next() {
		var id, seq int
		var onUpdate, onDelete, match string
		var r fkRow
		if err := rows.Scan(&id, &seq, &r.RefTable, &r.From, &r.To, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].RefTable < out[j].RefTable
	})
	return out
}

// foreignKeyCheckViolations returns the number of rows PRAGMA
// foreign_key_check reports for the whole database (0 means every foreign
// key actually resolves, regardless of whether enforcement is on).
func foreignKeyCheckViolations(t *testing.T, raw *sql.DB) int {
	t.Helper()
	rows, err := raw.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n
}
