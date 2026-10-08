package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// BUG-60: remove on an unmerged tree retains it, and the retained tree keeps
// its record and owner and can still be shared (which makes it active again).
func TestShareReactivatesARetainedWorktree(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/unmerged",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt.Path, "add", "new.txt")
	run(t, wt.Path, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "unlanded")
	got, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil || got.State != "retained" {
		t.Fatalf("Remove = %+v, %v; want retained", got, err)
	}
	kept, err := s.Get(ctx, wt.ID)
	if err != nil || kept.State != "retained" || kept.OwnerAgentID != "agt_1" {
		t.Fatalf("Get after remove = %+v, %v; want retained, owned by agt_1", kept, err)
	}
	if err := s.Share(ctx, wt.ID, "agt_2", "rw"); err != nil {
		t.Fatalf("Share on retained tree = %v, want nil", err)
	}
	after, err := s.Get(ctx, wt.ID)
	if err != nil || after.State != "active" || after.RetainedReason != "" || after.OwnerAgentID != "agt_1" {
		t.Fatalf("Get after share = %+v, %v; want active, no reason, owned by agt_1", after, err)
	}
}
