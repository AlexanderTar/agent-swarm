package worktree

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

func fixedNow() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }

// gitRepo makes a temp repo with one signed-off (unsigned) commit on main.
//
// Hermetic on purpose (Critical 1 fix round): SigningOK reads the *effective*
// git config, on purpose, because that's what git itself signs on — a global
// commit.gpgsign=true genuinely means signing is on even with no local
// override. That only makes SigningOK's tests trustworthy if the operator's
// own global/system config and GPG keyring can never leak in and change the
// answer, so every test gets its own global/system config (empty) and its own
// GNUPGHOME, and a test that wants a global value sets GIT_CONFIG_GLOBAL itself.
func gitRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GNUPGHOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "true"},
	} {
		run(t, dir, args...)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "README.md")
	run(t, dir, "-c", "commit.gpgsign=false", "commit", "-m", "init")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newService returns a service on a migrated temp DB with the repo, agent and
// item rows the foreign keys need.
func newService(t *testing.T, repoPath string) (*Service, string) {
	t.Helper()
	d := dbtest.Open(t)
	ctx := context.Background()
	_, err := d.ExecContext(ctx, `
		INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'Root', 'in_progress', 1, 1);
		INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_1', 'orch', 'fake', 'm', 'orchestrator', 'itm_1', 'itm_1', '', 'active', 1);
		INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_2', 'coder', 'fake', 'm', 'coder', 'itm_1', 'itm_1', '', 'active', 1);
		INSERT INTO repos (id, path, name, source, created_at, updated_at)
		VALUES ('repo_1', ?, 'proj', 'scan', 1, 1);`, repoPath)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{DB: d, Run: execx.Run, Now: fixedNow, Log: func(string, ...any) {}}, "repo_1"
}

func TestCreateMakesASiblingWorktreeAndRecordsTheBase(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	wt, err := s.Create(context.Background(), CreateInput{RepoID: repoID, RepoPath: repo,
		Branch: "task/task-101-login-form", OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(repo), "proj--task-101-login-form")
	if wt.Path != want {
		t.Fatalf("path = %q, want %q", wt.Path, want)
	}
	if fi, err := os.Stat(wt.Path); err != nil || !fi.IsDir() {
		t.Fatalf("worktree folder missing: %v", err)
	}
	if wt.BaseSHA == "" || wt.BaseRef == "" || wt.State != "active" {
		t.Fatalf("worktree = %+v", wt)
	}
	// the owner holds a reservation from the start (C4)
	var mode string
	if err := s.DB.QueryRowContext(context.Background(),
		`SELECT mode FROM worktree_reservations WHERE worktree_id = ? AND agent_id = 'agt_1'`, wt.ID).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "rw" {
		t.Fatalf("owner reservation mode = %q", mode)
	}
}

func TestCreateSuffixesACollidingPath(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	in := CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/task-101-login-form",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"}
	first, err := s.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Branch = "bug/task-101-login-form" // same slug after the type prefix is dropped
	second, err := s.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if second.Path != first.Path+"-2" {
		t.Fatalf("second path = %q, want %q", second.Path, first.Path+"-2")
	}
}

func TestCreateReusesAnExistingBranchWithoutDashB(t *testing.T) {
	repo := gitRepo(t)
	run(t, repo, "branch", "task/existing")
	s, repoID := newService(t, repo)
	fake := &execx.Fake{Responses: map[string]execx.Result{}}
	s.Run = recordingRunner(fake, execx.Run)
	if _, err := s.Create(context.Background(), CreateInput{RepoID: repoID, RepoPath: repo,
		Branch: "task/existing", OwnerAgentID: "agt_1", RootItemID: "itm_1"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, "worktree add") && strings.Contains(c, " -b ") {
			t.Fatalf("existing branch must not get -b: %s", c)
		}
	}
}

func TestRemoveKeepsADirtyWorktree(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/dirty",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "dirty" {
		t.Fatalf("worktree = %+v, want retained/dirty", out)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("a retained worktree must still exist: %v", err)
	}
}

// A git failure must read as dirty, never as clean (P1 carry: repos.Dirty is unsafe here).
func TestRemoveTreatsAGitFailureAsDirty(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/x",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[2] == "status" {
			return nil, errors.New("fatal: not a git repository")
		}
		return execx.Run(ctx, name, args...)
	}
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "dirty" {
		t.Fatalf("worktree = %+v, want retained/dirty on a git error", out)
	}
}

func TestRemoveKeepsAnUnmergedBranch(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/unmerged",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt.Path, "add", "f.txt")
	run(t, wt.Path, "-c", "commit.gpgsign=false", "commit", "-m", "work")
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "unmerged" {
		t.Fatalf("worktree = %+v, want retained/unmerged", out)
	}
}

// The "pushed" half of mergedOrPushed (upstream == HEAD) is the only guard
// between a committed-but-unmerged branch and deletion once merge-base fails
// (Important 2); it had no coverage at all.
func TestRemoveDeletesAPushedUnmergedBranch(t *testing.T) {
	repo := gitRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	run(t, repo, "clone", "--bare", repo, origin)
	run(t, repo, "remote", "add", "origin", origin)
	run(t, repo, "fetch", "origin")
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/pushed",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt.Path, "add", "f.txt")
	run(t, wt.Path, "-c", "commit.gpgsign=false", "commit", "-m", "work")
	run(t, wt.Path, "push", "-u", "origin", "task/pushed")
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed (pushed counts as safe)", out)
	}
}

// A branch pushed once, then amended with a further local commit that was
// never pushed, must still be retained: the upstream no longer equals HEAD.
func TestRemoveKeepsABranchWithAnUnpushedCommitAfterAPush(t *testing.T) {
	repo := gitRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	run(t, repo, "clone", "--bare", repo, origin)
	run(t, repo, "remote", "add", "origin", origin)
	run(t, repo, "fetch", "origin")
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/pushed-then-more",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt.Path, "add", "f.txt")
	run(t, wt.Path, "-c", "commit.gpgsign=false", "commit", "-m", "work")
	run(t, wt.Path, "push", "-u", "origin", "task/pushed-then-more")
	if err := os.WriteFile(filepath.Join(wt.Path, "g.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt.Path, "add", "g.txt")
	run(t, wt.Path, "-c", "commit.gpgsign=false", "commit", "-m", "more work, not pushed")
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "unmerged" {
		t.Fatalf("worktree = %+v, want retained/unmerged", out)
	}
}

func TestRemoveDeletesAMergedCleanWorktreeAndNeverForces(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/clean",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	real := s.Run
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return real(ctx, name, args...)
	}
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed", out)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("the folder should be gone: %v", err)
	}
	for _, c := range calls {
		if strings.Contains(c, "--force") {
			t.Fatalf("--force is never passed: %s", c)
		}
	}
}

func TestRemoveRefusesANonOwnerAndAnUnreleasedShare(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/shared",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(ctx, wt.ID, "agt_2"); err == nil {
		t.Fatal("only the owner may remove a worktree")
	}
	if err := s.Share(ctx, wt.ID, "agt_2", "ro"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(ctx, wt.ID, "agt_1"); err == nil {
		t.Fatal("an unreleased reservation must block removal (C4)")
	}
	if err := s.Release(ctx, wt.ID, "agt_2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(ctx, wt.ID, "agt_1"); err != nil {
		t.Fatalf("after release it should remove: %v", err)
	}
}

// The C4 guard must fully exclude a concurrent Share, not just check-then-race
// (Important 4): a Share landing between the "no other reservations" read and
// the eventual deletion must never succeed. Rig s.Run so the moment Remove
// reaches DirtyStrict's "status" call — i.e. once Remove already holds
// lockFor(wt.ID) and has passed its guard — a concurrent Share fires. Share
// blocks on the same per-worktree lock until Remove finishes, then sees the
// worktree is no longer active and is refused: it can never observe (or act
// on) an in-between state.
func TestShareRefusesAConcurrentClaimRace(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/race",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(chan struct{})
	shareErr := make(chan error, 1)
	real := s.Run
	var once sync.Once
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[2] == "status" {
			once.Do(func() { close(claimed) })
		}
		return real(ctx, name, args...)
	}
	go func() {
		<-claimed
		shareErr <- s.Share(context.Background(), wt.ID, "agt_2", "ro")
	}()
	if _, err := s.Remove(ctx, wt.ID, "agt_1"); err != nil {
		t.Fatal(err)
	}
	if err := <-shareErr; err == nil {
		t.Fatal("a Share racing the removal's claim must be refused")
	}
}

// retained_reason is a documented value set (spec:515: dirty|unmerged|
// remove_failed, or empty), not a free-text field — the board and menubar
// render it to the user. Exercise every path that writes it and read the
// persisted column back, so nothing (e.g. an internal sentinel) can leak into
// a field people see.
func TestRetainedReasonIsAlwaysADocumentedValue(t *testing.T) {
	documented := map[string]bool{"": true, "dirty": true, "unmerged": true, "remove_failed": true}
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()

	dirty, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/reason-dirty",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirty.Path, "x"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(ctx, dirty.ID, "agt_1"); err != nil {
		t.Fatal(err)
	}

	unmerged, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/reason-unmerged",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unmerged.Path, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, unmerged.Path, "add", "f.txt")
	run(t, unmerged.Path, "-c", "commit.gpgsign=false", "commit", "-m", "work")
	if _, err := s.Remove(ctx, unmerged.ID, "agt_1"); err != nil {
		t.Fatal(err)
	}

	failed, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/reason-failed",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	real := s.Run
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 3 && args[2] == "worktree" && args[3] == "remove" {
			return nil, errors.New("boom: simulated git worktree remove failure")
		}
		return real(ctx, name, args...)
	}
	if _, err := s.Remove(ctx, failed.ID, "agt_1"); err != nil {
		t.Fatal(err)
	}
	s.Run = real

	clean, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/reason-clean",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(ctx, clean.ID, "agt_1"); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{dirty.ID: "dirty", unmerged.ID: "unmerged", failed.ID: "remove_failed", clean.ID: ""}
	for wtID, wantReason := range want {
		var reason sql.NullString
		if err := s.DB.QueryRowContext(ctx, `SELECT retained_reason FROM worktrees WHERE id = ?`, wtID).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		if !documented[reason.String] {
			t.Errorf("worktree %s: retained_reason = %q is not a documented value", wtID, reason.String)
		}
		if reason.String != wantReason {
			t.Errorf("worktree %s: retained_reason = %q, want %q", wtID, reason.String, wantReason)
		}
	}
}

func TestReviewCreatesADetachedWorktreeAtTheSHA(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	sha := strings.TrimSpace(run(t, repo, "rev-parse", "HEAD"))
	wt, err := s.Review(ctx, CreateInput{RepoID: repoID, RepoPath: repo,
		OwnerAgentID: "agt_1", RootItemID: "itm_1"}, sha)
	if err != nil {
		t.Fatal(err)
	}
	if wt.Branch != "" || wt.DetachedSHA != sha {
		t.Fatalf("worktree = %+v, want detached at %s", wt, sha)
	}
	if !strings.HasSuffix(wt.Path, "proj--review-"+sha[:7]) {
		t.Fatalf("path = %q", wt.Path)
	}
	if _, err := s.Remove(ctx, wt.ID, "agt_1"); err != nil {
		t.Fatalf("a clean detached worktree removes cleanly: %v", err)
	}
}

func TestSweepRemovesEveryActiveWorktreeOfTheRoot(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	for _, br := range []string{"task/a", "task/b"} {
		if _, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: br,
			OwnerAgentID: "agt_1", RootItemID: "itm_1"}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.Sweep(ctx, "itm_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("swept %d worktrees, want 2", len(out))
	}
	for _, wt := range out {
		if wt.State != "removed" {
			t.Errorf("%s state = %s", wt.Path, wt.State)
		}
	}
}

func TestOnRetainedFires(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	var seen []string
	s.OnRetained = func(ctx context.Context, tx *sql.Tx, wt Worktree) error {
		seen = append(seen, wt.RetainedReason)
		return nil
	}
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/d",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt.Path, "x"), []byte("y"), 0o644)
	if _, err := s.Remove(ctx, wt.ID, "agt_1"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "dirty" {
		t.Fatalf("OnRetained saw %v", seen)
	}
}

// A remote that tracks the default branch makes defaultBase prefer
// "origin/<branch>" over the bare local name.
func TestDefaultBaseUsesOriginWhenARemoteTracksIt(t *testing.T) {
	repo := gitRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	run(t, repo, "clone", "--bare", repo, origin)
	run(t, repo, "remote", "add", "origin", origin)
	run(t, repo, "fetch", "origin")
	s, repoID := newService(t, repo)
	wt, err := s.Create(context.Background(), CreateInput{RepoID: repoID, RepoPath: repo,
		Branch: "task/uses-origin", OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if wt.BaseRef != "origin/main" {
		t.Fatalf("BaseRef = %q, want origin/main", wt.BaseRef)
	}
}

func TestShareRejectsAnUnknownMode(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/e",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Share(ctx, wt.ID, "agt_2", "bogus"); err == nil {
		t.Fatal("an unknown share mode must be rejected")
	}
}

func TestForAgentListsOwnedAndSharedWorktrees(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/f",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	owned, err := s.ForAgent(ctx, "agt_1")
	if err != nil || len(owned) != 1 || owned[0].ID != wt.ID {
		t.Fatalf("ForAgent(owner) = %+v, %v", owned, err)
	}
	if shared, err := s.ForAgent(ctx, "agt_2"); err != nil || len(shared) != 0 {
		t.Fatalf("ForAgent(non-participant) = %+v, %v", shared, err)
	}
	if err := s.Share(ctx, wt.ID, "agt_2", "ro"); err != nil {
		t.Fatal(err)
	}
	shared, err := s.ForAgent(ctx, "agt_2")
	if err != nil || len(shared) != 1 || shared[0].ID != wt.ID {
		t.Fatalf("ForAgent(shared) = %+v, %v", shared, err)
	}
	if err := s.Release(ctx, wt.ID, "agt_2"); err != nil {
		t.Fatal(err)
	}
	if released, err := s.ForAgent(ctx, "agt_2"); err != nil || len(released) != 0 {
		t.Fatalf("ForAgent(released) = %+v, %v", released, err)
	}
}

func TestReviewRefusesAShortSHA(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	if _, err := s.Review(context.Background(), CreateInput{RepoID: repoID, RepoPath: repo,
		OwnerAgentID: "agt_1", RootItemID: "itm_1"}, "abc"); err == nil {
		t.Fatal("a sha shorter than 7 characters must be refused")
	}
}

// Review's sha is agent-controlled input landing in a filepath.Join; a
// same-length string with path-traversal characters must be refused, not
// just a too-short one (Important 3).
func TestReviewRefusesANonHexSHA(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	if _, err := s.Review(context.Background(), CreateInput{RepoID: repoID, RepoPath: repo,
		OwnerAgentID: "agt_1", RootItemID: "itm_1"}, "../../etc"); err == nil {
		t.Fatal("a non-hex sha must be refused")
	}
}

func TestPathForWithoutASlashUsesTheWholeBranch(t *testing.T) {
	got := PathFor("/repos/proj", "standalone", func(string) bool { return false })
	if want := "/repos/proj--standalone"; got != want {
		t.Fatalf("PathFor = %q, want %q", got, want)
	}
}

func TestBranchNameWithNoTitleOmitsTheSlug(t *testing.T) {
	got := BranchName("bug", "BUG-1", "")
	if got != "bug/bug-1" {
		t.Fatalf("BranchName = %q, want bug/bug-1", got)
	}
}

func TestSigningOK(t *testing.T) {
	repo := gitRepo(t)
	s, _ := newService(t, repo)
	ctx := context.Background()
	if err := s.SigningOK(ctx, repo); err != nil {
		t.Fatalf("commit.gpgsign=true should pass: %v", err)
	}
	run(t, repo, "config", "commit.gpgsign", "false")
	err := s.SigningOK(ctx, repo)
	if err == nil || err.Error() != "Commit signing is off for proj. Enable it in git config." {
		t.Fatalf("err = %v", err)
	}
	run(t, repo, "config", "--unset", "commit.gpgsign")
	if err := s.SigningOK(ctx, repo); err == nil {
		t.Fatal("an unset commit.gpgsign is also off")
	}
}

// SigningOK must read git's *effective* config (Critical 1): a global
// commit.gpgsign=true with no local override is a real "signing is on", the
// exact setup this machine (and the spec's own P0-7 validation) runs under.
// Scoping the read to --local would report "off" here and block every spawn.
func TestSigningOKHonoursTheEffectiveGlobalConfig(t *testing.T) {
	repo := gitRepo(t) // hermetic: GIT_CONFIG_GLOBAL starts at os.DevNull
	run(t, repo, "config", "--unset", "commit.gpgsign")
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[commit]\n\tgpgsign = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	s, _ := newService(t, repo)
	if err := s.SigningOK(context.Background(), repo); err != nil {
		t.Fatalf("a global commit.gpgsign=true with no local override should pass: %v", err)
	}
}

// The golden is what ids.KebabMax("Ship the login form now please", 24) actually
// returns: it fills up to 24 characters and drops the first word that would not
// fit whole, so "now" survives and "please" does not. Verified against
// internal/ids/ids.go; do not shorten it to make a guess pass.
func TestBranchName(t *testing.T) {
	got := BranchName("epic", "EPIC-12", "Ship the login form now please")
	if got != "epic/epic-12-ship-the-login-form-now" {
		t.Fatalf("BranchName = %q", got)
	}
}

// recordingRunner records the argv into fake.Calls() and then runs the real command.
func recordingRunner(fake *execx.Fake, real execx.Runner) execx.Runner {
	rec := fake.Runner()
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		_, _ = rec(ctx, name, args...)
		return real(ctx, name, args...)
	}
}
