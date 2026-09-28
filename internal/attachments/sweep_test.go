package attachments

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
)

// insertItem writes the minimum NOT NULL columns of items, self-referencing
// root_id so the FK is satisfied without a separate parent row.
func insertItem(t *testing.T, d *db.DB, key, status string) {
	t.Helper()
	now := time.Now().UnixMilli()
	id := "item-" + key
	if _, err := d.DB.Exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES (?, ?, 'spike', ?, ?, ?, ?, ?)`, id, key, id, key, status, now, now); err != nil {
		t.Fatal(err)
	}
}

func TestSweepRemovesClosedAndMissingOnly(t *testing.T) {
	d := dbtest.Open(t)
	home := t.TempDir()
	for key, status := range map[string]string{"SPIKE-1": "done", "SPIKE-2": "cancelled", "SPIKE-3": "in_progress"} {
		insertItem(t, d, key, status)
	}
	for _, k := range []string{"SPIKE-1", "SPIKE-2", "SPIKE-3", "SPIKE-9"} {
		if err := os.MkdirAll(Dir(home, k), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := Sweep(context.Background(), d.DB, home)
	if err != nil || !slices.Equal(removed, []string{"SPIKE-1", "SPIKE-2", "SPIKE-9"}) {
		t.Fatalf("Sweep = %v, %v", removed, err)
	}
	if _, err := os.Stat(Dir(home, "SPIKE-3")); err != nil {
		t.Fatal("open item's attachments were removed")
	}
}

func TestSweepNoAttachmentsDir(t *testing.T) {
	d := dbtest.Open(t)
	removed, err := Sweep(context.Background(), d.DB, t.TempDir())
	if err != nil || removed != nil {
		t.Fatalf("Sweep = %v, %v, want nil, nil", removed, err)
	}
}
