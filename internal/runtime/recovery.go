package runtime

import (
	"context"
	"database/sql"
	"errors"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// AssignmentView is the durable assignment swarm_sync returns on every
// sync, independent of acked inbox rows (spec §3 successor recovery).
// Identity, brief and assignment refs survive replacement: only the
// session id, generation, token and provider session change.
type AssignmentView struct {
	AgentID, AgentName string
	Role               Role
	ItemKey            string
	Title              string
	Brief              string
	Generation         int
	SessionID          string
}

// WorkflowBinding names the workflow run the agent is a step of, when it
// is one. Empty WorkflowID means the agent runs a legacy assignment.
type WorkflowBinding struct {
	WorkflowID string
	StepID     string
	Round      int
}

// RecoveryBundle is swarm_sync's additive recovery payload: it rides on
// every generation's first sync only, independent of acked inbox rows.
// The successor replays unacked IDs (Sync's own Messages/Unacked, which
// reset per-generation delivery eligibility on startSession), keeps acked
// messages acked, and re-checks disk HEADs and live state before editing.
type RecoveryBundle struct {
	OperationID          string
	Mode                 ReplacementMode
	ManifestPath         string
	ManifestHash         string
	PredecessorSessionID string
	Generation           int // the successor generation this bundle is for
	CheckpointCursor     string
	Workflow             WorkflowBinding
}

// SyncRecoveryResult is one session's recovery read.
type SyncRecoveryResult struct {
	Assignment AssignmentView
	Recovery   *RecoveryBundle // non-nil on the generation's first sync after a replacement only
	FirstSync  bool
}

// SyncRecovery returns the session's durable assignment plus, on the
// generation's first sync after a handoff/recover replacement, the
// recovery bundle. Later syncs of the same session carry the assignment
// but no bundle. A session whose agent was never replaced never carries
// one. First-sync is tracked per sessions row (each generation is a new
// row), so it resets naturally across generations.
func (s *Store) SyncRecovery(ctx context.Context, sessionID string) (SyncRecoveryResult, error) {
	var out SyncRecoveryResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ses, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, a.ItemID)
		if err != nil {
			return err
		}
		it, err := s.Items.GetTx(ctx, tx, key)
		if err != nil {
			return err
		}
		// The brief lives on the agent row (Spawn renders and stores it
		// there); the item row carries the spec, not the brief.
		out.Assignment = AssignmentView{
			AgentID: a.ID, AgentName: a.Name, Role: a.Role,
			ItemKey: key, Title: it.Title, Brief: a.Brief,
			Generation: ses.Generation, SessionID: ses.ID,
		}
		var firstSync sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT first_sync_at FROM sessions WHERE id = ?`,
			ses.ID).Scan(&firstSync); err != nil {
			return err
		}
		out.FirstSync = !firstSync.Valid
		if out.FirstSync {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET first_sync_at = ? WHERE id = ?`,
				db.Millis(s.Now()), ses.ID); err != nil {
				return err
			}
		} else {
			return nil
		}
		bundle, ok, err := s.recoveryBundleTx(ctx, tx, a.ID, ses.Generation)
		if err != nil {
			return err
		}
		if ok {
			out.Recovery = bundle
		}
		return nil
	})
	return out, err
}

// recoveryBundleTx builds the bundle for the generation's first sync from
// the durable operation row: the op whose predecessor generation is one
// below this session's. Pause operations carry no successor, so only
// handoff/recover rows qualify.
func (s *Store) recoveryBundleTx(ctx context.Context, tx *sql.Tx, agentID string, generation int) (*RecoveryBundle, bool, error) {
	var b RecoveryBundle
	var mode string
	err := tx.QueryRowContext(ctx, `SELECT id, mode,
		COALESCE(session_id, ''), COALESCE(manifest_path, ''), COALESCE(manifest_hash, ''),
		COALESCE(checkpoint_id, '')
		FROM agent_operations
		WHERE agent_id = ? AND mode IN ('handoff', 'recover') AND generation + 1 = ?
		ORDER BY updated_at DESC LIMIT 1`,
		agentID, generation).Scan(&b.OperationID, &mode,
		&b.PredecessorSessionID, &b.ManifestPath, &b.ManifestHash, &b.CheckpointCursor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	b.Mode = ReplacementMode(mode)
	b.Generation = generation
	var wf WorkflowBinding
	if err := tx.QueryRowContext(ctx, `SELECT workflow_id, step_id, round FROM workflow_runs
		WHERE agent_id = ? ORDER BY round DESC, created_at DESC LIMIT 1`,
		agentID).Scan(&wf.WorkflowID, &wf.StepID, &wf.Round); err == nil {
		b.Workflow = wf
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	return &b, true, nil
}
