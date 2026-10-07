package worktree

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var lockfiles = []string{"pnpm-lock.yaml", "package-lock.json", "yarn.lock", "bun.lockb"}

const depsCloneTimeout = 120 * time.Second

// cloneDeps copies installed node_modules (depth 0-2) into the fresh review
// tree dst from the first same-repo source whose lockfiles match dst's byte
// for byte: an active worktree at sha, any active worktree, then the primary
// checkout. Best effort: every failure is logged and the tree is still usable.
func (s *Service) cloneDeps(ctx context.Context, repoID, repoPath, sha, dst string) {
	ctx, cancel := context.WithTimeout(ctx, depsCloneTimeout)
	defer cancel()
	var want []string
	for _, l := range lockfiles {
		if fileExists(filepath.Join(dst, l)) {
			want = append(want, l)
		}
	}
	if len(want) == 0 {
		return
	}
	for _, src := range s.depSources(ctx, repoID, repoPath, sha, dst) {
		if !sameLockfiles(src, dst, want) {
			continue
		}
		dirs := nodeModulesDirs(src)
		if len(dirs) == 0 {
			continue
		}
		for _, rel := range dirs {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755); err != nil {
				s.logf("worktree: clone deps %s: %v", rel, err)
				return
			}
			args := []string{"-R"}
			if runtime.GOOS == "darwin" {
				args = []string{"-cR"} // APFS clone
			}
			if out, err := s.Run(ctx, "cp", append(args, filepath.Join(src, rel), filepath.Join(dst, rel))...); err != nil {
				s.logf("worktree: clone deps %s from %s: %v: %s", rel, src, err, out)
				return
			}
		}
		return
	}
}

// depSources lists candidate source trees in preference order.
func (s *Service) depSources(ctx context.Context, repoID, repoPath, sha, dst string) []string {
	wts, err := s.query(ctx, `WHERE repo_id = ? AND state = 'active' ORDER BY created_at DESC`, repoID)
	if err != nil {
		s.logf("worktree: list deps sources: %v", err)
	}
	var atSHA, rest []string
	for _, wt := range wts {
		if wt.Path == dst || !fileExists(wt.Path) {
			continue
		}
		if out, err := s.git(ctx, wt.Path, "rev-parse", "HEAD"); err == nil && strings.TrimSpace(string(out)) == sha {
			atSHA = append(atSHA, wt.Path)
		} else {
			rest = append(rest, wt.Path)
		}
	}
	return append(append(atSHA, rest...), repoPath)
}

func sameLockfiles(src, dst string, names []string) bool {
	for _, n := range names {
		a, err := os.ReadFile(filepath.Join(src, n))
		if err != nil {
			return false
		}
		b, err := os.ReadFile(filepath.Join(dst, n))
		if err != nil || !bytes.Equal(a, b) {
			return false
		}
	}
	return true
}

// nodeModulesDirs returns node_modules paths relative to root at depth 0-2,
// never descending into a node_modules.
func nodeModulesDirs(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Name() == "node_modules" {
			out = append(out, rel)
			return filepath.SkipDir
		}
		if strings.Count(rel, string(filepath.Separator)) >= 2 {
			return filepath.SkipDir
		}
		return nil
	})
	return out
}
