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
	s   *Store
	mu  sync.Mutex
	prs map[string][]mergedPR // per repo path, cleared each pass
}

type mergedPR struct {
	Number      int    `json:"number"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
}

var githubRepoRe = regexp.MustCompile(`github\.com[:/]([^/]+/[^/]+?)(?:\.git)?/?$`)

// MergeEvidence is the worktree.Service hook for merged-PR evidence.
func (s *Store) MergeEvidence() worktree.MergeEvidence {
	return &mergeEvidence{s: s, prs: map[string][]mergedPR{}}
}

// BeginPass drops the cached PR lists so a new reclaim pass re-asks gh.
func (e *mergeEvidence) BeginPass() {
	e.mu.Lock()
	e.prs = map[string][]mergedPR{}
	e.mu.Unlock()
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
		WHERE i.root_id = ? AND m.repo_id = ? AND m.state = 'merged'`, wt.RootItemID, wt.RepoID)
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
	// ponytail: only the PR whose head branch is this tree's branch is
	// checked; a tree whose HEAD sits inside some other branch's merged PR
	// needs a per-PR ancestry scan, which costs a git call per PR per tree.
	for _, pr := range e.listMerged(ctx, wt.RepoID, repoPath) {
		if pr.HeadRefName != wt.Branch || pr.HeadRefOid == "" {
			continue
		}
		if e.git(ctx, wt.Path, "cat-file", "-e", pr.HeadRefOid+"^{commit}") != nil {
			// the head branch is usually deleted after merge; GitHub keeps the ref
			if e.git(ctx, repoPath, "fetch", "--quiet", "origin", fmt.Sprintf("refs/pull/%d/head", pr.Number)) != nil {
				continue
			}
		}
		if e.git(ctx, wt.Path, "merge-base", "--is-ancestor", head, pr.HeadRefOid) == nil {
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
	e.prs[repoPath] = prs
	return prs
}
