package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

const cloneDirPrefix = "code_sign_clone."

// cloneRoot is where Chromium-based apps put <app>.code_sign_clone dirs:
// $(getconf DARWIN_USER_DIR)/../X. Empty when it cannot be derived (not macOS).
func (s *Store) cloneRoot() string {
	if s.CloneRoot != "" {
		return s.CloneRoot
	}
	out, err := exec.Command("getconf", "DARWIN_USER_DIR").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return ""
	}
	return filepath.Join(strings.TrimSpace(string(out)), "..", "X")
}

// lsofPaths lists every file path open in the user's processes (lsof -Fn).
func lsofPaths() ([]string, error) {
	out, err := exec.Command("lsof", "-w", "-n", "-Fn").Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var paths []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "n/") {
			paths = append(paths, l[1:])
		}
	}
	return paths, nil
}

// ReclaimChromeClones removes <root>/<app>.code_sign_clone/code_sign_clone.*
// dirs. Chrome (and every Chromium app) copies its bundle there at launch and
// deletes the copy only on a clean quit, so a Chrome that was killed or
// abandoned leaks ~1.3k inodes per launch. A clone goes when its mtime is past
// reclaimGrace (any age under NoGrace) and nothing holds it: no process has a
// file open under it and no process command line runs from it. If the open
// file list cannot be read, nothing is removed. Takes the reclaim lock.
func (s *Store) ReclaimChromeClones(ctx context.Context, opt CleanupOptions) ([]CleanupResult, error) {
	root := s.cloneRoot()
	if root == "" {
		return nil, nil
	}
	apps, err := filepath.Glob(filepath.Join(root, "*.code_sign_clone"))
	if err != nil || len(apps) == 0 {
		return nil, err
	}
	var clones []string
	for _, app := range apps {
		if fi, err := os.Lstat(app); err != nil || !fi.IsDir() {
			continue
		}
		ents, err := os.ReadDir(app)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), cloneDirPrefix) {
				clones = append(clones, filepath.Join(app, e.Name()))
			}
		}
	}
	if len(clones) == 0 {
		return nil, nil
	}
	unlock, err := lockReclaim(s.Home)
	if err != nil {
		return nil, err
	}
	defer unlock()
	open := s.OpenPaths
	if open == nil {
		open = lsofPaths
	}
	held, err := open()
	if err != nil {
		return nil, err
	}
	procs, err := s.procs()
	if err != nil {
		return nil, err
	}
	heldBy := func(clone string) bool {
		for _, p := range held {
			if p == clone || strings.HasPrefix(p, clone+"/") {
				return true
			}
		}
		for _, p := range procs {
			if strings.Contains(p.Args, clone+"/") {
				return true
			}
		}
		return false
	}
	cutoff := db.Millis(s.Now().Add(-reclaimGrace))
	if opt.NoGrace {
		cutoff = db.Millis(s.Now())
	}
	var results []CleanupResult
	for _, c := range clones {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		keep := func(reason string) {
			results = append(results, CleanupResult{Path: c, Action: "kept", Reason: reason})
		}
		fi, err := os.Lstat(c)
		if err != nil || !fi.IsDir() {
			keep("not a directory")
			continue
		}
		if fi.ModTime().UnixMilli() > cutoff {
			keep("within grace")
			continue
		}
		if heldBy(c) {
			keep("held open by a process")
			continue
		}
		if opt.DryRun {
			results = append(results, CleanupResult{Path: c, Action: "would_remove"})
			continue
		}
		if err := os.RemoveAll(c); err != nil {
			s.logf("chrome clone: remove %s failed: %v", c, err)
			keep("error: " + err.Error())
			continue
		}
		s.logf("chrome clone: removed %s", c)
		results = append(results, CleanupResult{Path: c, Action: "removed"})
	}
	return results, nil
}
