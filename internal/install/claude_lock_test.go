package install_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// Review round 2, finding 1: a stale lock dir that keeps failing to remove
// (here, non-empty -- os.Remove on a non-empty dir always fails) must not
// spin the loop forever. WithClaudeConfigLock must give up at its deadline
// (2s) rather than looping at full CPU without ever checking it.
func TestWithClaudeConfigLockGivesUpWhenAStaleLockCannotBeRemoved(t *testing.T) {
	home := t.TempDir()
	lock := filepath.Join(home, ".claude.json.lock")
	if err := os.Mkdir(lock, 0o755); err != nil {
		t.Fatal(err)
	}
	// Non-empty: os.Remove(lock) will keep failing with ENOTEMPTY.
	if err := os.WriteFile(filepath.Join(lock, "busy"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-20 * time.Second)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- install.WithClaudeConfigLock(home, func() error {
			t.Error("fn must not run: the lock could never actually be acquired")
			return nil
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, install.ErrClaudeConfigBusy) {
			t.Fatalf("WithClaudeConfigLock returned %v, want ErrClaudeConfigBusy (review round 3, item 3)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WithClaudeConfigLock spun past its deadline instead of giving up")
	}
}

// A stale lock that CAN be removed (the ordinary case: an empty dir left
// behind by a crashed writer) is still cleared and the write proceeds.
func TestWithClaudeConfigLockRemovesAnOrdinaryStaleLock(t *testing.T) {
	home := t.TempDir()
	lock := filepath.Join(home, ".claude.json.lock")
	if err := os.Mkdir(lock, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-20 * time.Second)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := install.WithClaudeConfigLock(home, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("fn must run once the stale lock is cleared")
	}
	if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the lock must be removed on the way out")
	}
}
