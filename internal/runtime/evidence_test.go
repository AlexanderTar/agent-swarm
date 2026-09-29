package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/items"
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
