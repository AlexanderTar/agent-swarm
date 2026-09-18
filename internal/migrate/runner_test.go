package migrate_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/AlexanderTar/agent-swarm/internal/migrate"
)

// migrateEnv builds a complete fake machine: a fake home with a v4 database, both
// launchd plists, the v1 agent configs, and fake launchctl/lsof. S-5 and S-6.
type migrateEnv struct {
	Cfg       install.Config
	Runner    *migrate.Runner
	Fake      *execx.Fake
	Out       *bytes.Buffer
	Installed int // how many times DoInstall ran
}

func newMigrateEnv(t *testing.T) *migrateEnv {
	t.Helper()
	c := fakeHomeForMigrate(t)
	// The v1 database, at the path §20 reads.
	v1 := filepath.Join(c.Home, "swarm.db")
	if err := os.Rename(fullV1(t), v1); err != nil {
		t.Fatal(err)
	}
	// Both launchd plists (S-7: step 3 must back them up).
	if err := os.MkdirAll(c.LaunchAgentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{install.Label, install.UpdaterLabel} {
		p := filepath.Join(c.LaunchAgentsDir, name+".plist")
		if err := os.WriteFile(p, []byte("<plist>v1 "+name+"</plist>\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The v1 agent configuration step 8 edits.
	seedFile(t, filepath.Join("..", "install", "testdata", "codex", "config-v1.toml"), c.Codex("config.toml"))
	// A real ~/.swarm/kb always has these two folders from ordinary use; step 7
	// archives them (§20 step 7, §15).
	for _, name := range []string{"inbox", "handoffs"} {
		if err := os.MkdirAll(filepath.Join(c.Home, "kb", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// lsof's real "nothing holds this file" answer is a genuine exit code 1 with no
	// output; a real *exec.ExitError is built here (rather than a plain errors.New)
	// so lsofFoundNothing's errors.As check exercises the exact shape production
	// code produces, not a string that merely looks similar (Important 5).
	exit1 := exec.Command("sh", "-c", "exit 1").Run()
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.daemon":  {},
		"launchctl bootout gui/501/dev.swarm.updater": {},
		"lsof -t -- " + v1:                            {Err: exit1}, // nobody holds it
		"launchctl bootstrap gui/501 " + filepath.Join(c.LaunchAgentsDir, install.Label+".plist"):        {},
		"launchctl bootstrap gui/501 " + filepath.Join(c.LaunchAgentsDir, install.UpdaterLabel+".plist"): {},
	}}
	env := &migrateEnv{Cfg: c, Fake: f, Out: &bytes.Buffer{}}
	env.Runner = &migrate.Runner{
		Cfg: c, Run: f.Runner(), Out: env.Out,
		Now:       func() time.Time { return time.Unix(1700000000, 0).UTC() },
		Statfs:    func(string) (uint64, error) { return 100 << 30, nil }, // 100 GiB free
		DoInstall: func(context.Context) error { env.Installed++; return nil },
	}
	return env
}

// seedFile copies a fixture to dst inside the fake home. install_test has its own
// copy under the same name; the two packages cannot share test helpers.
func seedFile(t *testing.T, fixture, dst string) {
	t.Helper()
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeHomeForMigrate is install_test's fakeHome, rebuilt here because the two
// packages cannot share test helpers.
func fakeHomeForMigrate(t *testing.T) install.Config {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".swarm", "bin", "swarm")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return install.Config{Bin: bin, Home: filepath.Join(home, ".swarm"), UserHome: home,
		LaunchAgentsDir: filepath.Join(home, "Library", "LaunchAgents"), UID: 501, User: "fake"}
}

// §23.2 scenario 18, first half.
func TestMigrateProducesTheSpecTreeAndRenamesTheOldFile(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The old file is renamed, the new one is in place.
	if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v1.db")); err != nil {
		t.Errorf("swarm-v1.db: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v2.tmp.db")); !os.IsNotExist(err) {
		t.Error("the temp database survived the switch")
	}
	if install.HasLegacyData(filepath.Join(env.Cfg.Home, "swarm.db")) {
		t.Error("swarm.db still has a schema_meta table")
	}
	// A backup exists and passes integrity_check.
	backups, err := filepath.Glob(filepath.Join(env.Cfg.Home, "backups", "swarm-v1-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, %v", backups, err)
	}
	d, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var res string
	if err := d.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil || res != "ok" {
		t.Fatalf("integrity_check = %q, %v", res, err)
	}
	// Step 9 ran.
	if env.Installed != 1 {
		t.Errorf("DoInstall ran %d times, want 1", env.Installed)
	}
	// The journal is complete.
	j, err := migrate.LoadJournal(env.Cfg.Home)
	if err != nil || j == nil || !j.Complete {
		t.Fatalf("journal = %+v, %v", j, err)
	}
}

// §20: running it again prints "Already migrated" and changes nothing.
func TestMigrateASecondTimeSaysAlreadyMigratedAndChangesNothing(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(env.Cfg.Home, "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	env.Out.Reset()
	calls := len(env.Fake.Calls())
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.Out.String(), "Already migrated") {
		t.Errorf("out = %q, want \"Already migrated\"", env.Out.String())
	}
	after, _ := os.ReadFile(filepath.Join(env.Cfg.Home, "swarm.db"))
	if !bytes.Equal(before, after) {
		t.Error("the database changed on a second run")
	}
	if len(env.Fake.Calls()) != calls {
		t.Errorf("commands ran on a second migrate: %v", env.Fake.Calls()[calls:])
	}
	if env.Installed != 1 {
		t.Errorf("DoInstall ran again")
	}
}

// A corrupt/future-version journal must propagate as an error from Migrate itself,
// not just from LoadJournal in isolation.
func TestMigratePropagatesAJournalLoadError(t *testing.T) {
	env := newMigrateEnv(t)
	j := &migrate.Journal{Version: migrate.JournalVersion + 1}
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}
	err := env.Runner.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "newer build") {
		t.Fatalf("err = %v, want the future-version journal error", err)
	}
}

// §20 step 1: an unfinished journal offers --resume or --rollback.
func TestMigrateRefusesWhenAnUnfinishedJournalExists(t *testing.T) {
	env := newMigrateEnv(t)
	j := &migrate.Journal{Version: 1, StartedAt: 1}
	j.Begin(4, "build the new database")
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}
	err := env.Runner.Migrate(context.Background())
	if !errors.Is(err, migrate.ErrUnfinished) {
		t.Fatalf("err = %v, want ErrUnfinished", err)
	}
	for _, want := range []string{"--resume", "--rollback"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not offer %s", err, want)
		}
	}
}

// §20 step 1: the schema version must be 3 or 4.
func TestPreflightRefusesAnUnsupportedSchemaVersion(t *testing.T) {
	env := newMigrateEnv(t)
	d, err := sql.Open("sqlite", "file:"+filepath.Join(env.Cfg.Home, "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE schema_meta SET version = 2`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	err = env.Runner.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("err = %v, want a message naming version 2", err)
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v1.db")); !os.IsNotExist(err) {
		t.Error("a failed preflight must change nothing")
	}
}

// §20 step 1: 2× the database size plus the knowledge base must be free.
func TestPreflightRefusesWithoutEnoughFreeSpace(t *testing.T) {
	env := newMigrateEnv(t)
	env.Runner.Statfs = func(string) (uint64, error) { return 1 << 10, nil } // 1 KiB
	err := env.Runner.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "disk space") {
		t.Fatalf("err = %v, want a disk-space error", err)
	}
}

// A Statfs failure (not just "not enough space") must propagate, not be swallowed.
func TestPreflightPropagatesAStatfsError(t *testing.T) {
	env := newMigrateEnv(t)
	env.Runner.Statfs = func(string) (uint64, error) { return 0, errors.New("statfs boom") }
	err := env.Runner.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "statfs boom") {
		t.Fatalf("err = %v, want the Statfs error", err)
	}
}

// §20 step 2: both jobs are booted out, and the db is waited for.
func TestStopV1BootsOutBothJobsAndWaitsForTheDatabase(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(env.Fake.Calls(), "\n")
	for _, want := range []string{
		"launchctl bootout gui/501/dev.swarm.daemon",
		"launchctl bootout gui/501/dev.swarm.updater",
		"lsof -t -- " + filepath.Join(env.Cfg.Home, "swarm.db"),
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing call %q; calls =\n%s", want, calls)
		}
	}
}

// lsof keeps reporting a holder → the step fails rather than moving a live database.
func TestStopV1FailsWhenSomethingKeepsHoldingTheDatabase(t *testing.T) {
	env := newMigrateEnv(t)
	env.Fake.Responses["lsof -t -- "+filepath.Join(env.Cfg.Home, "swarm.db")] = execx.Result{Out: "4242\n"}
	env.Runner.HoldTimeout = 20 * time.Millisecond // keep the test fast
	err := env.Runner.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "4242") {
		t.Fatalf("err = %v, want a message naming the process still holding the database", err)
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v1.db")); !os.IsNotExist(err) {
		t.Error("nothing may be renamed while a process holds the database")
	}
}

// Important 5: lsof failing to run at all (a missing binary, a permission error,
// …) must not be mistaken for its normal "nothing holds this file" answer — both
// currently look like "empty stdout, non-nil err" unless the error's actual shape
// is checked. Treating a broken lsof as a pass could let step 6 rename a database a
// live v1 daemon still has open.
func TestStopV1FailsWhenLsofItselfCannotRun(t *testing.T) {
	env := newMigrateEnv(t)
	v1 := filepath.Join(env.Cfg.Home, "swarm.db")
	// A genuine "command not found" shape, not lsof's exit-code-1 "found nothing".
	_, lookErr := exec.LookPath("swarm-migrate-nonexistent-binary-xyz")
	if lookErr == nil {
		t.Fatal("test setup: this binary must not exist")
	}
	env.Fake.Responses["lsof -t -- "+v1] = execx.Result{Err: lookErr}
	err := env.Runner.Migrate(context.Background())
	if err == nil {
		t.Fatal("want an error: lsof itself failed to run, which must not be treated as \"free\"")
	}
	if strings.Contains(err.Error(), "still open by process") {
		t.Errorf("err = %v; this must be reported as lsof failing, not as something holding the file", err)
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v1.db")); !os.IsNotExist(err) {
		t.Error("nothing may be renamed while lsof's own failure is unresolved")
	}
}

// §20 step 3: an existing backup with the same timestamp is never overwritten.
func TestBackupNeverOverwritesAnExistingFile(t *testing.T) {
	env := newMigrateEnv(t)
	dir := filepath.Join(env.Cfg.Home, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The name the run will choose, already taken.
	existing := filepath.Join(dir, "swarm-v1-20231114-221320.db")
	if err := os.WriteFile(existing, []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(existing)
	if string(body) != "do not touch" {
		t.Error("the existing backup was overwritten")
	}
	got, _ := filepath.Glob(filepath.Join(dir, "swarm-v1-*.db"))
	if len(got) != 2 {
		t.Fatalf("backups = %v, want the existing one plus a suffixed new one", got)
	}
}

// S-7: the plists are in the step-3 backup, so rollback can reload the v1 jobs.
func TestBackupIncludesBothLaunchdPlists(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	dirs, err := filepath.Glob(filepath.Join(env.Cfg.Home, "backups", "config-*"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("config backups = %v, %v", dirs, err)
	}
	for _, name := range []string{install.Label + ".plist", install.UpdaterLabel + ".plist"} {
		body, err := os.ReadFile(filepath.Join(dirs[0], "LaunchAgents", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(string(body), "v1") {
			t.Errorf("%s = %q, want the v1 plist", name, body)
		}
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "codex", "config.toml")); err != nil {
		t.Errorf("the codex config was not backed up: %v", err)
	}
}

// A step-4 (build) failure — a v1 status with no §20 mapping — must propagate
// rather than being swallowed, and must not touch v1.
func TestBuildPropagatesAnImportFailure(t *testing.T) {
	env := newMigrateEnv(t)
	d, err := sql.Open("sqlite", "file:"+filepath.Join(env.Cfg.Home, "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET status = 'review' WHERE key = 'SW-361'`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	err = env.Runner.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "review") {
		t.Fatalf("err = %v, want the MapStatus error", err)
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v1.db")); !os.IsNotExist(err) {
		t.Error("a failed build must change nothing")
	}
}

// §20 step 5: a validation failure deletes the temp database and leaves v1 alone.
func TestAValidationFailureLeavesV1Untouched(t *testing.T) {
	env := newMigrateEnv(t)
	env.Runner.ValidateHook = func(context.Context) error { return errors.New("injected validation failure") }
	err := env.Runner.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "injected validation failure") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v2.tmp.db")); !os.IsNotExist(err) {
		t.Error("the temp database was left behind")
	}
	if !install.HasLegacyData(filepath.Join(env.Cfg.Home, "swarm.db")) {
		t.Error("swarm.db is no longer the v1 database")
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v1.db")); !os.IsNotExist(err) {
		t.Error("the switch ran despite the validation failure")
	}
}

// §20 step 6: the WAL and SHM files move with the database.
func TestSwitchRenamesTheWALAndSHMAlongsideTheDatabase(t *testing.T) {
	env := newMigrateEnv(t)
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(filepath.Join(env.Cfg.Home, "swarm.db"+suffix), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v1.db"+suffix)); err != nil {
			t.Errorf("swarm-v1.db%s: %v", suffix, err)
		}
	}
}

// DefaultStatfs is the production Statfs: a thin wrapper over the standard
// library's syscall.Statfs, checked against a throwaway temp dir only (S-6, S-5:
// never a real ~/.swarm path).
func TestDefaultStatfsReportsFreeBytesOnATempDir(t *testing.T) {
	free, err := migrate.DefaultStatfs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if free == 0 {
		t.Error("free = 0, want the temp filesystem to report some free space")
	}
}

// C1(d): swarm-v1.db existing alongside a swarm.db that is NOT Agent Swarm 1.x
// data, with no journal at all, means an earlier migration or rollback did not
// finish cleanly (a broken --rollback deleted the journal without ever actually
// restoring v1 — see the Ctrl-C-mid-step-6 scenario). "Already migrated" would be
// a lie, and there would be no CLI recovery path left if Migrate said that.
func TestMigrateRefusesWhenSwarmV1DBExistsButSwarmDBIsNotLegacyAndNoJournalExists(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Simulate the broken-rollback aftermath: the journal is gone (as a completed
	// rollback would leave it), but v1's data was never actually restored to
	// swarm.db — it is still the v2 database, exactly as a Ctrl-C mid-step-6 would
	// leave things.
	if err := os.Remove(filepath.Join(env.Cfg.Home, "migrate", "journal.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	env.Out.Reset()
	err := env.Runner.Migrate(context.Background())
	if err == nil {
		t.Fatal("want an error, not a silent \"Already migrated\"")
	}
	if strings.Contains(env.Out.String(), "Already migrated") {
		t.Fatal("must not claim Already migrated when v1's data was never actually restored")
	}
	// O3 round 3: the manual recovery must be factually correct, not just present.
	wantBootout := fmt.Sprintf("launchctl bootout gui/%d/%s", env.Cfg.UID, install.Label)
	for _, want := range []string{"swarm-v1.db", "swarm.db", "doctor --legacy", wantBootout} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q (a concrete recovery step)", err, want)
		}
	}
	// swarm.db exists here (it's the v2 database from the completed migration
	// above), so a bare "mv swarm-v1.db swarm.db" would silently clobber it —
	// the message must tell the user to move it aside first.
	if !strings.Contains(err.Error(), "aside") || !strings.Contains(err.Error(), "overwrite") {
		t.Errorf("err = %v, want it to warn that swarm.db would be overwritten and must be moved aside first", err)
	}
	// swarm doctor --legacy (install.Leftovers) has no launchd/plist check at all —
	// only symlinks, config table entries and the legacy db — so the message must
	// not claim otherwise.
	if strings.Contains(err.Error(), "launchd jobs") {
		t.Errorf("err = %v, want it to not claim doctor --legacy checks launchd jobs (it doesn't)", err)
	}
	// Confirm the message's honesty: --rollback really is a dead end here (no
	// journal), so it must not have been offered as if it would fix this.
	env.Out.Reset()
	if err := env.Runner.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !strings.Contains(env.Out.String(), "Nothing to roll back") {
		t.Error("--rollback should have nothing to do here; the refusal message must not point at it as a fix")
	}
}

// O3 round 3, item 4: swarm.db can be MISSING entirely in this same broken state
// (HasLegacyData is false for a missing file too), which is a different case from
// "swarm.db exists but isn't v1 data" — there is nothing to move aside, and the
// message must not claim the (nonexistent) file "is not Agent Swarm 1.x data".
func TestMigrateRefusalMessageOmitsMoveAsideWhenSwarmDBIsMissing(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(env.Cfg.Home, "migrate", "journal.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Unlike the sibling test, swarm.db itself is gone too — only the v1 backup
	// remains.
	if err := os.Remove(filepath.Join(env.Cfg.Home, "swarm.db")); err != nil {
		t.Fatal(err)
	}
	err := env.Runner.Migrate(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "aside") || strings.Contains(err.Error(), "overwrite") {
		t.Errorf("err = %v, want no move-aside step: there is nothing to overwrite", err)
	}
	if strings.Contains(err.Error(), "is not Agent Swarm 1.x data") {
		t.Errorf("err = %v, must not describe a missing file as \"is not Agent Swarm 1.x data\"", err)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err = %v, want it to say swarm.db does not exist", err)
	}
	wantBootout := fmt.Sprintf("launchctl bootout gui/%d/%s", env.Cfg.UID, install.Label)
	for _, want := range []string{"swarm-v1.db", "doctor --legacy", wantBootout} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

// A corrupt/future-version journal must propagate from DryRun too.
func TestDryRunPropagatesAJournalLoadError(t *testing.T) {
	env := newMigrateEnv(t)
	j := &migrate.Journal{Version: migrate.JournalVersion + 1}
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}
	err := env.Runner.DryRun(context.Background())
	if err == nil || !strings.Contains(err.Error(), "newer build") {
		t.Fatalf("err = %v, want the future-version journal error", err)
	}
}

// Minor 3: --dry-run against an unfinished journal must offer --resume/--rollback
// rather than printing a plan built against the current, possibly half-switched
// files.
func TestDryRunOffersResumeOrRollbackWhenAnUnfinishedJournalExists(t *testing.T) {
	env := newMigrateEnv(t)
	j := &migrate.Journal{Version: 1, StartedAt: 1}
	j.Begin(4, "build the new database")
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}
	err := env.Runner.DryRun(context.Background())
	if !errors.Is(err, migrate.ErrUnfinished) {
		t.Fatalf("err = %v, want ErrUnfinished", err)
	}
}

// --dry-run prints the plan and changes nothing.
func TestDryRunChangesNothing(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.DryRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := env.Out.String()
	for _, want := range []string{"Migrate the coach agent from Eve to ADK Go (endurio-chat)", "SW-673", "swarm-v1.db"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not mention %q:\n%s", want, out)
		}
	}
	if !install.HasLegacyData(filepath.Join(env.Cfg.Home, "swarm.db")) {
		t.Error("--dry-run modified the database")
	}
	if _, err := migrate.LoadJournal(env.Cfg.Home); err != nil {
		t.Fatal(err)
	} else if j, _ := migrate.LoadJournal(env.Cfg.Home); j != nil {
		t.Error("--dry-run wrote a journal")
	}
	if len(env.Fake.Calls()) != 0 {
		t.Errorf("--dry-run ran commands: %v", env.Fake.Calls())
	}
}
