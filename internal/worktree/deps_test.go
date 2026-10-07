package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// depsRepo is gitRepo plus a committed lockfile and .gitignore, and installed
// (ignored) node_modules at depth 0 and 2 in the primary checkout.
func depsRepo(t *testing.T) (repo, sha string) {
	t.Helper()
	repo = gitRepo(t)
	write := func(rel, body string) {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "node_modules\n")
	write("package-lock.json", "lock-v1\n")
	run(t, repo, "add", ".gitignore", "package-lock.json")
	run(t, repo, "-c", "commit.gpgsign=false", "commit", "-m", "deps")
	write("node_modules/pkg/index.js", "x\n")
	write("apps/web/node_modules/dep/index.js", "y\n")
	return repo, strings.TrimSpace(run(t, repo, "rev-parse", "HEAD"))
}

func TestReviewClonesNodeModulesWhenLockfilesMatch(t *testing.T) {
	repo, sha := depsRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Review(ctx, CreateInput{RepoID: repoID, RepoPath: repo, OwnerAgentID: "agt_1", RootItemID: "itm_1"}, sha)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"node_modules/pkg/index.js", "apps/web/node_modules/dep/index.js"} {
		if _, err := os.Stat(filepath.Join(wt.Path, rel)); err != nil {
			t.Fatalf("review tree lacks %s: %v", rel, err)
		}
	}
	if dirty, err := s.DirtyStrict(ctx, wt.Path); dirty || err != nil {
		t.Fatalf("review tree with cloned deps dirty=%v err=%v, want clean", dirty, err)
	}
	if got, err := s.Remove(ctx, wt.ID, "agt_1"); err != nil || got.State != "removed" {
		t.Fatalf("remove = %+v, %v; want removed", got, err)
	}
}

func TestReviewSkipsNodeModulesWhenLockfilesDiffer(t *testing.T) {
	repo, sha := depsRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "package-lock.json"), []byte("lock-v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, repoID := newService(t, repo)
	wt, err := s.Review(context.Background(), CreateInput{RepoID: repoID, RepoPath: repo, OwnerAgentID: "agt_1", RootItemID: "itm_1"}, sha)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("node_modules stat err = %v, want not exist", err)
	}
}
