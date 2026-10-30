package runtime

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/AlexanderTar/swarmrun/internal/db"
	"github.com/AlexanderTar/swarmrun/internal/ids"
	"github.com/AlexanderTar/swarmrun/internal/items"
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

// OnRootDone is items.Store.RootDone (spec locked decision 1, as amended by
// CHORE-171): rootID just reached Done. Non-orchestrator agents still queued
// or active on it finish: a spawning/running session gets a daemon-written
// completed checkpoint on its own item and attempt, and resolveAlive kills
// the pane once the killCompletedAfter grace (60 s) has passed and the pane
// reads idle, or at killCompletedBusyCap (2 h) even if still busy. An
// orchestrator is never finished here: no daemon completed, no operation
// cancel, so the kill paths never arm; a paused or interrupted orchestrator
// stays resumable. Once the root is Done, an orchestrator's own completed is
// accepted and ends the session through the normal grace + idle path, and an
// idle one is nudged for it (nudgeFinalNote).
func (s *Store) OnRootDone(ctx context.Context, tx *sql.Tx, rootID string) error {
	var rootKey string
	if err := tx.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, rootID).Scan(&rootKey); err != nil {
		return err
	}
	type agentRow struct {
		id, name, itemID, sesID, role string
		state                         SessionState
		attempt                       int
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.name, a.item_id, a.role, COALESCE(ses.id, ''),
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
		if err := rows.Scan(&r.id, &r.name, &r.itemID, &r.role, &r.sesID, &st, &r.attempt); err != nil {
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
		// CHORE-171: orchestrators end only on their own final note. A live
		// orchestrator (root or child) on a Done root gets no daemon
		// completed and its operations are not cancelled, so
		// CompletedCheckpointAt stays nil and neither the 60 s idle kill
		// nor the 2 h busy cap arms. This generalises the spike carve-out
		// (spec decision 1c) to every root type. Paused or interrupted
		// orchestrators are left resumable on the same grounds: finishing
		// them here would end an agent the user never saw a final note
		// from. Other roles finish exactly as before.
		if r.role == string(RoleOrchestrator) {
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
// cancelled item goes through the same Cancel the board uses; so does one on a
// task an orchestrator forced to Done (CHORE-18), whose workflow was already
// cancelled in that same transaction. One failure is
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
		WHERE (i.status = 'cancelled' OR (i.status = 'done' AND i.override_json IS NOT NULL))
		AND a.state IN ('queued', 'active')`)
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
