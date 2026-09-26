# Plan: codex short CODEX_HOME (SUN_LEN fix)

Spec: `docs/specs/2026-09-26-codex-short-home.md`. TDD order, each step:
failing test -> run and watch fail -> minimal implementation -> run and
verify pass -> commit.

## 1. `execx.RunEnv`

- Test (`internal/execx/execx_test.go`): `TestRunEnvMergesExtraVariablesOverTheAmbientEnvironment`,
  `TestRunEnvKeepsTheAmbientEnvironmentOtherwiseIntact`,
  `TestRunEnvReportsStderrOnFailure` -- mirror the existing `StartEnv` tests
  but for a blocking, output-returning call.
- Impl (`internal/execx/execx.go`): `type RunnerEnv func(ctx, env, name, args...) ([]byte, error)`;
  `func RunEnv(...)` mirrors `Run` with `cmd.Env = os.Environ()` + overrides
  (mirrors `StartEnv`'s env-merge).
- `go test ./internal/execx/...`

## 2. `adapter.Deps.RunEnv`

- Add `RunEnv execx.RunnerEnv` field to `Deps` in `internal/adapter/adapter.go`.
- No test of its own (a struct field); covered by step 4's Wake test.

## 3. `CodexHomeDir` / `CodexHomeDirName`

- Test (`internal/adapter/codex_test.go`):
  - `TestCodexHomeDirSocketPathFitsSunLen`: home = `/Users/<32 a's>/.swarm`,
    session id = 30 `s`'s; `len(CodexHomeDir(home, id) + "/app-server-control/app-server-control.sock") <= 103`.
  - `TestCodexHomeDirIsDeterministicAndSessionScoped`: same (home, id) ->
    same path; different id -> different path.
- Impl (`internal/adapter/codex.go`): `CodexHomeDirName(sessionID) string`
  (`hex.EncodeToString(sha256.Sum256([]byte(sessionID))[:4])`);
  `CodexHomeDir(home, sessionID) string` (`filepath.Join(home, "cx", CodexHomeDirName(sessionID))`).
- `go test ./internal/adapter/... -run TestCodexHomeDir`

## 4. `setupEnv` + `flags` (`--no-daemon`) + `Wake`

- Test (`internal/adapter/codex_test.go`):
  - `TestCodexLaunchUsesTheShortHomeAndNoDaemon`: `Launch(...).Env["CODEX_HOME"] == CodexHomeDir(d.Home, spec.SessionID)`;
    argv contains `--no-daemon`.
  - `TestCodexWakeUsesTheSessionsCodexHome`: fake `d.RunEnv` closure captures
    env + argv (same pattern as `agy_test.go`'s `d.StartEnv` closures);
    assert `env["CODEX_HOME"] == CodexHomeDir(d.Home, w.SessionID)` and argv
    is `codex queue --thread <id> --message <notice>`.
- Impl (`internal/adapter/codex.go`):
  - `setupEnv`: `codexHome := CodexHomeDir(c.d.Home, s.SessionID)` (was
    `filepath.Join(c.d.launchDir(s.SessionID), "codex-home")`).
  - `flags`: prepend `"--no-daemon"` with the decision comment (spec
    decision 1).
  - `Wake`: `codexHome := CodexHomeDir(c.d.Home, w.SessionID)`;
    `c.d.RunEnv(ctx, map[string]string{"CODEX_HOME": codexHome}, "codex", "queue", "--thread", w.ProviderSessionID, "--message", w.Notice)`.
- `go test ./internal/adapter/...` (full package, confirm no regressions in
  the existing isolated-home / instructions / resume tests).

## 5. `reclaimCodexHomes` + `Reconcile` wiring

- Test (`internal/runtime/reconcile_test.go`):
  - `TestReclaimCodexHomesRemovesOnlyDeadSessionDirs`: `t.TempDir()` home,
    two dirs under `<home>/cx` named via `adapter.CodexHomeDirName`, one
    "live" id passed in -> only the other is removed.
  - `TestReclaimCodexHomesToleratesNoCxDir`: no `<home>/cx` at all -> no
    error.
- Impl (`internal/runtime/reconcile.go`): `func reclaimCodexHomes(home string, liveSessionIDs []string) error`
  (pure, `os.ReadDir` + `os.RemoveAll`, tolerates `os.IsNotExist`); call it
  from `Reconcile` after `ResumeOperations`, using the `live` slice already
  collected at the top of `Reconcile` (map each `liveRow.SessionID`); log and
  swallow its error (a cleanup miss must not fail the whole reconcile tick).
- `go test ./internal/runtime/... -run "TestReclaimCodexHomes|TestReconcile"`

## 6. Wire `RunEnv` in production

- `cmd/swarm/daemon.go`: `adDeps.RunEnv = execx.RunEnv`.
- `go build ./...`

## 7. Live probe (isolated tmux socket, scratch home, never `~/.swarm`)

- Compute a `CodexHomeDir`-shaped scratch path by hand (or via a throwaway
  test) for a scratch home + session id.
- `tmux -L cxprobe2 new-session ... "CODEX_HOME=<path> codex --no-daemon ..."`,
  answer the trust prompt, reach the idle prompt.
- `CODEX_HOME=<path> codex queue --thread <thread id> --message <text>` and
  confirm the pane picks it up.
- `tmux -L cxprobe2 kill-server`; `pgrep -fl codex` / `pgrep -fl app-server`
  to confirm no orphan process.
- Clean up the scratch dir.

## 8. Full verification

- `go test ./... -count=1`
- `go vet ./...`
- `test -z "$(gofmt -l .)"`

## Round 1 review fixes (2026-09-26)

Steps 1-8 above landed keyed on the swarm **session** id, which review round
1 found blocking: `startSession` mints a new session id on every resume, so
`CodexHomeDir` landed a resumed session in a fresh, empty home. See the
spec's "Review round 1" sections for the full reasoning. Fixes, TDD order:

### R1.1 Re-key on agent id

- Test (`internal/adapter/codex_test.go`): rename/rewrite
  `TestCodexHomeDirIsDeterministicAndAgentScoped` (was
  `...AndSessionScoped`) to use agent ids; `TestCodexLaunchUsesTheShortHomeAndNoDaemon`
  asserts against `CodexHomeDir(d.Home, spec.AgentID)`;
  `TestCodexWakeUsesTheAgentsCodexHome` (was `...TheSessionsCodexHome`) sets
  `WakeTarget.AgentID`; new `TestCodexResumeLandsOnTheSameHomeAsTheOriginalLaunch`
  (Launch with one spec, Resume with the same `AgentID` but a different
  `SessionID` and a `ProviderSessionID` -- same `CODEX_HOME` both times).
- Impl: `adapter.Spec` and `adapter.WakeTarget` gain `AgentID`;
  `CodexHomeDirName`/`CodexHomeDir` doc comments and params renamed to
  `agentID`; `setupEnv` uses `s.AgentID`; `Wake` uses `w.AgentID`;
  `internal/runtime/agents.go` `startSession`'s `adapter.Spec{}` gets
  `AgentID: a.ID`; `internal/runtime/wake.go`'s two `WakeTarget{}` sites gain
  `AgentID` (the `WakeOnQuotaReset` query needed `a.id` added to its
  `SELECT`).
- `go test ./internal/adapter/... ./internal/runtime/...`

### R1.2 `setupEnv` SUN_LEN runtime guard (MINOR 1)

- Test: `TestCodexSetupEnvRejectsAHomeThatWouldExceedSunLen` -- a real,
  writable `t.TempDir()`-based home padded long enough to push the socket
  path over 103 bytes (not an unwritable path, so the assertion is actually
  exercising the guard and not an incidental `MkdirAll` permissions error).
- Impl: `setupEnv` computes `sock := codexHome + codexSocketSuffix` and
  returns an error before `MkdirAll` if `len(sock) > codexSocketPathMax`.
- Side effect: `testDeps` (`internal/adapter/adapter_test.go`) had to stop
  using `t.TempDir()` directly for `Deps.Home` -- Go nests it under the full
  test name, which for several existing test names alone exceeds the
  budget. New `shortTempDir(t)` helper uses `os.MkdirTemp("/tmp", "sw")`
  instead (not `os.TempDir()`/`""`, whose macOS value,
  `/var/folders/<hash>/T/`, is itself already ~50 chars).

### R1.3 `reclaimCodexHomes`: agent-keyed keep-set, snapshot race guard, MINORs 2/3

- Test (`internal/runtime/reconcile_test.go`):
  - `TestReclaimCodexHomesRemovesOnlyDeadAgentDirs` (was
    `...DeadSessionDirs`): agent-id-keyed.
  - `TestReclaimCodexHomesWithEmptyHomeIsANoOp` (MINOR 2): asserts no `cx`
    dir appears in the test's cwd.
  - `TestReclaimCodexHomesLogsARemoveFailureAndKeepsSweeping` (MINOR 3): one
    entry's parent dir chmod'd read-only so its own `RemoveAll` fails;
    assert the failure is logged AND a second, removable entry still goes.
  - `TestReclaimCodexHomesSkipsADirNewerThanTheSnapshot` (IMPORTANT 2): a
    dir's real mtime is "now"; the sweep is told its snapshot was taken an
    hour ago; assert the dir survives.
  - `TestReconcileKeepsAPausedSessionsCodexHomeButRemovesADeadOnesSession`:
    updated to key off the WORKER AGENT's id (`w.ID`), not its session id.
- Impl: `reclaimCodexHomes(home string, keepAgentIDs []string, snapshotAt time.Time, logf func(string, ...any))`
  -- no return value (logs internally, never fails the reconcile tick);
  returns immediately on `home == ""`; keep-set built from
  `CodexHomeDirName(agentID)`; per-entry `info.ModTime().After(snapshotAt)`
  skip; per-entry `RemoveAll` failure goes through `logf`, loop continues.
  `Reconcile`'s call site: `snapshotAt := time.Now()` (real wall-clock, NOT
  `s.now()` -- it's compared against real filesystem mtimes, which don't
  respect an injected/logical clock); keep-set query changes to `SELECT
  DISTINCT agent_id FROM sessions WHERE state NOT IN (...)`.
- `go test ./internal/runtime/... -run "TestReclaimCodexHomes|TestReconcileKeeps"`

### R1.4 One-time `reclaimOldCodexLaunchHomes` (MINOR 4)

- Test: `TestReclaimOldCodexLaunchHomesRemovesOnlyTerminalSessionsCodexHome`
  -- two separate `newStore(t)` fixtures (one `worker()` per store; the
  existing `worker()` helper seeds a fixed `EPIC-1`/`TASK-1` item key, so
  calling it twice against the same store conflicts), one with its session
  forced to `completed` (its `codex-home` must go, a sibling file in the
  same per-launch dir must survive), one left non-terminal (its `codex-home`
  must survive).
- Impl: `(s *Store) reclaimOldCodexLaunchHomes(ctx) error` -- lists
  `<home>/run/launch/*`, queries terminal session ids, removes only
  `<entry>/codex-home` for terminal ids where it exists. Called from
  `Reconcile` alongside `reclaimCodexHomes`.

### R1.5 Full verification

- `go test ./... -count=1`
- `go vet ./...`
- `test -z "$(gofmt -l .)"`
- `go test -race ./internal/runtime/... -timeout 40m`
