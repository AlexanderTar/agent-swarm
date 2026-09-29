package runtime

import (
	"context"
	"os"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// ReclaimWorkDirs removes <home>/work/<name> for every agent that is finished
// (or acknowledged) past reclaimGrace with no live session. Retry recreates
// the dir (agents.go MkdirAll) and every adapter rewrites its launch files
// into it, so nothing durable lives there. Dirs with no agent row (orphans)
// go once their mtime is past the same grace. Entries that are not real
// directories (symlinks included) are never touched.
func (s *Store) ReclaimWorkDirs(ctx context.Context, opt CleanupOptions) ([]CleanupResult, error) {
	root := filepath.Join(s.Home, "work")
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
