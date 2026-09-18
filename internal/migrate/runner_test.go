package migrate_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
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

	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.daemon":  {},
		"launchctl bootout gui/501/dev.swarm.updater": {},
		"lsof -t -- " + v1:                            {Err: errors.New("exit status 1")}, // nobody holds it
		"launchctl bootstrap gui/501 " + filepath.Join(c.LaunchAgentsDir, install.Label+".plist"):        {},
		"launchctl bootstrap gui/501 " + filepath.Join(c.LaunchAgentsDir, install.UpdaterLabel+".plist"): {},
	}}
	env := &migrateEnv{Cfg: c, Fake: f, Out: &bytes.Buffer{}}
	env.Runner = &migrate.Runner{
		Cfg: c, Run: f.Runner(), Out: env.Out,
		Now:    func() time.Time { return time.Unix(1700000000, 0).UTC() },
		Statfs: func(string) (uint64, error) { return 100 << 30, nil }, // 100 GiB free
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
