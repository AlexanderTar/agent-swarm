package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// scratchSlug is how Claude Code names a project dir under its scratch root:
// the launch cwd with every non-alphanumeric byte replaced by '-'
// (/Users/x/.swarm/work/foo -> -Users-x--swarm-work-foo). It is lossy, so it
// is only ever applied forward, never reversed.
func scratchSlug(cwd string) string {
	b := []byte(cwd)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

func (s *Store) scratchRoot() string {
	if s.ScratchRoot != "" {
		return s.ScratchRoot
	}
	return fmt.Sprintf("/tmp/claude-%d", os.Getuid())
}

// ReclaimScratchpads removes Claude Code's per-agent scratch dirs
// (<scratch root>/<slug>/<session-uuid>/{scratchpad,tasks}, GBs of DerivedData
// each) once the agent that launched in <home>/work/<dir> is gone. The root is
// shared with the user's own Claude sessions, so only entries named like
// slug(<home>/work/) are considered; each is matched forward against the slug
// of every session cwd under <home>/work (never prefix-matched, so foo does
// not claim foo-2). A referenced entry goes when none of its sessions is live
// and every owner is finished past reclaimGrace; an unreferenced one goes once
// its own mtime is past the same grace. Entries that are not real directories
// (symlinks included) are never touched. Takes the same lock as the worktree
// pass so `swarm cleanup` cannot race the daemon.
func (s *Store) ReclaimScratchpads(ctx context.Context, opt CleanupOptions) ([]CleanupResult, error) {
	root := s.scratchRoot()
	if fi, err := os.Lstat(root); err == nil && !fi.IsDir() {
		return []CleanupResult{{Path: root, Action: "kept", Reason: "scratch root is not a real directory"}}, nil
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	unlock, err := lockReclaim(s.Home)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cutoff := db.Millis(s.Now().Add(-reclaimGrace))
	if opt.NoGrace {
		cutoff = db.Millis(s.Now())
	}
	workDir := filepath.Join(s.Home, "work")
	prefix := scratchSlug(workDir + "/")
	type ref struct{ n, blocking int }
	refs := map[string]*ref{}
	rows, err := s.DB.QueryContext(ctx, `SELECT x.cwd,
		x.state IN ('spawning','running','pause_requested','quiescing','stopping')
		 OR NOT (a.state IN ('finished', 'acknowledged')
			AND a.finished_at IS NOT NULL AND a.finished_at <= ?)
		FROM sessions x JOIN agents a ON a.id = x.agent_id
		WHERE substr(x.cwd, 1, length(?) + 1) = ? || '/'`, cutoff, workDir, workDir)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cwd string
		var blocks bool
		if err := rows.Scan(&cwd, &blocks); err != nil {
			rows.Close()
			return nil, err
		}
		r := refs[scratchSlug(cwd)]
		if r == nil {
			r = &ref{}
			refs[scratchSlug(cwd)] = r
		}
		r.n++
		if blocks {
			r.blocking++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var results []CleanupResult
	for _, e := range entries {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		path := filepath.Join(root, e.Name())
		keep := func(reason string) {
			results = append(results, CleanupResult{Path: path, Action: "kept", Reason: reason})
		}
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() {
			keep("not a directory")
			continue
		}
		if r := refs[e.Name()]; r == nil {
			if fi.ModTime().UnixMilli() > cutoff {
				keep("orphan within grace")
				continue
			}
		} else if r.blocking > 0 {
			keep("agent live or within grace")
			continue
		}
		if opt.DryRun {
			results = append(results, CleanupResult{Path: path, Action: "would_remove"})
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			s.logf("scratchpad: remove %s failed: %v", path, err)
			keep("error: " + err.Error())
			continue
		}
		s.logf("scratchpad: removed %s", path)
		results = append(results, CleanupResult{Path: path, Action: "removed"})
	}
	return results, nil
}
