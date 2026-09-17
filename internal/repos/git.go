// Package repos discovers git repositories under the home folder (§12.3).
package repos

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

type GitInfo struct {
	RemoteURL     string
	RemoteOwner   string
	DefaultBranch string
}

var scpLike = regexp.MustCompile(`^[^@/]+@[^:/]+:(.+)$`)

// ParseRemoteOwner returns the first path segment of a remote URL that has at
// least owner/repo, or "".
func ParseRemoteOwner(raw string) string {
	var path string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return ""
		}
		path = u.Path
	case scpLike.MatchString(raw):
		path = scpLike.FindStringSubmatch(raw)[1]
	default:
		return ""
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		return ""
	}
	return parts[0]
}

func git(ctx context.Context, run execx.Runner, path string, args ...string) (string, error) {
	out, err := run(ctx, "git", append([]string{"-C", path}, args...)...)
	return strings.TrimSpace(string(out)), err
}

// ReadGitInfo reads the remote and default branch with local git commands only.
func ReadGitInfo(ctx context.Context, run execx.Runner, path string) GitInfo {
	var info GitInfo
	if u, err := git(ctx, run, path, "config", "--get", "remote.origin.url"); err == nil {
		info.RemoteURL = u
		info.RemoteOwner = ParseRemoteOwner(u)
	}
	if ref, err := git(ctx, run, path, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		info.DefaultBranch = strings.TrimPrefix(ref, "refs/remotes/origin/")
		return info
	}
	for _, b := range []string{"main", "master"} {
		if _, err := git(ctx, run, path, "show-ref", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			info.DefaultBranch = b
			return info
		}
	}
	info.DefaultBranch, _ = git(ctx, run, path, "branch", "--show-current")
	return info
}

// IsRepo reports whether path/.git is a directory (worktrees and submodules have a .git file).
func IsRepo(path string) bool {
	fi, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil && fi.IsDir()
}

// MainRepoOf maps a repo to itself and a worktree to its main repo (via the
// gitdir in its .git file). Submodules and non-repos return false.
func MainRepoOf(path string) (string, bool) {
	if IsRepo(path) {
		return path, true
	}
	body, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return "", false
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(body)), "gitdir: ")
	if !ok {
		return "", false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(path, gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	sep := string(filepath.Separator)
	i := strings.Index(gitdir, sep+".git"+sep+"worktrees"+sep)
	if i < 0 {
		return "", false
	}
	return gitdir[:i], true
}

// Dirty reports uncommitted changes; errors count as clean.
func Dirty(ctx context.Context, run execx.Runner, path string) bool {
	out, err := git(ctx, run, path, "status", "--porcelain")
	return err == nil && out != ""
}
