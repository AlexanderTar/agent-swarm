package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// evidenceFixture is a store whose one repo has a bare origin with main
// pushed, the merge evidence wired in, and gh answered by ghOut/ghErr (git
// still runs for real).
type evidenceFixture struct {
	s        *Store
	epic     items.Item
	repoID   string
	repoPath string
	bare     string
	ghOut    string
	ghErr    error
}

func newEvidenceFixture(t *testing.T) *evidenceFixture {
	t.Helper()
	s, _, _ := clockStore(t)
	f := &evidenceFixture{s: s, epic: seedEpicWithTask(t, s)}
	f.repoID = seedRepo(t, s, "proj")
	f.repoPath = repoPathFor(t, s, f.repoID)
	f.bare = filepath.Join(t.TempDir(), "origin.git")
	gitOutput(t, filepath.Dir(f.bare), "init", "--bare", "-b", "main", f.bare)
	gitOutput(t, f.repoPath, "remote", "add", "origin", f.bare)
	gitOutput(t, f.repoPath, "push", "origin", "main")
	gitOutput(t, f.repoPath, "fetch", "origin")
	mustExec(t, s.DB, `UPDATE repos SET remote_url = 'https://github.com/acme/proj.git', remote_owner = 'acme' WHERE id = ?`, f.repoID)
	seedReclaimAgent(t, s, "owner_1", f.epic.ID, "")
	finishReclaimAgent(t, s, "owner_1", s.Now())
	s.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "gh" {
			want := "pr list --repo acme/proj --state merged --limit 1000 --json number,headRefName,headRefOid"
			if got := strings.Join(args, " "); got != want {
				return nil, fmt.Errorf("unexpected gh args: %s", got)
			}
			return []byte(f.ghOut), f.ghErr
		}
		return execx.Run(ctx, name, args...)
	}
	s.Worktree.Evidence = s.MergeEvidence()
	return f
}

// tree makes a worktree on branch with one commit that no ref contains.
func (f *evidenceFixture) tree(t *testing.T, branch string) worktree.Worktree {
	t.Helper()
	wt := seedReclaimWorktree(t, f.s, f.repoID, f.repoPath, branch, "owner_1", f.epic.ID)
	commitFile(t, wt.Path, "f.txt", "work")
	return wt
}

func (f *evidenceFixture) reclaim(t *testing.T, wt worktree.Worktree) (string, string) {
	t.Helper()
	out, err := f.s.Worktree.ReclaimOne(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	return out.State, out.RetainedReason
}

// A retained/unmerged row is a reclaim candidate. Before the squash the
// pass keeps it; after the same content is on origin/main (HEAD is not an
// ancestor) the next pass removes it.
func TestReclaimWorktreesRemovesARetainedTreeAfterASquashLands(t *testing.T) {
	f := newEvidenceFixture(t)
	wt := f.tree(t, "task/retained-squash")
	mustExec(t, f.s.DB, `UPDATE worktrees SET state = 'retained', retained_reason = 'unmerged' WHERE id = ?`, wt.ID)
	f.ghErr = errors.New("gh unavailable")
	if _, err := f.s.ReclaimWorktreesWith(context.Background(), CleanupOptions{NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	if state, reason := reclaimWorktreeState(t, f.s, wt.ID); state != "retained" || reason != "unmerged" {
		t.Fatalf("before the squash: state/reason = %s/%s, want retained/unmerged", state, reason)
	}
	// One commit, message "land", with later.txt as well as f.txt. Two
	// commitFile calls can recreate the branch commit (same tree, parent,
	// and message in the same second), which would make HEAD an ancestor.
	if err := os.WriteFile(filepath.Join(f.repoPath, "f.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repoPath, "later.txt"), []byte("later"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, f.repoPath, "add", "f.txt", "later.txt")
	gitOutput(t, f.repoPath, "-c", "commit.gpgsign=false", "commit", "-m", "land")
	gitOutput(t, f.repoPath, "push", "-q", "origin", "main")
	head := strings.TrimSpace(gitOutput(t, wt.Path, "rev-parse", "HEAD"))
	anc := exec.Command("git", "merge-base", "--is-ancestor", head, "refs/remotes/origin/main")
	anc.Dir = wt.Path
	if err := anc.Run(); err == nil {
		t.Fatal("precondition: a squashed HEAD is not an ancestor of origin/main")
	}
	if _, err := f.s.ReclaimWorktreesWith(context.Background(), CleanupOptions{NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	if state, reason := reclaimWorktreeState(t, f.s, wt.ID); state != "removed" || reason != "" {
		t.Fatalf("after the squash: state/reason = %s/%s, want removed", state, reason)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("path still exists: %v", err)
	}
}

func TestReclaimUsesMergedItemMergeHeadBranch(t *testing.T) {
	f := newEvidenceFixture(t)
	wt := f.tree(t, "task/via-item-merge")
	head := strings.TrimSpace(gitOutput(t, wt.Path, "rev-parse", "HEAD"))
	gitOutput(t, f.repoPath, "branch", "swarm/integ", head)
	// integrated checkpoint row the item_merges FK needs
	seedReclaimSession(t, f.s, "ses_1", "owner_1", Completed)
	mustExec(t, f.s.DB, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary,
		next_json, blockers_json, git_json, verify_json, artifacts_json, processed_json, findings_json, daemon_written, created_at)
		VALUES ('ckp_1', 'ses_1', 'owner_1', ?, 'progress', 1, 's', '[]', '[]', '[]', '[]', '[]', '[]', '[]', 0, ?)`,
		f.epic.ID, db.Millis(f.s.Now()))
	mustExec(t, f.s.DB, `INSERT INTO item_merges (id, item_id, integrated_checkpoint, repo, repo_id, kind, url, number, base, head, state, created_at)
		VALUES ('mrg_1', ?, 'ckp_1', 'proj', ?, 'pr', 'https://github.com/acme/proj/pull/9', 9, 'main', 'swarm/integ', 'open', 1)`,
		f.epic.ID, f.repoID)
	f.ghErr = errors.New("gh unavailable")
	if state, reason := f.reclaim(t, wt); state != "retained" || reason != "unmerged" {
		t.Fatalf("open merge row: state/reason = %s/%s, want retained/unmerged", state, reason)
	}
	mustExec(t, f.s.DB, `UPDATE item_merges SET state = 'merged' WHERE id = 'mrg_1'`)
	if state, _ := f.reclaim(t, wt); state != "removed" {
		t.Fatalf("merged row whose head branch contains HEAD: state = %s, want removed", state)
	}
}

func TestReclaimUsesAMergedPRHeadOIDFetchedFromPullRef(t *testing.T) {
	f := newEvidenceFixture(t)
	wt := f.tree(t, "task/squashed")
	head := strings.TrimSpace(gitOutput(t, wt.Path, "rev-parse", "HEAD"))
	// The PR head one commit past the tree's HEAD exists only on the remote's
	// refs/pull/7/head, as after a squash merge with the branch deleted.
	gitOutput(t, f.repoPath, "push", "origin", head+":refs/heads/tmp")
	clone := filepath.Join(t.TempDir(), "clone")
	gitOutput(t, filepath.Dir(clone), "clone", "-q", "-b", "tmp", f.bare, clone)
	gitOutput(t, clone, "config", "user.email", "t@example.invalid")
	gitOutput(t, clone, "config", "user.name", "T")
	commitFile(t, clone, "late.txt", "late")
	oid := strings.TrimSpace(gitOutput(t, clone, "rev-parse", "HEAD"))
	gitOutput(t, clone, "push", "-q", "origin", oid+":refs/pull/7/head")
	gitOutput(t, f.repoPath, "push", "origin", ":refs/heads/tmp")
	f.ghOut = fmt.Sprintf(`[{"number":7,"headRefName":"task/squashed","headRefOid":%q}]`, oid)
	if state, _ := f.reclaim(t, wt); state != "removed" {
		t.Fatalf("state = %s, want removed: HEAD is contained in merged PR #7's head", state)
	}
}

func TestReclaimTreatsAGhFailureAsNoEvidence(t *testing.T) {
	f := newEvidenceFixture(t)
	wt := f.tree(t, "task/gh-down")
	f.ghErr = errors.New("gh: not logged in")
	if state, reason := f.reclaim(t, wt); state != "retained" || reason != "unmerged" {
		t.Fatalf("state/reason = %s/%s, want retained/unmerged", state, reason)
	}
}

// A reclaim pass caches fetches; once the pass ends, a direct ReclaimOne must
// fetch afresh and see what was pushed since.
func TestReclaimOneFetchesAfreshAfterAPassEnds(t *testing.T) {
	f := newEvidenceFixture(t)
	wt := f.tree(t, "task/stale-cache")
	f.ghErr = errors.New("gh unavailable")
	if _, err := f.s.ReclaimWorktreesWith(context.Background(), CleanupOptions{NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	if state, reason := reclaimWorktreeState(t, f.s, wt.ID); state != "active" && state != "retained" {
		t.Fatalf("after the pass: state/reason = %s/%s, want unmerged and kept", state, reason)
	}
	head := strings.TrimSpace(gitOutput(t, wt.Path, "rev-parse", "HEAD"))
	oldMain := strings.TrimSpace(gitOutput(t, f.repoPath, "rev-parse", "refs/remotes/origin/main"))
	gitOutput(t, f.repoPath, "push", "origin", head+":refs/heads/main")
	// push also moves the local tracking ref; put it back so only a fetch can fix it
	gitOutput(t, f.repoPath, "update-ref", "refs/remotes/origin/main", oldMain)
	if state, _ := f.reclaim(t, wt); state != "removed" {
		t.Fatalf("state = %s, want removed: HEAD is now in origin/main and a direct call must re-fetch", state)
	}
}

// gh output is untrusted: a headRefOid that is not a full hex object id must
// never reach git as an argument.
func TestReclaimIgnoresANonHexHeadRefOid(t *testing.T) {
	f := newEvidenceFixture(t)
	wt := f.tree(t, "task/bad-oid")
	const bad = "--upload-pack=touch-pwned"
	f.ghOut = fmt.Sprintf(`[{"number":3,"headRefName":"task/bad-oid","headRefOid":%q}]`, bad)
	inner := f.s.Exec
	var leaked bool
	f.s.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		for _, a := range args {
			if strings.Contains(a, bad) {
				leaked = true
			}
		}
		return inner(ctx, name, args...)
	}
	if state, reason := f.reclaim(t, wt); state != "retained" || reason != "unmerged" {
		t.Fatalf("state/reason = %s/%s, want retained/unmerged", state, reason)
	}
	if leaked {
		t.Fatal("a non-hex headRefOid was passed to git")
	}
}

// HEAD in no ref and no merged PR (gh answers, with nothing): retained unmerged.
func TestReclaimRetainsATreeWithNoMergeEvidence(t *testing.T) {
	f := newEvidenceFixture(t)
	wt := f.tree(t, "task/no-evidence")
	f.ghOut = `[]`
	if state, reason := f.reclaim(t, wt); state != "retained" || reason != "unmerged" {
		t.Fatalf("state/reason = %s/%s, want retained/unmerged", state, reason)
	}
}

// A task branch merged (true merge) into an integration branch whose PR was
// merged counts as merged, though the PR's head branch is a different one.
func TestReclaimUsesAnIntegrationPRHeadContainingHEAD(t *testing.T) {
	f := newEvidenceFixture(t)
	wt := f.tree(t, "task/via-integration")
	head := strings.TrimSpace(gitOutput(t, wt.Path, "rev-parse", "HEAD"))
	gitOutput(t, f.repoPath, "branch", "integ/tmp", "origin/main")
	integ := filepath.Join(t.TempDir(), "integ")
	gitOutput(t, f.repoPath, "worktree", "add", "-q", integ, "integ/tmp")
	gitOutput(t, integ, "config", "user.email", "t@example.invalid")
	gitOutput(t, integ, "config", "user.name", "T")
	commitFile(t, integ, "other.txt", "other")
	gitOutput(t, integ, "merge", "--no-ff", "-q", "-m", "merge task", head)
	oid := strings.TrimSpace(gitOutput(t, integ, "rev-parse", "HEAD"))
	f.ghOut = fmt.Sprintf(`[{"number":589,"headRefName":"integ/tmp","headRefOid":%q}]`, oid)
	if state, _ := f.reclaim(t, wt); state != "removed" {
		t.Fatalf("state = %s, want removed: HEAD is contained in merged integration PR #589's head", state)
	}
}

// A merged PR on another branch that does not contain HEAD is no evidence.
func TestReclaimRetainsATreeNotInAnyMergedPRHead(t *testing.T) {
	f := newEvidenceFixture(t)
	wt := f.tree(t, "task/not-in-integration")
	gitOutput(t, f.repoPath, "branch", "integ/other", "origin/main")
	oid := strings.TrimSpace(gitOutput(t, f.repoPath, "rev-parse", "integ/other"))
	f.ghOut = fmt.Sprintf(`[{"number":590,"headRefName":"integ/other","headRefOid":%q}]`, oid)
	if state, reason := f.reclaim(t, wt); state != "retained" || reason != "unmerged" {
		t.Fatalf("state/reason = %s/%s, want retained/unmerged", state, reason)
	}
}

// A dry run must not touch the network or write the repo: no git fetch at all
// (origin or refs/pull), and worktree status is read without the index lock.
func TestReclaimDryRunRunsNoGitFetch(t *testing.T) {
	f := newEvidenceFixture(t)
	wt := f.tree(t, "task/dry")
	head := strings.TrimSpace(gitOutput(t, wt.Path, "rev-parse", "HEAD"))
	gitOutput(t, f.repoPath, "push", "origin", head+":refs/heads/tmp")
	clone := filepath.Join(t.TempDir(), "clone")
	gitOutput(t, filepath.Dir(clone), "clone", "-q", "-b", "tmp", f.bare, clone)
	gitOutput(t, clone, "config", "user.email", "t@example.invalid")
	gitOutput(t, clone, "config", "user.name", "T")
	commitFile(t, clone, "late.txt", "late")
	oid := strings.TrimSpace(gitOutput(t, clone, "rev-parse", "HEAD"))
	gitOutput(t, clone, "push", "-q", "origin", oid+":refs/pull/7/head")
	gitOutput(t, f.repoPath, "push", "origin", ":refs/heads/tmp")
	f.ghOut = fmt.Sprintf(`[{"number":7,"headRefName":"task/dry","headRefOid":%q}]`, oid)
	inner := f.s.Exec
	var calls []string
	f.s.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return inner(ctx, name, args...)
	}
	innerWT := f.s.Worktree.Run
	f.s.Worktree.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return innerWT(ctx, name, args...)
	}
	if _, err := f.s.ReclaimWorktreesWith(context.Background(), CleanupOptions{DryRun: true, NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	sawStatus := false
	for _, c := range calls {
		if strings.Contains(c, " fetch") {
			t.Fatalf("dry run ran %q", c)
		}
		if strings.Contains(c, "status --porcelain") {
			sawStatus = true
			if !strings.Contains(c, "--no-optional-locks status") {
				t.Fatalf("status without --no-optional-locks: %q", c)
			}
		}
	}
	if !sawStatus {
		t.Fatal("expected a git status call")
	}
}

// mergeInto merges branch into the tree at integ's path.
func mergeInto(t *testing.T, integ worktree.Worktree, branch string) {
	t.Helper()
	gitOutput(t, integ.Path, "-c", "commit.gpgsign=false", "-c", "user.email=t@example.invalid",
		"-c", "user.name=T", "merge", "--no-edit", branch)
}

// BUG-108: two clean package trees of one root, both merged into a sibling
// worktree's integration branch (not main), are both removed.
func TestReclaimTreatsAHeadInASiblingWorktreeBranchAsMerged(t *testing.T) {
	f := newEvidenceFixture(t)
	f.ghErr = errors.New("gh unavailable")
	integ := seedReclaimWorktree(t, f.s, f.repoID, f.repoPath, "chore/integ", "owner_1", f.epic.ID)
	a, b := f.tree(t, "task/a"), f.tree(t, "task/b")
	// distinct commits: identical content in the same second would share a SHA
	commitFile(t, a.Path, "a.txt", "a")
	commitFile(t, b.Path, "b.txt", "b")
	if state, reason := f.reclaim(t, a); state != "retained" || reason != "unmerged" {
		t.Fatalf("A before the merge: state/reason = %s/%s, want retained/unmerged", state, reason)
	}
	mergeInto(t, integ, "task/a")
	mergeInto(t, integ, "task/b")
	for name, wt := range map[string]worktree.Worktree{"A": a, "B": b} {
		if state, reason := f.reclaim(t, wt); state != "removed" {
			t.Fatalf("%s merged into the sibling integration branch: state/reason = %s/%s, want removed", name, state, reason)
		}
	}
	if state, _ := reclaimWorktreeState(t, f.s, integ.ID); state != "active" {
		t.Fatalf("integration tree state = %s, want active", state)
	}
}

// siblingFixture is a root with an integration tree and a package tree A
// holding one commit that no ref contains yet.
func newSiblingFixture(t *testing.T) (*evidenceFixture, worktree.Worktree, worktree.Worktree) {
	t.Helper()
	f := newEvidenceFixture(t)
	f.ghErr = errors.New("gh unavailable")
	integ := seedReclaimWorktree(t, f.s, f.repoID, f.repoPath, "chore/integ", "owner_1", f.epic.ID)
	a := f.tree(t, "task/a")
	commitFile(t, a.Path, "a.txt", "a") // its own commit: f.tree's is shared by same-second siblings
	return f, integ, a
}

func (f *evidenceFixture) wantRetainedUnmerged(t *testing.T, wt worktree.Worktree, why string) {
	t.Helper()
	if state, reason := f.reclaim(t, wt); state != "retained" || reason != "unmerged" {
		t.Fatalf("%s: state/reason = %s/%s, want retained/unmerged", why, state, reason)
	}
}

func TestReclaimRetainsATreeNoSiblingBranchContains(t *testing.T) {
	f, integ, a := newSiblingFixture(t)
	b := f.tree(t, "task/b")
	commitFile(t, b.Path, "b.txt", "b")
	mergeInto(t, integ, "task/b") // contains B, not A
	f.wantRetainedUnmerged(t, a, "HEAD in no sibling branch")
}

func TestReclaimIgnoresASiblingOfAnotherRoot(t *testing.T) {
	f, _, a := newSiblingFixture(t)
	other := seedEpicWithTask(t, f.s)
	seedReclaimAgent(t, f.s, "owner_2", other.ID, "")
	finishReclaimAgent(t, f.s, "owner_2", f.s.Now())
	integ2 := seedReclaimWorktree(t, f.s, f.repoID, f.repoPath, "chore/other-root", "owner_2", other.ID)
	mergeInto(t, integ2, "task/a")
	f.wantRetainedUnmerged(t, a, "branch of a sibling in another root")
}

func TestReclaimIgnoresASiblingOfAnotherRepo(t *testing.T) {
	f, _, a := newSiblingFixture(t)
	repo2 := seedRepo(t, f.s, "other")
	seedReclaimWorktree(t, f.s, repo2, repoPathFor(t, f.s, repo2), "chore/other-repo", "owner_1", f.epic.ID)
	// the name exists in A's repo too, holding A's commit
	gitOutput(t, f.repoPath, "branch", "chore/other-repo", "task/a")
	f.wantRetainedUnmerged(t, a, "branch of a sibling row in another repo")
}

func TestReclaimIgnoresARemovedSibling(t *testing.T) {
	f, integ, a := newSiblingFixture(t)
	mergeInto(t, integ, "task/a")
	mustExec(t, f.s.DB, `UPDATE worktrees SET state = 'removed' WHERE id = ?`, integ.ID)
	f.wantRetainedUnmerged(t, a, "branch of a removed sibling row")
}

func TestReclaimCountsARetainedSibling(t *testing.T) {
	f, integ, a := newSiblingFixture(t)
	mergeInto(t, integ, "task/a")
	mustExec(t, f.s.DB, `UPDATE worktrees SET state = 'retained', retained_reason = 'unmerged' WHERE id = ?`, integ.ID)
	if state, _ := f.reclaim(t, a); state != "removed" {
		t.Fatalf("branch of a retained sibling: state = %s, want removed", state)
	}
}

func TestReclaimTreatsADetachedHeadInASiblingBranchAsMerged(t *testing.T) {
	f, integ, _ := newSiblingFixture(t)
	sha := strings.TrimSpace(gitOutput(t, f.repoPath, "rev-parse", "HEAD"))
	rev, err := f.s.Worktree.Review(context.Background(), worktree.CreateInput{RepoID: f.repoID,
		RepoPath: f.repoPath, OwnerAgentID: "owner_1", RootItemID: f.epic.ID}, sha)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, rev.Path, "rev.txt", "rev") // moves HEAD off detached_sha
	head := strings.TrimSpace(gitOutput(t, rev.Path, "rev-parse", "HEAD"))
	f.wantRetainedUnmerged(t, rev, "detached HEAD in no branch")
	mergeInto(t, integ, head)
	if state, _ := f.reclaim(t, rev); state != "removed" {
		t.Fatalf("detached HEAD in a sibling branch: state = %s, want removed", state)
	}
}

// tddBuildSpec is a build step with a tdd gate and one review step that
// retries it. fixRoundCoder seeds the coder's run at round.
func tddBuildSpec() workflow.Spec {
	return workflow.Spec{Steps: []workflow.Step{
		{ID: "build", Run: "coder", Gates: []workflow.Gate{workflow.GateTDD}},
		{ID: "review", Review: []string{"reviewer"}, Of: "build"},
	}}
}

func (f *evidenceFixture) fixRoundCoder(t *testing.T, spec workflow.Spec, round int, units ...string) (Session, string) {
	t.Helper()
	ctx := context.Background()
	setItemWorkflow(t, f.s, "TASK-1", spec)
	if len(units) > 0 {
		setItemUnits(t, f.s, "TASK-1", units...)
	}
	// The fixture's reclaim owner is a finished orchestrator with no session,
	// which StartOrchestrator tries to recover. Drop it so a real one can start.
	if _, err := f.s.DB.ExecContext(ctx, `DELETE FROM agents WHERE id = 'owner_1'`); err != nil {
		t.Fatal(err)
	}
	orch, _, err := f.s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: f.epic.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	coder, _, err := f.s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "build it"}})
	if err != nil {
		t.Fatalf("spawn coder: %v", err)
	}
	ses, err := f.s.LatestSession(ctx, coder.ID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	it, err := f.s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	workflowID, _ := seedWorkflowRun(t, f.s, it.ID, orch.RootItemID, orch.ID, coder.ID, "build", "coder", round)
	return ses, workflowID
}

func (f *evidenceFixture) cancelBuild(t *testing.T, workflowID string, round int) {
	t.Helper()
	if _, err := f.s.DB.ExecContext(context.Background(), `INSERT INTO workflow_runs
		(id, workflow_id, step_id, round, role, state, created_at)
		VALUES (?, ?, 'build', ?, 'coder', 'cancelled', ?)`,
		ids.New("wfr"), workflowID, round, db.Millis(f.s.Now())); err != nil {
		t.Fatal(err)
	}
}

func unitPair(unit int) []Verify {
	return []Verify{
		{Cmd: "go test ./x", Phase: "red", OK: false, Unit: unit},
		{Cmd: "go test ./x", Phase: "green", OK: true, Unit: unit},
	}
}

// TASK-1419: review changes_requested at round 4 (major on unit 3), a
// cancelled build at round 5, fresh coder at round 6. The latest review
// before this run is round 4, so only unit 3 needs a new pair. An older
// major on unit 1 must not come back.
func TestFixRoundReadsLatestReviewBeforeThisRun(t *testing.T) {
	f := newEvidenceFixture(t)
	ctx := context.Background()
	ses, workflowID := f.fixRoundCoder(t, tddBuildSpec(), 6, "one", "two", "three", "four", "five")
	seedReviewFindings(t, f.s, workflowID, 2, []workflow.Finding{
		{Severity: "major", File: "a.go", Summary: "old unit 1", Unit: 1},
	})
	seedReviewFindings(t, f.s, workflowID, 4, []workflow.Finding{
		{Severity: "major", File: "c.go", Summary: "fix unit 3", Unit: 3},
	})
	f.cancelBuild(t, workflowID, 5)

	if _, err := f.s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "fixed",
		Verification: unitPair(3)}); err != nil {
		t.Fatalf("round 6 should need only unit 3 from the round-4 review: %v", err)
	}
}

// A round-4 major on unit 5 is still required when the coder only records unit 3.
func TestFixRoundLatestReviewStillRequiresTheNamedUnit(t *testing.T) {
	f := newEvidenceFixture(t)
	ctx := context.Background()
	ses, workflowID := f.fixRoundCoder(t, tddBuildSpec(), 6, "one", "two", "three", "four", "five")
	seedReviewFindings(t, f.s, workflowID, 4, []workflow.Finding{
		{Severity: "major", File: "e.go", Summary: "fix unit 5", Unit: 5},
	})
	f.cancelBuild(t, workflowID, 5)

	_, err := f.s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "fixed",
		Verification: unitPair(3)})
	want := `TDD evidence missing for unit(s) 5: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// No reviewer rows at all is a first run, however high the round is.
func TestFixRoundWithNoReviewStillRequiresEveryUnit(t *testing.T) {
	f := newEvidenceFixture(t)
	ctx := context.Background()
	ses, workflowID := f.fixRoundCoder(t, tddBuildSpec(), 6, "one", "two", "three", "four", "five")
	f.cancelBuild(t, workflowID, 5)

	_, err := f.s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done"})
	want := `TDD evidence missing for unit(s) 1,2,3,4,5: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// Another step's review loop can leave this build's first run at a high
// round. That review is not this step's, so every unit is still required.
func TestFixRoundMultiLoopGuardIgnoresAnotherStepsReview(t *testing.T) {
	f := newEvidenceFixture(t)
	ctx := context.Background()
	spec := workflow.Spec{Steps: []workflow.Step{
		{ID: "design", Run: "designer"},
		{ID: "review-design", Review: []string{"ui_reviewer"}, Of: "design", Loop: &workflow.Loop{Fix: "design"}},
		{ID: "build", Run: "coder", Gates: []workflow.Gate{workflow.GateTDD}},
		{ID: "review-build", Review: []string{"reviewer"}, Of: "build", Loop: &workflow.Loop{Fix: "build"}},
	}}
	ses, workflowID := f.fixRoundCoder(t, spec, 6, "one", "two")
	seedReviewFindingsAs(t, f.s, workflowID, "review-design", 4, "ui_reviewer", "changes_requested",
		[]workflow.Finding{{Severity: "major", File: "d.go", Summary: "fix the design", Unit: 1}})

	if _, err := f.s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Progress, Summary: "unit 1",
		Verification: unitPair(1)}); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done"})
	want := `TDD evidence missing for unit(s) 2: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// A fresh round-6 build brief includes the round-4 findings, not the empty
// cancelled round in between, and not an older review.
func TestRoundFindingLinesUsesLatestReviewBeforeThisRun(t *testing.T) {
	f := newEvidenceFixture(t)
	_, workflowID := f.fixRoundCoder(t, tddBuildSpec(), 6, "one", "two", "three", "four", "five")
	seedReviewFindings(t, f.s, workflowID, 2, []workflow.Finding{
		{Severity: "major", File: "a.go", Summary: "old unit 1", Unit: 1},
	})
	seedReviewFindings(t, f.s, workflowID, 4, []workflow.Finding{
		{Severity: "major", File: "c.go", Summary: "fix unit 3", Unit: 3},
	})
	f.cancelBuild(t, workflowID, 5)

	spec := tddBuildSpec()
	lines, err := f.s.roundFindingLines(context.Background(), workflowID, &spec, "build", 6)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "fix unit 3") || strings.Contains(got, "old unit 1") {
		t.Fatalf("lines = %q, want the round-4 finding only", got)
	}
}
