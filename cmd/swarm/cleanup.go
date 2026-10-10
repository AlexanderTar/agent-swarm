package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// cmdCleanup runs the work-dir and scratchpad passes and the worktree reclaim pass once, on
// the database directly like the other offline commands.
func cmdCleanup(args []string, stdout, stderr io.Writer) int {
	fs, home, _ := flags("cleanup", stderr, false)
	dry := fs.Bool("dry-run", false, "list what would be removed and change nothing")
	noGrace := fs.Bool("no-grace", false, "ignore the one-hour grace after an agent finishes")
	discard := fs.String("discard", "", "force-remove one worktree (path or id) of a done or cancelled root; the branch is kept")
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
	if *discard != "" {
		wt.Events = events.New(d, time.Now)
		w, unlanded, err := wt.Discard(ctx, *discard)
		if err != nil {
			return fail(stderr, err)
		}
		count := fmt.Sprintf("%d commits not on %s", unlanded, w.BaseRef)
		if unlanded < 0 {
			count = "unlanded commits unknown"
		}
		fmt.Fprintf(stdout, "worktree discarded %s; branch %s kept (%s)\n", w.Path, w.Branch, count)
		return 0
	}
	opt := runtime.CleanupOptions{DryRun: *dry, NoGrace: *noGrace}

	counts := map[string]int{}
	printResults := func(kind string, rs []runtime.CleanupResult) {
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
	printResults("workdir", dirs)
	pads, padErr := rt.ReclaimScratchpads(ctx, opt)
	printResults("scratchpad", pads)
	trees, treeErr := rt.ReclaimWorktreesWith(ctx, opt)
	printResults("worktree", trees)
	fmt.Fprintf(stdout, "%d removed, %d would_remove, %d kept, %d untracked\n",
		counts["removed"], counts["would_remove"], counts["kept"], counts["untracked"])
	for _, err := range []error{dirErr, padErr, treeErr} {
		if err != nil {
			return fail(stderr, err)
		}
	}
	return 0
}
