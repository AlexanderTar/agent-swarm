package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// ClaudeTrustMinVersion is the Claude version D1's per-session
// ~/.claude.json write (dialog-needs-you spec) was verified live against
// (P-C5, Task 14a). An older, or undetectable, installed version cannot be
// promised to use the same key or write path.
const ClaudeTrustMinVersion = "2.1.283"

// claudeVersionAtLeast reports whether v (e.g. "2.1.283", "2.1.283 (Claude Code)")
// is at least min, comparing major.minor.patch numerically -- a plain string
// compare is wrong the moment any component reaches two digits ("9" < "10"
// as strings, backwards as versions).
func claudeVersionAtLeast(v, min string) bool {
	vf := strings.Fields(v)
	if len(vf) == 0 {
		return false
	}
	a, aok := parseVersion(vf[0])
	b, bok := parseVersion(min)
	if !aok || !bok {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ClaudeJSONPath is the one file D1 writes into and D2/D3/D4 inspect.
func ClaudeJSONPath(c Config) string { return filepath.Join(c.UserHome, ".claude.json") }

// claudeJSONWritable reports whether ClaudeJSONPath(c) exists and can be
// written to: it must be a writable regular file.
//
// Review round 2, finding 3: this used to treat a missing file as writable
// whenever its parent dir was, on the theory that D1's write would create
// it. D1's own pre-trust write (adapter.trustClaudeWorkspace) no longer
// creates a missing ~/.claude.json -- that hides Claude's own
// missing-config/backup-restore path -- so a missing file must FAIL this
// check too, per D4's "doesn't exist or isn't writable" and D3's "missing"
// install warning.
func claudeJSONWritable(c Config) bool {
	path := ClaudeJSONPath(c)
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	if fi.IsDir() {
		return false
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// SwarmOwnedClaudeWorkspace is internal/runtime's swarmOwnedWorkspace,
// duplicated here rather than imported: internal/adapter already imports
// internal/install, and internal/runtime imports internal/adapter, so
// install importing runtime would cycle. Keep both in sync by hand; the
// predicate is five lines and covered by its own test on each side.
//
// Unlike the runtime copy it also accepts a path under home's realpath:
// D1 writes the realpath twin of every cwd, and on a symlinked home (macOS
// /tmp -> /private/tmp, say) that twin is under realpath(home), not home.
// Review round 3, item 3: ForgetFolder's twin key must pass this too.
func SwarmOwnedClaudeWorkspace(home, path string) bool {
	if swarmOwnedUnder(home, path) {
		return true
	}
	if real, err := filepath.EvalSymlinks(home); err == nil && real != home {
		return swarmOwnedUnder(real, path)
	}
	return false
}

func swarmOwnedUnder(home, path string) bool {
	if home == "" || path == "" {
		return false
	}
	path = filepath.Clean(path)
	for _, root := range []string{"work", "worktrees"} {
		prefix := filepath.Clean(filepath.Join(home, root)) + string(filepath.Separator)
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// ClaudeSessions is what the daemon reports about Claude sessions' work
// dirs, for D3's prune and D4's live-session check (dialog-needs-you spec).
type ClaudeSessions struct {
	// Live: running or spawning sessions of active Claude agents.
	Live []LiveClaudeSession
	// Finished: cwds of finished or acknowledged Claude agents' sessions,
	// minus any cwd a not-yet-finished agent of any kind still uses.
	Finished []string
}

type LiveClaudeSession struct{ Agent, Cwd string }

// ClaudeSessionsFunc asks the running daemon for ClaudeSessions
// (cmd/swarm's daemonClaudeSessions, over the authenticated CLI client). An
// error means the daemon is offline: callers skip that part with a one-line
// note. A nil func skips it silently.
type ClaudeSessionsFunc func(context.Context) (ClaudeSessions, error)

// withRealpaths returns paths plus each one's realpath where it differs:
// D1 trusts both keys, so both must be matched.
func withRealpaths(paths []string) map[string]bool {
	out := map[string]bool{}
	for _, p := range paths {
		out[p] = true
		if real, err := filepath.EvalSymlinks(p); err == nil {
			out[real] = true
		}
	}
	return out
}

// staleClaudeEntry is D3's predicate, shared with D4's count: a
// Swarm-owned entry whose dir is gone (ENOENT only), or that belongs to a
// finished session's work dir.
func staleClaudeEntry(c Config, p string, finished map[string]bool) bool {
	return SwarmOwnedClaudeWorkspace(c.Home, p) && (finished[p] || isGone(p))
}

// PruneStaleClaudeTrustEntries removes every projects[...] entry in
// ~/.claude.json that staleClaudeEntry matches (D3, dialog-needs-you spec):
// Swarm-owned, and either gone from disk or one of finished (a finished
// session's work dir, as the daemon reported it; realpath twins included).
// The user's own entries are never touched. A missing file is (0, nil):
// nothing to prune. Every other refusal (empty, `null`, unparsable, lock
// busy) is returned as an error for the caller to print; the file is never
// rewritten then.
//
// Runs through EditClaudeProjects: Claude's own lock, symlink-safe,
// re-read-compare before the atomic write (review rounds 2 and 3).
func PruneStaleClaudeTrustEntries(c Config, finished []string) (int, error) {
	done := withRealpaths(finished)
	removed := 0
	err := EditClaudeProjects(c.UserHome, func(projects map[string]json.RawMessage) (bool, error) {
		removed = 0
		for p := range projects {
			if staleClaudeEntry(c, p, done) {
				delete(projects, p)
				removed++
			}
		}
		return removed > 0, nil
	})
	if errors.Is(err, ErrClaudeConfigMissing) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// CheckAndPruneClaudeTrust is D3's install-time step: verify ~/.claude.json is
// writable and the installed Claude version is one D1's trust key was
// verified on, then prune stale Swarm-owned entries. It never fails the
// install (D1's per-session write already has its own fallback, the dialog
// auto-answer and the Needs-you row); it returns the exact lines to print,
// per the spec's "Install copy": nothing when there is nothing to report.
func CheckAndPruneClaudeTrust(ctx context.Context, c Config, run execx.Runner, sessions ClaudeSessionsFunc) []string {
	var lines []string
	if !claudeJSONWritable(c) {
		lines = append(lines, "~/.claude.json is missing or not writable: Claude sessions will hit the trust dialog. Fix its permissions, then run swarm install again.")
	} else {
		version, ok := claudeInstalledVersion(ctx, run)
		if !ok || !claudeVersionAtLeast(version, ClaudeTrustMinVersion) {
			v := version
			if v == "" {
				v = "unknown"
			}
			lines = append(lines, fmt.Sprintf("Claude %s is older than %s: the trust-dialog key was verified on %s+. Continuing, but sessions may still hit the trust dialog.",
				v, ClaudeTrustMinVersion, ClaudeTrustMinVersion))
		}
	}
	var finished []string
	if sessions != nil {
		if got, err := sessions(ctx); err != nil {
			lines = append(lines, "Skipped pruning finished sessions' Claude trust entries: the daemon isn't running.")
		} else {
			finished = got.Finished
		}
	}
	n, err := PruneStaleClaudeTrustEntries(c, finished)
	switch {
	case errors.Is(err, ErrClaudeConfigBusy):
		lines = append(lines, "Skipped pruning ~/.claude.json: another process holds its lock. Run swarm install again.")
	case err != nil:
		lines = append(lines, fmt.Sprintf("Couldn't prune ~/.claude.json: %v", err))
	case n > 0:
		lines = append(lines, fmt.Sprintf("Removed %d stale Swarm-owned entries from ~/.claude.json.", n))
	}
	return lines
}

// isGone reports whether p definitely no longer exists: only ENOENT counts.
// Any other stat error (EACCES, EIO, ...) means "can't tell", and an entry
// that might still be live is never pruned or counted stale over it.
func isGone(p string) bool {
	_, err := os.Stat(p)
	return errors.Is(err, os.ErrNotExist)
}

func claudeInstalledVersion(ctx context.Context, run execx.Runner) (string, bool) {
	out, err := run(ctx, "claude", "--version")
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

// CheckClaudeTrust is D4's "Claude trust" doctor check. It shares
// claudeJSONWritable/claudeVersionAtLeast/staleClaudeEntry with
// CheckAndPruneClaudeTrust so the FAIL/WARN conditions and the prune
// candidates are never computed two different ways. sessions (the daemon's
// view, D4's live-session WARN) may be nil; when it errors, the check still
// runs and its detail ends with a skip note.
func CheckClaudeTrust(ctx context.Context, c Config, run execx.Runner, sessions ClaudeSessionsFunc) Check {
	if !claudeJSONWritable(c) {
		return Check{"Claude trust", false,
			"~/.claude.json is missing or not writable. Claude sessions will hit the trust dialog. Run swarm install."}
	}
	version, ok := claudeInstalledVersion(ctx, run)
	if !ok {
		return Check{"Claude trust", false,
			"Couldn't determine the installed Claude version. Update Claude, then run swarm doctor again."}
	}
	if !claudeVersionAtLeast(version, ClaudeTrustMinVersion) {
		return Check{"Claude trust", false,
			fmt.Sprintf("Claude %s is older than %s, where the trust-dialog key was verified. Update Claude, then run swarm doctor again.",
				version, ClaudeTrustMinVersion)}
	}
	var got ClaudeSessions
	note := ""
	if sessions != nil {
		var err error
		if got, err = sessions(ctx); err != nil {
			note = " Live-session check skipped: the daemon isn't running."
		}
	}
	projects, err := readClaudeProjects(c)
	if err != nil {
		return Check{"Claude trust", false, err.Error()}
	}
	finished := withRealpaths(got.Finished)
	stale, owned := 0, 0
	for p := range projects {
		if SwarmOwnedClaudeWorkspace(c.Home, p) {
			owned++
		}
		if staleClaudeEntry(c, p, finished) {
			stale++
		}
	}
	var warns []string
	if stale > 0 {
		warns = append(warns, fmt.Sprintf("%d stale Swarm-owned entries in ~/.claude.json. Run swarm install to prune them.", stale))
	}
	for _, l := range got.Live {
		trusted := false
		for k := range withRealpaths([]string{l.Cwd}) {
			if _, ok := projects[k]; ok {
				trusted = true
			}
		}
		if !trusted {
			warns = append(warns, l.Agent+" is running but its workspace isn't trusted in ~/.claude.json yet.")
		}
	}
	if len(warns) > 0 {
		return Check{"Claude trust", true, strings.Join(warns, " ") + note}
	}
	return Check{"Claude trust", true,
		fmt.Sprintf("~/.claude.json is writable, Claude %s, %d Swarm-owned entries.", version, owned) + note}
}

// readClaudeProjects reads ~/.claude.json's projects map for doctor. A
// missing or unparsable file is an empty map: doctor never fails on a file
// it can't parse (the writability check above already covers "missing").
func readClaudeProjects(c Config) (map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(ClaudeJSONPath(c))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	_ = json.Unmarshal(raw, &doc)
	return doc.Projects, nil
}
