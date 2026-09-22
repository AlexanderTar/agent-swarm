# agy Native Wake via `--input-format stream-json` Specification

## Prerequisite Fix (already delivered in this change): agy onboarding-isolation bug

While probing agy for a live-push mechanism, every swarm-spawned `agy` agent was found to fail 100% of the time: `internal/adapter/agy.go`'s `setupEnv` isolated the spawned agent's `HOME` and symlinked only two named files out of the real `~/.gemini/antigravity-cli/` (`antigravity-oauth-token`, `settings.json`). The onboarding-completed flag actually lives in a third file in that same directory, `antigravity_state.pbtxt` (`agent_onboarding_completed: AGENT_ONBOARDING_STATE_COMPLETED`), which the allowlist silently dropped. Every swarm-spawned agy therefore looked like a fresh install, hit the interactive "choose your color scheme" wizard, and hung until reaped — confirmed live (`agent.preflight_failed`, "Couldn't start agent") and reproduced/fixed in isolation before touching the real adapter.

**Fix**: `setupEnv` now symlinks the whole `~/.gemini/antigravity-cli/` directory instead of an allowlist of files, falling back to an empty local directory when the real one doesn't exist yet (fresh machine, first-ever agy spawn). This is unrelated to the native-wake work below except that it was found while investigating it, and it must land first — the wake mechanism can't be verified live against an agy that can't start.

- `internal/adapter/agy.go` — `setupEnv`
- `internal/adapter/agy_test.go` — `TestAgyIsolatedHomeCarriesOnboardingState` (regression test, red without the fix)
- Verified: `go test ./internal/adapter/...` (all 39 tests pass), `go build ./...`

## Context

1. **`agy.Wake()` is a stub today.** `internal/adapter/agy.go:216-218`:
   ```go
   func (a *Agy) Wake(ctx context.Context, sess WakeTarget) (bool, error) {
   	return false, nil
   }
   ```
   `WakeDue` (`internal/runtime/wake.go:107-171`) treats `delivered == false` as "no native wake available" and falls through to `tryPaste`: tmux `send-keys` of `IdleToken` into the pane, gated on the pane being confirmed idle (`internal/runtime/wake.go:198-214`). agy and cursor are the only two kinds with no native path; claude and codex both push live (claude via an SSE-backed MCP notification into the running process's stdin — `internal/adapter/claude.go:166-174`, `internal/mcpshim/shim.go:237-289`; codex via its own `codex queue --thread <id> --message <notice>` CLI — `internal/adapter/codex.go:211-217`).

2. **codex's pattern: a short-lived side-channel process, not a replacement execution model.** `Codex.Launch` (`internal/adapter/codex.go`) still runs codex interactively under tmux, unchanged. `Codex.Wake` separately shells `codex queue --thread <ProviderSessionID> --message <notice>` via `Deps.Run` (`internal/adapter/codex.go:211-217`) — a one-shot process that injects a message into the *same live thread* codex's `app-server` already holds open, then exits. The interactive tmux session is never touched or replaced.

3. **agy has no `queue`-equivalent subcommand.** `agy --help` subcommands are: `agent, agents, changelog, help, install, mcp, mic-serve, models, plugin, plugins, remote-control, update`. None injects a message into a live conversation by ID.

4. **`agy --remote-control` is a human browser-takeover feature, not automation IPC.** Confirmed live: `agy --remote-control` (interactive) prints `Open https://antigravity.google.com/r/<id> on another device to take over.` — a WebRTC/browser remote-control surface tied to a Google-account-scoped background daemon (`agy remote-control status` shows a single machine-wide daemon, not a per-session channel). Ruled out.

5. **`--input-format stream-json` is a genuine long-lived, multi-turn process.** `agy --input-format stream-json --output-format stream-json --print=''` starts a process that reads NDJSON from stdin and does not exit after one line — confirmed live: it tolerated multiple unrecognized `event` values (`warning: ignoring unsupported stream input message event "<x>"`, process stays alive) and one genuinely fatal-but-recognized value (`error: stream input message event "control_request" is not supported yet` — the exact `control_request`/`control_response` naming Claude Code's own control protocol uses, so this is clearly headed toward a real push protocol, just unfinished in build 1.2.8).

6. **`--conversation <id>` attaches a second process to an existing, still-running conversation.** Confirmed live: with an interactive `agy -i` session live and idle on conversation `462eee4b-...`, a second process launched with `agy --conversation 462eee4b-... --input-format stream-json --output-format stream-json --print=''` produced an `init` event carrying the *same* conversation_id and consumed real tokens sized to that conversation's existing history (~19.8k input tokens, matching the prior exchange) — i.e. it read the live conversation's state, not a fresh one. This is the structural analog to codex's `queue --thread`: a short-lived side process targeting an existing conversation ID.

7. **The exact input schema is real but not yet reliably reproducible.** One clean run with `{"event":"user","message":{"prompt_text":"say hi and nothing else"}}` produced a full, real turn: `step_update` events (`step_type:"user_input"` → `"agent_response"` with streamed `text_delta`) then `{"event":"result","status":"SUCCESS","response":"hi\n",...}` with real usage (20,151 input / 91 output tokens). A byte-identical retry in a fresh conversation, and a retry against the live `--conversation` attach in (6), both instead returned `error: stream input "user" message has no content` — despite the second case burning real tokens (19,873 input / 156 output / 155 thinking), suggesting the failure mode is inconsistent, not a fixed schema mismatch. All of this reverse-engineering was done via `tmux send-keys` typing into a pty, which is itself a plausible source of the flakiness (character-by-character injection with sleeps, not an atomic pipe write) — a real Go implementation writing the full line to `Proc.Stdin` in one `Write` call has not been tested and may not share this flakiness.

8. **The codebase already has the right seam for this.** `execx.Proc`/`execx.Starter` (`internal/execx/execx.go:35-66`) is documented exactly for this shape: *"Proc is a running process with an open stdin (codex app-server needs it open between writes)."* `catalog/fetch.go:193` already uses `Deps.Start` to hold a `codex ... app-server` subprocess's stdin open for JSON-RPC. Tests already stub `Deps.Start` with a fake `*execx.Proc` (`internal/catalog/fetch_test.go:229-237`). No new process-management primitive is needed — `Agy.Wake` reuses `Deps.Start` the same way.

9. **cursor-agent has no equivalent surface at all — confirmed out of scope.** No `--input-format` flag exists. `cursor-agent --print --output-format stream-json` is strictly one-shot: one `result` event, process exits immediately (confirmed live — session died right after the result). `agent persist`/`attach` is a human terminal-reattach feature (survives disconnect), functionally identical to what swarm already gets from tmux; not a programmatic push channel. `cursor.go`'s `Wake()` stub correctly reflects reality today; this spec does not change it.

## Locked Decisions

1. **`Agy.Wake` spawns a fresh, short-lived process per wake call — it does not hold a persistent pipe open across the agent's lifetime, and it does not touch `Launch`/`Resume`.** This mirrors codex's `Wake` exactly (one-shot side-channel, interactive session untouched): the primary agy session keeps running under tmux with today's `-i` interactive Launch unchanged.

2. **Target the live conversation via `--conversation <w.ProviderSessionID>`.** `WakeTarget.ProviderSessionID` (already populated for agy sessions — same field `Resume` already consumes) is passed straight through; no new field is added to `WakeTarget`.

3. **Reuse the session's existing isolated HOME**, not a fresh one. `Agy.Wake` computes `agyHome := filepath.Join(a.d.launchDir(w.SessionID), "agy-home")` — the exact path `setupEnv` already created at `Launch` time — and sets `HOME` to it. `setupEnv` is not re-run (the symlinks and `mcp_config.json` it wrote already exist there).

4. **Command line**: `agy --conversation <ProviderSessionID> --input-format stream-json --output-format stream-json --dangerously-skip-permissions --print=''`, started via `Deps.Start` (not `Deps.Run`, since a payload must be written to stdin).

5. **Wire payload**: write exactly one line, `{"event":"user","message":{"prompt_text":"<w.Notice>"}}\n`, to `Proc.Stdin`, then close it (`Stdin.Close()`) so agy sees EOF after the one turn and exits on its own. `w.Notice` is JSON-string-escaped (existing `encoding/json` marshal of the payload struct, not manual string concatenation).

6. **Reliability gate before this ships**: because finding 7 above shows the schema is not yet proven reliable, **Task 1 of the implementation plan is a dedicated protocol-probe spike** (a real Go program driving `exec.Command` + `StdinPipe`, not tmux) that must observe **three consecutive clean `SUCCESS` results across three distinct fresh conversations** before the schema in Decision 5 is treated as confirmed. If the spike instead finds a different reliable schema, that schema replaces Decision 5's payload verbatim (the field names in Decision 5 are the current best evidence, not an unchangeable contract) — but the *shape* of decisions 1-4 and 7-9 does not change.

7. **`Wake` returns `(true, nil)` once the process starts and the stdin write succeeds** (fire-and-forget — matching codex's semantics: `Wake` does not block on or verify the model actually acted on the notice). If `Deps.Start` fails, or the stdin write errors, `Wake` returns `(false, err)`.

8. **No behavior change to the existing paste fallback.** `WakeDue`'s existing logic (`internal/runtime/wake.go:137-150`) already falls through to `tryPaste` whenever `Wake` returns `false`. This spec does not touch that path — it stays as the safety net if the new native path ever fails for a given call, so partial reliability of the new mechanism is acceptable, not a blocker.

9. **Concurrency safety must be verified, not assumed.** The spike (Task 1) must also confirm that injecting via a second `--conversation`-attached process while the primary interactive session is live and idle does not corrupt or desync that session's own next interaction (run several injections back-to-back, then drive a real prompt through the *interactive* pane and confirm its response and history are intact).

10. **cursor is explicitly out of scope for this spec.** No change to `cursor.go`.

## File List
- `internal/adapter/agy.go` — implement `Wake`
- `internal/adapter/agy_test.go` — unit tests stubbing `Deps.Start`
- `internal/adapter/agy_wake_probe_test.go` (or a `cmd/`-local scratch probe, promoted to a real test once the schema is confirmed) — the Task 1 spike
- `skills/swarm/SKILL.md` / `internal/install/skills/swarm/SKILL.md` — note that agy now natively wakes, no longer relies solely on idle-paste

## Verification
- Run `go test -v ./internal/adapter/... -run TestAgyWake`
- Run `go test -v ./internal/runtime/... -run TestWakeDue`
- Manual live check: start a real agy session via `swarm start`, send it a `swarm_send` from another agent, confirm the daemon log shows `wake: native wake for <agent>` succeeding (not falling through to a paste), and that the agy session's own next `swarm_sync` sees the message without a tmux paste having occurred.
