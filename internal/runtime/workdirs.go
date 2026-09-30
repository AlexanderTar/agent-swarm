package runtime

import (
	"context"
	"os"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// ReclaimWorkDirs removes <home>/work/<dir> once nothing live refers to it.
// A dir is referenced by any session whose cwd is the dir (or inside it) and
// by any agent named like the dir; agents are renamed after launch, so the
// session cwd, not the agent name, is the authoritative link. A reference
// blocks removal while its session is live or its agent is not finished (or
// acknowledged) past reclaimGrace. Retry recreates the dir (agents.go
// MkdirAll) and every adapter rewrites its launch files into it, so nothing
// durable lives there. A dir with no reference at all (an orphan) goes once
// its own mtime -- the last time an entry was added or removed directly in
// it, not the newest file inside -- is past the same grace. <home>/work must
// itself be a real directory and entries that are not real directories
// (symlinks included) are never touched.
func (s *Store) ReclaimWorkDirs(ctx context.Context, opt CleanupOptions) ([]CleanupResult, error) {
	root := filepath.Join(s.Home, "work")
	if fi, err := os.Lstat(root); err == nil && !fi.IsDir() {
		s.logf("workdir: %s is not a real directory; skipping", root)
		return []CleanupResult{{Path: root, Action: "kept", Reason: "work root is not a real directory"}}, nil
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cutoff := db.Millis(s.Now().Add(-reclaimGrace))
	if opt.NoGrace {
		cutoff = db.Millis(s.Now())
	}
	var results []CleanupResult
	for _, e := range entries {
		if ctx.Err() != nil {
			return results, ctx.Err()
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
		var agents, blocking int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(NOT (a.state IN ('finished', 'acknowledged')
				AND a.finished_at IS NOT NULL AND a.finished_at <= ?)
			 OR EXISTS (SELECT 1 FROM sessions x WHERE x.agent_id = a.id
				AND x.state IN ('spawning','running','pause_requested','quiescing','stopping'))), 0)
			FROM agents a WHERE a.name = ?`, cutoff, e.Name()).Scan(&agents, &blocking); err != nil {
			keep("error: " + err.Error())
			continue
		}
		var cwdRefs, cwdBlocking int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(x.state IN ('spawning','running','pause_requested','quiescing','stopping')
			 OR NOT (a.state IN ('finished', 'acknowledged')
				AND a.finished_at IS NOT NULL AND a.finished_at <= ?)), 0)
			FROM sessions x JOIN agents a ON a.id = x.agent_id
			WHERE x.cwd = ? OR substr(x.cwd, 1, length(?) + 1) = ? || '/'`,
			cutoff, path, path, path).Scan(&cwdRefs, &cwdBlocking); err != nil {
			keep("error: " + err.Error())
			continue
		}
		agents += cwdRefs
		blocking += cwdBlocking
		if agents == 0 {
			// Orphan: judge by the dir's own mtime against the same grace.
			if fi.ModTime().UnixMilli() > cutoff {
				keep("orphan within grace")
				continue
			}
		} else if blocking > 0 {
			keep("agent live or within grace")
			continue
		}
		if opt.DryRun {
			results = append(results, CleanupResult{Path: path, Action: "would_remove"})
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			s.logf("workdir: remove %s failed: %v", path, err)
			keep("error: " + err.Error())
			continue
		}
		s.logf("workdir: removed %s", path)
		results = append(results, CleanupResult{Path: path, Action: "removed"})
	}
	return results, nil
}
