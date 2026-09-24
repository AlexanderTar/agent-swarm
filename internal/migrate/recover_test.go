package migrate_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/AlexanderTar/agent-swarm/internal/migrate"
)

// §23.3: "A crash after each of steps 2-9 (simulated) is followed by --resume
// completing and --rollback restoring v1 exactly (files, launchd plists, config
// hashes)." This is that matrix.
func TestResumeCompletesAfterACrashAtEveryStep(t *testing.T) {
	for step := 2; step <= 9; step++ {
		t.Run(stepName(step), func(t *testing.T) {
			env := newMigrateEnv(t)
			env.Runner.FailAfter = step
			if err := env.Runner.Migrate(context.Background()); !errors.Is(err, migrate.ErrInjected) {
				t.Fatalf("want ErrInjected, got %v", err)
			}
			j, err := migrate.LoadJournal(env.Cfg.Home)
			if err != nil || j == nil || j.Complete {
				t.Fatalf("journal = %+v, %v", j, err)
			}
			if got := j.LastDone(); got != step {
				t.Fatalf("LastDone = %d, want %d", got, step)
			}

			env.Runner.FailAfter = 0
			if err := env.Runner.Resume(context.Background()); err != nil {
				t.Fatalf("resume: %v", err)
			}
			j, err = migrate.LoadJournal(env.Cfg.Home)
			if err != nil || j == nil || !j.Complete {
				t.Fatalf("journal after resume = %+v, %v", j, err)
			}
			// The end state is the same whatever step crashed.
			if install.HasLegacyData(filepath.Join(env.Cfg.Home, "swarm.db")) {
				t.Error("swarm.db is still the v1 database")
			}
			if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v1.db")); err != nil {
				t.Errorf("swarm-v1.db: %v", err)
			}
			if _, err := os.Stat(filepath.Join(env.Cfg.Home, "kb-archive", "inbox")); err != nil {
				t.Errorf("kb-archive/inbox: %v", err)
			}
			toml, _ := os.ReadFile(env.Cfg.Codex("config.toml"))
			if strings.Contains(string(toml), "mcp_servers.swarm") {
				t.Error("the v1 codex MCP table survived")
			}
			if env.Installed == 0 {
				t.Error("step 9 never ran")
			}
		})
	}
}

func TestRollbackRestoresV1ExactlyAfterACrashAtEveryStep(t *testing.T) {
	for step := 2; step <= 9; step++ {
		t.Run(stepName(step), func(t *testing.T) {
			env := newMigrateEnv(t)
			before := snapshot(t, env.Cfg)
			env.Runner.FailAfter = step
			if err := env.Runner.Migrate(context.Background()); !errors.Is(err, migrate.ErrInjected) {
				t.Fatalf("want ErrInjected, got %v", err)
			}
			if err := env.Runner.Rollback(context.Background()); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			after := snapshot(t, env.Cfg)
			// Full set equality, not just before ⊆ after: a test named "RestoresV1Exactly"
			// must also catch a file rollback leaves BEHIND that was never there before
			// (Important 4) — a one-directional subset check would never flag that.
			for path, want := range before {
				got, ok := after[path]
				if !ok {
					t.Errorf("%s is missing after the rollback", path)
					continue
				}
				if got != want {
					t.Errorf("%s changed: %s → %s", path, want, got)
				}
			}
			for path := range after {
				if _, ok := before[path]; ok {
					continue
				}
				// M2 (explicitly deferred, non-blocking): SQLite's own -wal/-shm sidecar
				// files can be left beside the restored v1 database (a 0-byte WAL, a
				// pre-allocated SHM) as a byproduct of opening it read-only for
				// integrity_check during the backup/undo path. Hygiene only — the
				// database itself is exact — so it is excluded here rather than fixed in
				// this round.
				if strings.HasSuffix(path, "-wal") || strings.HasSuffix(path, "-shm") {
					continue
				}
				t.Errorf("%s exists after the rollback but did not exist before it", path)
			}
			// The v1 launchd jobs were bootstrapped again (§20 recovery).
			calls := strings.Join(env.Fake.Calls(), "\n")
			for _, label := range []string{install.Label, install.UpdaterLabel} {
				want := "launchctl bootstrap gui/501 " + filepath.Join(env.Cfg.LaunchAgentsDir, label+".plist")
				if !strings.Contains(calls, want) {
					t.Errorf("missing %q; calls =\n%s", want, calls)
				}
			}
			// The journal is gone, so a later swarm migrate starts clean.
			j, err := migrate.LoadJournal(env.Cfg.Home)
			if err != nil || j != nil {
				t.Errorf("journal = %+v, %v; a completed rollback must remove it", j, err)
			}
			// The temp database is gone.
			if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm-v2.tmp.db")); !os.IsNotExist(err) {
				t.Error("the temp database survived the rollback")
			}
		})
	}
}

// Follow-up ticket #2 (P5 final review, real end-to-end run): Journal.Begin
// appends rather than replaces, so a step that crashed and was then completed via
// --resume leaves two Step records for the same N — a stale one with DoneAt == 0
// and the resumed one that finished. Rollback's incomplete-step scan must not
// flag that stale record: the step DID complete, just not on the first try.
//
// A genuine mid-step crash needs a step that fails without setting DoneAt (unlike
// FailAfter, which only injects a failure AFTER the current step's DoneAt is
// already set — that models a crash BETWEEN steps, not the duplicate-record bug).
// Step 9's DoInstall is journaled as idempotent by requirement (same seam
// TestInstallJournalsBeforeDoInstallRunsSoAPartialCrashIsStillRecorded uses), so
// failing it once and succeeding on --resume reproduces the exact ticket scenario:
// two Step{N: 9} records, the first with DoneAt == 0.
func TestRollbackAfterAResumeDoesNotFlagAStepThatActuallyCompleted(t *testing.T) {
	env := newMigrateEnv(t)
	before := snapshot(t, env.Cfg)

	calls := 0
	env.Runner.DoInstall = func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("simulated crash mid-install")
		}
		env.Installed++
		return nil
	}
	if err := env.Runner.Migrate(context.Background()); err == nil {
		t.Fatal("want the injected DoInstall failure to surface")
	}
	j, err := migrate.LoadJournal(env.Cfg.Home)
	if err != nil || j == nil || j.Complete {
		t.Fatalf("journal after the crash = %+v, %v", j, err)
	}
	if got := j.LastDone(); got != 8 {
		t.Fatalf("LastDone = %d, want 8 (step 9 crashed mid-step)", got)
	}

	if err := env.Runner.Resume(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	j, err = migrate.LoadJournal(env.Cfg.Home)
	if err != nil || j == nil || !j.Complete {
		t.Fatalf("journal after resume = %+v, %v", j, err)
	}
	// Confirm the duplicate-record setup this test relies on is actually in place:
	// two records for step 9, the first stale (DoneAt == 0), the second completed.
	var nine []migrate.Step
	for _, st := range j.Steps {
		if st.N == 9 {
			nine = append(nine, st)
		}
	}
	if len(nine) != 2 || nine[0].DoneAt != 0 || nine[1].DoneAt == 0 {
		t.Fatalf("want two step-9 records (stale then completed), got %+v", nine)
	}

	err = env.Runner.Rollback(context.Background())
	if err != nil {
		t.Fatalf("rollback after a crash-then-resume must succeed cleanly, got: %v", err)
	}

	// Same full-set-equality check TestRollbackRestoresV1ExactlyAfterACrashAtEveryStep
	// uses: the fix must not just suppress the false-positive error, the restore
	// underneath it must actually be correct.
	after := snapshot(t, env.Cfg)
	for path, want := range before {
		got, ok := after[path]
		if !ok {
			t.Errorf("%s is missing after the rollback", path)
			continue
		}
		if got != want {
			t.Errorf("%s changed: %s → %s", path, want, got)
		}
	}
	for path := range after {
		if _, ok := before[path]; ok {
			continue
		}
		if strings.HasSuffix(path, "-wal") || strings.HasSuffix(path, "-shm") {
			continue
		}
		t.Errorf("%s exists after the rollback but did not exist before it", path)
	}
}

// S-7's blocking case, called out on its own because it is the one that matters
// most: step 9 overwrites dev.swarm.daemon.plist, so the backup must predate it.
func TestRollbackAfterStepNineRestoresTheV1PlistByteForByte(t *testing.T) {
	env := newMigrateEnv(t)
	plist := filepath.Join(env.Cfg.LaunchAgentsDir, install.Label+".plist")
	want, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	// Make step 9 actually rewrite the plist, as install.Install does.
	env.Runner.DoInstall = func(context.Context) error {
		env.Installed++
		return os.WriteFile(plist, []byte("<plist>v2 daemon</plist>\n"), 0o644)
	}
	env.Runner.FailAfter = 9
	if err := env.Runner.Migrate(context.Background()); !errors.Is(err, migrate.ErrInjected) {
		t.Fatalf("want ErrInjected, got %v", err)
	}
	if got, _ := os.ReadFile(plist); string(got) == string(want) {
		t.Fatal("the test did not exercise the overwrite")
	}
	if err := env.Runner.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("plist = %q, want the v1 plist %q", got, want)
	}
}

// §20: with no journal, --resume has nothing to continue.
func TestResumeWithNoJournalErrors(t *testing.T) {
	env := newMigrateEnv(t)
	err := env.Runner.Resume(context.Background())
	if err == nil || !strings.Contains(err.Error(), "swarm migrate") {
		t.Fatalf("err = %v, want it to point at swarm migrate", err)
	}
}

// §20: a completed journal means "Already migrated", same as a plain Migrate.
func TestResumeWhenAlreadyMigratedSaysSo(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	env.Out.Reset()
	if err := env.Runner.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.Out.String(), "Already migrated") {
		t.Errorf("out = %q, want \"Already migrated\"", env.Out.String())
	}
}

// A journal that recorded step 2 starting but never finishing (a crash mid-step,
// not via the FailAfter seam) must still resume from step 2, not step 1 (there is
// no step 1 to run: preflight is not journaled).
func TestResumeStartsAtStepTwoWhenNoStepEverFinished(t *testing.T) {
	env := newMigrateEnv(t)
	j := &migrate.Journal{Version: 1, StartedAt: 1}
	j.Begin(2, "stop the Agent Swarm 1.x jobs") // StartedAt set, never DoneAt
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}
	if err := env.Runner.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.Out.String(), "Resuming from step 2.") {
		t.Errorf("out = %q, want it to resume from step 2", env.Out.String())
	}
}

// S-7: rollback attempts every remaining action even after an earlier one fails,
// and reports the failure rather than stopping halfway.
func TestRollbackAttemptsEveryActionAfterAFailureAndReportsIt(t *testing.T) {
	env := newMigrateEnv(t)
	j := &migrate.Journal{Version: 1, StartedAt: 1}
	s := j.Begin(3, "back up the database and the configuration")
	// Undo() replays a step's actions newest-added-first, so the action added LAST
	// executes FIRST. The satisfiable rename is added first (so it executes second,
	// genuinely AFTER the failure below) — swapped from an earlier version of this
	// test where the failing action was added first and so, after reversal, actually
	// ran last, meaning it never blocked anything downstream: that version passed
	// even when Rollback was patched to `break` on the first failure, which proves
	// it had zero discriminating power (Important 1).
	moved := filepath.Join(env.Cfg.Home, "swarm-v1-test.db")
	if err := os.Rename(filepath.Join(env.Cfg.Home, "swarm.db"), moved); err != nil {
		t.Fatal(err)
	}
	s.Add(migrate.Action{Kind: "rename", From: filepath.Join(env.Cfg.Home, "swarm.db"), To: moved})
	// A restore whose backup copy is missing, added SECOND so it executes FIRST:
	// this must fail, but the rename above (executing after it) must still run.
	missing := filepath.Join(env.Cfg.Home, "backups", "config-x", "codex", "config.toml")
	s.Add(migrate.Action{Kind: "restore", From: missing, To: env.Cfg.Codex("config.toml")})
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}

	err := env.Runner.Rollback(context.Background())
	if err == nil || !strings.Contains(err.Error(), "problem") {
		t.Fatalf("err = %v, want a reported problem", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %v, want it to name the missing backup", err)
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.Home, "swarm.db")); err != nil {
		t.Errorf("the satisfiable rename was not attempted despite the earlier failure: %v", err)
	}
}

// §20 step 9 / S-7: when no plist existed before this migration (a fresh install,
// unlike most fixtures where the v1 daemon plist is already there), rollback must
// remove the one step 9 wrote rather than leaving it behind.
func TestInstallRemovesANewlyWrittenPlistOnRollback(t *testing.T) {
	env := newMigrateEnv(t)
	plist := filepath.Join(env.Cfg.LaunchAgentsDir, install.Label+".plist")
	if err := os.Remove(plist); err != nil {
		t.Fatal(err)
	}
	env.Runner.DoInstall = func(context.Context) error {
		env.Installed++
		return os.WriteFile(plist, []byte("<plist>v2 daemon</plist>\n"), 0o644)
	}
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.Runner.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("the plist step 9 wrote should have been removed by the rollback")
	}
}

// O1: step 9 must journal its own undo actions BEFORE r.DoInstall runs, not after.
// Reproduces the reviewer's exact scenario: a DoInstall that does real work (writes
// the v2 plist, creates a fresh skill directory) and then fails partway through —
// modeling a Ctrl-C during the real, ~120s installAgents step. Both of step 9's own
// actions (the artifact-removal set and the bootout) are knowable before DoInstall
// ever runs, unlike step 8's RemoveLegacy* calls, so there is no reason for either
// to be lost just because DoInstall did not return.
func TestInstallJournalsBeforeDoInstallRunsSoAPartialCrashIsStillRecorded(t *testing.T) {
	env := newMigrateEnv(t)
	plist := filepath.Join(env.Cfg.LaunchAgentsDir, install.Label+".plist")
	skillDir := env.Cfg.Claude("skills", "swarm")
	env.Runner.DoInstall = func(context.Context) error {
		env.Installed++
		if err := os.WriteFile(plist, []byte("<plist>v2 daemon</plist>\n"), 0o644); err != nil {
			return err
		}
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			return err
		}
		return context.Canceled // a Ctrl-C partway through the real installAgents step
	}
	err := env.Runner.Migrate(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	j, err := migrate.LoadJournal(env.Cfg.Home)
	if err != nil || j == nil {
		t.Fatalf("journal = %+v, %v", j, err)
	}
	var step9 *migrate.Step
	for i := range j.Steps {
		if j.Steps[i].N == 9 {
			step9 = &j.Steps[i]
		}
	}
	if step9 == nil || len(step9.Undo) == 0 {
		t.Fatalf("step 9's undo actions were not journaled before DoInstall ran: %+v", step9)
	}
	var sawBootout bool
	for _, a := range step9.Undo {
		if a.Kind == "launchctl" && len(a.Args) > 0 && a.Args[0] == "bootout" {
			sawBootout = true
		}
	}
	if !sawBootout {
		t.Errorf("the bootout was not journaled before DoInstall ran: %+v", step9.Undo)
	}

	err = env.Runner.Rollback(context.Background())
	if err == nil || !strings.Contains(err.Error(), "9") {
		t.Fatalf("err = %v, want it to name interrupted step 9", err)
	}
	if !strings.Contains(strings.Join(env.Fake.Calls(), "\n"), "launchctl bootout gui/501/dev.swarm.daemon") {
		t.Error("rollback never attempted the bootout")
	}
	if _, err := os.Lstat(skillDir); !os.IsNotExist(err) {
		t.Error("the fresh skill directory survived rollback")
	}
}

// O1(c): a failed action and an interrupted step can both be true of the same
// rollback (e.g. bootstrapping the restored v1 plist genuinely fails with
// something install.NotLoaded does not recognize, on the very rollback that is
// also cleaning up after a step that never finished) — both signals must reach the
// user, not just whichever is checked first.
func TestRollbackReportsBothAFailureAndAnIncompleteStepTogether(t *testing.T) {
	env := newMigrateEnv(t)
	j := &migrate.Journal{Version: 1, StartedAt: 1}
	s := j.Begin(6, "switch the database files")
	s.StartedAt = 1
	missing := filepath.Join(env.Cfg.Home, "backups", "config-x", "codex", "config.toml")
	s.Add(migrate.Action{Kind: "restore", From: missing, To: env.Cfg.Codex("config.toml")})
	// s.DoneAt is deliberately left zero: this step never finished.
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}

	err := env.Runner.Rollback(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "problem") {
		t.Errorf("err = %v, want it to report the failed restore", err)
	}
	if !strings.Contains(err.Error(), "interrupted") || !strings.Contains(err.Error(), "6") {
		t.Errorf("err = %v, want it to ALSO name the interrupted step, not just the failure", err)
	}
}

// undo tolerates a rename that was already undone (or never happened): a missing
// `to` is a no-op, not an error.
func TestRollbackSkipsARenameThatWasAlreadyUndone(t *testing.T) {
	env := newMigrateEnv(t)
	j := &migrate.Journal{Version: 1, StartedAt: 1}
	s := j.Begin(6, "switch the database files")
	s.Add(migrate.Action{Kind: "rename",
		From: filepath.Join(env.Cfg.Home, "swarm.db"),
		To:   filepath.Join(env.Cfg.Home, "does-not-exist.db")})
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}
	if err := env.Runner.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// An unrecognized journal action kind is reported rather than silently skipped:
// Task 13's table is meant to be exhaustive, and a mismatch here is a bug, not a
// no-op.
func TestRollbackReportsAnUnknownActionKind(t *testing.T) {
	env := newMigrateEnv(t)
	j := &migrate.Journal{Version: 1, StartedAt: 1}
	s := j.Begin(3, "back up the database and the configuration")
	s.Add(migrate.Action{Kind: "bogus"})
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}
	err := env.Runner.Rollback(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unknown journal action") {
		t.Fatalf("err = %v, want it to name the unknown action kind", err)
	}
}

// Important 3: step 9 may create ~/.claude's v2 skill folder and ~/.local/bin/swarm
// fresh, with no v1 symlink at that same path to overwrite — filesToBackUp's
// restore-from-copy mechanism only covers regular files with prior content, never a
// freshly created directory tree or link. Rollback must still clean these up rather
// than leaving v2's own artifacts stranded after claiming to have restored v1.
// O2: install.WriteSkills runs for all four agent kinds (claude, codex, cursor,
// agy), each writing both install.SkillNames — eight possible fresh directories,
// not just Claude's two. The fake DoInstall here independently derives all eight
// paths (plus LocalBin) straight from install.Kinds/install.SkillNames/SkillsDir —
// the same ground truth production code uses — rather than being wired to write
// only to whatever newArtifactPaths already checks; a version of this test that
// only exercised Claude's two paths passed even when the other six agents' skill
// folders were never cleaned up, because it could never expose a gap in that list.
func TestInstallRemovesAllFreshSkillDirsAndLocalBinOnRollback(t *testing.T) {
	env := newMigrateEnv(t)
	var created []string
	env.Runner.DoInstall = func(context.Context) error {
		env.Installed++
		for _, k := range install.Kinds {
			for _, name := range install.SkillNames() {
				dir := filepath.Join(env.Cfg.SkillsDir(k), name)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+name+"\n"), 0o644); err != nil {
					return err
				}
				created = append(created, dir)
			}
		}
		if err := os.MkdirAll(filepath.Dir(env.Cfg.LocalBin()), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(env.Cfg.Bin, env.Cfg.LocalBin()); err != nil {
			return err
		}
		created = append(created, env.Cfg.LocalBin())
		return nil
	}
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(created) != len(install.Kinds)*len(install.SkillNames())+1 {
		t.Fatalf("test setup created %d paths, want %d", len(created), len(install.Kinds)*len(install.SkillNames())+1)
	}
	if err := env.Runner.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range created {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived rollback: %v", p, err)
		}
	}
}

// C2: v1 and v2 share the same launchd label (dev.swarm.daemon). After a fully
// successful migration, v2 is registered under it; rollback must boot that out
// BEFORE bootstrapping the restored v1 plist over the same label, or v1 comes back
// fighting v2 for one label (or v2 keeps running against the just-restored v1
// schema and crash-loops under KeepAlive). This needs no crash injection at all —
// it is exactly scenario 28, rollback after a completed migration.
func TestRollbackBootsOutV2BeforeBootstrappingV1(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := len(env.Fake.Calls())
	if err := env.Runner.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := env.Fake.Calls()[before:]
	bootoutV2, bootstrapV1 := -1, -1
	wantBootout := "launchctl bootout gui/501/dev.swarm.daemon"
	wantBootstrap := "launchctl bootstrap gui/501 " + filepath.Join(env.Cfg.LaunchAgentsDir, install.Label+".plist")
	for i, c := range calls {
		if c == wantBootout && bootoutV2 == -1 {
			bootoutV2 = i
		}
		if c == wantBootstrap {
			bootstrapV1 = i
		}
	}
	if bootoutV2 == -1 {
		t.Fatalf("rollback never booted out the v2 daemon; calls =\n%s", strings.Join(calls, "\n"))
	}
	if bootstrapV1 == -1 {
		t.Fatalf("rollback never bootstrapped the v1 daemon; calls =\n%s", strings.Join(calls, "\n"))
	}
	if bootoutV2 > bootstrapV1 {
		t.Errorf("bootout at call %d ran after bootstrap at call %d; v1 must not start until v2 is stopped",
			bootoutV2, bootstrapV1)
	}
}

// C1(c): a step that started but never finished (StartedAt set, DoneAt zero) is
// what a genuine hard crash — including a bare Ctrl-C, which has no chance to run
// any cleanup — leaves behind; it is distinct from the FailAfter test seam, which
// only fires AFTER a step's DoneAt is recorded (a clean stop between steps, not a
// crash mid-step). Rollback must still replay whatever that step DID manage to
// journal, but it cannot claim unqualified success: it genuinely does not know
// what else, if anything, that step did before being cut off.
func TestRollbackRefusesUnqualifiedSuccessAfterAGenuineMidStepCrash(t *testing.T) {
	env := newMigrateEnv(t)
	j := &migrate.Journal{Version: 1, StartedAt: 1}
	s := j.Begin(6, "switch the database files")
	s.StartedAt = 1
	// One rename genuinely happened and was journaled before the simulated crash.
	from := filepath.Join(env.Cfg.Home, "swarm.db")
	to := filepath.Join(env.Cfg.Home, "swarm-v1.db")
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	s.Add(migrate.Action{Kind: "rename", From: from, To: to})
	// s.DoneAt is deliberately left zero: this step never finished.
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}

	err := env.Runner.Rollback(context.Background())
	if err == nil {
		t.Fatal("want an error: step 6 never finished, so Rollback cannot claim full success")
	}
	if !strings.Contains(err.Error(), "interrupted") || !strings.Contains(err.Error(), "6") {
		t.Errorf("err = %v, want it to name the interrupted step", err)
	}
	// The one action that WAS journaled must still have been undone.
	if _, err := os.Stat(from); err != nil {
		t.Errorf("the journaled rename was not undone: %v", err)
	}
	// The journal is still removed even though full success cannot be claimed: a
	// later swarm migrate should start clean rather than seeing a stale journal.
	if j2, err := migrate.LoadJournal(env.Cfg.Home); err != nil || j2 != nil {
		t.Errorf("journal = %+v, %v; should have been removed", j2, err)
	}
}

// Important 2: step 7's resume guard compares the on-disk file's actual hash
// against the sha256 already recorded in the database, not just whether a file
// exists at that path — so a truncated write (a crash mid-os.WriteFile) gets
// repaired instead of silently accepted as "already done". Everything needed to
// detect and fix this is already in the database (content and sha256).
func TestKnowledgeBaseRepairsATruncatedNoteFileOnResume(t *testing.T) {
	env := newMigrateEnv(t)
	env.Runner.FailAfter = 6 // stop right before step 7 ever runs
	if err := env.Runner.Migrate(context.Background()); !errors.Is(err, migrate.ErrInjected) {
		t.Fatalf("want ErrInjected, got %v", err)
	}
	// A stray, 0-byte file sits where step 7 will write one of the imported notes —
	// modeling a crash mid-os.WriteFile.
	target := filepath.Join(env.Cfg.Home, "kb", "imported", "SW-674.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	env.Runner.FailAfter = 0
	if err := env.Runner.Resume(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 || !strings.Contains(string(body), "SW-674") {
		t.Errorf("the truncated note was not repaired: %q", body)
	}
}

// A launchctl undo action that fails for a real reason (not "not loaded") is a
// reportable problem, not something to swallow — Rollback must still attempt every
// other action and report this one.
func TestRollbackReportsARealLaunchctlFailure(t *testing.T) {
	env := newMigrateEnv(t)
	plist := filepath.Join(env.Cfg.LaunchAgentsDir, install.Label+".plist")
	env.Fake.Responses["launchctl bootstrap gui/501 "+plist] = execx.Result{
		Err: errors.New("exit status 5: Input/output error")}
	j := &migrate.Journal{Version: 1, StartedAt: 1}
	s := j.Begin(2, "stop the Agent Swarm 1.x jobs")
	s.Add(migrate.Action{Kind: "launchctl", Args: []string{"bootstrap", "gui/501", plist}})
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}
	err := env.Runner.Rollback(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("err = %v, want it to report the launchctl failure", err)
	}
	// N1: the failing action's actual argv must be named, not just the error —
	// From/To are both empty for a launchctl action, so without this the message
	// used to read "launchctl  → : Bootstrap failed: 37…" with no way to tell
	// which launchctl call failed.
	if !strings.Contains(err.Error(), "bootstrap gui/501 "+plist) {
		t.Errorf("err = %v, want it to name the failing launchctl command", err)
	}
}

// A second run of step 7 (e.g. via --resume after a later step crashed) must not
// rewrite notes that are already correct on disk — only a genuine mismatch (a
// truncated or missing file) should trigger a rewrite (Important 2's other half).
func TestKnowledgeBaseIsIdempotentWhenNotesAreAlreadyCorrect(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(env.Cfg.Home, "kb", "imported", "SW-674.md")
	before, err := os.Stat(note)
	if err != nil {
		t.Fatal(err)
	}
	beforeBody, err := os.ReadFile(note)
	if err != nil {
		t.Fatal(err)
	}
	// Pretend only step 6 finished, so a resume re-runs step 7 (and 8, 9) fresh.
	j := &migrate.Journal{Version: 1, StartedAt: 1}
	s := j.Begin(6, "switch the database files")
	s.DoneAt = 1
	if err := j.Save(env.Cfg.Home); err != nil {
		t.Fatal(err)
	}
	if err := env.Runner.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(note)
	if err != nil {
		t.Fatal(err)
	}
	afterBody, err := os.ReadFile(note)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterBody) != string(beforeBody) {
		t.Error("the note's content changed")
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an already-correct note was rewritten (mtime changed)")
	}
}

// A rollback with nothing to roll back is not an error.
func TestRollbackWithNoJournalSaysSo(t *testing.T) {
	env := newMigrateEnv(t)
	if err := env.Runner.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.Out.String(), "Nothing to roll back") {
		t.Errorf("out = %q", env.Out.String())
	}
}

// §20 step 7: only inbox and handoffs are archived.
func TestKnowledgeBaseArchivesOnlyInboxAndHandoffs(t *testing.T) {
	env := newMigrateEnv(t)
	kb := filepath.Join(env.Cfg.Home, "kb")
	for _, name := range []string{"inbox", "handoffs", "specs", "plans", "decisions"} {
		if err := os.MkdirAll(filepath.Join(kb, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(kb, name, "a.md"), []byte("# "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.Runner.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"inbox", "handoffs"} {
		if _, err := os.Stat(filepath.Join(env.Cfg.Home, "kb-archive", name, "a.md")); err != nil {
			t.Errorf("kb-archive/%s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(kb, name)); !os.IsNotExist(err) {
			t.Errorf("kb/%s survived", name)
		}
	}
	for _, name := range []string{"specs", "plans", "decisions"} {
		if _, err := os.Stat(filepath.Join(kb, name, "a.md")); err != nil {
			t.Errorf("kb/%s was moved: %v", name, err)
		}
	}
	// Step 7 flushes the note bodies step 4 computed.
	if _, err := os.Stat(filepath.Join(kb, "imported")); err != nil {
		t.Errorf("kb/imported: %v", err)
	}
}

// stepName labels the subtests.
func stepName(n int) string {
	return map[int]string{2: "after stop", 3: "after backup", 4: "after build", 5: "after validate",
		6: "after switch", 7: "after kb", 8: "after integrations", 9: "after install"}[n]
}

// snapshot hashes every file under the fake home that a rollback must restore.
func snapshot(t *testing.T, c install.Config) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range []string{c.Home, c.LaunchAgentsDir, c.Codex(), c.Cursor(), c.Gemini(), c.Claude()} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			// The backups folder and the journal are migration artefacts, not state.
			if strings.Contains(p, string(filepath.Separator)+"backups"+string(filepath.Separator)) ||
				strings.Contains(p, string(filepath.Separator)+"migrate"+string(filepath.Separator)) {
				return nil
			}
			body, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			out[p] = sha256Hex(body)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
