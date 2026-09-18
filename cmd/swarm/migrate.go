package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/AlexanderTar/agent-swarm/internal/migrate"
)

// execLookPath is exec.LookPath, named as a variable so a test can replace it.
var execLookPath = exec.LookPath

// migrateRun is the test seam.
var migrateRun = func(ctx context.Context, r *migrate.Runner, mode string) error {
	switch mode {
	case "dry-run":
		return r.DryRun(ctx)
	case "resume":
		return r.Resume(ctx)
	case "rollback":
		return r.Rollback(ctx)
	default:
		return r.Migrate(ctx)
	}
}

func cmdMigrate(args []string, stdout, stderr io.Writer) int {
	fs, home, _ := flags("migrate", stderr, false)
	dry := fs.Bool("dry-run", false, "print the plan and change nothing")
	resume := fs.Bool("resume", false, "continue an unfinished migration")
	rollback := fs.Bool("rollback", false, "undo an unfinished migration")
	if code, done := parse(fs, args); done {
		return code
	}
	mode := "migrate"
	set := 0
	for flag, name := range map[*bool]string{dry: "dry-run", resume: "resume", rollback: "rollback"} {
		if *flag {
			set++
			mode = name
		}
	}
	if set > 1 {
		fmt.Fprintln(stderr, "swarm: --dry-run, --resume and --rollback are mutually exclusive")
		return 2
	}
	cfg, err := newConfig(*home)
	if err != nil {
		return fail(stderr, err)
	}
	r := &migrate.Runner{
		Cfg: cfg, Run: execx.Run, Now: time.Now, Out: stdout,
		Statfs: migrate.DefaultStatfs,
		DoInstall: func(ctx context.Context) error {
			// Step 9 is the same install the user would run by hand.
			if err := installLaunchd(ctx, cfg, execx.Run, false, stdout); err != nil {
				return err
			}
			o := install.AgentsOpts{
				Cfg: cfg, Run: execx.RunFor(120 * time.Second), HTTP: &http.Client{Timeout: 30 * time.Second},
				MarketplaceURL: install.MarketplaceURL,
				Installed:      install.InstalledKinds(execLookPath),
				// The user already chose to migrate, so the v1 release folders go.
				Confirm: func(string) bool { return true },
				Out:     stdout,
			}
			return installAgents(ctx, o)
		},
	}
	if err := migrateRun(context.Background(), r, mode); err != nil {
		return fail(stderr, err)
	}
	return 0
}
