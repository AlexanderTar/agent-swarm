package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ResolveWorktreeRef maps a worktree id (wt_...) or the absolute path
// swarm_worktree create returned to the worktree id. An unknown ref gets the
// spec's copy, never a raw sql error.
func (s *Store) ResolveWorktreeRef(ctx context.Context, ref string) (string, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM worktrees WHERE id = ? OR path = ? LIMIT 1`, ref, ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("Unknown worktree %s. Pass the worktree id or path that swarm_worktree create returned.", ref)
	}
	return id, err
}
