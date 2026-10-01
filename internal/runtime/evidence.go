package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// mergeEvidence is worktree.MergeEvidence 3 and 4: a merged item_merges row's
// head branch, or a merged GitHub PR's head OID, contains the tree's HEAD.
// Every failure means "no evidence".
type mergeEvidence struct {
	s      *Store
	mu     sync.Mutex
	prs    map[string][]mergedPR // per repo path, cleared each pass
	dryRun bool
}

type mergedPR struct {
	Number      int    `json:"number"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
}

// fullOID is a complete git object id; gh output is untrusted, so anything
// else must never reach git as an argument.
var fullOID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

var githubRepoRe = regexp.MustCompile(`github\.com[:/]([^/]+/[^/]+?)(?:\.git)?/?$`)

// MergeEvidence is the worktree.Service hook for merged-PR evidence.
func (s *Store) MergeEvidence() worktree.MergeEvidence {
	return &mergeEvidence{s: s, prs: map[string][]mergedPR{}}
}

// BeginPass drops the cached PR lists so a new reclaim pass re-asks gh. A
// dryRun pass never fetches refs/pull/N/head.
func (e *mergeEvidence) BeginPass(dryRun bool) {
	e.mu.Lock()
	e.prs = map[string][]mergedPR{}
	e.dryRun = dryRun
	e.mu.Unlock()
}

// EndPass drops the cached PR lists once a pass is over.
func (e *mergeEvidence) EndPass() { e.BeginPass(false) }

func (e *mergeEvidence) isDryRun() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dryRun
}

func (e *mergeEvidence) Merged(ctx context.Context, wt worktree.Worktree, repoPath, head string) bool {
	return e.itemMergeContains(ctx, wt, head) || e.mergedPRContains(ctx, wt, repoPath, head)
}

func (e *mergeEvidence) git(ctx context.Context, dir string, args ...string) error {
	_, err := e.s.runner()(ctx, "git", append([]string{"-C", dir}, args...)...)
	return err
}

func (e *mergeEvidence) itemMergeContains(ctx context.Context, wt worktree.Worktree, head string) bool {
	rows, err := e.s.DB.QueryContext(ctx, `SELECT m.head FROM item_merges m JOIN items i ON i.id = m.item_id
		WHERE i.root_id = ? AND m.repo_id = ? AND m.state = 'merged' AND m.kind != 'kept'`, wt.RootItemID, wt.RepoID)
	if err != nil {
		return false
	}
	var heads []string
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil {
			heads = append(heads, h)
		}
	}
	rows.Close()
	for _, h := range heads {
		for _, ref := range []string{"refs/heads/" + h, "refs/remotes/origin/" + h} {
			if e.git(ctx, wt.Path, "merge-base", "--is-ancestor", head, ref) == nil {
				return true
			}
		}
	}
	return false
}

func (e *mergeEvidence) mergedPRContains(ctx context.Context, wt worktree.Worktree, repoPath, head string) bool {
	if wt.Branch == "" {
		return false
	}
	prs := e.listMerged(ctx, wt.RepoID, repoPath)
	if e.anyMergedPRHeadContains(ctx, wt, head, prs) {
		return true
	}
	// A PR whose head branch is this tree's branch may have its head OID only
	// on refs/pull/N/head, which the ref scan above cannot see.
	for _, pr := range prs {
		if pr.HeadRefName != wt.Branch || !fullOID.MatchString(pr.HeadRefOid) {
			continue
		}
		if e.git(ctx, wt.Path, "cat-file", "-e", pr.HeadRefOid+"^{commit}") != nil {
			// the head branch is usually deleted after merge; GitHub keeps the ref
			if e.isDryRun() || e.git(ctx, repoPath, "fetch", "--quiet", "origin", fmt.Sprintf("refs/pull/%d/head", pr.Number)) != nil {
				continue
			}
		}
		if e.git(ctx, wt.Path, "merge-base", "--is-ancestor", head, pr.HeadRefOid) == nil {
			return true
		}
	}
	return false
}

// anyMergedPRHeadContains is true when a local ref tip containing head is the
// head OID of any merged PR (e.g. an integration PR from another branch): one
// for-each-ref per tree, no per-PR ancestry loop.
// ponytail: only PR heads that are a local ref tip are seen; a deleted
// integration branch is covered only by the same-branch pull-ref path.
func (e *mergeEvidence) anyMergedPRHeadContains(ctx context.Context, wt worktree.Worktree, head string, prs []mergedPR) bool {
	heads := map[string]bool{}
	for _, pr := range prs {
		if fullOID.MatchString(pr.HeadRefOid) {
			heads[pr.HeadRefOid] = true
		}
	}
	if len(heads) == 0 || !fullOID.MatchString(head) {
		return false
	}
	out, err := e.s.runner()(ctx, "git", "-C", wt.Path, "for-each-ref", "--contains", head,
		"--format=%(objectname)", "refs/remotes", "refs/heads")
	if err != nil {
		return false
	}
	for _, oid := range strings.Fields(string(out)) {
		if heads[oid] {
			return true
		}
	}
	return false
}

// listMerged is the repo's merged PRs from gh, once per pass; nil for a
// non-GitHub repo or on any gh failure.
func (e *mergeEvidence) listMerged(ctx context.Context, repoID, repoPath string) []mergedPR {
	e.mu.Lock()
	defer e.mu.Unlock()
	if prs, ok := e.prs[repoPath]; ok {
		return prs
	}
	var remote string
	_ = e.s.DB.QueryRowContext(ctx, `SELECT COALESCE(remote_url, '') FROM repos WHERE id = ?`, repoID).Scan(&remote)
	var prs []mergedPR
	if m := githubRepoRe.FindStringSubmatch(strings.TrimSpace(remote)); isGitHubRemote(remote) && m != nil {
		out, err := e.s.runner()(ctx, "gh", "pr", "list", "--repo", m[1], "--state", "merged",
			"--limit", "1000", "--json", "number,headRefName,headRefOid")
		if err != nil || json.Unmarshal(out, &prs) != nil {
			e.s.logf("worktree: gh merged PR list for %s failed, no PR evidence: %v", repoPath, err)
			prs = nil
		}
	}
	if len(prs) >= 1000 {
		e.s.logf("worktree: gh returned %d merged PRs for %s, the --limit; older PRs give no evidence", len(prs), repoPath)
	}
	e.prs[repoPath] = prs
	return prs
}
