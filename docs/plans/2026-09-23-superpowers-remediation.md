# Superpowers Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every swarm-spawned agent (Claude, Codex, Cursor, Agy) actually receive the superpowers plugin and swarm skills inside its isolated launch environment, fix the stale spec/plan path in the orchestrator skill, and close the gap that lets an epic/bug reach `completed` without ever producing a plan or debug report.

**Architecture:** Three independent fixes in the same repo: (1) adapter `setupEnv` symlink parity fixes in `internal/adapter/{codex,agy}.go`, verified by new unit tests and one cross-kind table-driven guard test; (2) a one-line path edit in `skills/swarm-orchestrator/SKILL.md`; (3) a new artifact-presence gate added to the existing `completed`-checkpoint validation in `internal/runtime/checkpoint.go`, reusing the existing `artifacts` table (no schema change).

**Tech Stack:** Go 1.x (standard library `os`, `path/filepath`, `database/sql`), the repo's existing `internal/adapter`, `internal/runtime`, and `internal/items` packages, Go's built-in `testing` package (table-driven tests, no external test framework).

**Spec:** `docs/specs/2026-09-23-superpowers-remediation.md`

## Global Constraints

- No new abstraction layer for path management — each adapter's `setupEnv` gets direct `symlinkIfExists` calls, matching the existing Agy fix's pattern exactly. Future drift is caught by a test (Task 3), not by production indirection.
- The enforcement gate (Task 5) applies only to root items of type `epic` or `bug` that are not `tdd_exempt`. `chore`, `story`, and `task` are never gated by this change.
- Never delete or weaken an existing test to make a new one pass. `TestWriteCheckpointAllowsOrchestratorCompletedOnItsOwnEpic` currently completes a fresh epic with no artifact — Task 5 must update it (register a plan first), not delete or loosen it.
- Every new/changed Go file must pass `go build ./...`, `go vet ./...`, and its package's `go test ./...` before being considered done.
- Commit after each task (or each numbered step within Task 5, since it has multiple independently meaningful commits).

---

### Task 1: Codex — carry hooks, skills, and the plugin cache into the isolated `CODEX_HOME`

**Files:**
- Modify: `internal/adapter/codex.go:52-68` (`setupEnv`)
- Test: `internal/adapter/codex_test.go`

**Interfaces:**
- Consumes: `symlinkIfExists(src, dst string) error`, already defined in `internal/adapter/agy.go` (same package `adapter`, no import needed).
- Produces: no new exported names — `setupEnv`'s behavior changes only.

- [ ] **Step 1: Write the failing test**

Add to `internal/adapter/codex_test.go`:

```go
// P0 (2026-09-23): setupEnv isolated CODEX_HOME to a fresh empty directory and
// symlinked only auth.json into it. ~/.codex/hooks.json (swarm's hook wiring),
// ~/.codex/skills/ (the swarm/swarm-orchestrator skills `swarm install`
// writes there), and ~/.codex/plugins/ (the superpowers marketplace plugin
// cache) were never carried over -- a spawned Codex agent had zero swarm
// protocol awareness and zero superpowers skills. Mirrors agy's fix
// (TestAgyIsolatedHomeCarriesSkillsAndHooks).
func TestCodexIsolatedHomeCarriesHooksSkillsAndPlugins(t *testing.T) {
	d := testDeps(t)

	hooksPath := filepath.Join(d.UserHome, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const hooksBody = `{"hooks":{"SessionStart":[]}}`
	if err := os.WriteFile(hooksPath, []byte(hooksBody), 0o644); err != nil {
		t.Fatal(err)
	}

	skillPath := filepath.Join(d.UserHome, ".codex", "skills", "swarm", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const skillBody = "# Working as a Swarm agent\n..."
	if err := os.WriteFile(skillPath, []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}

	pluginPath := filepath.Join(d.UserHome, ".codex", "plugins", "cache", "obra",
		"superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const pluginBody = "# Brainstorming\n..."
	if err := os.WriteFile(pluginPath, []byte(pluginBody), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := newCodex(d).Launch(codexSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	codexHome := l.Env["CODEX_HOME"]

	gotHooks, err := os.ReadFile(filepath.Join(codexHome, "hooks.json"))
	if err != nil {
		t.Fatalf("expected hooks.json reachable in the isolated home: %v", err)
	}
	if string(gotHooks) != hooksBody {
		t.Errorf("hooks.json = %q, want %q", gotHooks, hooksBody)
	}

	gotSkill, err := os.ReadFile(filepath.Join(codexHome, "skills", "swarm", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected the swarm skill reachable in the isolated home: %v", err)
	}
	if string(gotSkill) != skillBody {
		t.Errorf("skill body = %q, want %q", gotSkill, skillBody)
	}

	gotPlugin, err := os.ReadFile(filepath.Join(codexHome, "plugins", "cache", "obra",
		"superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected the superpowers plugin reachable in the isolated home: %v", err)
	}
	if string(gotPlugin) != pluginBody {
		t.Errorf("plugin body = %q, want %q", gotPlugin, pluginBody)
	}
}

// setupEnv must not fail when none of hooks.json/skills/plugins exist yet
// (a machine where `swarm install` hasn't run for codex, or a bare test Deps).
func TestCodexIsolatedHomeToleratesMissingHooksSkillsAndPlugins(t *testing.T) {
	d := testDeps(t)
	if _, err := newCodex(d).Launch(codexSpec(t)); err != nil {
		t.Fatalf("Launch must not fail when hooks/skills/plugins are absent: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/adapter/... -run TestCodexIsolatedHome -v`
Expected: `TestCodexIsolatedHomeCarriesHooksSkillsAndPlugins` FAILs with "expected hooks.json reachable in the isolated home: ... no such file or directory" (the second and third assertions would also fail, but the test stops at the first `t.Fatalf`). `TestCodexIsolatedHomeToleratesMissingHooksSkillsAndPlugins` PASSes already (nothing to symlink yet, so today's code is a no-op here) — that's expected, it's a regression guard for the fix about to be written, not a red step.

- [ ] **Step 3: Write minimal implementation**

In `internal/adapter/codex.go`, inside `setupEnv` (currently lines 52-68), add after the existing `auth.json` symlink block and before `return map[string]string{"CODEX_HOME": codexHome}, nil`:

```go
	for _, rel := range []string{"hooks.json", "skills", "plugins"} {
		if err := symlinkIfExists(
			filepath.Join(c.d.UserHome, ".codex", rel),
			filepath.Join(codexHome, rel),
		); err != nil {
			return nil, err
		}
	}
```

The full function becomes:

```go
func (c *Codex) setupEnv(s Spec) (map[string]string, error) {
	codexHome := filepath.Join(c.d.launchDir(s.SessionID), "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return nil, err
	}
	if c.d.UserHome != "" {
		userAuth := filepath.Join(c.d.UserHome, ".codex", "auth.json")
		if _, err := os.Stat(userAuth); err == nil {
			symAuth := filepath.Join(codexHome, "auth.json")
			_ = os.Remove(symAuth)
			if err := os.Symlink(userAuth, symAuth); err != nil {
				return nil, err
			}
		}
	}
	for _, rel := range []string{"hooks.json", "skills", "plugins"} {
		if err := symlinkIfExists(
			filepath.Join(c.d.UserHome, ".codex", rel),
			filepath.Join(codexHome, rel),
		); err != nil {
			return nil, err
		}
	}
	return map[string]string{"CODEX_HOME": codexHome}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/adapter/... -run TestCodexIsolatedHome -v`
Expected: both tests PASS.

- [ ] **Step 5: Run the full adapter package test suite**

Run: `go test ./internal/adapter/...`
Expected: PASS (no other test depended on `setupEnv` leaving `hooks.json`/`skills`/`plugins` absent).

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/codex.go internal/adapter/codex_test.go
git commit -m "fix(adapter): carry codex hooks, skills, and the plugin cache into its isolated home"
```

---

### Task 2: Agy — carry the superpowers plugin cache into the isolated home

**Files:**
- Modify: `internal/adapter/agy.go:70-73` (`setupEnv`)
- Test: `internal/adapter/agy_test.go`

**Interfaces:**
- Consumes: `symlinkIfExists(src, dst string) error` (same file, already defined).
- Produces: none new.

- [ ] **Step 1: Write the failing test**

Add to `internal/adapter/agy_test.go`, after `TestAgyIsolatedHomeCarriesSkillsAndHooks`:

```go
// P0 (2026-09-23): yesterday's fix carried hooks.json and (via the
// antigravity-cli symlink) the swarm/swarm-orchestrator skills into the
// isolated home, but never ~/.gemini/config/plugins/ -- so the superpowers
// marketplace plugin itself (brainstorming, systematic-debugging,
// writing-plans skill definitions) still wasn't reachable, even though
// Agy.SuperpowersInstalled() checks the real home and reports "installed."
func TestAgyIsolatedHomeCarriesSuperpowersPlugin(t *testing.T) {
	d := testDeps(t)
	pluginPath := filepath.Join(d.UserHome, ".gemini", "config", "plugins",
		"superpowers", "skills", "brainstorming", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const pluginBody = "# Brainstorming\n..."
	if err := os.WriteFile(pluginPath, []byte(pluginBody), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := newAgy(d).Launch(agySpec(t))
	if err != nil {
		t.Fatal(err)
	}
	agyHome := l.Env["HOME"]

	got, err := os.ReadFile(filepath.Join(agyHome, ".gemini", "config", "plugins",
		"superpowers", "skills", "brainstorming", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected the superpowers plugin reachable in the isolated home: %v", err)
	}
	if string(got) != pluginBody {
		t.Errorf("plugin body = %q, want %q", got, pluginBody)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/adapter/... -run TestAgyIsolatedHomeCarriesSuperpowersPlugin -v`
Expected: FAIL with "expected the superpowers plugin reachable in the isolated home: ... no such file or directory".

- [ ] **Step 3: Write minimal implementation**

In `internal/adapter/agy.go`, inside `setupEnv`, immediately after the existing `hooks.json` symlink block (currently lines 70-73):

```go
	if err := symlinkIfExists(filepath.Join(a.d.UserHome, ".gemini", "config", "hooks.json"),
		filepath.Join(agyHome, ".gemini", "config", "hooks.json")); err != nil {
		return nil, err
	}
	if err := symlinkIfExists(filepath.Join(a.d.UserHome, ".gemini", "config", "plugins"),
		filepath.Join(agyHome, ".gemini", "config", "plugins")); err != nil {
		return nil, err
	}
```

(Only the second `if` block is new; the first is the existing hooks.json symlink shown for placement context.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/adapter/... -run TestAgyIsolatedHomeCarriesSuperpowersPlugin -v`
Expected: PASS.

- [ ] **Step 5: Run the full adapter package test suite**

Run: `go test ./internal/adapter/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/agy.go internal/adapter/agy_test.go
git commit -m "fix(adapter): carry the superpowers plugin cache into agy's isolated home"
```

---

### Task 3: Cross-kind parity guard — every isolating kind's `setupEnv` must cover every root `SuperpowersInstalled` globs under

**Files:**
- Create: `internal/adapter/superpowers_parity_test.go`

**Interfaces:**
- Consumes: `testDeps(t)` (`internal/adapter/adapter_test.go`), `codexSpec(t)` (`internal/adapter/codex_test.go`), `agySpec(t)` (`internal/adapter/agy_test.go`), `newCodex(d)`/`newAgy(d)` constructors, `Launch(spec) (Launch, error)` where `Launch.Env map[string]string`.
- Produces: none new — this is a guard test only, run by CI/`go test ./...` like any other test.

**Purpose:** Tasks 1 and 2 fixed two instances of the same drift (a `SuperpowersInstalled()` glob path with no matching `setupEnv` symlink). This test makes a third instance of that drift fail CI immediately instead of shipping silently, without adding any production abstraction.

- [ ] **Step 1: Write the test**

```go
package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

// isolatingKindFixture describes, for one isolating agent kind, where its
// SuperpowersInstalled() check looks on the real UserHome and where its
// setupEnv's isolated environment is expected to make that same content
// reachable. A kind landing here with a mismatch means SuperpowersInstalled
// could report "installed" for a spawn that will not actually see the
// plugin -- exactly the bug fixed in Tasks 1 and 2.
type isolatingKindFixture struct {
	name          string
	realRel       []string // path segments under UserHome holding the plugin fixture
	isolatedRel   []string // path segments under the isolated home the fixture must appear at
	launch        func(t *testing.T, d Deps) (Launch, error)
	isolatedHome  func(l Launch) string
}

func superpowersParityFixtures() []isolatingKindFixture {
	return []isolatingKindFixture{
		{
			name:         "codex",
			realRel:      []string{".codex", "plugins", "cache", "obra", "superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md"},
			isolatedRel:  []string{"plugins", "cache", "obra", "superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md"},
			launch:       func(t *testing.T, d Deps) (Launch, error) { return newCodex(d).Launch(codexSpec(t)) },
			isolatedHome: func(l Launch) string { return l.Env["CODEX_HOME"] },
		},
		{
			name:         "agy",
			realRel:      []string{".gemini", "config", "plugins", "superpowers", "skills", "brainstorming", "SKILL.md"},
			isolatedRel:  []string{".gemini", "config", "plugins", "superpowers", "skills", "brainstorming", "SKILL.md"},
			launch:       func(t *testing.T, d Deps) (Launch, error) { return newAgy(d).Launch(agySpec(t)) },
			isolatedHome: func(l Launch) string { return l.Env["HOME"] },
		},
	}
}

func TestSuperpowersPluginReachesEveryIsolatedHome(t *testing.T) {
	for _, f := range superpowersParityFixtures() {
		t.Run(f.name, func(t *testing.T) {
			d := testDeps(t)
			real := append([]string{d.UserHome}, f.realRel...)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(real...)), 0o755); err != nil {
				t.Fatal(err)
			}
			const body = "# Brainstorming\n..."
			if err := os.WriteFile(filepath.Join(real...), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			l, err := f.launch(t, d)
			if err != nil {
				t.Fatal(err)
			}
			isolated := append([]string{f.isolatedHome(l)}, f.isolatedRel...)
			got, err := os.ReadFile(filepath.Join(isolated...))
			if err != nil {
				t.Fatalf("%s: superpowers plugin not reachable in isolated home: %v", f.name, err)
			}
			if string(got) != body {
				t.Errorf("%s: plugin body = %q, want %q", f.name, got, body)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it passes**

Run: `go test ./internal/adapter/... -run TestSuperpowersPluginReachesEveryIsolatedHome -v`
Expected: both `codex` and `agy` subtests PASS (Tasks 1 and 2 already fixed the underlying code).

- [ ] **Step 3: Commit**

```bash
git add internal/adapter/superpowers_parity_test.go
git commit -m "test(adapter): guard that every isolating kind's setupEnv carries the superpowers plugin"
```

---

### Task 4: Cursor — verify whether `CURSOR_DATA_DIR` isolates plugin resolution, fix only if it does

**Files:**
- Modify (conditionally, only if verification finds a real gap): `internal/adapter/cursor.go:50-74` (`setupEnv`)
- Test (conditionally): `internal/adapter/cursor_test.go`, and if fixed, add a `cursor` fixture to `superpowersParityFixtures()` in `internal/adapter/superpowers_parity_test.go` (Task 3's file).

**Interfaces:**
- Consumes: `symlinkIfExists` (same as Tasks 1-2), `testDeps(t)`.
- Produces: none new beyond what Tasks 1-3 already produce.

This is an empirical check, not a code-first task — the spec's locked assumption (`internal/adapter/cursor.go:49`'s existing comment) is that `CURSOR_DATA_DIR` doesn't affect plugin resolution, so there may be nothing to fix. Verify before writing any code.

- [ ] **Step 1: Run a real isolated cursor-agent session and inspect its plugin listing**

```bash
mkdir -p /tmp/cursor-isolation-check
CURSOR_DATA_DIR=/tmp/cursor-isolation-check cursor-agent --print "list your available skills" 2>&1 | tee /tmp/cursor-isolation-check.log
```

Read the output. Look for whether `superpowers`/`brainstorming` (or any plugin normally visible in a non-isolated session) appears.

- [ ] **Step 2: Compare against a non-isolated baseline**

```bash
cursor-agent --print "list your available skills" 2>&1 | tee /tmp/cursor-baseline.log
diff /tmp/cursor-baseline.log /tmp/cursor-isolation-check.log
```

- [ ] **Step 3a: If the two outputs show the same plugins (assumption holds, no fix needed)**

Add a one-line code comment recording the finding at `internal/adapter/cursor.go:49`, right after the existing sentence ending "...only CURSOR_DATA_DIR)":

```go
// Verified 2026-09-23 (docs/specs/2026-09-23-superpowers-remediation.md):
// an isolated CURSOR_DATA_DIR still resolves plugins from the real
// ~/.cursor/plugins -- cursor-agent's plugin lookup isn't CURSOR_DATA_DIR-relative.
```

Then:

```bash
git add internal/adapter/cursor.go
git commit -m "docs(adapter): record that cursor's plugin resolution is unaffected by CURSOR_DATA_DIR isolation"
```

Skip Steps 3b-6 below; Task 4 is done.

- [ ] **Step 3b: If the isolated output is missing plugins the baseline has (assumption is wrong, fix needed)**

Write the failing test in `internal/adapter/cursor_test.go`, mirroring Task 1's Step 1 pattern exactly but for Cursor:

```go
func TestCursorIsolatedDataDirCarriesSuperpowersPlugin(t *testing.T) {
	d := testDeps(t)
	pluginPath := filepath.Join(d.UserHome, ".cursor", "plugins", "cache", "obra",
		"superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const pluginBody = "# Brainstorming\n..."
	if err := os.WriteFile(pluginPath, []byte(pluginBody), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := newCursor(d).Launch(cursorSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	cursorHome := l.Env["CURSOR_DATA_DIR"]

	got, err := os.ReadFile(filepath.Join(cursorHome, "plugins", "cache", "obra",
		"superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected the superpowers plugin reachable in the isolated data dir: %v", err)
	}
	if string(got) != pluginBody {
		t.Errorf("plugin body = %q, want %q", got, pluginBody)
	}
}
```

(`cursorSpec(t)` is the existing spec-builder helper already defined at `internal/adapter/cursor_test.go:15` — used here as-is.)

- [ ] **Step 4b: Run to verify it fails**

Run: `go test ./internal/adapter/... -run TestCursorIsolatedDataDirCarriesSuperpowersPlugin -v`
Expected: FAIL, plugin not found.

- [ ] **Step 5b: Write minimal implementation**

In `internal/adapter/cursor.go`'s `setupEnv`, add before `return map[string]string{"CURSOR_DATA_DIR": cursorHome}, nil`:

```go
	if err := symlinkIfExists(filepath.Join(c.d.UserHome, ".cursor", "plugins"),
		filepath.Join(cursorHome, "plugins")); err != nil {
		return nil, err
	}
```

- [ ] **Step 6b: Run to verify it passes, add the parity fixture, commit**

```bash
go test ./internal/adapter/... -run TestCursorIsolatedDataDirCarriesSuperpowersPlugin -v
```

Then add a third entry to `superpowersParityFixtures()` in `internal/adapter/superpowers_parity_test.go`:

```go
		{
			name:         "cursor",
			realRel:      []string{".cursor", "plugins", "cache", "obra", "superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md"},
			isolatedRel:  []string{"plugins", "cache", "obra", "superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md"},
			launch:       func(t *testing.T, d Deps) (Launch, error) { return newCursor(d).Launch(cursorSpec(t)) },
			isolatedHome: func(l Launch) string { return l.Env["CURSOR_DATA_DIR"] },
		},
```

```bash
go test ./internal/adapter/...
git add internal/adapter/cursor.go internal/adapter/cursor_test.go internal/adapter/superpowers_parity_test.go
git commit -m "fix(adapter): carry the superpowers plugin cache into cursor's isolated data dir"
```

---

### Task 5: Enforcement gate — a root epic/bug can't reach `completed` without a registered plan/debug_report

**Files:**
- Modify: `internal/runtime/checkpoint.go` (add helpers near `verifyOK`/`priorVerify` at lines ~106-182; add the gate call inside the `WriteCheckpoint` transaction, immediately after the existing block ending at line 409)
- Modify: `internal/runtime/checkpoint_test.go:606-621` (`TestWriteCheckpointAllowsOrchestratorCompletedOnItsOwnEpic` — update, do not delete)
- Test: `internal/runtime/checkpoint_test.go` (new tests, added near the modified test)

**Interfaces:**
- Consumes: `items.Item{ID, Type, TddExempt}` (existing, `internal/items/model.go`), `items.Epic`/`items.Bug` type constants, `items.Error{Code, Message}` and `items.CodeBadRequest`, the existing `artifacts` table columns `item_id`, `kind` (`internal/runtime/artifacts.go`'s `RegisterArtifact`/`ArtifactsFor` already use these), `planBody`/`reportBody`/`writeFile(t, body)` test fixtures (`internal/runtime/helpers_test.go:227`, `internal/runtime/materialize_test.go:118`, `internal/runtime/helpers_test.go:235`), `s.RegisterArtifact(ctx, sessionID, op, itemKey, kind, path, requestID)` (existing, `internal/runtime/artifacts.go:327`), `seedTopLevelItem(t, s, typ)` (existing, `internal/runtime/checkpoint_test.go:521`), `s.StartOrchestrator(ctx, OrchestratorInput{ItemKey, Kind, Model})` (existing), `s.Items.Update(ctx, key, items.Patch{TddExempt, Revision}, actor)` (existing, `internal/items/store.go:325`).
- Produces: `requiredArtifactKind(it items.Item) string` and `(s *Store) hasArtifact(ctx context.Context, tx *sql.Tx, itemID, kind string) (bool, error)` — package-level in `internal/runtime`, callable by any future gate in the same package.

- [ ] **Step 1: Write the failing tests**

Add to `internal/runtime/checkpoint_test.go`, after `TestWriteCheckpointRefusesCompletedOnABug` (line 598) and before the existing `TestWriteCheckpointAllowsOrchestratorCompletedOnItsOwnEpic`:

```go
// P0 (2026-09-23): nothing required an orchestrator to have produced a plan
// before completing its own epic -- POST /api/items lets a human create an
// epic directly (no spike), so brainstorming/writing-plans could be skipped
// entirely. This closes that gap at the one place every root item's
// completion already passes through.
func TestCompletedOnEpicRequiresARegisteredPlan(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Epic)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "epic accepted"})
	if err == nil {
		t.Fatal("expected completed to be refused with no registered plan")
	}
	const want = `completed requires a registered plan for this epic. Register one with swarm_artifact register, or set tdd_exempt if this genuinely needs neither.`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestCompletedOnEpicSucceedsOnceAPlanIsRegistered(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Epic)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterArtifact(ctx, ses.ID, "register", it.Key, "plan", writeFile(t, planBody), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "epic accepted"}); err != nil {
		t.Fatalf("completed must succeed once a plan is registered: %v", err)
	}
}

func TestCompletedOnBugRequiresARegisteredDebugReport(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Bug)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "bug closed"})
	if err == nil {
		t.Fatal("expected completed to be refused with no registered debug_report")
	}
	const want = `completed requires a registered debug_report for this bug. Register one with swarm_artifact register, or set tdd_exempt if this genuinely needs neither.`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestCompletedOnBugSucceedsOnceADebugReportIsRegistered(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Bug)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterArtifact(ctx, ses.ID, "register", it.Key, "debug_report", writeFile(t, reportBody), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "bug closed"}); err != nil {
		t.Fatalf("completed must succeed once a debug_report is registered: %v", err)
	}
}

func TestCompletedOnChoreNeedsNoArtifact(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Chore)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "chore done"}); err != nil {
		t.Fatalf("a chore root must never require an artifact: %v", err)
	}
}

func TestCompletedOnTddExemptEpicNeedsNoArtifact(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Epic)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	exempt := "spike-research"
	if _, err := s.Items.Update(ctx, it.Key, items.Patch{TddExempt: &exempt, Revision: it.Revision},
		items.Orchestrator(orch.ID, orch.RootItemID)); err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "epic accepted"}); err != nil {
		t.Fatalf("a tdd_exempt epic must never require an artifact: %v", err)
	}
}
```

Then update the existing test at line 606 (do not delete it — CLAUDE.md's test discipline requires updating a test whose contract changed, not removing it) so it still exercises exactly what its comment says (an orchestrator ending its own epic must not be blocked by the *role* gate) without being incidentally blocked by the *new* artifact gate:

```go
func TestWriteCheckpointAllowsOrchestratorCompletedOnItsOwnEpic(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Epic)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterArtifact(ctx, ses.ID, "register", it.Key, "plan", writeFile(t, planBody), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "epic accepted"}); err != nil {
		t.Fatalf("an orchestrator must be able to end its own epic with completed: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/... -run TestCompletedOn -v`
Expected: `TestCompletedOnEpicRequiresARegisteredPlan` and `TestCompletedOnBugRequiresARegisteredDebugReport` FAIL (no error returned — completed currently succeeds unconditionally). The `...SucceedsOnce...`, `...ChoreNeedsNoArtifact`, `...TddExemptEpicNeedsNoArtifact` tests PASS already (today's code never refuses, so registering an artifact or not makes no difference yet) — that's expected.

Run: `go test ./internal/runtime/... -run TestWriteCheckpointAllowsOrchestratorCompletedOnItsOwnEpic -v`
Expected: PASS (the updated version registers a plan first, so it isn't testing the new gate's red state — it's confirming the *role* gate still passes, which was never broken).

- [ ] **Step 3: Write minimal implementation**

In `internal/runtime/checkpoint.go`, add these two functions near `verifyOK` (after it, before `jsonArray`, i.e. after line 122):

```go
// requiredArtifactKind returns the artifact kind a root item's completed
// checkpoint must have on record before it's accepted, or "" if none is
// required (not a gated root type, or tdd_exempt). Only epic and bug roots
// are gated -- chore is the codebase's designated lightweight root type,
// and story/task are never roots.
func requiredArtifactKind(it items.Item) string {
	if it.TddExempt != "" {
		return ""
	}
	switch it.Type {
	case items.Epic:
		return "plan"
	case items.Bug:
		return "debug_report"
	default:
		return ""
	}
}

// hasArtifact reports whether an artifact of the given kind is registered
// against itemID, queried inside the caller's own transaction so it sees
// anything registered earlier in the same request and can't race a
// concurrent registration.
func (s *Store) hasArtifact(ctx context.Context, tx *sql.Tx, itemID, kind string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM artifacts WHERE item_id = ? AND kind = ? LIMIT 1`,
		itemID, kind).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
```

Then, in the `WriteCheckpoint` transaction, immediately after the existing block that currently ends at line 409 (the `if in.Kind == CompletedCkp && it.TddExempt == "" { ... }` TDD-verify gate) and before `ckpID := ids.New("ckp")`:

```go
		if in.Kind == CompletedCkp {
			if kind := requiredArtifactKind(it); kind != "" {
				ok, err := s.hasArtifact(ctx, tx, it.ID, kind)
				if err != nil {
					return err
				}
				if !ok {
					return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
						"completed requires a registered %s for this %s. Register one with "+
							"swarm_artifact register, or set tdd_exempt if this genuinely needs neither.",
						kind, it.Type)}
				}
			}
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/... -run TestCompletedOn -v`
Expected: all six new tests PASS.

Run: `go test ./internal/runtime/... -run TestWriteCheckpointAllowsOrchestratorCompletedOnItsOwnEpic -v`
Expected: PASS.

- [ ] **Step 5: Run the full runtime package test suite**

Run: `go test ./internal/runtime/...`
Expected: PASS. Pay particular attention to any other existing test that completes an epic or bug root without registering a plan/debug_report first (e.g. anything in `materialize_test.go` that drives a spike through to a real completed epic) — if one exists and fails, update it the same way Step 1 updated `TestWriteCheckpointAllowsOrchestratorCompletedOnItsOwnEpic` (register the appropriate artifact before completing), never by deleting or weakening the assertion.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/checkpoint.go internal/runtime/checkpoint_test.go
git commit -m "feat(runtime): require a registered plan/debug_report before an epic/bug root can complete"
```

---

### Task 6: Fix the stale `~/.superpowers/` path in the orchestrator skill

**Files:**
- Modify: `skills/swarm-orchestrator/SKILL.md:45`

**Interfaces:** none — this is a text-only change to agent-facing instructions, not code.

- [ ] **Step 1: Make the edit**

Change line 45 from:

```
- Write specs to `~/.superpowers/specs/<YYYY-MM-DD>-<SPIKE-KEY>-<slug>.md`, plans to `~/.superpowers/plans/<YYYY-MM-DD>-<SPIKE-KEY>-<slug>.md`, debug reports to `~/.superpowers/specs/<YYYY-MM-DD>-<SPIKE-KEY>-debug-<slug>.md`. Never write them into a repo. Use `## ` headings for sections.
```

to:

```
- Write specs to `~/.swarm/specs/<YYYY-MM-DD>-<SPIKE-KEY>-<slug>.md`, plans to `~/.swarm/plans/<YYYY-MM-DD>-<SPIKE-KEY>-<slug>.md`, debug reports to `~/.swarm/specs/<YYYY-MM-DD>-<SPIKE-KEY>-debug-<slug>.md`. Never write them into a repo. Use `## ` headings for sections.
```

- [ ] **Step 2: Grep the repo for any other stale reference**

Run: `grep -rn "~/.superpowers\|\.superpowers/specs\|\.superpowers/plans" skills/ docs/ internal/ 2>/dev/null`
Expected: only the line just edited matches (confirms this was the only stale reference — if others appear, fix them the same way before committing).

- [ ] **Step 3: Commit**

```bash
git add skills/swarm-orchestrator/SKILL.md
git commit -m "docs(skills): move swarm-orchestrator spec/plan/debug-report path to ~/.swarm/, matching the existing worktree centralization"
```

---

## Follow-up (not in this plan)

**Interactive-session enforcement.** The last ~20 commits on `main` before this plan (agy-skills fixes, native-wake, repo-proposal schema) all carried the `Co-Authored-By: Claude Sonnet 5` trailer, meaning they came from an interactive Claude Code session, not a swarm-spawned one (`skills/swarm/SKILL.md` rule 10 forbids swarm-spawned agents from adding that trailer). Nothing in this plan reaches that population — no agent-swarm Go code runs in an interactive session's process. Closing that gap needs a Claude Code hook (`settings.json`, via the `update-config` skill) — e.g. a `PreToolUse` hook on `git commit` that checks whether the current session touched `docs/specs/`/`docs/plans/` before allowing a non-docs-only commit. This needs its own design pass (a real false-positive risk: legitimate no-spec commits like a one-line typo fix shouldn't be blocked), tracked as a separate task outside this plan per the spec's "Explicitly out of scope" section.
