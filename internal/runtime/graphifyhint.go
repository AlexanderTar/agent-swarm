package runtime

import (
	"context"
	"os"
	"path/filepath"
)

// GraphifyHintFor is the SessionStart line for the agent's active worktrees
// ("" when there is nothing to say). Callers log a lookup error and skip the
// hint: it never blocks a hook or a launch.
func (s *Store) GraphifyHintFor(ctx context.Context, agentID string) (string, error) {
	if s == nil || s.Worktree == nil || agentID == "" {
		return "", nil
	}
	wts, err := s.Worktree.ForAgent(ctx, agentID)
	if err != nil {
		return "", err
	}
	var paths []string
	var ready []bool
	for _, wt := range wts {
		if wt.State != "active" {
			continue
		}
		paths = append(paths, wt.Path)
		_, statErr := os.Stat(filepath.Join(wt.Path, "graphify-out", "graph.json"))
		ready = append(ready, statErr == nil)
	}
	return GraphifyHint(paths, ready), nil
}
