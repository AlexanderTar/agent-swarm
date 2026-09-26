package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
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

// RecoveryCheckpoint is one checkpoint with full fields plus provenance:
// which session and generation wrote it. swarm_read strips
// next/blockers/git/verification/artifacts/provenance from checkpoint
// output; this history keeps all of it so the successor replays the
// predecessor's evidence instead of re-doing (or inventing) it.
type RecoveryCheckpoint struct {
	Checkpoint
	Generation int
	Verdict    string
	Findings   []workflow.Finding
}

// RecoveryHistory returns the agent's checkpoints across all its
// generations, newest first, paginated by a stable (created_at, id) cursor:
// rows strictly after cursor in that order ("" means "from the top"), so
// checkpoints sharing a millisecond are never dropped or repeated. next is
// the cursor for the following page, "" when this page is the last. Default
// limit 50, max 200.
func (s *Store) RecoveryHistory(ctx context.Context, agentID string, limit int, cursor string) (out []RecoveryCheckpoint, next string, err error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if _, err := s.agentByID(ctx, agentID); err != nil {
		return nil, "", err
	}
	cutoff, cutoffID := int64(1)<<62, ""
	if cursor != "" {
		ms, id, ok := strings.Cut(cursor, ":")
		n, perr := strconv.ParseInt(ms, 10, 64)
		if !ok || perr != nil || id == "" {
			return nil, "", &items.Error{Code: items.CodeBadRequest, Message: "recovery cursor must be a next_cursor value."}
		}
		cutoff, cutoffID = n, id
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT c.id, c.session_id, c.agent_id, c.item_id, c.kind, c.attempt,
		COALESCE(c.resolution, ''), c.summary, c.next_json, c.blockers_json, c.git_json, c.verify_json,
		c.artifacts_json, c.processed_json, c.daemon_written, c.created_at,
		COALESCE(c.verdict, ''), COALESCE(c.findings_json, '[]'), s.generation
		FROM checkpoints c JOIN sessions s ON s.id = c.session_id
		WHERE c.agent_id = ? AND (c.created_at < ? OR (c.created_at = ? AND c.id < ?))
		ORDER BY c.created_at DESC, c.id DESC LIMIT ?`, agentID, cutoff, cutoff, cutoffID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var c RecoveryCheckpoint
		var kind string
		var nextJSON, blockersJSON, gitJSON, verifyJSON, artifactsJSON, processedJSON string
		var daemonWritten int
		var created int64
		var findingsJSON string
		if err := rows.Scan(&c.ID, &c.SessionID, &c.AgentID, &c.ItemID, &kind, &c.Attempt,
			&c.Resolution, &c.Summary, &nextJSON, &blockersJSON, &gitJSON, &verifyJSON, &artifactsJSON,
			&processedJSON, &daemonWritten, &created, &c.Verdict, &findingsJSON, &c.Generation); err != nil {
			return nil, "", err
		}
		c.Kind = CheckpointKind(kind)
		json.Unmarshal([]byte(nextJSON), &c.Next)
		json.Unmarshal([]byte(blockersJSON), &c.Blockers)
		json.Unmarshal([]byte(gitJSON), &c.Git)
		json.Unmarshal([]byte(verifyJSON), &c.Verification)
		json.Unmarshal([]byte(artifactsJSON), &c.Artifacts)
		json.Unmarshal([]byte(processedJSON), &c.Processed)
		json.Unmarshal([]byte(findingsJSON), &c.Findings)
		c.DaemonWritten = daemonWritten != 0
		c.CreatedAt = db.FromMillis(created)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = fmt.Sprintf("%d:%s", db.Millis(last.CreatedAt), last.ID)
	}
	return out, next, nil
}

// RecoveryWorktree is one worktree the agent owns, with the recorded HEAD
// the successor re-checks on disk before editing.
type RecoveryWorktree struct {
	ID, Path, Branch, BaseSHA, State string
}

// RecoveryArtifactRevision is one stored revision with its content hash.
type RecoveryArtifactRevision struct {
	Revision int
	SHA256   string
}

// RecoveryArtifact is one readable artifact with all its revisions/hashes.
type RecoveryArtifact struct {
	ID, Kind, Path string
	HeadRevision   int
	Revisions      []RecoveryArtifactRevision
}

// RecoveryRequest is one agent- or item-owned request, which survives
// replacement across sessions (Batch 1: requests follow the agent).
type RecoveryRequest struct {
	ID, Kind, State string
}

// RecoveryResourcesResult is the successor's resource discovery: readable
// requests, worktrees (IDs/paths) and artifact revisions/hashes.
type RecoveryResourcesResult struct {
	Worktrees []RecoveryWorktree
	Artifacts []RecoveryArtifact
	Requests  []RecoveryRequest
}

// RecoveryResources returns the agent's owned worktrees, its item's and
// own artifacts with revisions/hashes, and its surviving requests.
func (s *Store) RecoveryResources(ctx context.Context, agentID string) (RecoveryResourcesResult, error) {
	var out RecoveryResourcesResult
	a, err := s.agentByID(ctx, agentID)
	if err != nil {
		return out, err
	}
	wrows, err := s.DB.QueryContext(ctx, `SELECT id, path, COALESCE(branch, ''), base_sha, state
		FROM worktrees WHERE owner_agent_id = ? ORDER BY path`, agentID)
	if err != nil {
		return out, err
	}
	for wrows.Next() {
		var w RecoveryWorktree
		if err := wrows.Scan(&w.ID, &w.Path, &w.Branch, &w.BaseSHA, &w.State); err != nil {
			wrows.Close()
			return out, err
		}
		out.Worktrees = append(out.Worktrees, w)
	}
	wrows.Close()
	if err := wrows.Err(); err != nil {
		return out, err
	}
	arows, err := s.DB.QueryContext(ctx, `SELECT id, kind, path, head_revision FROM artifacts
		WHERE item_id = ? OR created_by = ? ORDER BY path`, a.ItemID, agentID)
	if err != nil {
		return out, err
	}
	for arows.Next() {
		var art RecoveryArtifact
		if err := arows.Scan(&art.ID, &art.Kind, &art.Path, &art.HeadRevision); err != nil {
			arows.Close()
			return out, err
		}
		rrows, err := s.DB.QueryContext(ctx, `SELECT revision, sha256 FROM artifact_revisions
			WHERE artifact_id = ? ORDER BY revision`, art.ID)
		if err != nil {
			arows.Close()
			return out, err
		}
		for rrows.Next() {
			var r RecoveryArtifactRevision
			if err := rrows.Scan(&r.Revision, &r.SHA256); err != nil {
				rrows.Close()
				arows.Close()
				return out, err
			}
			art.Revisions = append(art.Revisions, r)
		}
		rrows.Close()
		if err := rrows.Err(); err != nil {
			arows.Close()
			return out, err
		}
		out.Artifacts = append(out.Artifacts, art)
	}
	arows.Close()
	if err := arows.Err(); err != nil {
		return out, err
	}
	qrows, err := s.DB.QueryContext(ctx, `SELECT id, kind, state FROM requests
		WHERE agent_id = ? ORDER BY created_at`, agentID)
	if err != nil {
		return out, err
	}
	for qrows.Next() {
		var r RecoveryRequest
		if err := qrows.Scan(&r.ID, &r.Kind, &r.State); err != nil {
			qrows.Close()
			return out, err
		}
		out.Requests = append(out.Requests, r)
	}
	qrows.Close()
	return out, qrows.Err()
}
