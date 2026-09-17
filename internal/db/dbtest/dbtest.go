// Package dbtest opens a migrated database in a temp dir for tests.
package dbtest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

func Open(t testing.TB) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
