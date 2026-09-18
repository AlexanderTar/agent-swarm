package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

var (
	// ErrUnfinished is §20's "it offers --resume or --rollback".
	ErrUnfinished = errors.New("An unfinished migration is in progress. " +
		"Run `swarm migrate --resume` to continue it, or `swarm migrate --rollback` to undo it.")
	// ErrInjected is the crash seam (FailAfter), used only by tests.
	ErrInjected = errors.New("migrate: injected failure")
)

// Runner drives §20's ten steps. Every field is injected (S-5, S-6); nothing here
// has a production default.
type Runner struct {
	Cfg          install.Config
	Run          execx.Runner
	Now          func() time.Time
	Out          io.Writer
	DoInstall    func(ctx context.Context) error  // step 9
	Statfs       func(dir string) (uint64, error) // free bytes
	FailAfter    int                               // test seam: ErrInjected after this step
	ValidateHook func(ctx context.Context) error  // test seam for step 5
	HoldTimeout  time.Duration                     // step 2's lsof wait; 20 s when zero
}

func (r *Runner) home() string     { return r.Cfg.Home }
func (r *Runner) v1Path() string   { return filepath.Join(r.home(), "swarm.db") }
func (r *Runner) keptPath() string { return filepath.Join(r.home(), "swarm-v1.db") }
func (r *Runner) tmpPath() string  { return filepath.Join(r.home(), "swarm-v2.tmp.db") }
func (r *Runner) kbPath() string   { return filepath.Join(r.home(), "kb") }
func (r *Runner) stamp() string    { return r.Now().UTC().Format("20060102-150405") }
func (r *Runner) logf(f string, a ...any) {
	if r.Out != nil {
		fmt.Fprintf(r.Out, f+"\n", a...)
	}
}

// DefaultStatfs reports the free bytes on dir's filesystem. syscall.Statfs is in the
// standard library, so §20's free-space preflight needs no new dependency.
func DefaultStatfs(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// Migrate runs §20's steps in order. It is idempotent: a completed journal means
// "Already migrated" and nothing happens.
func (r *Runner) Migrate(ctx context.Context) error {
	j, err := LoadJournal(r.home())
	if err != nil {
		return err
	}
	switch {
	case j != nil && j.Complete:
		r.logf("Already migrated")
		return nil
	case j != nil:
		return ErrUnfinished
	}
	if !install.HasLegacyData(r.v1Path()) {
		// swarm-v1.db existing alongside a non-legacy swarm.db, with no journal at
		// all, is not a clean v2 install: it means an earlier migration or rollback
		// did not finish cleanly (the journal is gone, but v1's data was never fully
		// restored either). "Already migrated" would be a lie here.
		if _, err := os.Stat(r.keptPath()); err == nil {
			return fmt.Errorf("%s exists but there is no migration journal and %s is not "+
				"Agent Swarm 1.x data; an earlier `swarm migrate` or `swarm migrate --rollback` "+
				"did not finish. Run `swarm migrate --rollback` to attempt recovery.",
				r.keptPath(), r.v1Path())
		}
		// No v1 data, no leftover swarm-v1.db, and no journal: an earlier build, or a
		// fresh install.
		r.logf("Already migrated")
		return nil
	}
	if err := r.preflight(ctx); err != nil {
		return err
	}
	j = &Journal{Version: JournalVersion, StartedAt: r.Now().UnixMilli()}
	return r.runFrom(ctx, j, 2)
}

// runFrom executes every step greater than or equal to first, journaling before and
// after each one (S-7). Resume calls it with the step after the last completed one.
func (r *Runner) runFrom(ctx context.Context, j *Journal, first int) error {
	steps := []struct {
		N    int
		Name string
		Fn   func(ctx context.Context, j *Journal, s *Step) error
	}{
		{2, "stop the Agent Swarm 1.x jobs", r.stopV1},
		{3, "back up the database and the configuration", r.backup},
		{4, "build the new database", r.build},
		{5, "validate the new database", r.validate},
		{6, "switch the database files", r.switchFiles},
		{7, "archive and import the knowledge base", r.knowledgeBase},
		{8, "remove the Agent Swarm 1.x integrations", r.integrations},
		{9, "install the new daemon", r.install},
	}
	for _, st := range steps {
		if st.N < first {
			continue
		}
		// A cancellation caught between steps (e.g. Ctrl-C during the previous
		// step's own cleanup) must not start a brand new step.
		if err := ctx.Err(); err != nil {
			return err
		}
		s := j.Begin(st.N, st.Name)
		s.StartedAt = r.Now().UnixMilli()
		if err := j.Save(r.home()); err != nil {
			return err
		}
		r.logf("%d/9 %s", st.N, st.Name)
		if err := st.Fn(ctx, j, s); err != nil {
			// The journal already records what this step did before it failed, so
			// --rollback and --resume both have something to work from.
			_ = j.Save(r.home())
			return err
		}
		s.DoneAt = r.Now().UnixMilli()
		if err := j.Save(r.home()); err != nil {
			return err
		}
		if r.FailAfter == st.N {
			return ErrInjected
		}
	}
	j.Complete = true
	if err := j.Save(r.home()); err != nil {
		return err
	}
	r.logf("Migration complete. The Agent Swarm 1.x database is kept at %s.", r.keptPath())
	return nil
}

// preflight is §20 step 1.
func (r *Runner) preflight(ctx context.Context) error {
	d, err := OpenV1(r.v1Path())
	if err != nil {
		return err
	}
	defer d.Close()
	v, err := V1Version(ctx, d)
	if err != nil {
		return fmt.Errorf("reading the Agent Swarm 1.x schema version: %w", err)
	}
	if v != 3 && v != 4 {
		return fmt.Errorf("%s is at schema version %d; swarm migrate handles versions 3 and 4", r.v1Path(), v)
	}
	// Every key §20 names must exist, read WAL-aware (S-8) before anything is stopped.
	if _, err := ReadV1(ctx, d, SourceKeys()); err != nil {
		return err
	}
	dbSize, err := fileSize(r.v1Path())
	if err != nil {
		return err
	}
	kbSize, err := dirSize(r.kbPath())
	if err != nil {
		return err
	}
	need := 2*dbSize + kbSize
	free, err := r.Statfs(r.home())
	if err != nil {
		return err
	}
	if free < need {
		return fmt.Errorf("not enough disk space: %d bytes free, %d needed (twice the database plus the knowledge base)",
			free, need)
	}
	return nil
}

// stopV1 is §20 step 2.
func (r *Runner) stopV1(ctx context.Context, j *Journal, s *Step) error {
	for _, label := range []string{install.Label, install.UpdaterLabel} {
		target := fmt.Sprintf("gui/%d/%s", r.Cfg.UID, label)
		// The undo action (bring the v1 job back from the plist step 3 backs up) is
		// journaled BEFORE the bootout runs (S-7/C1): its content does not depend on
		// the bootout's outcome, and journaling first means a hard crash (including a
		// bare Ctrl-C, which is a real kill with no cleanup chance) right after the
		// bootout still leaves --rollback with a record of what to undo.
		s.Add(Action{Kind: "launchctl", Args: []string{"bootstrap",
			fmt.Sprintf("gui/%d", r.Cfg.UID), filepath.Join(r.Cfg.LaunchAgentsDir, label+".plist")}})
		if err := j.Save(r.home()); err != nil {
			return err
		}
		if _, err := r.Run(ctx, "launchctl", "bootout", target); err != nil && !install.NotLoaded(err) {
			return fmt.Errorf("launchctl bootout %s: %w", label, err)
		}
	}
	deadline := r.HoldTimeout
	if deadline == 0 {
		deadline = 20 * time.Second
	}
	var last string
	for waited := time.Duration(0); waited < deadline; waited += 500 * time.Millisecond {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		out, err := r.Run(ctx, "lsof", "-t", "--", r.v1Path())
		last = strings.TrimSpace(string(out))
		if last == "" {
			if lsofFoundNothing(err) {
				return nil
			}
			// lsof itself failed to run (missing binary, permission error, …) rather
			// than reporting "nothing holds it": treating this as a pass could rename a
			// database a live v1 daemon still has open.
			return fmt.Errorf("checking whether anything still holds %s: %w", r.v1Path(), err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s is still open by process %s; quit it and run swarm migrate again", r.v1Path(), last)
}

// lsofFoundNothing reports whether err is exactly lsof's normal "nothing holds this
// file" answer: a real *exec.ExitError with code 1, or no error at all. Any other
// shape (a missing binary, a permission error, an unexpected exit code) is a real
// failure and must not be treated as "free".
func lsofFoundNothing(err error) bool {
	if err == nil {
		return true
	}
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 1
}

// backup is §20 step 3: VACUUM INTO, integrity_check, then the config copies.
func (r *Runner) backup(ctx context.Context, j *Journal, s *Step) error {
	dir := filepath.Join(r.home(), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := uniquePath(filepath.Join(dir, "swarm-v1-"+r.stamp()+".db"))
	// The undo (remove the backup file) is journaled before VACUUM INTO runs: "remove"
	// is idempotent on a path that was never created, so this is safe even if the
	// process is killed before VACUUM INTO ever starts (C1/S-7).
	s.Add(Action{Kind: "remove", To: dst})
	if err := j.Save(r.home()); err != nil {
		return err
	}
	// VACUUM INTO runs through modernc.org/sqlite; no sqlite3 CLI is needed. The path
	// is a bound parameter, which SQLite accepts for VACUUM INTO. V1DSN (mode=ro) is
	// reused rather than a bare read-write DSN, so S-8's read-only guard actually
	// covers this connection too — a read-write handle could checkpoint the v1 WAL
	// before step 5 even validates.
	src, err := sql.Open("sqlite", V1DSN(r.v1Path()))
	if err != nil {
		return err
	}
	defer src.Close()
	if _, err := src.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("VACUUM INTO %s: %w", dst, err)
	}
	check, err := sql.Open("sqlite", "file:"+dst+"?mode=ro")
	if err != nil {
		return err
	}
	defer check.Close()
	var res string
	if err := check.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("the backup at %s failed integrity_check: %s", dst, res)
	}

	// Every file step 8 edits, plus both launchd plists (S-7). Each restore action is
	// journaled — and saved — immediately before its copy is made, not batched until
	// the loop finishes, so a crash partway through the loop still leaves an accurate
	// record of exactly which copies exist (C1).
	cfgDir := uniquePath(filepath.Join(dir, "config-"+r.stamp()))
	for _, f := range r.filesToBackUp() {
		if _, err := os.Stat(f.src); err != nil {
			continue // a file the user never had needs no backup
		}
		target := filepath.Join(cfgDir, f.rel)
		s.Add(Action{Kind: "restore", From: target, To: f.src})
		if err := j.Save(r.home()); err != nil {
			return err
		}
		if err := install.CopyFile(f.src, target); err != nil {
			return err
		}
	}
	r.logf("    backup: %s", dst)
	return nil
}

// filesToBackUp is every path the migration may edit, with the relative name it
// takes inside the config backup folder.
func (r *Runner) filesToBackUp() []struct{ src, rel string } {
	c := r.Cfg
	return []struct{ src, rel string }{
		{c.Codex("config.toml"), filepath.Join("codex", "config.toml")},
		{c.Codex("hooks.json"), filepath.Join("codex", "hooks.json")},
		{c.Cursor("hooks.json"), filepath.Join("cursor", "hooks.json")},
		{c.Cursor("mcp.json"), filepath.Join("cursor", "mcp.json")},
		{c.Cursor("cli-config.json"), filepath.Join("cursor", "cli-config.json")},
		{c.Gemini("GEMINI.md"), filepath.Join("gemini", "GEMINI.md")},
		{c.Gemini("config", "hooks.json"), filepath.Join("gemini", "hooks.json")},
		{c.Gemini("antigravity", "mcp_config.json"), filepath.Join("gemini", "mcp_config.json")},
		// S-7: step 9 overwrites dev.swarm.daemon.plist, so rollback needs both.
		{filepath.Join(c.LaunchAgentsDir, install.Label+".plist"),
			filepath.Join("LaunchAgents", install.Label+".plist")},
		{filepath.Join(c.LaunchAgentsDir, install.UpdaterLabel+".plist"),
			filepath.Join("LaunchAgents", install.UpdaterLabel+".plist")},
	}
}

// build is §20 step 4.
func (r *Runner) build(ctx context.Context, j *Journal, s *Step) error {
	s.Add(Action{Kind: "remove", To: r.tmpPath()})
	if err := j.Save(r.home()); err != nil {
		return err
	}
	if err := os.Remove(r.tmpPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	rep, err := Import(ctx, ImportInput{V1Path: r.v1Path(), NewPath: r.tmpPath(),
		KBDir: r.kbPath(), Now: r.Now})
	if err != nil {
		return err
	}
	for _, n := range rep.Notes {
		r.logf("    note: %s", n)
	}
	r.logf("    imported %d items", len(rep.Items))
	return nil
}

// validate is §20 step 5. A failure deletes the temp database and leaves v1 alone.
func (r *Runner) validate(ctx context.Context, j *Journal, s *Step) error {
	fail := func(err error) error {
		os.Remove(r.tmpPath())
		return err
	}
	if r.ValidateHook != nil {
		if err := r.ValidateHook(ctx); err != nil {
			return fail(err)
		}
	}
	rep, err := LoadReport(r.tmpPath())
	if err != nil {
		return fail(err)
	}
	d, err := db.Open(ctx, r.tmpPath())
	if err != nil {
		return fail(err)
	}
	defer d.Close()
	if err := Validate(ctx, d, rep); err != nil {
		return fail(err)
	}
	return nil
}

// switchFiles is §20 step 6. Each rename is its own journaled action, saved before
// it runs: a hard crash (including Ctrl-C) between two renames must not lose track
// of the one that already happened (C1).
func (r *Runner) switchFiles(ctx context.Context, j *Journal, s *Step) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		from, to := r.v1Path()+suffix, r.keptPath()+suffix
		if _, err := os.Stat(from); err != nil {
			continue
		}
		s.Add(Action{Kind: "rename", From: from, To: to})
		if err := j.Save(r.home()); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	s.Add(Action{Kind: "rename", From: r.tmpPath(), To: r.v1Path()})
	if err := j.Save(r.home()); err != nil {
		return err
	}
	if err := os.Rename(r.tmpPath(), r.v1Path()); err != nil {
		return err
	}
	return nil
}

// knowledgeBase, integrations and install (§20 steps 7-9) live in kb.go and
// integrations.go.

// DryRun is §20's "--dry-run prints the plan and changes nothing".
func (r *Runner) DryRun(ctx context.Context) error {
	j, err := LoadJournal(r.home())
	if err != nil {
		return err
	}
	switch {
	case j != nil && j.Complete:
		r.logf("Already migrated")
		return nil
	case j != nil:
		// An unfinished migration means --dry-run's plan (built against the current,
		// possibly half-switched files) would be misleading; offer the real options.
		return ErrUnfinished
	}
	if err := r.preflight(ctx); err != nil {
		return err
	}
	d, err := OpenV1(r.v1Path())
	if err != nil {
		return err
	}
	defer d.Close()
	src, err := ReadV1(ctx, d, SourceKeys())
	if err != nil {
		return err
	}
	r.logf("swarm migrate would:")
	r.logf("  stop %s and %s", install.Label, install.UpdaterLabel)
	r.logf("  back up %s to %s/backups/", r.v1Path(), r.home())
	r.logf("  import %d Agent Swarm 1.x tasks into this tree:", len(src))
	for _, root := range Table {
		from := "spec-authored"
		if len(root.From) > 0 {
			from = root.From[0]
		}
		r.logf("    %s %s  (%s, %s)", strings.ToUpper(root.Type), root.Title, from, root.Status)
		for _, s := range root.Stories {
			r.logf("      STORY %s  (%d tasks)", s.Title, len(s.Tasks))
		}
		if len(root.Tasks) > 0 {
			r.logf("      %d tasks", len(root.Tasks))
		}
	}
	r.logf("  leave these in %s: %s", r.keptPath(), strings.Join(NotImported, ", "))
	r.logf("  rename %s to %s", r.v1Path(), r.keptPath())
	r.logf("  archive kb/inbox and kb/handoffs to %s", filepath.Join(r.home(), "kb-archive"))
	r.logf("  remove the Agent Swarm 1.x integrations and run swarm install")
	return nil
}

// uniquePath appends -2, -3, … before the extension until the path is free (§20:
// "An existing backup with the same timestamp is never overwritten").
func uniquePath(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d%s", base, n, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func fileSize(p string) (uint64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return uint64(fi.Size()), nil
}

// dirSize is the total size of dir; an absent dir is zero, not an error.
func dirSize(dir string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		total += uint64(fi.Size())
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}
