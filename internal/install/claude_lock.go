package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// ErrClaudeConfigBusy is WithClaudeConfigLock's answer when the lock stayed
// held for the whole ClaudeConfigLockTimeout window: fn did not run. Callers
// log it and move on (D1: a pre-trust failure never blocks a spawn) or, for
// D2's reconcile pass, stop and retry next tick without marking anything
// done. Review round 3, item 3: this used to be a silent nil, which let
// reconcile mark a session "forgotten" when nothing had been written.
var ErrClaudeConfigBusy = errors.New("claude.json lock is busy; skipped the write")

// ErrClaudeConfigMissing: ~/.claude.json does not exist. Swarm never creates
// it (that would hide Claude's own missing-config/backup-restore path).
var ErrClaudeConfigMissing = errors.New("claude.json does not exist; not creating it")

// WithClaudeConfigLock runs fn while holding Claude's own mkdir lock around
// ~/.claude.json (path = userHome/.claude.json). It is best-effort: a lock
// that stays held for the whole timeout window is treated as busy, fn is
// skipped, and ErrClaudeConfigBusy is returned -- the caller logs and moves
// on rather than blocking (D1: a pre-trust failure never blocks a spawn; the
// dialog auto-answer and the Needs-you escalation are the fallback).
//
// Lock path: always the UNRESOLVED <userHome>/.claude.json.lock, even when
// ~/.claude.json is a symlink. Verified in the Claude 2.1.283 binary:
// saveConfigWithLock calls proper-lockfile as
// lock(file, {lockfilePath: `${file}.lock`, ...}) on the global config path
// as given. An explicit lockfilePath bypasses proper-lockfile's default
// realpath resolution of the lock name, so Claude never locks
// <symlink target>.lock.
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
			return ErrClaudeConfigBusy // busy for the whole window; skip the write
		}
		time.Sleep(ClaudeConfigLockRetryEvery)
	}
}

// WriteFileAtomic writes data to a temp file in path's folder, fsyncs it and
// renames it over path, keeping path's existing mode when it exists (mode is
// the default for a new file). It is the one atomic writer shared by the
// adapters' per-launch config files and every ~/.claude.json writer
// (review round 3, item 2: it replaces the adapter's own copy and
// WriteIfChanged's non-synced ".swarm-tmp" path for this file). path must
// already be resolved: rename replaces a symlink at path with a regular file.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// EditClaudeProjects is the one read-modify-write of ~/.claude.json's
// "projects" map, shared by D1's pre-trust, D2's forget and D3's prune.
// Under WithClaudeConfigLock it resolves a symlinked file to its target,
// reads it, hands edit the decoded projects map (never nil), and writes the
// result back to the target only when edit reports a change and the bytes
// did not move underneath it (re-read-compare, up to 3 attempts). Every
// other top-level key and every untouched entry keeps its JSON value, but
// the file is re-serialized (sorted keys, compact, HTML-escaped).
// ponytail: whole-file re-marshal; splice the projects bytes into the
// original buffer if a dotfiles-tracked ~/.claude.json needs stable diffs.
//
// It refuses, returning an error and writing nothing, when the file is
// missing (ErrClaudeConfigMissing), 0-byte or whitespace-only, the literal
// `null`, not a JSON object, or its projects value does not parse. Review
// round 3, item 1: an empty or `null` file used to decode to a nil map and
// be rewritten as {"projects":...}, wiping oauthAccount, mcpServers and
// everything else Claude keeps there.
func EditClaudeProjects(userHome string, edit func(projects map[string]json.RawMessage) (bool, error)) error {
	return WithClaudeConfigLock(userHome, func() error {
		path := ClaudeJSONPath(Config{UserHome: userHome})
		for attempt := 0; attempt < 3; attempt++ {
			target, err := filepath.EvalSymlinks(path)
			if errors.Is(err, os.ErrNotExist) {
				return ErrClaudeConfigMissing
			}
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(target)
			if errors.Is(err, os.ErrNotExist) {
				return ErrClaudeConfigMissing
			}
			if err != nil {
				return err
			}
			if len(bytes.TrimSpace(raw)) == 0 {
				return fmt.Errorf("claude.json is empty; not rewriting it")
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(raw, &doc); err != nil {
				return fmt.Errorf("claude.json does not parse: %w", err)
			}
			if doc == nil {
				return fmt.Errorf("claude.json is null; not rewriting it")
			}
			var projects map[string]json.RawMessage
			if p := strings.TrimSpace(string(doc["projects"])); p != "" && p != "null" {
				if err := json.Unmarshal(doc["projects"], &projects); err != nil {
					return fmt.Errorf("claude.json projects does not parse: %w", err)
				}
			}
			if projects == nil {
				projects = map[string]json.RawMessage{}
			}
			changed, err := edit(projects)
			if err != nil || !changed {
				return err
			}
			pb, err := json.Marshal(projects)
			if err != nil {
				return err
			}
			doc["projects"] = pb
			out, err := json.Marshal(doc)
			if err != nil {
				return err
			}
			if cur, _ := os.ReadFile(target); !bytes.Equal(cur, raw) {
				continue // the file changed under us; re-read and redo the edit
			}
			return WriteFileAtomic(target, out, 0o600)
		}
		return fmt.Errorf("claude.json kept changing under us")
	})
}
