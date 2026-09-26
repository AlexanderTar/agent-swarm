package install

import (
	"context"
	"encoding/json"
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

// claudeJSONWritable reports whether ClaudeJSONPath(c) can be written to: an
// existing file must itself be a writable regular file; a missing one only
// needs its parent directory to be writable, since D1's atomic write creates
// it (temp file + rename) rather than requiring it to pre-exist.
func claudeJSONWritable(c Config) bool {
	path := ClaudeJSONPath(c)
	if fi, err := os.Stat(path); err == nil {
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
	f, err := os.CreateTemp(filepath.Dir(path), ".swarm-writable-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// swarmOwnedClaudeWorkspace is internal/runtime's swarmOwnedWorkspace,
// duplicated here rather than imported: internal/adapter already imports
// internal/install, and internal/runtime imports internal/adapter, so
// install importing runtime would cycle. Keep both in sync by hand; the
// predicate is five lines and covered by its own test on each side.
func swarmOwnedClaudeWorkspace(home, path string) bool {
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

// PruneStaleClaudeTrustEntries removes every projects[...] entry in
// ~/.claude.json that is Swarm-owned (swarmOwnedClaudeWorkspace) and whose
// directory no longer exists on disk (D3, dialog-needs-you spec). The
// user's own entries, and any Swarm-owned entry whose workspace still
// exists, are never touched. A missing or unparsable file is left alone
// (0, nil): there is nothing to prune, and a file this codebase cannot
// safely parse is never rewritten.
func PruneStaleClaudeTrustEntries(c Config) (int, error) {
	path := ClaudeJSONPath(c)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return 0, nil // not ours to fix; leave the user's file exactly as it is
	}
	var projects map[string]json.RawMessage
	if len(doc["projects"]) > 0 {
		if err := json.Unmarshal(doc["projects"], &projects); err != nil {
			return 0, nil
		}
	}
	removed := 0
	for p := range projects {
		if !swarmOwnedClaudeWorkspace(c.Home, p) {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			continue // still exists; not stale
		}
		delete(projects, p)
		removed++
	}
	if removed == 0 {
		return 0, nil
	}
	pb, err := json.Marshal(projects)
	if err != nil {
		return 0, err
	}
	doc["projects"] = pb
	out, err := json.Marshal(doc)
	if err != nil {
		return 0, err
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if _, err := WriteIfChanged(path, out, mode); err != nil {
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
func CheckAndPruneClaudeTrust(ctx context.Context, c Config, run execx.Runner) []string {
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
	if n, err := PruneStaleClaudeTrustEntries(c); err == nil && n > 0 {
		lines = append(lines, fmt.Sprintf("Removed %d stale Swarm-owned entries from ~/.claude.json.", n))
	}
	return lines
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
// claudeJSONWritable/claudeVersionAtLeast/swarmOwnedClaudeWorkspace with
// CheckAndPruneClaudeTrust so the FAIL/WARN conditions and the prune
// candidates are never computed two different ways.
//
// NOTE (scope, flagged for review rather than picked silently): D4 also
// calls for a WARN when a live Claude session's workspace has no trust
// entry at all. Doctor is a standalone CLI command with no DB handle today
// (cmd/swarm/commands.go builds it from Config + execx.Runner only, no
// Store) -- adding that WARN needs either a new daemon HTTP endpoint or a
// direct read-only DB open here, and either is a real decision this task
// does not make unilaterally. That one WARN is not implemented; everything
// else in D4 is.
func CheckClaudeTrust(ctx context.Context, c Config, run execx.Runner) Check {
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
	stale, owned, err := claudeTrustEntryCounts(c)
	if err != nil {
		return Check{"Claude trust", false, err.Error()}
	}
	if stale > 0 {
		return Check{"Claude trust", true,
			fmt.Sprintf("%d stale Swarm-owned entries in ~/.claude.json. Run swarm install to prune them.", stale)}
	}
	return Check{"Claude trust", true,
		fmt.Sprintf("~/.claude.json is writable, Claude %s, %d Swarm-owned entries.", version, owned)}
}

// claudeTrustEntryCounts scans ~/.claude.json once for both D4 numbers:
// how many Swarm-owned entries exist in total, and how many of those are
// stale (their workspace no longer exists on disk).
func claudeTrustEntryCounts(c Config) (stale, owned int, err error) {
	raw, err := os.ReadFile(ClaudeJSONPath(c))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	var doc struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return 0, 0, nil // not doctor's place to fail on a file it can't parse
	}
	for p := range doc.Projects {
		if !swarmOwnedClaudeWorkspace(c.Home, p) {
			continue
		}
		owned++
		if _, err := os.Stat(p); err != nil {
			stale++
		}
	}
	return stale, owned, nil
}
