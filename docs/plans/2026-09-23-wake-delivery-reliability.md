# Plan — 2026-09-23 wake delivery reliability

Spec: `docs/specs/2026-09-23-wake-delivery-reliability.md`.
Worktree `../agent-swarm--paste-truncation-fix`, branch `fix/wake-delivery-reliability`.
TDD throughout: failing test → watch it fail → minimal fix → watch it pass → commit.

## Task 1 — native wake is retried for each new message batch

**Files:** `internal/runtime/wake_test.go`, `internal/runtime/wake.go`.

1. Add `TestNativeWakeIsRetriedForEachNewMessageBatch` to `wake_test.go`,
   modelled on `TestNativeWakeWithoutASyncFallsBackToThePaste`: wake once
   (native, `fa.WakeOK = true`), advance the clock, send a *new* message to the
   same agent, run `WakeDue`, and assert `fa.Woke` grew — i.e. native was
   attempted a second time and nothing was pasted.
2. `go test ./internal/runtime/ -run NativeWakeIsRetried` — watch it fail
   (native is never re-attempted, so the notice is pasted instead).
3. In `wakeCandidates`, move the `r.NativeTried = true` assignment out of the
   `if lastWake.Valid` block to after `r.NewestPendingAt` is set:

   ```go
   if r.LastWakeAt != nil {
       r.NativeTried = !r.NewestPendingAt.After(*r.LastWakeAt)
   }
   ```
4. `go test ./internal/runtime/` — all green, including
   `TestNativeWakeSkipsThePaste` and `TestNativeWakeWithoutASyncFallsBackToThePaste`.
5. Commit: `fix(wake): retry native wake for each new message batch`.

## Task 2 — native wake stops claiming success with nobody listening

**Files:** `internal/adapter/adapter.go`, `internal/adapter/claude.go`,
`internal/adapter/claude_test.go`, `internal/runtime/wake.go`,
`cmd/swarm/daemon.go`, `internal/httpapi/agentio_test.go`.

1. Add `TestClaudeWakeIsNotDeliveredWithoutASubscriber` to `claude_test.go`:
   a `Deps.PublishWake` returning `(false, nil)` must make `Claude.Wake`
   return `delivered == false`.
2. Watch it fail to compile (signature is `error`-only) — that is the failure.
3. Change:
   - `adapter.Deps.PublishWake` → `func(ctx context.Context, sessionID, notice string) (bool, error)`
   - `Claude.Wake` → return the bool through.
   - `Store.PublishWake` → `return len(subs) > 0, nil`.
   - `cmd/swarm/daemon.go` and `internal/httpapi/agentio_test.go` call sites.
4. `go build ./... && go test ./internal/adapter/ ./internal/runtime/ ./internal/httpapi/` — green.
5. Commit: `fix(wake): a native claude wake with no subscriber is not delivered`.

## Task 3 — chunked, paced paste

**Files:** `internal/spawn/tmux_test.go`, `internal/spawn/tmux.go`.

1. Add `TestPasteLineNeverExceedsOneTtyReadPerChunk` to `tmux_test.go`. Real
   tmux, same `newSpawner(t)` harness as its neighbours; `t.Skip` if `python3`
   is missing, mirroring the existing tmux skip. The pane runs a raw-mode
   reader that records the length of every `os.read()`:

   ```python
   import sys, os, tty, termios, select, time
   fd = sys.stdin.fileno(); tty.setraw(fd)
   sizes, total, t0 = [], 0, time.time()
   while time.time() - t0 < 5:
       if select.select([fd], [], [], 0.2)[0]:
           b = os.read(fd, 65536)
           if not b: break
           sizes.append(len(b)); total += len(b)
   termios.tcsetattr(fd, termios.TCSAFLUSH, termios.tcgetattr(fd))
   open(sys.argv[1], "w").write("%d %s" % (total, max(sizes)))
   ```

   Paste ~1700 bytes via `PasteLine`, then assert total == len(payload) **and**
   max read < 1022.
2. `go test ./internal/spawn/ -run NeverExceedsOneTtyRead` — watch it fail on
   `main`'s single write (max read == 1022).
3. Rewrite `pasteViaTempFile`: split `line` into ≤`pasteChunkSize` chunks cut
   on rune boundaries; per chunk rewrite the temp file, `load-buffer`,
   `paste-buffer -d`, then a ctx-aware `pasteChunkGap` sleep; after the loop
   sleep once more, then `Keys(ctx, name, "Enter")`.
4. `go test ./internal/spawn/` — green, `TestPasteLineDeliversExactlyOneLine`
   included (it now exercises the single-chunk path).
5. Commit: `fix(paste): chunk long notices so the target's tty read never truncates`.

## Task 4 — docs + full verification

1. `go build ./... && go test ./...`
2. Commit the spec and plan.
