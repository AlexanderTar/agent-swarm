package worktree

import (
	"context"
	"strings"
	"sync"
)

// MergeEvidence reports extra evidence that wt's HEAD (head) is merged, beyond
// the base rules. Never errors: unknown = false.
type MergeEvidence interface {
	Merged(ctx context.Context, wt Worktree, repoPath, head string) bool
}

// passState caches per-repo fetches for one reclaim pass. Outside a pass
// (Remove, ReclaimOne called directly) every check fetches afresh.
type passState struct {
	mu      sync.Mutex
	fetched map[string]bool
}

// BeginPass starts a reclaim pass: each repo is fetched at most once until the
// next BeginPass.
func (s *Service) BeginPass() {
	s.pass.mu.Lock()
	s.pass.fetched = map[string]bool{}
	s.pass.mu.Unlock()
}

// fetchOnce runs `git fetch origin` for repoPath, once per pass. A failed
// fetch is logged and leaves whatever refs are already local.
func (s *Service) fetchOnce(ctx context.Context, repoPath string) {
	s.pass.mu.Lock()
	if s.pass.fetched != nil {
		if s.pass.fetched[repoPath] {
			s.pass.mu.Unlock()
			return
		}
		s.pass.fetched[repoPath] = true
	}
	s.pass.mu.Unlock()
	if _, err := s.git(ctx, repoPath, "fetch", "--quiet", "origin"); err != nil {
		s.logf("worktree: fetch %s failed: %v", repoPath, err)
	}
}

// isAncestor reports whether commit is an ancestor of (or equal to) ref, in
// the repo at dir.
func (s *Service) isAncestor(ctx context.Context, dir, commit, ref string) bool {
	_, err := s.git(ctx, dir, "merge-base", "--is-ancestor", commit, ref)
	return err == nil
}

// mergedElsewhere is merged evidence 2-4: contained in the fetched remote
// default branch, or vouched for by s.Evidence.
func (s *Service) mergedElsewhere(ctx context.Context, wt Worktree, head string) bool {
	repoPath, err := s.repoPath(ctx, wt.RepoID)
	if err != nil {
		return false
	}
	s.fetchOnce(ctx, repoPath)
	if name, err := s.defaultName(ctx, repoPath); err == nil &&
		s.isAncestor(ctx, wt.Path, head, "refs/remotes/origin/"+name) {
		return true
	}
	return s.Evidence != nil && s.Evidence.Merged(ctx, wt, repoPath, head)
}

func (s *Service) headSHA(ctx context.Context, wt Worktree) (string, bool) {
	out, err := s.git(ctx, wt.Path, "rev-parse", "HEAD")
	return strings.TrimSpace(string(out)), err == nil
}
