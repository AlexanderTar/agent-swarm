package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// seedCleanupHome builds a home with a finished agent that owns a work dir and
// a real worktree, plus an orphan work dir. Both agents finished long ago.
func seedCleanupHome(t *testing.T) (home, agentDir, orphanDir, wtPath string) {
	t.Helper()
	home = t.TempDir()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(home, "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	for _, q := range []string{
		`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at) VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'e', 'in_progress', 1, 2)`,
		`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at, finished_at) VALUES ('agt_1', 'done-agent', 'fake', 'fake-1', 'orchestrator', 'itm_1', 'itm_1', 'b', 'finished', 3, 4)`,
	} {
		if _, err := d.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO repos (id, path, name, default_branch, source, created_at, updated_at) VALUES ('repo_1', ?, 'r', 'main', 'manual', 1, 1)`, repo); err != nil {
		t.Fatal(err)
	}
	wt := &worktree.Service{DB: d, Run: execx.Run, Now: time.Now, Home: home}
	w, err := wt.Create(ctx, worktree.CreateInput{RepoID: "repo_1", RepoPath: repo, Branch: "task/x", OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	agentDir = filepath.Join(home, "work", "done-agent")
	orphanDir = filepath.Join(home, "work", "ghost")
	for _, p := range []string{agentDir, orphanDir} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return home, agentDir, orphanDir, w.Path
}

func TestCleanupDryRunListsBothKindsAndDeletesNothing(t *testing.T) {
	home, agentDir, orphanDir, wtPath := seedCleanupHome(t)
	var out bytes.Buffer
	if code := run([]string{"cleanup", "--home", home, "--dry-run", "--no-grace"}, &out, &out); code != 0 {
		t.Fatalf("exit = %d, output = %q", code, out.String())
	}
	for _, want := range []string{"workdir would_remove " + agentDir, "workdir would_remove " + orphanDir, "worktree would_remove " + wtPath} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	for _, p := range []string{agentDir, orphanDir, wtPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("dry run deleted %s", p)
		}
	}
}

func TestCleanupRemovesAndNoGraceIsRequiredForFreshOrphans(t *testing.T) {
	home, agentDir, orphanDir, wtPath := seedCleanupHome(t)
	var out bytes.Buffer
	// Without --no-grace the agent is long finished (removed), but the fresh orphan stays.
	if code := run([]string{"cleanup", "--home", home}, &out, &out); code != 0 {
		t.Fatalf("exit = %d, output = %q", code, out.String())
	}
	if _, err := os.Stat(orphanDir); err != nil {
		t.Fatalf("fresh orphan removed without --no-grace:\n%s", out.String())
	}
	if _, err := os.Stat(agentDir); err == nil {
		t.Fatalf("finished agent's dir should be removed:\n%s", out.String())
	}
	if _, err := os.Stat(wtPath); err == nil {
		t.Fatalf("finished agent's worktree should be removed:\n%s", out.String())
	}
	out.Reset()
	if code := run([]string{"cleanup", "--home", home, "--no-grace"}, &out, &out); code != 0 {
		t.Fatalf("exit = %d, output = %q", code, out.String())
	}
	if _, err := os.Stat(orphanDir); err == nil {
		t.Fatalf("--no-grace should remove the fresh orphan:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "removed") {
		t.Fatalf("no summary in output:\n%s", out.String())
	}
}

func TestCleanupDiscardRefusesActiveRootThenDiscardsDoneRootKeepingTheBranch(t *testing.T) {
	home, _, _, wtPath := seedCleanupHome(t)
	var out, errb bytes.Buffer
	if code := run([]string{"cleanup", "--home", home, "--discard", wtPath}, &out, &errb); code == 0 {
		t.Fatalf("discard under an in-progress root exited 0:\n%s", out.String())
	}
	if !strings.Contains(errb.String(), "EPIC-1") || !strings.Contains(errb.String(), "in progress") {
		t.Fatalf("stderr = %q, want the root and its status", errb.String())
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("refused discard removed the tree: %v", err)
	}

	d, err := db.Open(context.Background(), filepath.Join(home, "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(context.Background(), `UPDATE items SET status = 'done' WHERE id = 'itm_1'`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	out.Reset()
	errb.Reset()
	if code := run([]string{"cleanup", "--home", home, "--discard", wtPath}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "task/x") || !strings.Contains(out.String(), "0 commits") {
		t.Fatalf("output = %q, want the kept branch and unlanded count", out.String())
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("tree still exists: %v", err)
	}
	if code := run([]string{"cleanup", "--home", home, "--discard", "/nope"}, &out, &errb); code == 0 {
		t.Fatal("discard of an unknown path exited 0")
	}
}
