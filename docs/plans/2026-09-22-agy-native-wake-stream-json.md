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
3. **Schema confirmation** (part A of the test): call `runAgyStreamTurn(t, "", "reply with the exact single word PROBE_A and nothing else")` three times, each with no `--conversation` (fresh conversation each run). Assert all three return `status == "SUCCESS"` and `response` contains `PROBE_A`. If any of the three fails, the test fails with the full stdout captured — that failure output is the evidence for picking a different schema (adjust the payload literal in the helper and rerun; do not guess blind, iterate against this harness).
4. **Concurrency / non-corruption check** (part B of the test): 
   - Call `runAgyStreamTurn(t, "", "reply with the exact single word ANCHOR and nothing else")`, capture its `conversation_id`.
   - Sequentially call `runAgyStreamTurn(t, conversationID, "reply with the exact single word MARKER_1 and nothing else")`, then `MARKER_2`, then `MARKER_3`, asserting `SUCCESS` each time.
   - Call `runAgyStreamTurn(t, conversationID, "list every marker word you were just told, in the exact order you received them, space-separated, and nothing else")`.
   - Assert the final response contains `MARKER_1 MARKER_2 MARKER_3` in that order — proving sequential `--conversation`-attached injections land in the same conversation, in order, without corrupting or dropping history.
5. Run: `AGY_LIVE_PROBE=1 go test -v ./internal/adapter/... -run TestAgyStreamJSONProtocolProbe`.
6. Record the confirmed-working payload shape (and any deviation from the spec's Decision 5 guess) as a doc comment directly above the constant added in Task 2 — this is the artifact this spike exists to produce.

## Task 2: Implement `Agy.Wake`

Files:
- `internal/adapter/agy.go`
- `internal/adapter/agy_test.go`

TDD Steps:
1. In `internal/adapter/agy_test.go`, add `TestAgyWake`:
   - Build a `Deps` with `Start` stubbed to a closure that: asserts the argv is exactly `["agy", "--conversation", "<id>", "--input-format", "stream-json", "--output-format", "stream-json", "--dangerously-skip-permissions", "--print="]` for a given `WakeTarget{SessionID: "ses_1", ProviderSessionID: "conv_1", Notice: "hello"}`; returns an `*execx.Proc` whose `Stdin` is an in-memory `io.WriteCloser` (e.g. backed by a `bytes.Buffer` wrapped to satisfy `io.WriteCloser`, recording what was written and whether `Close` was called) and whose `Stdout` is a `strings.NewReader` of a canned `{"event":"result","result":{"status":"SUCCESS"}}\n` line.
   - Assert `Wake` returns `(true, nil)`.
   - Assert the bytes written to `Stdin` unmarshal to the confirmed payload shape from Task 1, with `message.prompt_text == "hello"` (or whatever field Task 1 confirmed).
   - Assert `Stdin.Close()` was called.
   - Add a second case: `Deps.Start` returns an error; assert `Wake` returns `(false, err)`.
2. Run `go test -v ./internal/adapter/... -run TestAgyWake` and observe failure (current stub always returns `(false, nil)`).
3. In `internal/adapter/agy.go`, replace the stub:
   ```go
   type agyWakeMessage struct {
   	Event   string `json:"event"`
   	Message struct {
   		PromptText string `json:"prompt_text"`
   	} `json:"message"`
   }

   func (a *Agy) Wake(ctx context.Context, w WakeTarget) (bool, error) {
   	agyHome := filepath.Join(a.d.launchDir(w.SessionID), "agy-home")
   	proc, err := a.d.Start(ctx, "agy",
   		"--conversation", w.ProviderSessionID,
   		"--input-format", "stream-json", "--output-format", "stream-json",
   		"--dangerously-skip-permissions", "--print=")
   	if err != nil {
   		return false, err
   	}
   	msg := agyWakeMessage{Event: "user"}
   	msg.Message.PromptText = w.Notice
   	payload, err := json.Marshal(msg)
   	if err != nil {
   		proc.Kill()
   		return false, err
   	}
   	if _, err := proc.Stdin.Write(append(payload, '\n')); err != nil {
   		proc.Kill()
   		return false, err
   	}
   	if err := proc.Stdin.Close(); err != nil {
   		proc.Kill()
   		return false, err
   	}
   	return true, nil
   }
   ```
   (`agyHome`/`HOME` env wiring: `Deps.Start` needs the same `env["HOME"] = agyHome` treatment `setupEnv` gives `Launch`/`Resume` — extend `Deps.Start`'s call to pass env, or add an `a.d.StartEnv(ctx, env, name, args...)` seam if `Deps.Start` does not currently accept env. Check `execx.Starter`'s signature before writing this line: if it lacks an env parameter, that is a small, separate prerequisite change to `execx.Starter`/`Deps.Start` and every existing caller (`catalog/fetch.go`), done as its own red/green step before this one.)
4. Run `go test -v ./internal/adapter/... -run TestAgyWake` and verify pass.
5. Run the full adapter suite: `go test -v ./internal/adapter/...`.

## Task 3: Update Skills & Docs

Files:
- `skills/swarm/SKILL.md`
- `internal/install/skills/swarm/SKILL.md`

Steps:
1. Wherever the skill currently describes wake behavior (Rule 2/6 area), note that agy now natively wakes via a side-channel process rather than relying solely on idle-paste.
2. Run `make skills-sync`.

## Task 4: Live Verification (manual, not unit-testable)
1. `swarm start` (or `swarm new`) a real epic/spike with an `agy` orchestrator and one `agy` child.
2. From the child, `swarm_send` to the parent while the parent is mid-turn (not idle).
3. Tail `~/.swarm/logs/daemon.err.log` and confirm a `wake: native wake for <parent>` line with no error, and that `WakeDue` does not fall through to `tryPaste` for that message (no corresponding tmux paste in the log).
4. Confirm the parent's next `swarm_checkpoint`/`swarm_sync` reflects it saw the message without a human or paste having triggered it.
