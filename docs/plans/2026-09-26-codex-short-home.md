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
