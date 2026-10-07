package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

func seedRefWorktree(t *testing.T, s *Store) {
	t.Helper()
	_, w, _ := worker(t, s)
	it, err := s.Items.Get(context.Background(), "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.Now())
	mustExec(t, s.DB, `INSERT INTO repos (id, name, path, default_branch, source, created_at, updated_at)
		VALUES ('repo_ref', 'refrepo', '/tmp/refrepo', 'main', 'manual', ?, ?)`, now, now)
	mustExec(t, s.DB, `INSERT INTO worktrees (id, repo_id, path, branch, base_ref, base_sha, state, owner_agent_id, root_item_id, created_at)
		VALUES ('wt_ref', 'repo_ref', '/tmp/ref-wt', 'b', 'main', 'deadbee', 'active', ?, ?, ?)`, w.ID, it.RootID, now)
}

func TestResolveWorktreeRef(t *testing.T) {
	s, _, _ := newStore(t)
	seedRefWorktree(t, s)
	ctx := context.Background()
	for _, ref := range []string{"wt_ref", "/tmp/ref-wt"} {
		id, err := s.ResolveWorktreeRef(ctx, ref)
		if err != nil || id != "wt_ref" {
			t.Fatalf("ResolveWorktreeRef(%q) = %q, %v; want wt_ref", ref, id, err)
		}
	}
	_, err := s.ResolveWorktreeRef(ctx, "/tmp/nope")
	want := "Unknown worktree /tmp/nope. Pass the worktree id or path that swarm_worktree create returned."
	if err == nil || err.Error() != want {
		t.Fatalf("unknown ref error = %v; want %q", err, want)
	}
}

func TestBriefWorktreesUnknownRef(t *testing.T) {
	s, _, _ := newStore(t)
	seedRefWorktree(t, s)
	_, err := s.BriefWorktrees(context.Background(), []WorkflowWorktree{{WorktreeID: "wt_missing", Mode: "rw"}})
	if err == nil || !strings.Contains(err.Error(), "Unknown worktree wt_missing") {
		t.Fatalf("BriefWorktrees unknown = %v; want Unknown worktree error", err)
	}
}
