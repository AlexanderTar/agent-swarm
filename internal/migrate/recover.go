package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// Resume is §20's "--resume continues from the last completed step". The step that
// was interrupted is re-run from the start; every step is written to be safe to
// re-run: step 3 picks a fresh unique backup name, step 4 deletes and rebuilds the
// temp database, step 6 skips a rename whose source is already gone, step 7 skips a
// move or a note write whose target already exists, step 8 is idempotent by
// construction, and step 9 is swarm install, which is idempotent by requirement.
func (r *Runner) Resume(ctx context.Context) error {
	j, err := LoadJournal(r.home())
	if err != nil {
		return err
	}
	if j == nil {
		return errors.New("There is no unfinished migration to resume. Run `swarm migrate`.")
	}
	if j.Complete {
		r.logf("Already migrated")
		return nil
	}
	from := j.LastDone() + 1
	if from < 2 {
		from = 2
	}
	r.logf("Resuming from step %d.", from)
	return r.runFrom(ctx, j, from)
}

// Rollback is §20's "--rollback undoes the journaled steps in reverse". Every action
// is attempted even when an earlier one fails: stopping half way through a rollback
// is the worst of the three outcomes, so the failures are collected and reported.
func (r *Runner) Rollback(ctx context.Context) error {
	j, err := LoadJournal(r.home())
	if err != nil {
		return err
	}
	if j == nil {
		r.logf("Nothing to roll back.")
		return nil
	}
	var failures []string
	unrestorable := false
	for _, a := range j.Undo() {
		if err := r.undo(ctx, a); err != nil {
			failures = append(failures, fmt.Sprintf("%s %s → %s: %v", a.Kind, a.From, a.To, err))
		}
	}
	for _, s := range j.Steps {
		if s.N == 8 && s.DoneAt != 0 {
			unrestorable = true
		}
	}
	if unrestorable {
		r.logf("The Agent Swarm 1.x symlinks and release folders were not re-created; "+
			"they pointed into %s, which is gone. Everything else was restored.", r.home()+"/app")
	}
	if err := os.Remove(JournalPath(r.home())); err != nil && !os.IsNotExist(err) {
		failures = append(failures, "removing the journal: "+err.Error())
	}
	if len(failures) > 0 {
		return fmt.Errorf("the rollback finished with %d problem(s):\n  %s",
			len(failures), strings.Join(failures, "\n  "))
	}
	r.logf("Rolled back. Agent Swarm 1.x is running again.")
	return nil
}

// undo applies one journaled action, per Task 13's table.
func (r *Runner) undo(ctx context.Context, a Action) error {
	switch a.Kind {
	case "rename":
		if _, err := os.Stat(a.To); err != nil {
			return nil // never moved, or already moved back
		}
		return os.Rename(a.To, a.From)
	case "restore":
		if _, err := os.Stat(a.From); err != nil {
			return fmt.Errorf("the backup copy %s is missing", a.From)
		}
		return install.CopyFile(a.From, a.To)
	case "remove":
		if err := os.RemoveAll(a.To); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	case "launchctl":
		if len(a.Args) == 0 {
			return nil
		}
		// The plist may be gone if step 3 never reached it; bootstrap would fail, and
		// that is a reportable problem, not something to swallow.
		if _, err := r.Run(ctx, "launchctl", a.Args...); err != nil && !install.NotLoaded(err) {
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown journal action %q", a.Kind)
}
