package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// cmdCleanup runs the work-dir pass and the worktree reclaim pass once, on
// the database directly like the other offline commands.
func cmdCleanup(args []string, stdout, stderr io.Writer) int {
	fs, home, _ := flags("cleanup", stderr, false)
	dry := fs.Bool("dry-run", false, "list what would be removed and change nothing")
	noGrace := fs.Bool("no-grace", false, "ignore the one-hour grace after an agent finishes")
	if code, done := parse(fs, args); done {
		return code
	}
	path := filepath.Join(*home, "swarm.db")
	if _, err := os.Stat(path); err != nil {
		return fail(stderr, fmt.Errorf("no swarm database at %s", path))
	}
	ctx := context.Background()
	d, err := db.Open(ctx, path)
	if err != nil {
		return fail(stderr, err)
	}
	defer d.Close()
	logf := func(string, ...any) {}
	wt := &worktree.Service{DB: d, Run: execx.Run, Now: time.Now, Log: logf, Home: *home}
	rt := &runtime.Store{DB: d, Home: *home, Now: time.Now, Log: logf, Worktree: wt}
	wt.Evidence = rt.MergeEvidence()
	opt := runtime.CleanupOptions{DryRun: *dry, NoGrace: *noGrace}

	counts := map[string]int{}
	print := func(kind string, rs []runtime.CleanupResult) {
		for _, r := range rs {
			counts[r.Action]++
			if r.Reason != "" {
				fmt.Fprintf(stdout, "%s %s %s (%s)\n", kind, r.Action, r.Path, r.Reason)
			} else {
				fmt.Fprintf(stdout, "%s %s %s\n", kind, r.Action, r.Path)
			}
		}
	}
	dirs, dirErr := rt.ReclaimWorkDirs(ctx, opt)
	print("workdir", dirs)
	trees, treeErr := rt.ReclaimWorktreesWith(ctx, opt)
	print("worktree", trees)
	fmt.Fprintf(stdout, "%d removed, %d would_remove, %d kept, %d untracked\n",
		counts["removed"], counts["would_remove"], counts["kept"], counts["untracked"])
	for _, err := range []error{dirErr, treeErr} {
		if err != nil {
			return fail(stderr, err)
		}
	}
	return 0
}
