package worktree_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/notify"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// TestRetainRaisesARealNotificationWithoutRollingBack is the review-round
// regression for Critical 2: OnWorktreeRetained's Args used to omit
// ROOT-KEY, the one placeholder the §17.5 worktree.retained template
// requires, and Render failing closed rolled back retain's own transaction
// (worktree.go's retain runs OnRetained inside it) — so a dirty or unmerged
// worktree at root completion never actually got marked retained in the DB
// at all, and retain returned an error up into the §12.2 final sweep.
//
// No test anywhere previously wired internal/worktree, internal/runtime and
// internal/notify together for real: internal/worktree's own
// TestOnRetainedFires stubs OnRetained, and internal/runtime's own tests
// never drive a real worktree.Service. This lives in an external package
// specifically so it can import both without the notify -> runtime ->
// worktree cycle (worktree_test depends on runtime and worktree; nothing
// depends back on worktree_test, so it isn't a cycle).
func TestRetainRaisesARealNotificationWithoutRollingBack(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GNUPGHOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "T")
	run("config", "user.email", "t@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")

	d := dbtest.Open(t)
	now := time.Now
	ev := events.New(d, now)
	ctx := context.Background()

	it := &items.Store{DB: d, Events: ev, Now: now}
	root, err := it.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Retain test"}, items.User("t"))
	if err != nil {
		t.Fatal(err)
	}

	// Minimal runtime.Store: OnWorktreeRetained only needs DB (for itemKey)
	// and Notify — no Tmux/Adapters/Items wiring required for this path.
	rt := &runtime.Store{DB: d, Notify: &notify.Service{DB: d, Events: ev, Now: now}}
	wtSvc := &worktree.Service{DB: d, Run: execx.Run, Now: now, OnRetained: rt.OnWorktreeRetained}

	if _, err := d.ExecContext(ctx, `INSERT INTO repos (id, name, path, source, created_at, updated_at)
		VALUES ('repo_t', 'proj', ?, 'manual', 0, 0)`, repo); err != nil {
		t.Fatal(err)
	}
	// worktrees.owner_agent_id is a real FK; "agt_1" needs a row to point to.
	if _, err := d.ExecContext(ctx, `INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id,
		brief, state, created_at) VALUES ('agt_1', 'test-agent', 'fake', 'fake-1', 'orchestrator', ?, ?, '', 'active', 0)`,
		root.ID, root.ID); err != nil {
		t.Fatal(err)
	}

	wt, err := wtSvc.Create(ctx, worktree.CreateInput{RepoID: "repo_t", RepoPath: repo, Branch: "task/dirty",
		OwnerAgentID: "agt_1", RootItemID: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "dirty.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := wtSvc.Remove(ctx, wt.ID, "agt_1"); err != nil {
		t.Fatalf("Remove (which retains a dirty worktree) failed: %v", err)
	}

	// The transaction committed: the worktree really is retained in the DB,
	// not rolled back by a Render failure.
	var state string
	if err := d.QueryRowContext(ctx, `SELECT state FROM worktrees WHERE id = ?`, wt.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "retained" {
		t.Fatalf("worktree state = %q, want retained", state)
	}

	// A real notification was raised, with the epic's key rendered in — not
	// a literal {ROOT-KEY}, and not silently dropped.
	var body string
	if err := d.QueryRowContext(ctx, `SELECT body FROM notifications WHERE kind = 'worktree.retained'`).Scan(&body); err != nil {
		t.Fatalf("no worktree.retained notification row: %v", err)
	}
	if !strings.Contains(body, root.Key) {
		t.Fatalf("notification body = %q, want it to contain %s", body, root.Key)
	}
	if strings.ContainsAny(body, "{}") {
		t.Fatalf("notification body = %q, has an unsubstituted placeholder", body)
	}
}
