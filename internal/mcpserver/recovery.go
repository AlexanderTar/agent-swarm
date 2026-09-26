package mcpserver

import (
	"context"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// recoveryInput is swarm_read's additive recovery read (spec §3 successor
// recovery): agent-scoped paginated checkpoint history with full
// fields/provenance plus readable requests, worktrees (IDs/paths) and
// artifact revisions/hashes. cursor is a created_at-millis cursor (0 means
// "from the top"). This object owns its own limit/cursor; the general
// refs/filter/repos/since_seq paths are untouched.
type recoveryInput struct {
	Agent  string `json:"agent"`
	Limit  int    `json:"limit"`
	Cursor *int64 `json:"cursor"`
}

// assignmentOut renders SyncRecovery's durable assignment for the wire.
func assignmentOut(a runtime.AssignmentView) map[string]any {
	return map[string]any{
		"agent": a.AgentID, "name": a.AgentName, "role": string(a.Role),
		"item": a.ItemKey, "title": a.Title, "brief": a.Brief,
		"generation": a.Generation, "session": a.SessionID,
	}
}

// recoveryBundleOut renders SyncRecovery's bundle for the wire (nil when
// this sync is not the generation's first).
func recoveryBundleOut(b *runtime.RecoveryBundle) any {
	if b == nil {
		return nil
	}
	wf := map[string]any{"workflow_id": b.Workflow.WorkflowID, "step_id": b.Workflow.StepID, "round": b.Workflow.Round}
	return map[string]any{
		"operation_id": b.OperationID, "mode": string(b.Mode),
		"manifest_path": b.ManifestPath, "manifest_hash": b.ManifestHash,
		"predecessor_session": b.PredecessorSessionID, "generation": b.Generation,
		"checkpoint_cursor": b.CheckpointCursor, "workflow": wf,
	}
}

// recoveryOut runs the swarm_read recovery object: history with full
// fields/provenance plus resource discovery.
func (s *Server) recoveryOut(ctx context.Context, in *recoveryInput) (map[string]any, error) {
	agentID, err := s.recoveryAgentID(ctx, in.Agent)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var before time.Time
	if in.Cursor != nil && *in.Cursor > 0 {
		before = db.FromMillis(*in.Cursor)
	}
	hist, err := s.RT.RecoveryHistory(ctx, agentID, limit, before)
	if err != nil {
		return nil, err
	}
	cps := make([]any, 0, len(hist))
	for _, c := range hist {
		var itemKey string
		_ = s.RT.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`,
			c.ItemID).Scan(&itemKey)
		cps = append(cps, map[string]any{
			"checkpoint_id": c.ID, "item": itemKey, "kind": string(c.Kind),
			"summary": c.Summary, "resolution": c.Resolution,
			"next": c.Next, "blockers": c.Blockers, "git": c.Git,
			"verification": c.Verification, "artifacts": c.Artifacts,
			"processed": c.Processed, "verdict": c.Verdict, "findings": c.Findings,
			"session_id": c.SessionID, "generation": c.Generation, "agent": c.AgentID,
			"created_at": c.CreatedAt, "daemon_written": c.DaemonWritten,
		})
	}
	res, err := s.RT.RecoveryResources(ctx, agentID)
	if err != nil {
		return nil, err
	}
	wts := make([]any, 0, len(res.Worktrees))
	for _, w := range res.Worktrees {
		wts = append(wts, map[string]any{
			"id": w.ID, "path": w.Path, "branch": w.Branch, "base_sha": w.BaseSHA, "state": w.State,
		})
	}
	arts := make([]any, 0, len(res.Artifacts))
	for _, a := range res.Artifacts {
		revs := make([]any, 0, len(a.Revisions))
		for _, r := range a.Revisions {
			revs = append(revs, map[string]any{"revision": r.Revision, "sha256": r.SHA256})
		}
		arts = append(arts, map[string]any{
			"artifact_id": a.ID, "kind": a.Kind, "path": a.Path,
			"revision": a.HeadRevision, "revisions": revs,
		})
	}
	reqs := make([]any, 0, len(res.Requests))
	for _, r := range res.Requests {
		reqs = append(reqs, map[string]any{"request_id": r.ID, "kind": r.Kind, "state": r.State})
	}
	var name string
	if a, err := s.RT.AgentByID(ctx, agentID); err == nil {
		name = a.Name
	}
	return map[string]any{
		"agent": name, "checkpoints": cps,
		"worktrees": wts, "artifacts": arts, "requests": reqs,
	}, nil
}

// recoveryAgentID resolves the recovery object's agent by id or name.
func (s *Server) recoveryAgentID(ctx context.Context, ref string) (string, error) {
	if a, err := s.RT.AgentByID(ctx, ref); err == nil {
		return a.ID, nil
	}
	a, err := s.RT.Agent(ctx, ref)
	if err != nil {
		return "", err
	}
	return a.ID, nil
}
