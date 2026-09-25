package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Preservation policy (spec §3): shared by Pause and Handoff (Pause
// disables auto-launch). The predecessor stops new work, finishes or
// interrupts its atomic action, and may use read/edit/shell/wait/commit
// under existing permissions. Denied: delegation, new workflow steps,
// push/deploy, and completed checkpoints. Anything preservation denies
// must fail closed with an explicit error, never a silent skip.

// preservationSaveMCP are the MCP tools a preserving predecessor may use:
// sync/read the state, checkpoint handoff/blocked/failed, ask/withdraw,
// report a blocker, snapshot specs/plans via the artifact registry.
var preservationSaveMCP = []string{
	"swarm_sync", "swarm_read", "swarm_checkpoint", "swarm_ask", "swarm_blocker", "swarm_artifact",
}

// preservationDeniedMCP starts new work: delegation mints agents outside
// the replaced identity, and a workflow step belongs to the engine.
var preservationDeniedMCP = []string{"swarm_spawn", "swarm_workflow"}

// PreservationMCPAllowed gates one MCP tool name in preservation mode.
func PreservationMCPAllowed(tool string) error {
	if slices.Contains(preservationDeniedMCP, tool) {
		return &items.Error{Code: items.CodeConflict,
			Message: "preservation: " + tool + " starts new work; finish saving instead."}
	}
	if !slices.Contains(preservationSaveMCP, tool) {
		return &items.Error{Code: items.CodeConflict,
			Message: "preservation: " + tool + " is not a save tool; finish saving instead."}
	}
	return nil
}

// PreservationCheckpointAllowed gates checkpoint kinds in preservation
// mode: handoff, blocked and failed only (mirrors pauseAllowedKinds --
// the daemon enforces it whatever the caller claims).
func PreservationCheckpointAllowed(kind CheckpointKind) error {
	if slices.Contains(pauseAllowedKinds, kind) {
		return nil
	}
	return &items.Error{Code: items.CodeConflict,
		Message: "preservation: only handoff, blocked and failed checkpoints while preserving."}
}

// preservationDelegationNative mirrors the hook's native fork set plus the
// Workflow tool: every one of these starts work outside this session.
var preservationDelegationNative = []string{
	"Agent", "Task", "Fork", "fork", "invoke_subagent", "subagent",
	"dispatch_agent", "spawn_agent", "Workflow", "workflow",
}

// PreservationNativeAllowed gates one native tool name in preservation
// mode: everything but delegation is a save-path tool (read, edit, shell,
// wait, commit) kept under the session's existing permissions.
func PreservationNativeAllowed(toolName string) error {
	if slices.Contains(preservationDelegationNative, toolName) {
		return &items.Error{Code: items.CodeConflict,
			Message: "preservation: " + toolName + " delegates; finish saving instead."}
	}
	return nil
}

var pushRe = regexp.MustCompile(`(^|[;&|()\s])git\s+push\b`)
var deployRe = regexp.MustCompile(`(?i)(^|[;&|()\s])(deploy\b|kubectl\s+(apply|create)|terraform\s+apply|fly\s+deploy|railway\s+(up|deploy))`)

// PreservationCommandAllowed gates one shell command in preservation mode:
// push/deploy leave the machine, everything else (inspect, stage, commit,
// snapshot, wait on owned commands) stays under existing permissions.
func PreservationCommandAllowed(command string) error {
	if pushRe.MatchString(command) {
		return &items.Error{Code: items.CodeConflict,
			Message: "preservation: git push is denied while preserving; commit locally instead."}
	}
	if deployRe.MatchString(command) {
		return &items.Error{Code: items.CodeConflict,
			Message: "preservation: deploy is denied while preserving; finish saving instead."}
	}
	if strings.TrimSpace(command) == "" {
		return &items.Error{Code: items.CodeBadRequest, Message: "preservation: empty command."}
	}
	return nil
}

// Handoff manifest (schema v1, spec §3): the predecessor's saved work,
// written atomically, then the handoff checkpoint last (checkpoint
// binding). The daemon validates ownership, manifest hash, worktree HEADs
// and writer absence before ready; any failure yields explicit blocked,
// never a fabricated success.
const manifestSchemaVersion = 1

// ManifestWorktree is one assigned worktree with its recorded HEAD (from
// the worktree row) and the HEAD observed on disk at write time ("" when
// the tree was unreadable -- validation then blocks rather than trusting
// the recorded value).
type ManifestWorktree struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Branch       string `json:"branch"`
	RecordedHead string `json:"recorded_head"`
	ObservedHead string `json:"observed_head"`
	State        string `json:"state"`
}

// ManifestArtifact is one registry artifact at its head revision, with the
// content snapshot preserved under the handoff dir's artifacts/.
type ManifestArtifact struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Revision int    `json:"revision"`
	SHA256   string `json:"sha256"`
	Snapshot string `json:"snapshot"`
}

// HandoffManifest is the decoded manifest.json.
type HandoffManifest struct {
	SchemaVersion int    `json:"schema_version"`
	OperationID   string `json:"operation_id"`
	AgentID       string `json:"agent_id"`
	PredecessorID string `json:"predecessor_session_id"`
	Attempt       int    `json:"attempt"`
	Generation    int    `json:"generation"`
	Mode          string `json:"mode"`
	Assignment    struct {
		ItemKey string `json:"item_key"`
		Title   string `json:"title"`
		Brief   string `json:"brief"`
	} `json:"assignment"`
	Workflow *WorkflowBinding `json:"workflow_binding,omitempty"`
	Next     []string         `json:"next_action"`
	Blockers []string         `json:"blockers"`
	Worktrees []ManifestWorktree `json:"worktrees"`
	Artifacts []ManifestArtifact `json:"artifacts"`
	Verification []Verify `json:"verification"`
	// Commands reserves the owned-command-handle list. No durable
	// command-handle table exists in this codebase (worker-lifecycle
	// retention is Batch 3-4 scope), so this is always empty rather than
	// an invented history.
	Commands         []string `json:"commands"`
	Requests         []string `json:"request_ids"`
	Messages         []string `json:"message_ids"`
	CheckpointCursor string   `json:"checkpoint_cursor"`
	Path             string   `json:"-"`
	Hash             string   `json:"-"`
}

// readDiskHEAD reports the worktree's current disk HEAD. It is a package
// seam so tests stub it without a git binary; production shells out to
// git. An unreadable tree is an error, never a guessed SHA.
var readDiskHEAD = func(path string) (string, error) {
	out, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ManifestDir is <swarm home>/handoffs/<agent-id>/<operation-id>/.
func ManifestDir(home, agentID, opID string) string {
	return filepath.Join(home, "handoffs", agentID, opID)
}

// WriteHandoffManifest assembles the schema-v1 manifest for opID at the
// handoff checkpoint checkpointID, writes it atomically (temp file plus
// rename, so a crash never leaves a half manifest), snapshots registry
// artifact contents under artifacts/, and records the path/hash on the
// operation row. Manifest first, checkpoint last is the caller's order --
// this is the "manifest" half of that binding.
func (s *Store) WriteHandoffManifest(ctx context.Context, opID, checkpointID string) (HandoffManifest, error) {
	var m HandoffManifest
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var mode, agentID, predecessorID string
		var generation int
		if err := tx.QueryRowContext(ctx, `SELECT mode, agent_id,
			COALESCE(session_id, ''), generation FROM agent_operations WHERE id = ?`,
			opID).Scan(&mode, &agentID, &predecessorID, &generation); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &items.Error{Code: items.CodeNotFound, Message: fmt.Sprintf("No operation %s.", opID)}
			}
			return err
		}
		a, err := s.agentByIDTx(ctx, tx, agentID)
		if err != nil {
			return err
		}
		if predecessorID == "" {
			return &items.Error{Code: items.CodeBadRequest, Message: "preservation: operation has no predecessor session."}
		}
		var attempt int
		if err := tx.QueryRowContext(ctx, `SELECT attempt FROM sessions WHERE id = ?`,
			predecessorID).Scan(&attempt); err != nil {
			return &items.Error{Code: items.CodeBadRequest, Message: "preservation: predecessor session is gone."}
		}
		key, err := s.itemKey(ctx, tx, a.ItemID)
		if err != nil {
			return err
		}
		it, err := s.Items.GetTx(ctx, tx, key)
		if err != nil {
			return err
		}
		ckpt, err := s.checkpointByIDTx(ctx, tx, checkpointID)
		if err != nil {
			return err
		}
		if ckpt.AgentID != agentID {
			return &items.Error{Code: items.CodeBadRequest,
				Message: "preservation: checkpoint belongs to another agent."}
		}
		m = HandoffManifest{
			SchemaVersion: manifestSchemaVersion,
			OperationID:   opID, AgentID: agentID, PredecessorID: predecessorID,
			Attempt: attempt, Generation: generation, Mode: mode,
			Next: ckpt.Next, Blockers: ckpt.Blockers, Verification: ckpt.Verification,
			Commands: []string{}, Requests: []string{}, Messages: []string{},
		}
		m.Assignment.ItemKey = key
		m.Assignment.Title = it.Title
		m.Assignment.Brief = a.Brief
		var wf WorkflowBinding
		if err := tx.QueryRowContext(ctx, `SELECT workflow_id, step_id, round FROM workflow_runs
			WHERE agent_id = ? ORDER BY round DESC, created_at DESC LIMIT 1`,
			agentID).Scan(&wf.WorkflowID, &wf.StepID, &wf.Round); err == nil {
			m.Workflow = &wf
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		wrows, err := tx.QueryContext(ctx, `SELECT id, path, COALESCE(branch, ''),
			base_sha, state FROM worktrees
			WHERE owner_agent_id = ? AND state IN ('active', 'retained') ORDER BY path`, agentID)
		if err != nil {
			return err
		}
		for wrows.Next() {
			var w ManifestWorktree
			if err := wrows.Scan(&w.ID, &w.Path, &w.Branch, &w.RecordedHead, &w.State); err != nil {
				wrows.Close()
				return err
			}
			if head, err := readDiskHEAD(w.Path); err == nil {
				w.ObservedHead = head
			}
			m.Worktrees = append(m.Worktrees, w)
		}
		wrows.Close()
		if err := wrows.Err(); err != nil {
			return err
		}
		arows, err := tx.QueryContext(ctx, `SELECT id, kind, path, head_revision FROM artifacts
			WHERE item_id = ? OR created_by = ? ORDER BY path`, a.ItemID, agentID)
		if err != nil {
			return err
		}
		for arows.Next() {
			var art ManifestArtifact
			var content, sha string
			var id string
			if err := arows.Scan(&id, &art.Kind, &art.Path, &art.Revision); err != nil {
				arows.Close()
				return err
			}
			art.ID = id
			if err := tx.QueryRowContext(ctx, `SELECT sha256, content FROM artifact_revisions
				WHERE artifact_id = ? AND revision = ?`, id, art.Revision).Scan(&sha, &content); err != nil {
				arows.Close()
				return err
			}
			art.SHA256 = sha
			art.Snapshot = fmt.Sprintf("artifacts/%s-r%d.md", id, art.Revision)
			if err := s.writeSnapshot(ManifestDir(s.Home, agentID, opID), art.Snapshot, content); err != nil {
				arows.Close()
				return err
			}
			m.Artifacts = append(m.Artifacts, art)
		}
		arows.Close()
		if err := arows.Err(); err != nil {
			return err
		}
		qrows, err := tx.QueryContext(ctx, `SELECT id FROM requests
			WHERE agent_id = ? AND state NOT IN ('answered', 'withdrawn', 'closed') ORDER BY created_at`, agentID)
		if err != nil {
			return err
		}
		for qrows.Next() {
			var id string
			if err := qrows.Scan(&id); err != nil {
				qrows.Close()
				return err
			}
			m.Requests = append(m.Requests, id)
		}
		qrows.Close()
		if err := qrows.Err(); err != nil {
			return err
		}
		mrows, err := tx.QueryContext(ctx, `SELECT id FROM messages
			WHERE to_agent_id = ? AND state <> 'acked' ORDER BY seq`, agentID)
		if err != nil {
			return err
		}
		for mrows.Next() {
			var id string
			if err := mrows.Scan(&id); err != nil {
				mrows.Close()
				return err
			}
			m.Messages = append(m.Messages, id)
		}
		mrows.Close()
		if err := mrows.Err(); err != nil {
			return err
		}
		m.CheckpointCursor = checkpointID
		raw, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		dir := ManifestDir(s.Home, agentID, opID)
		path, err := writeAtomic(dir, "manifest.json", raw)
		if err != nil {
			return err
		}
		m.Path = path
		m.Hash = fmt.Sprintf("%x", sha256.Sum256(raw))
		_, err = tx.ExecContext(ctx, `UPDATE agent_operations SET manifest_path = ?,
			manifest_hash = ?, checkpoint_id = ?, updated_at = ? WHERE id = ?`,
			path, m.Hash, checkpointID, db.Millis(s.Now()), opID)
		return err
	})
	return m, err
}

// writeSnapshot stores one artifact snapshot under dir (creating it), so
// the manifest's hashes always have content beside them even if the
// registry is later compacted.
func (s *Store) writeSnapshot(dir, name, content string) error {
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
}

// writeAtomic writes raw to dir/name via a temp file plus rename: a crash
// mid-write leaves the old manifest (or none), never a half one.
func writeAtomic(dir, name string, raw []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	final := filepath.Join(dir, name)
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return final, nil
}

// checkpointByIDTx reads one checkpoint with the fields the manifest
// needs. (Checkpoints() is item-scoped; the manifest names its checkpoint
// by id.)
func (s *Store) checkpointByIDTx(ctx context.Context, tx *sql.Tx, id string) (Checkpoint, error) {
	var c Checkpoint
	var kind string
	var nextJSON, blockersJSON, gitJSON, verifyJSON, artifactsJSON, processedJSON string
	var daemonWritten int
	var created int64
	err := tx.QueryRowContext(ctx, `SELECT id, session_id, agent_id, item_id, kind, attempt,
		COALESCE(resolution, ''), summary, next_json, blockers_json, git_json, verify_json, artifacts_json,
		processed_json, daemon_written, created_at FROM checkpoints WHERE id = ?`, id).Scan(
		&c.ID, &c.SessionID, &c.AgentID, &c.ItemID, &kind, &c.Attempt,
		&c.Resolution, &c.Summary, &nextJSON, &blockersJSON, &gitJSON, &verifyJSON, &artifactsJSON,
		&processedJSON, &daemonWritten, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return c, &items.Error{Code: items.CodeNotFound, Message: "Unknown checkpoint."}
	}
	if err != nil {
		return c, err
	}
	c.Kind = CheckpointKind(kind)
	json.Unmarshal([]byte(nextJSON), &c.Next)
	json.Unmarshal([]byte(blockersJSON), &c.Blockers)
	json.Unmarshal([]byte(gitJSON), &c.Git)
	json.Unmarshal([]byte(verifyJSON), &c.Verification)
	json.Unmarshal([]byte(artifactsJSON), &c.Artifacts)
	json.Unmarshal([]byte(processedJSON), &c.Processed)
	c.DaemonWritten = daemonWritten != 0
	c.CreatedAt = db.FromMillis(created)
	return c, nil
}
