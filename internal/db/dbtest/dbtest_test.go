package dbtest

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

func TestOpenReturnsIsolatedMigratedDBs(t *testing.T) {
	a, b := Open(t), Open(t)
	for _, d := range []*db.DB{a, b} {
		var v int
		if err := d.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v != db.SchemaVersion {
			t.Fatalf("user_version = %d, want %d", v, db.SchemaVersion)
		}
	}
	if _, err := a.ExecContext(context.Background(), `INSERT INTO settings(key, value_json, updated_at) VALUES ('x','1',1)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.QueryRow(`SELECT count(*) FROM settings WHERE key = 'x'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second DB saw %d rows from the first", n)
	}
}

func TestTemplateIsBuiltOnceAndKeyedByMigrationHash(t *testing.T) {
	p1 := templatePath(t)
	fi1, err := os.Stat(p1)
	if err != nil {
		t.Fatal(err)
	}
	p2 := templatePath(t)
	fi2, err := os.Stat(p2)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 || !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatalf("template rebuilt: %s vs %s", p1, p2)
	}
	if !strings.Contains(p1, db.MigrationsHash()) {
		t.Fatalf("template path %q is not keyed by migration hash %q", p1, db.MigrationsHash())
	}
	for _, side := range []string{"-wal", "-shm"} {
		if fi, err := os.Stat(p1 + side); err == nil && fi.Size() > 0 {
			t.Fatalf("template has non-empty %s sidecar", side)
		}
	}
}
