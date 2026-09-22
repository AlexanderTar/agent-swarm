# agy Native Wake via `--input-format stream-json` Implementation Plan

## Task 0 (done): Fix the agy Onboarding-Isolation Bug

Files:
- `internal/adapter/agy.go`
- `internal/adapter/agy_test.go`

TDD Steps (completed):
1. Added `TestAgyIsolatedHomeCarriesOnboardingState` in `agy_test.go`: writes a fake `antigravity_state.pbtxt` (containing `agent_onboarding_completed: AGENT_ONBOARDING_STATE_COMPLETED`) alongside the token/settings fixtures already used by `TestAgyIsolatedMCPAndInstructions`, then asserts it is readable at the same relative path inside the isolated `agyHome`.
2. Ran `go test ./internal/adapter/... -run TestAgyIsolatedHomeCarriesOnboardingState -v` — failed (`no such file or directory`), confirming the allowlist drops the file.
3. In `internal/adapter/agy.go`, replaced the two-file symlink loop in `setupEnv` with a single symlink of the whole `~/.gemini/antigravity-cli/` directory (falling back to `os.MkdirAll` when the real directory doesn't exist yet).
4. Reran the new test — passed. Ran the full package: `go test ./internal/adapter/... -v` — all 39 tests pass, no regressions (the existing `TestAgyIsolatedMCPAndInstructions` still passes because `os.Stat` follows the new directory symlink transparently). `go build ./...` clean.

## Task 1: Protocol-Probe Spike — Confirm the Wire Schema and Concurrency Safety

This is a real, runnable Go test, not a scratch script — but it makes real paid API calls against the signed-in `agy` account, so it is gated behind an env var and skipped by default (including in CI).

Files:
- `internal/adapter/agy_wake_probe_test.go`

Steps:
1. Add `TestAgyStreamJSONProtocolProbe(t *testing.T)`:
   ```go
   func TestAgyStreamJSONProtocolProbe(t *testing.T) {
   	if os.Getenv("AGY_LIVE_PROBE") != "1" {
   		t.Skip("live probe against real agy CLI + API; set AGY_LIVE_PROBE=1 to run")
   	}
   	...
   }
   ```
2. Helper `runAgyStreamTurn(t, conversationID, promptText string) (status, response string)`:
   - Build argv: `agy --input-format stream-json --output-format stream-json --dangerously-skip-permissions --print=''`, prepending `--conversation <conversationID>` when non-empty.
   - `exec.Command`, `cmd.StdinPipe()`, `cmd.StdoutPipe()`, `cmd.Start()`.
   - Marshal `{"event":"user","message":{"prompt_text":promptText}}` with `encoding/json`, write it plus `"\n"` to stdin in one `Write` call, then `stdin.Close()`.
   - Scan stdout line by line, decoding each as `{"event":string, "conversation_id":string, "result":{"status":string,"response":string}}` (extra fields ignored); keep the last `event:"result"` line seen.
   - `cmd.Wait()` with a 60s timeout.
   - Return the result's `status` and `response`, and the `conversation_id` from the `init` line (needed by step 3).
3. **Schema confirmation** (part A of the test): call `runAgyStreamTurn(t, "", "reply with the exact single word PROBE_A and nothing else")` three times, each with no `--conversation` (fresh conversation each run). Assert all three return `status == "SUCCESS"` and `response` contains `PROBE_A`.
4. **Concurrency / non-corruption check** (part B of the test): 
   - Call `runAgyStreamTurn(t, "", "reply with the exact single word ANCHOR and nothing else")`, capture its `conversation_id`.
   - Sequentially call `runAgyStreamTurn(t, conversationID, "reply with the exact single word MARKER_1 and nothing else")`, then `MARKER_2`, then `MARKER_3`, asserting `SUCCESS` each time.
   - Call `runAgyStreamTurn(t, conversationID, "list every marker word you were just told, in the exact order you received them, space-separated, and nothing else")`.
   - Assert the final response contains `MARKER_1 MARKER_2 MARKER_3` in that order.
5. Run: `AGY_LIVE_PROBE=1 go test -v ./internal/adapter/... -run TestAgyStreamJSONProtocolProbe`.

**Result (done):** first pass (guessed field names via `tmux send-keys`, real `$HOME`) was inconclusive — one real success, several `"message has no content"` failures, and a 60s timeout with no error against the officially-documented schema. The advisor traced the timeout to the real `$HOME`'s `agentmemory` MCP server (`~/.gemini/settings.json`) cold-starting on every agy launch — a confound the harness needed to control for, since production `Wake()` runs under the isolated `agy-home` (no extra MCP servers), not the real `$HOME`. A web search surfaced Google's own docs (antigravity.google/docs/cli/headless/), which give the schema directly: `{"event":"user","message":{"content":"<string>"}}`, no `--print` flag. Rewriting the harness to (a) use `newAgy(deps).setupEnv(...)`'s isolated HOME — the exact environment `Wake()` runs under — and (b) hold stdin open until the result event arrives instead of closing immediately after the write, both `SchemaConfirmation` (3/3 fresh conversations, 12.6s total) and `ConcurrencySafety` (5-turn sequence, correct marker ordering, 24.6s total) passed clean. See `internal/adapter/agy_wake_probe_test.go`.

## Task 1a: Prerequisite — `execx.StartEnv`

`execx.Starter`/`Deps.Start` take no environment override; every existing caller (`catalog/fetch.go`'s and `internal/usage/codex.go`'s `codex app-server` probes) is fine running under the daemon's own ambient environment. `Agy.Wake` is not: `--conversation <id>` only finds the right conversation under the session's isolated `agy-home`, not the daemon's real `$HOME`. Rather than change `Starter`'s signature (which would ripple into `catalog`, `usage`, and their tests for no reason), added a sibling seam:

Files: `internal/execx/execx.go`, `internal/execx/execx_test.go`, `internal/adapter/adapter.go` (`Deps.StartEnv` field), `cmd/swarm/daemon.go` (wire `StartEnv: execx.StartEnv`).

Done: `TestStartEnvMergesExtraVariablesOverTheAmbientEnvironment` and `TestStartEnvKeepsTheAmbientEnvironmentOtherwiseIntact` (red without `StartEnv`, green after adding it as `Start`'s twin sharing a `startCmd` helper, plus `cmd.Env = os.Environ()` merged with the override map).

## Task 2: Implement `Agy.Wake` (done)

Files:
- `internal/adapter/agy.go`
- `internal/adapter/agy_test.go`

TDD Steps (as run):
1. Replaced the now-obsolete `TestAgyHasNoNativeWake` (agy genuinely has native wake now — the old assertion is not a behavior anyone wants preserved) with `TestAgyWakeWritesTheDocumentedPayloadWithoutBlockingOnTheTurn` and `TestAgyWakeReturnsFalseWhenStartFails` in `agy_test.go`. The first stubs `Deps.StartEnv` to return a fake `*execx.Proc` whose `Stdout` is one end of an `io.Pipe()` (so the test controls exactly when the "turn" finishes) and whose `Stdin` is an in-memory capturing `io.WriteCloser`; it asserts (a) `Wake` returns `(true, nil)` within 500ms even though nothing has been written to the pipe yet — proving `Wake` does not block on the turn — (b) the argv and `HOME` env passed to `StartEnv` match Decisions 2-4, (c) the bytes written to stdin unmarshal to `{"event":"user","message":{"content":"hello"}}`, (d) stdin is *not* yet closed right after `Wake` returns, and (e) once the test writes a canned `result` event to the pipe, a background reaper closes stdin and calls `Kill` within 2s.
2. Ran `go test ./internal/adapter/... -run TestAgyWake -v` — failed (stub always returns `(false, nil)`).
3. Implemented `Wake` and `drainWakeTurn` in `agy.go` per Decisions 3-7: `StartEnv` with `HOME` set to the session's isolated `agy-home`, argv per Decision 4, marshal-and-write the confirmed payload, then `go drainWakeTurn(proc)` and return `(true, nil)` immediately. `drainWakeTurn` scans `proc.Stdout` for a line containing `"event":"result"` (or a 5-minute bound, `agyWakeResultTimeout`), then closes stdin and kills the process.
4. Ran `go test ./internal/adapter/... -run TestAgyWake -v` — passed. Ran `go test -race ./internal/adapter/... ./internal/execx/...` — passed, no data races. Ran `go build ./...` and the full `go test ./...` (after `make web-build`, which a fresh worktree needs since `web/dist` is gitignored) — all green.

## Task 3: Update Skills & Docs — not applicable

Checked `skills/swarm/SKILL.md` for wake-mechanism content to update: there isn't any. The skill is agent-facing (how an agent should behave — call `swarm_sync`, ack messages, etc.); wake delivery is a daemon-internal implementation detail no agent-facing instruction describes per-kind. No file needed a change here.

## Task 4: Live Verification (manual, not unit-testable)
1. `swarm start` (or `swarm new`) a real epic/spike with an `agy` orchestrator and one `agy` child.
2. From the child, `swarm_send` to the parent while the parent is mid-turn (not idle).
3. Tail `~/.swarm/logs/daemon.err.log` and confirm a `wake: native wake for <parent>` line with no error, and that `WakeDue` does not fall through to `tryPaste` for that message (no corresponding tmux paste in the log).
4. Confirm the parent's next `swarm_checkpoint`/`swarm_sync` reflects it saw the message without a human or paste having triggered it.
