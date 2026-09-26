# Codex short CODEX_HOME (SUN_LEN fix)

## Context

Since codex 0.157.0, codex starts a per-`CODEX_HOME` app-server daemon by
default. Its control socket lives at
`$CODEX_HOME/app-server-control/app-server-control.sock`. macOS caps a unix
socket path (`SUN_LEN`) at 104 bytes including the NUL terminator -- 103
usable bytes.

Swarm's per-launch `CODEX_HOME` (`internal/adapter/codex.go` `setupEnv`,
pre-fix) was `<swarm home>/run/launch/<session id>/codex-home`. On a real
machine that path is long enough that the socket path exceeds 103 bytes, so
every codex launch fails immediately:

```
failed to connect to …/app-server-control.sock: path must be shorter than SUN_LEN
```

Confirmed live (2026-09-26, isolated `tmux -L cxprobe`, scratch `CODEX_HOME`,
never `~/.swarm`):
- A short `CODEX_HOME` (e.g. `/tmp/cxp/ch`) starts fine.
- `--no-daemon` with the *long* path also starts fine (no daemon, no socket).
- A short *symlink* to the long path does NOT work: codex resolves the
  realpath before building the socket path.

Side effects of the pre-fix state: every failed launch left an orphan
`app-server --managed-daemon` process, and every `CODEX_HOME` installed a
~314 MB daemon package the first time the daemon started.

Separately, `Codex.Wake` (`codex.go`, pre-fix) ran `codex queue --thread ...`
with no `CODEX_HOME` set, so it always targeted the ambient `~/.codex`
instead of the session's isolated home. Confirmed live: `codex queue` against
a thread that lives in a different `CODEX_HOME` fails with `no rollout found
for thread id ... (code -32603)`. Every wake against an isolated-home codex
session (i.e. every codex session Swarm launches) silently failed.

## Locked decisions

1. **Codex launches with `--no-daemon`.** Probed live: `codex queue` (which
   `Wake` uses) works identically with or without the daemon running.
   `--no-daemon` therefore has no functional cost and two real benefits: no
   orphan `app-server --managed-daemon` process per session, and no ~314 MB
   daemon package installed per (now per-session, not shared) `CODEX_HOME`.
   Decision recorded as a comment at the `--no-daemon` flag site in
   `codex.go`.
2. **`CODEX_HOME` moves to `<swarm home>/cx/<8 hex chars>`**, where the 8 hex
   chars are `hex(sha256(session id)[:4])`. This is short regardless of how
   long the swarm home path or the session id are (verified: for a 32-char
   home path segment and a 30-char session id, the resulting socket path is
   101 bytes, under the 103-byte cap, even though this fix's own
   `--no-daemon` choice means that socket is never actually created --
   margin is kept in case a future codex version doesn't accept
   `--no-daemon`, or a manual daemon start is ever needed for debugging).
   Exposed as `adapter.CodexHomeDir(home, sessionID)` (full path) and
   `adapter.CodexHomeDirName(sessionID)` (just the hash, for the reconcile
   sweep's keep-set) so `setupEnv`, `Wake`, and the runtime cleanup sweep all
   compute the same path without duplicating the hash.
3. **`Wake` sets `CODEX_HOME`** to the same `CodexHomeDir` value `setupEnv`
   used for that session, via a new `execx.RunEnv` (mirrors the existing
   `execx.Run`/`execx.StartEnv` pair, filling the missing
   blocking-call-with-env-override quadrant) threaded through
   `adapter.Deps.RunEnv`.
4. **Cleanup is scoped to codex's own short-home dirs, not general
   launch-dir GC.** There is no launch-dir cleanup anywhere in this codebase
   today (`run/launch/<session id>/` is never removed, for any adapter, by
   any code path -- confirmed by exhaustive grep). Retrofitting a general
   teardown hook into the 9 existing `Tmux.Kill` call sites
   (`agents.go` x2, `checkpoint.go` x2, `pause.go`, `replacement.go`,
   `reconcile.go` x2, `workflow.go`) is out of scope for this bug fix. codex's
   short-home dirs are cheap to name deterministically from a live session
   id, so a small dedicated sweep (`reclaimCodexHomes`, called once per
   `Reconcile` tick, which already runs every 5s) removes any `<home>/cx`
   entry that isn't a currently-live session. No daemon process to kill
   (decision 1: `--no-daemon`).

## File list

- `internal/execx/execx.go` (+test): add `RunnerEnv` type and `RunEnv` func,
  mirroring `Runner`/`Run` with an env override (mirrors the existing
  `Starter`/`StarterEnv` pair).
- `internal/adapter/adapter.go`: add `Deps.RunEnv execx.RunnerEnv`.
- `internal/adapter/codex.go` (+test): add `CodexHomeDir`/`CodexHomeDirName`;
  `setupEnv` uses `CodexHomeDir` instead of the old per-launch path; `flags`
  adds `--no-daemon` with a decision comment; `Wake` uses `RunEnv` with
  `CODEX_HOME` set.
- `internal/runtime/reconcile.go` (+test): add `reclaimCodexHomes`, called
  once per `Reconcile` tick.
- `cmd/swarm/daemon.go`: wire `adDeps.RunEnv = execx.RunEnv`.

## Verification

- `go test ./... -count=1`
- `go vet ./...`
- `test -z "$(gofmt -l .)"`
- Live probe (isolated `tmux -L cxprobe`/`cxprobe2` sockets, scratch
  `CODEX_HOME` under a temp dir shaped like the real `CodexHomeDir` output,
  never `~/.swarm`): launched codex with `--no-daemon`, reached the idle
  prompt, queued a wake message with `CODEX_HOME` set to the session's short
  home, confirmed the agent received and echoed it, killed the pane, and
  confirmed no orphan `codex`/`app-server` process remained.

## Explicitly out of scope

- General launch-dir garbage collection for any adapter (claude, agy,
  cursor, muse). Tracked as pre-existing debt, not introduced or worsened by
  this change.
- Migrating already-running sessions' `CODEX_HOME`: a session launched before
  this fix keeps its old (broken) home until it's relaunched; there is no
  live-migration path and none is needed since those sessions were already
  failing to start.
