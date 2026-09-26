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
		// The spike's own live orchestrator is inside swarm_materialize, or it
		// already wrote completed with a resolution (close_spike); it ends
		// through that checkpoint, not a daemon one (spec decision 1c).
		if rootType == string(items.Spike) && r.itemID == rootID && r.state.Live() {
			continue
		}
		// ponytail: in-tx, so no lockAgentOperations; a driver already past
		// its launch commit could still start a successor (its handoff is
		// refused and a close records completed). Take the lock post-commit
		// if that ever shows up.
		if err := s.cancelAgentOperationsTx(ctx, tx, r.id); err != nil {
			return err
		}
		if r.state.Live() && !r.state.Pausing() {
			if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind,
				attempt, summary, daemon_written, created_at) VALUES (?, ?, ?, ?, 'completed', ?, ?, 1, ?)`,
				ids.New("ckp"), r.sesID, r.id, r.itemID, r.attempt, fmt.Sprintf(rootDoneSummary, rootKey), now); err != nil {
				return err
			}
			continue
		}
		// resolveDeadInner would end a pausing session paused, not completed,
		// so everything that is not spawning/running finishes here; a leftover
		// pane goes on Reconcile's next finished-agent pass.
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'completed', ended_at = COALESCE(ended_at, ?)
			WHERE id = ? AND state IN ('pause_requested', 'quiescing', 'stopping', 'paused', 'interrupted')`,
			now, r.sesID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'finished', finished_at = ? WHERE id = ?`, now, r.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE worktree_reservations SET released_at = ?
			WHERE agent_id = ? AND released_at IS NULL`, now, r.id); err != nil {
			return err
		}
		if err := s.publishAgentChanged(ctx, tx, r.name, rootID); err != nil {
			return err
		}
	}
	return nil
}

// cancelWorkOnCancelledItems is Reconcile's cascade pass (spec locked
// decision 5): work still running on a cancelled item stops. Workflows go
// first, because Spawn has no cancelled-item guard and a running workflow row
// could respawn a run. Then every queued or active agent assigned to a
// cancelled item goes through the same Cancel the board uses. One failure is
// logged, never returned, so it cannot stall the rest of Reconcile.
func (s *Store) cancelWorkOnCancelledItems(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT i.key, i.root_id FROM workflows w JOIN items i ON i.id = w.item_id
		WHERE i.status = 'cancelled' AND w.state IN ('running', 'escalated')
		AND w.created_at = (SELECT MAX(created_at) FROM workflows WHERE item_id = w.item_id)`)
	if err != nil {
		return err
	}
	var wfs [][2]string
	for rows.Next() {
		var key, rootID string
		if err := rows.Scan(&key, &rootID); err != nil {
			rows.Close()
			return err
		}
		wfs = append(wfs, [2]string{key, rootID})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, wf := range wfs {
		// CancelWorkflow only reads the caller's root for its scope check.
		if _, err := s.CancelWorkflow(ctx, Agent{RootItemID: wf[1]}, wf[0], "", ""); err != nil {
			s.logf("reconcile: cancel workflow on cancelled %s: %v", wf[0], err)
		}
	}
	names, err := s.queryIDs(ctx, `SELECT a.name FROM agents a JOIN items i ON i.id = a.item_id
		WHERE i.status = 'cancelled' AND a.state IN ('queued', 'active')`)
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err := s.Cancel(ctx, name, "", ""); err != nil {
			s.logf("reconcile: cancel %s on a cancelled item: %v", name, err)
		}
	}
	return nil
}
