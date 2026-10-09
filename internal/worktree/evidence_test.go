package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fetchCounter wraps s.Run and counts `git fetch` invocations per repo path.
func fetchCounter(s *Service) map[string]int {
	fetches := map[string]int{}
	real := s.Run
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) >= 3 && args[0] == "-C" && args[2] == "fetch" {
			fetches[args[1]]++
		}
		return real(ctx, name, args...)
	}
	return fetches
}

type passEvidence struct {
	began, ended int
	dryRun       bool
	merged       bool
}

func (e *passEvidence) BeginPass(dryRun bool) { e.began++; e.dryRun = dryRun }
func (e *passEvidence) EndPass()              { e.ended++ }
func (e *passEvidence) Merged(context.Context, Worktree, string, string) bool {
	return e.merged
}

func TestPassFetchesEachRepoOncePerPass(t *testing.T) {
	repo := gitRepo(t)
	withOrigin(t, repo)
	s, _ := newService(t, repo)
	fetches := fetchCounter(s)
	ctx := context.Background()

	s.fetchOnce(ctx, repo)
	s.fetchOnce(ctx, repo)
	if fetches[repo] != 2 {
		t.Fatalf("fetches outside a pass = %d, want 2 (every check fetches afresh)", fetches[repo])
	}

	s.BeginPass(false)
	s.fetchOnce(ctx, repo)
	s.fetchOnce(ctx, repo)
	if fetches[repo] != 3 {
		t.Fatalf("fetches in a pass = %d, want 3 (one more, then cached)", fetches[repo])
	}
	s.BeginPass(false)
	s.fetchOnce(ctx, repo)
	if fetches[repo] != 4 {
		t.Fatalf("fetches after a new pass = %d, want 4", fetches[repo])
	}
	s.EndPass()
	s.fetchOnce(ctx, repo)
	if fetches[repo] != 5 {
		t.Fatalf("fetches after EndPass = %d, want 5 (cache dropped)", fetches[repo])
	}
}

func TestDryRunPassNeverFetches(t *testing.T) {
	repo := gitRepo(t)
	withOrigin(t, repo)
	s, _ := newService(t, repo)
	fetches := fetchCounter(s)
	s.BeginPass(true)
	s.fetchOnce(context.Background(), repo)
	if fetches[repo] != 0 {
		t.Fatalf("dry-run pass fetched %d times, want 0", fetches[repo])
	}
	s.EndPass()
	s.fetchOnce(context.Background(), repo)
	if fetches[repo] != 1 {
		t.Fatalf("fetches after the dry-run pass = %d, want 1", fetches[repo])
	}
}

func TestPassBoundariesReachTheEvidenceSource(t *testing.T) {
	repo := gitRepo(t)
	s, _ := newService(t, repo)
	ev := &passEvidence{}
	s.Evidence = ev
	s.BeginPass(true)
	if ev.began != 1 || !ev.dryRun {
		t.Fatalf("evidence began=%d dryRun=%v, want 1/true", ev.began, ev.dryRun)
	}
	s.EndPass()
	if ev.ended != 1 {
		t.Fatalf("evidence ended=%d, want 1", ev.ended)
	}
}

func TestFetchFailureIsLoggedAndLeavesLocalRefs(t *testing.T) {
	repo := gitRepo(t) // no origin remote: fetch fails
	s, _ := newService(t, repo)
	var logged []string
	s.Log = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	s.fetchOnce(context.Background(), repo)
	if len(logged) != 1 || !strings.Contains(logged[0], "fetch") || !strings.Contains(logged[0], repo) {
		t.Fatalf("log = %q, want one fetch-failure line naming the repo", logged)
	}
}

func TestMergedElsewhereUsesEvidenceAndHandlesUnknownRepo(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/ev", OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	head, ok := s.headSHA(ctx, wt)
	if !ok || len(head) < 40 {
		t.Fatalf("headSHA = %q, %v", head, ok)
	}
	if s.mergedElsewhere(ctx, wt, head) {
		t.Fatal("no origin and no evidence: want not merged")
	}
	s.Evidence = &passEvidence{merged: true}
	if !s.mergedElsewhere(ctx, wt, head) {
		t.Fatal("evidence vouching for the head: want merged")
	}
	ghost := wt
	ghost.RepoID = "repo_missing"
	if s.mergedElsewhere(ctx, ghost, head) {
		t.Fatal("unknown repo must never count as merged")
	}
	gone := wt
	gone.Path = filepath.Join(t.TempDir(), "nope")
	if _, ok := s.headSHA(ctx, gone); ok {
		t.Fatal("headSHA of a missing path: want ok=false")
	}
}

func TestPruneRepoDropsMetadataOfAVanishedWorktree(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/prune", OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run(t, repo, "worktree", "list"), "prune") {
		t.Fatal("precondition: git still lists the vanished worktree")
	}
	if err := s.PruneRepo(ctx, repoID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(run(t, repo, "worktree", "list"), "task/prune") {
		t.Fatal("worktree metadata survived PruneRepo")
	}
	if err := s.PruneRepo(ctx, "repo_missing"); err == nil {
		t.Fatal("PruneRepo of an unknown repo: want an error")
	}
}

func TestUntrackedReportsOnlyDirectoriesNoLiveRowPointsAt(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	got, err := s.Untracked(ctx)
	if err != nil || got != nil {
		t.Fatalf("Untracked with no worktrees dir = %v, %v; want nil, nil", got, err)
	}
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/known", OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(s.worktreesDir(), "stray")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.worktreesDir(), "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = s.Untracked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{stray}) {
		t.Fatalf("Untracked = %v, want only %v (known %s and plain files skipped)", got, stray, wt.Path)
	}
}

func TestUntrackedPropagatesAnUnreadableWorktreesDir(t *testing.T) {
	repo := gitRepo(t)
	s, _ := newService(t, repo)
	// worktrees is a regular file: ReadDir fails with something other than ErrNotExist.
	if err := os.WriteFile(s.worktreesDir(), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Untracked(context.Background()); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Untracked err = %v, want a non-NotExist error", err)
	}
}

func TestListFiltersByStateAndRoot(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_2', 'EPIC-2', 'epic', 'itm_2', 'Other', 'in_progress', 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/a", OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/b", OwnerAgentID: "agt_2", RootItemID: "itm_2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE worktrees SET state = 'removed', removed_at = 5 WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}

	live, err := s.List(ctx, "", "")
	if err != nil || len(live) != 1 || live[0].ID != a.ID {
		t.Fatalf("List live = %+v, %v; want only %s", live, err, a.ID)
	}
	if live[0].RootKey != "EPIC-1" || live[0].OwnerName != "orch" || live[0].Branch != "task/a" {
		t.Fatalf("joined fields = %+v", live[0])
	}
	removed, err := s.List(ctx, "", "removed")
	if err != nil || len(removed) != 1 || removed[0].ID != b.ID {
		t.Fatalf("List removed = %+v, %v; want only %s", removed, err, b.ID)
	}
	if removed[0].RemovedAt == nil || removed[0].OwnerName != "coder" {
		t.Fatalf("removed row = %+v, want RemovedAt set and owner coder", removed[0])
	}
	scoped, err := s.List(ctx, "EPIC-2", "removed")
	if err != nil || len(scoped) != 1 || scoped[0].ID != b.ID {
		t.Fatalf("List EPIC-2 = %+v, %v", scoped, err)
	}
	none, err := s.List(ctx, "EPIC-2", "")
	if err != nil || len(none) != 0 {
		t.Fatalf("List EPIC-2 live = %+v, %v; want none", none, err)
	}
}
