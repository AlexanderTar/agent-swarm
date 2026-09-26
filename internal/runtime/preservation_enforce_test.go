package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// IMPORTANT 3: push with git's global options still leaves the machine, and a
// sweeping add stages whatever is dirty (other agents' work, secrets), so
// preservation denies both. Explicit task-owned paths still pass.
func TestPreservationDeniesGlobalOptionPushAndSweepingAdd(t *testing.T) {
	for _, cmd := range []string{
		"git -C /tmp/wt push origin HEAD",
		"git -C wt push",
		"git -c core.sshCommand=ssh push",
		"git --git-dir=/tmp/wt/.git push",
		"git add -A",
		"git add --all",
		"git add .",
		"git -C /tmp/wt add -A",
		"git -C /tmp/wt add .",
		"cd wt && git add -A && git commit -m wip",
	} {
		if err := PreservationCommandAllowed(cmd); err == nil {
			t.Errorf("PreservationCommandAllowed(%q) = nil, want denial", cmd)
		}
	}
	for _, cmd := range []string{
		"git -C /tmp/wt status --porcelain",
		"git -C /tmp/wt add src/main.go",
		"git add ./src/main.go",
		"git -C /tmp/wt commit -m wip -- src/main.go",
	} {
		if err := PreservationCommandAllowed(cmd); err != nil {
			t.Errorf("PreservationCommandAllowed(%q) = %v, want nil (explicit save)", cmd, err)
		}
	}
}

// seedDirtyHandoff sets up a pending handoff for a worker owning one rw
// worktree, with disk HEAD and status stubbed.
func seedDirtyHandoff(t *testing.T, status string) (*Store, Agent, Session, Operation) {
	t.Helper()
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.Now())
	it, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO repos (id, name, path, default_branch, source, created_at, updated_at)
		VALUES ('repo_dirty', 'repo1', '/tmp/repo1', 'main', 'manual', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO worktrees (id, repo_id, path, branch, base_ref, base_sha, state, owner_agent_id, root_item_id, created_at)
		VALUES ('wt_dirty', 'repo_dirty', '/tmp/dirty-wt', 'b', 'main', 'deadbee', 'active', ?, ?, ?)`,
		w.ID, it.RootID, now); err != nil {
		t.Fatal(err)
	}
	oldHEAD, oldStatus := readDiskHEAD, readDiskStatus
	readDiskHEAD = func(string) (string, error) { return "deadbee", nil }
	readDiskStatus = func(string) (string, error) { return status, nil }
	t.Cleanup(func() { readDiskHEAD, readDiskStatus = oldHEAD, oldStatus })
	panes(tm, Pane{Session: wSes.TmuxName})
	tm.env[wSes.TmuxName] = map[string]string{"SWARM_SESSION": wSes.ID}
	op, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "dirty", "")
	if err != nil {
		t.Fatal(err)
	}
	return s, w, wSes, op
}

// Uncommitted tracked changes block the ready claim, naming the dirty paths;
// the manifest records them either way.
func TestPreservationReadyBlocksOnDirtyTrackedChanges(t *testing.T) {
	s, _, wSes, op := seedDirtyHandoff(t, " M owned.go\n?? scratch.go\n")
	ctx := context.Background()
	_, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "claiming ready"})
	if err == nil {
		t.Fatal("handoff checkpoint over a dirty tracked tree must refuse the ready claim")
	}
	if !strings.Contains(err.Error(), "owned.go") || strings.Contains(err.Error(), "scratch.go") {
		t.Fatalf("refusal = %q, want it to name the blocking tracked path owned.go (not untracked scratch.go)", err)
	}
	got, _ := s.getOperation(ctx, op.ID)
	if got.Phase != PhaseBlocked || !strings.Contains(got.Error, "owned.go") {
		t.Fatalf("op = %s (%q), want blocked naming owned.go", got.Phase, got.Error)
	}
	m := readManifest(t, s, op.ID)
	if len(m.Worktrees) != 1 || strings.Join(m.Worktrees[0].Dirty, ",") != "owned.go,scratch.go" {
		t.Fatalf("manifest worktrees = %+v, want dirty paths owned.go,scratch.go", m.Worktrees)
	}
}

// Untracked-only leftovers (scratch the agent chose not to commit) do not
// block, but the manifest still lists them for the successor.
func TestPreservationReadyAllowsUntrackedOnlyAndRecordsIt(t *testing.T) {
	s, _, wSes, op := seedDirtyHandoff(t, "?? scratch.go\n")
	ctx := context.Background()
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "saved"}); err != nil {
		t.Fatalf("untracked-only tree must not block: %v", err)
	}
	m := readManifest(t, s, op.ID)
	if len(m.Worktrees) != 1 || strings.Join(m.Worktrees[0].Dirty, ",") != "scratch.go" {
		t.Fatalf("manifest worktrees = %+v, want dirty path scratch.go", m.Worktrees)
	}
}

func readManifest(t *testing.T, s *Store, opID string) HandoffManifest {
	t.Helper()
	var path string
	if err := s.DB.QueryRow(`SELECT COALESCE(manifest_path, '') FROM agent_operations WHERE id = ?`, opID).Scan(&path); err != nil || path == "" {
		t.Fatalf("no manifest recorded: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m HandoffManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(fmt.Errorf("manifest: %w", err))
	}
	return m
}
