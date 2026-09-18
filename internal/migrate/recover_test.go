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
	// A restore whose backup copy is missing: this must fail...
	missing := filepath.Join(env.Cfg.Home, "backups", "config-x", "codex", "config.toml")
	s.Add(migrate.Action{Kind: "restore", From: missing, To: env.Cfg.Codex("config.toml")})
	// ...but a later, satisfiable action must still be attempted.
	moved := filepath.Join(env.Cfg.Home, "swarm-v1-test.db")
	if err := os.Rename(filepath.Join(env.Cfg.Home, "swarm.db"), moved); err != nil {
		t.Fatal(err)
	}
	s.Add(migrate.Action{Kind: "rename", From: filepath.Join(env.Cfg.Home, "swarm.db"), To: moved})
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
