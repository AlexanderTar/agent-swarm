package runtime

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// rootDoneSummary is the daemon-written completed checkpoint's summary; %s is
// the root key (docs/specs/2026-09-26-root-finish-and-cancel-cascade.md).
const rootDoneSummary = "%s is done; Swarm finished this agent."

// errRootAcceptedHandoff is WriteCheckpoint's handoff refusal on a done root.
const errRootAcceptedHandoff = "Root accepted; write completed."

// rootIsDone reports whether the top-level item rootID is Done. q is s.DB or a tx.
func (s *Store) rootIsDone(ctx context.Context, q txQuerier, rootID string) (bool, error) {
	var st string
	err := q.QueryRowContext(ctx, `SELECT status FROM items WHERE id = ?`, rootID).Scan(&st)
	return st == string(items.Done), err
}

// OnRootDone is items.Store.RootDone (spec locked decision 1): rootID just
// reached Done, so every agent still queued or active on it finishes. A
// spawning/running session gets a daemon-written completed checkpoint on its
// own item and attempt; resolveAlive kills the pane killCompletedAfter later
// and resolveDeadInner ends it completed, which leaves the orchestrator a
// minute to read approval_result and post its final message.
func (s *Store) OnRootDone(ctx context.Context, tx *sql.Tx, rootID string) error {
	var rootKey, rootType string
	if err := tx.QueryRowContext(ctx, `SELECT key, type FROM items WHERE id = ?`, rootID).Scan(&rootKey, &rootType); err != nil {
		return err
	}
	_ = rootType // the spike guard (1.5) reads it
	type agentRow struct {
		id, name, itemID, sesID string
		state                   SessionState
		attempt                 int
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.name, a.item_id, COALESCE(ses.id, ''),
		COALESCE(ses.state, ''), COALESCE(ses.attempt, 0)
		FROM agents a LEFT JOIN sessions ses ON ses.id = (SELECT id FROM sessions
			WHERE agent_id = a.id ORDER BY generation DESC, attempt DESC LIMIT 1)
		WHERE a.root_item_id = ? AND a.state IN ('queued', 'active')`, rootID)
	if err != nil {
		return err
	}
	var todo []agentRow
	for rows.Next() {
		var r agentRow
		var st string
		if err := rows.Scan(&r.id, &r.name, &r.itemID, &r.sesID, &st, &r.attempt); err != nil {
			rows.Close()
			return err
		}
		r.state = SessionState(st)
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	now := db.Millis(s.now())
	for _, r := range todo {
		if r.state.Live() {
			if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind,
				attempt, summary, daemon_written, created_at) VALUES (?, ?, ?, ?, 'completed', ?, ?, 1, ?)`,
				ids.New("ckp"), r.sesID, r.id, r.itemID, r.attempt, fmt.Sprintf(rootDoneSummary, rootKey), now); err != nil {
				return err
			}
		}
	}
	return nil
}
