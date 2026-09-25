package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// TestPreservationAllowsSaveButNotNewWork pins the preservation-mode
// rules (shared by Pause and Handoff; Pause disables auto-launch): the
// predecessor may use read/edit/shell/wait/commit under existing
// permissions, and is denied delegation, new workflow steps, push/deploy
// and completed checkpoints.
func TestPreservationAllowsSaveButNotNewWork(t *testing.T) {
	for _, tool := range []string{"swarm_sync", "swarm_read", "swarm_checkpoint", "swarm_ask", "swarm_blocker", "swarm_artifact"} {
		if err := PreservationMCPAllowed(tool); err != nil {
			t.Fatalf("PreservationMCPAllowed(%q) = %v, want nil (save path)", tool, err)
		}
	}
	for _, tool := range []string{"swarm_spawn", "swarm_workflow"} {
		if err := PreservationMCPAllowed(tool); err == nil {
			t.Fatalf("PreservationMCPAllowed(%q) = nil, want denial (new work)", tool)
		}
	}
	for _, kind := range []CheckpointKind{Handoff, BlockedCkp, FailedCkp} {
		if err := PreservationCheckpointAllowed(kind); err != nil {
			t.Fatalf("PreservationCheckpointAllowed(%q) = %v, want nil", kind, err)
		}
	}
	for _, kind := range []CheckpointKind{Accepted, Progress, CompletedCkp} {
		if err := PreservationCheckpointAllowed(kind); err == nil {
			t.Fatalf("PreservationCheckpointAllowed(%q) = nil, want denial", kind)
		}
	}
	for _, tool := range []string{"Read", "Edit", "Write", "Bash", "Glob", "Grep"} {
		if err := PreservationNativeAllowed(tool); err != nil {
			t.Fatalf("PreservationNativeAllowed(%q) = %v, want nil (save path)", tool, err)
		}
	}
	for _, tool := range []string{"Agent", "Task", "Fork", "invoke_subagent", "Workflow", "dispatch_agent"} {
		if err := PreservationNativeAllowed(tool); err == nil {
			t.Fatalf("PreservationNativeAllowed(%q) = nil, want denial (delegation)", tool)
		}
	}
	for _, cmd := range []string{"git status", "git commit -m wip", "git diff --stat", "go test ./..."} {
		if err := PreservationCommandAllowed(cmd); err != nil {
			t.Fatalf("PreservationCommandAllowed(%q) = %v, want nil", cmd, err)
		}
	}
	for _, cmd := range []string{"git push origin main", "git push --tags", "npm run deploy", "kubectl apply -f k.yaml"} {
		if err := PreservationCommandAllowed(cmd); err == nil {
			t.Fatalf("PreservationCommandAllowed(%q) = nil, want denial (push/deploy)", cmd)
		}
	}
}

// TestHandoffManifestPreservesScratchArtifacts pins the handoff manifest
// (schema v1): written atomically under
// <swarm home>/handoffs/<agent-id>/<operation-id>/, carrying operation,
// agent, predecessor, attempt and generation IDs, assignment refs,
// unit/step + next action, blockers, worktrees with HEADs, artifacts with
// hashes (snapshot content preserved), verification results, request and
// message IDs, workflow binding and the checkpoint cursor -- with the
// path/hash recorded on the operation row.
func TestHandoffManifestPreservesScratchArtifacts(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}

	// One scratch spec with two revisions in the artifact registry, plus
	// one owned worktree with a recorded HEAD.
	now := db.Millis(s.Now())
	it, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	const rev1 = "# plan v1\n"
	const rev2 = "# plan v2\n"
	sum1 := fmt.Sprintf("%x", sha256.Sum256([]byte(rev1)))
	sum2 := fmt.Sprintf("%x", sha256.Sum256([]byte(rev2)))
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO artifacts
		(id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_scratch', ?, 'spec', 'docs/scratch.md', 2, ?, ?)`, it.ID, w.ID, now); err != nil {
		t.Fatal(err)
	}
	for _, rev := range []struct {
		n int
		h string
		b string
	}{{1, sum1, rev1}, {2, sum2, rev2}} {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO artifact_revisions
			(artifact_id, revision, sha256, content, sections_json, created_at)
			VALUES ('art_scratch', ?, ?, ?, '[]', ?)`, rev.n, rev.h, rev.b, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO repos (id, name, path, default_branch, source, created_at, updated_at)
		VALUES ('repo_scratch', 'repo1', '/tmp/repo1', 'main', 'manual', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO worktrees (id, repo_id, path, branch, base_ref, base_sha, state, owner_agent_id, root_item_id, created_at)
		VALUES ('wt_scratch', 'repo_scratch', '/tmp/repo1-wt', 'branch-1', 'main', 'deadbee', 'active', ?, ?, ?)`,
		w.ID, it.RootID, now); err != nil {
		t.Fatal(err)
	}

	// Handoff operation (predecessor pane alive, so it parks) and the
	// handoff checkpoint, manifest last per the binding order.
	panes(tm, Pane{Session: wSes.TmuxName})
	op, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "h1", "")
	if err != nil {
		t.Fatalf("request err = %v", err)
	}
	if err := s.SetSessionState(ctx, wSes.ID, PauseRequested); err != nil {
		t.Fatal(err)
	}
	ckpt, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff,
		Summary:           "saving half-done unit",
		Next:              []string{"finish unit 2"},
		Blockers:          []string{"waiting on review"},
		Verification:      []Verify{{Cmd: "go test ./...", OK: true}},
		Artifacts:         []string{},
	})
	if err != nil {
		t.Fatalf("handoff checkpoint err = %v", err)
	}

	manifest, err := s.WriteHandoffManifest(ctx, op.ID, ckpt.CheckpointID)
	if err != nil {
		t.Fatalf("WriteHandoffManifest err = %v", err)
	}
	wantDir := filepath.Join(s.Home, "handoffs", w.ID, op.ID)
	if manifest.Path != filepath.Join(wantDir, "manifest.json") {
		t.Fatalf("manifest path = %q, want under %q", manifest.Path, wantDir)
	}
	raw, err := os.ReadFile(manifest.Path)
	if err != nil {
		t.Fatalf("manifest not on disk: %v", err)
	}
	var decoded struct {
		SchemaVersion int `json:"schema_version"`
		AgentID       string `json:"agent_id"`
		OperationID   string `json:"operation_id"`
		Predecessor   string `json:"predecessor_session_id"`
		Generation    int    `json:"generation"`
		Assignment    struct {
			ItemKey string `json:"item_key"`
		} `json:"assignment"`
		Next     []string `json:"next_action"`
		Blockers []string `json:"blockers"`
		Worktrees []struct {
			Path string `json:"path"`
			Head string `json:"recorded_head"`
		} `json:"worktrees"`
		Artifacts []struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
			SHA256   string `json:"sha256"`
		} `json:"artifacts"`
		CheckpointCursor string `json:"checkpoint_cursor"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	if decoded.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", decoded.SchemaVersion)
	}
	if decoded.OperationID != op.ID || decoded.AgentID != w.ID || decoded.Predecessor != wSes.ID {
		t.Fatalf("manifest identity = %+v, want op/agent/predecessor IDs", decoded)
	}
	if decoded.Assignment.ItemKey != "TASK-1" {
		t.Fatalf("manifest assignment = %+v, want TASK-1", decoded.Assignment)
	}
	if len(decoded.Next) != 1 || decoded.Next[0] != "finish unit 2" {
		t.Fatalf("manifest next = %+v, want the checkpoint's next action", decoded.Next)
	}
	if len(decoded.Blockers) != 1 {
		t.Fatalf("manifest blockers = %+v, want the recorded blocker", decoded.Blockers)
	}
	if len(decoded.Worktrees) != 1 || decoded.Worktrees[0].Path != "/tmp/repo1-wt" || decoded.Worktrees[0].Head != "deadbee" {
		t.Fatalf("manifest worktrees = %+v, want the owned tree with its HEAD", decoded.Worktrees)
	}
	if len(decoded.Artifacts) != 1 || decoded.Artifacts[0].SHA256 != sum2 || decoded.Artifacts[0].Revision != 2 {
		t.Fatalf("manifest artifacts = %+v, want art_scratch r2 with its hash", decoded.Artifacts)
	}
	if decoded.CheckpointCursor != ckpt.CheckpointID {
		t.Fatalf("manifest cursor = %q, want %q", decoded.CheckpointCursor, ckpt.CheckpointID)
	}
	// Snapshot content preserved under artifacts/.
	snap, err := os.ReadFile(filepath.Join(wantDir, "artifacts", "art_scratch-r2.md"))
	if err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	if string(snap) != rev2 {
		t.Fatalf("snapshot = %q, want rev2 content", snap)
	}
	// Path/hash recorded on the operation row.
	var gotPath, gotHash string
	if err := s.DB.QueryRowContext(ctx, `SELECT manifest_path, manifest_hash FROM agent_operations WHERE id = ?`,
		op.ID).Scan(&gotPath, &gotHash); err != nil {
		t.Fatal(err)
	}
	if gotPath != manifest.Path {
		t.Fatalf("op manifest_path = %q, want %q", gotPath, manifest.Path)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(raw))
	if gotHash != sum {
		t.Fatalf("op manifest_hash = %q, want sha256 %q", gotHash, sum)
	}
}
