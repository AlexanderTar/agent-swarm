package adapter

import (
	"os"
	"path/filepath"
	"strings"
)

// swarmSharedDirs is the allowlist of agent-facing subdirs of the swarm home
// that an isolated HOME links in. Skills tell agents to use ~/.swarm paths
// (screenshots, specs, plans, research, designs, kb), so without these links
// a spawned agent's mkdir -p recreates them inside the launch dir instead of
// reaching the real shared folders. Everything else -- run/ (which holds the
// launch dir the isolated HOME itself lives under: linking it would cycle),
// db files, config.json -- is never linked.
var swarmSharedDirs = []string{"screenshots", "specs", "plans", "research", "designs", "kb"}

// linkSwarmShared links the agent-facing subdirs of the real swarm home into
// an isolated HOME. When swarmHome sits under userHome, destBase
// (<isolatedHome>/<swarmHome rel userHome>) is created as a REAL directory
// holding one symlink per swarmSharedDirs entry, each pointing at its real
// counterpart (created first with MkdirAll, so an absent real dir is never
// skipped). When swarmHome is not under userHome it does nothing.
//
// It is idempotent across Resume (setupEnv reruns against the same
// launchDir): stale links are removed and recreated, while a real file or a
// non-empty real directory the agent itself created is left untouched --
// never RemoveAll'd.
func linkSwarmShared(isolatedHome, userHome, swarmHome string, logf func(string, ...any)) error {
	if isolatedHome == "" || userHome == "" || swarmHome == "" {
		return nil
	}
	rel, err := filepath.Rel(userHome, swarmHome)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	base := filepath.Join(isolatedHome, rel)
	// A legacy isolated HOME may hold base as a link (e.g. to the real swarm
	// home): linking through it would replace real shared dirs with links to
	// themselves. Remove only the link, then make base a real dir.
	if fi, err := os.Lstat(base); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(base); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	for _, sub := range swarmSharedDirs {
		real := filepath.Join(swarmHome, sub)
		if err := os.MkdirAll(real, 0o755); err != nil {
			return err
		}
		dst := filepath.Join(base, sub)
		// base can already resolve to the real swarm home (a linked ancestor,
		// e.g. muse-home/foo -> ~/foo for a ~/foo/.swarm home): dst is then the
		// real dir itself, and replacing it would link it to itself.
		if rfi, err := os.Stat(real); err == nil {
			if dfi, err := os.Stat(dst); err == nil && os.SameFile(rfi, dfi) {
				continue
			}
		}
		fi, err := os.Lstat(dst)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				// Stale link (dangling or wrong target): replace it.
				if err := os.Remove(dst); err != nil {
					return err
				}
			} else if fi.IsDir() {
				entries, err := os.ReadDir(dst)
				if err != nil {
					return err
				}
				if len(entries) > 0 {
					// Real content the agent created: leave it alone.
					logf("swarm-shared: %s is a real dir with content; leaving it alone", dst)
					continue
				}
				if err := os.Remove(dst); err != nil {
					return err
				}
			} else {
				logf("swarm-shared: %s already exists and is not a dir; leaving it alone", dst)
				continue
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Symlink(real, dst); err != nil {
			return err
		}
	}
	return nil
}
