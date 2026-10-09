// Package dbtest opens a migrated database in a temp dir for tests.
package dbtest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

var (
	tmplOnce sync.Once
	tmplPath string
	tmplErr  error
)

// Open returns a migrated database in t.TempDir(), closed on cleanup. It
// copies a template migrated once per process (migrations are slow under
// -race) and opens the copy, where migrate is a no-op.
func Open(t testing.TB) *db.DB {
	t.Helper()
	src := templatePath(t)
	dst := filepath.Join(t.TempDir(), "swarm.db")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// templatePath returns a fully migrated database file with no -wal sidecar,
// building it on first use. It lives under os.TempDir keyed by
// db.MigrationsHash, so a migration edit gets a fresh template and test
// processes (one per package) share the same one.
func templatePath(t testing.TB) string {
	t.Helper()
	tmplOnce.Do(func() { tmplPath, tmplErr = buildTemplate() })
	if tmplErr != nil {
		t.Fatal(tmplErr)
	}
	return tmplPath
}

func buildTemplate() (string, error) {
	dir := filepath.Join(os.TempDir(), "swarm-dbtest-"+db.MigrationsHash())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	final := filepath.Join(dir, "template.db")
	if _, err := os.Stat(final); err == nil {
		return final, nil
	}
	// Build in a private dir, then rename: concurrent package processes
	// never see a half-written template.
	work, err := os.MkdirTemp(dir, "build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	built := filepath.Join(work, "template.db")
	d, err := db.Open(context.Background(), built)
	if err != nil {
		return "", err
	}
	// Fold the WAL into the main file so the copy needs no sidecar.
	if _, err := d.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		d.Close()
		return "", err
	}
	if err := d.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(built, final); err != nil {
		return "", err
	}
	return final, nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
