package attachments

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/db"
	"github.com/AlexanderTar/swarmrun/internal/db/dbtest"
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

// insertChild writes a child item whose root_id points at rootKey's item.
func insertChild(t *testing.T, d *db.DB, key, rootKey, status string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := d.DB.Exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES (?, ?, 'task', ?, ?, ?, ?, ?)`, "item-"+key, key, "item-"+rootKey, key, status, now, now); err != nil {
		t.Fatal(err)
	}
}

// A Done task under an in-progress root keeps its dir: Sweep keys on the root.
func TestSweepKeepsClosedTaskUnderOpenRoot(t *testing.T) {
	d := dbtest.Open(t)
	home := t.TempDir()
	insertItem(t, d, "BUG-1", "in_progress")
	insertChild(t, d, "TASK-1", "BUG-1", "done")
	insertChild(t, d, "TASK-2", "BUG-1", "cancelled")
	for _, k := range []string{"TASK-1", "TASK-2"} {
		if err := os.MkdirAll(Dir(home, k), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := Sweep(context.Background(), d.DB, home)
	if err != nil || len(removed) != 0 {
		t.Fatalf("Sweep = %v, %v, want nothing removed", removed, err)
	}
}

// Children of a closed root are swept, whatever the child's own status.
func TestSweepRemovesChildOfClosedRoot(t *testing.T) {
	d := dbtest.Open(t)
	home := t.TempDir()
	insertItem(t, d, "BUG-1", "done")
	insertChild(t, d, "TASK-1", "BUG-1", "in_progress")
	if err := os.MkdirAll(Dir(home, "TASK-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	removed, err := Sweep(context.Background(), d.DB, home)
	if err != nil || !slices.Equal(removed, []string{"TASK-1"}) {
		t.Fatalf("Sweep = %v, %v", removed, err)
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

func TestSweepLoopLogsEachRemovedKey(t *testing.T) {
	d := dbtest.Open(t)
	home := t.TempDir()
	insertItem(t, d, "SPIKE-1", "done")
	for _, k := range []string{"SPIKE-1", "SPIKE-9"} {
		if err := os.MkdirAll(Dir(home, k), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var lines []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		SweepLoop(ctx, d.DB, home, time.Hour, func(format string, args ...any) {
			lines = append(lines, fmt.Sprintf(format, args...))
			if len(lines) == 2 {
				cancel()
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SweepLoop did not log both removals")
	}
	got := strings.Join(lines, "\n")
	for _, k := range []string{"SPIKE-1", "SPIKE-9"} {
		if !strings.Contains(got, k) {
			t.Fatalf("log lines %q do not mention %s", lines, k)
		}
	}
}
