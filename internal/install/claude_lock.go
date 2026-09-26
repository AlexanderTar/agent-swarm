package install

import (
	"errors"
	"os"
	"time"
)

// ClaudeConfigLockStaleAfter/ClaudeConfigLockRetryEvery/ClaudeConfigLockTimeout
// are D1's mkdir-lock protocol (dialog-needs-you spec §E, confirmed live as
// P-C5, Task 14a): Claude itself takes this same "<file>.lock" directory
// lock around its own writes to ~/.claude.json, held only for the instant of
// the write.
//
// Lives in internal/install (moved from internal/adapter in review round 2,
// finding 2) so both the adapter's per-session writes and
// PruneStaleClaudeTrustEntries's install-time prune take the same lock
// around ~/.claude.json, rather than the prune racing Claude's own writes
// unlocked.
const (
	ClaudeConfigLockStaleAfter = 10 * time.Second
	ClaudeConfigLockRetryEvery = 100 * time.Millisecond
	ClaudeConfigLockTimeout    = 2 * time.Second
)

// WithClaudeConfigLock runs fn while holding Claude's own mkdir lock around
// ~/.claude.json (path = userHome/.claude.json). It is best-effort: a lock
// that stays held for the whole timeout window is treated as busy, fn is
// skipped, and nil is returned -- the caller logs and moves on rather than
// blocking or failing (D1: a pre-trust failure never blocks a spawn; the
// dialog auto-answer and the Needs-you escalation are the fallback).
//
// A stale lock (older than ClaudeConfigLockStaleAfter) is removed exactly
// once per call, per the spec's "remove it once and retry" -- not once per
// loop iteration. Review round 2, finding 1: the old adapter version did
// `_ = os.Remove(lock); continue`, re-entering the loop without checking the
// deadline. When Remove kept failing (a non-empty lock dir, or EACCES) that
// spun the loop at full CPU forever instead of giving up at the deadline.
// Removing once and then always falling through to the deadline/sleep below
// bounds every path -- a stuck removal is just treated as still-busy.
func WithClaudeConfigLock(userHome string, fn func() error) error {
	lock := ClaudeJSONPath(Config{UserHome: userHome}) + ".lock"
	deadline := time.Now().Add(ClaudeConfigLockTimeout)
	staleRemoveAttempted := false
	for {
		err := os.Mkdir(lock, 0o755)
		if err == nil {
			defer os.Remove(lock)
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if !staleRemoveAttempted {
			if fi, statErr := os.Stat(lock); statErr == nil && time.Since(fi.ModTime()) > ClaudeConfigLockStaleAfter {
				_ = os.Remove(lock) // stale; best-effort, exactly once
				staleRemoveAttempted = true
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil // busy for the whole window; skip the write, don't fail
		}
		time.Sleep(ClaudeConfigLockRetryEvery)
	}
}
