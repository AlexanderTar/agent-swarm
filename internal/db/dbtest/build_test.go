package dbtest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// A fresh TMPDIR forces buildTemplate down its build path rather than the
// shared template other packages already left in os.TempDir.
func TestBuildTemplateBuildsAMigratedFileThenReusesIt(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	p, err := buildTemplate()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(p)
	if filepath.Base(dir) != "swarm-dbtest-"+db.MigrationsHash() {
		t.Fatalf("template dir = %s", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "template.db" {
		t.Fatalf("template dir holds %v, want only template.db", entries)
	}
	d, err := db.Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := d.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if v != db.SchemaVersion {
		t.Fatalf("user_version = %d, want %d", v, db.SchemaVersion)
	}
	again, err := buildTemplate()
	if err != nil || again != p {
		t.Fatalf("second build = %q, %v; want the existing %q", again, err, p)
	}
}

func TestBuildTemplateFailsWhenTheTempDirIsAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", f)
	if _, err := buildTemplate(); err == nil {
		t.Fatal("buildTemplate succeeded under a TMPDIR that is a regular file")
	}
}

func TestCopyFileFailsOnAMissingSource(t *testing.T) {
	dir := t.TempDir()
	if err := copyFile(filepath.Join(dir, "missing.db"), filepath.Join(dir, "out.db")); err == nil {
		t.Fatal("copyFile succeeded with no source file")
	}
	if _, err := os.Stat(filepath.Join(dir, "out.db")); !os.IsNotExist(err) {
		t.Fatalf("copyFile left a destination behind: %v", err)
	}
}
