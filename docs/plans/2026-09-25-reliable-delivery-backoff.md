# Plan — 2026-09-25 reliable delivery with exponential backoff

Companion to `docs/specs/2026-09-25-reliable-delivery-backoff.md`.
Worktree: `../agent-swarm--reliable-delivery`, branch
`fix/reliable-delivery-backoff`. Strict TDD order below.

## Task 1 — Failing test: exponential backoff shape

File: `internal/runtime/wake_test.go` (append; reuse `newStore(t)` + fake clock).

```go
func TestWakeBackoffIsExponentialWithFiveMinuteCap(t *testing.T) {
    cases := map[int]time.Duration{
        0: 5 * time.Second, 1: 10 * time.Second, 2: 20 * time.Second,
        3: 40 * time.Second, 4: 80 * time.Second, 5: 160 * time.Second,
        6: 5 * time.Minute, 10: 5 * time.Minute,
    }
    for n, want := range cases {
        if got := backoffForFailures(n); got != want {
            t.Errorf("backoffForFailures(%d) = %v, want %v", n, got, want)
        }
    }
}
```

Run: `go test ./internal/runtime/ -run TestWakeBackoffIsExponential -v` → RED
(`undefined: backoffForFailures`). Commit test only (no impl).

Consumes: nothing. Produces: failing test.

## Task 2 — Minimal impl: backoff func + consts

File: `internal/runtime/wake.go`.

```go
const wakeFailBase = 5 * time.Second
const wakeFailCap = 5 * time.Minute

func backoffForFailures(n int) time.Duration {
    d := wakeFailBase << n // 5s * 2^n
    if d <= 0 || d > wakeFailCap {
        return wakeFailCap
    }
    return d
}
```

Run Task 1 test → GREEN. Run
`go test ./internal/runtime/ -run 'Wake|Undeliverable|Paste' ` → all pass.
Commit.

## Task 3 — Failing test: failure backoff gates retries, reset on success

Append to `internal/runtime/wake_test.go`:

```go
// Two failures space the next attempt by 20 s (not fixed 30 s); a success
// resets the count so the next failure starts at 5 s again.
func TestWakeFailureBackoffGatesAndResets(t *testing.T) { ... }
```

Suggested body: StartSpike fake agent, enqueue immediate message, advance
past `pasteDelay`, force two `recordWakeFailure`-equivalent states via two
`WakeDue` passes with non-idle pane (use `Pane{Command:"swarm-fake-agent"}`
+ busy capture or empty command), assert third pass within 20 s does not
paste and after 20 s does; then `markWoken`, fail once, assert next gate is
5 s. Use existing helpers (`panes()`, `at.Advance`, fake `Tmux.PasteLine`
count). Keep it to one test; mirror `TestPasteRetryIntervalEnforced` shape.

Run → RED (still fixed-30 s gating). Commit test only.

## Task 4 — Wire backoff into WakeDue + recordWakeFailure

`internal/runtime/wake.go`:

- Add `recordWakeFailure(ctx, sessionID, at)` (increments `pasteAttempts`,
  stamps `lastPasteAttemptAt`; mirrors `recordPasteAttemptMem` so existing
  `getPasteAttempts` keeps working; update comments: map now counts
  consecutive wake failures, not just paste skips).
- Replace fixed check (old line ~195):
  `s.Now().Sub(*LastPasteAttemptAt) < pasteRetry` with
  `< backoffForFailures(r.PasteAttempts)`.
- Keep success cooldown untouched.

Run Task 3 test → GREEN. Full runtime wake tests GREEN. Commit.

## Task 5 — Failing test: per-session error isolation

```go
// Session A Capture-errors every tick; session B idle with pending must
// still be pasted and WakeDue must return nil.
func TestWakeDueIsolatesPerSessionPasteErrors(t *testing.T) { ... }
```

Use `erroringTmux` per-session variant or a fake whose `Capture` fails only
for A's tmux name (extend the existing fake in-test, do not change prod
fakes). Two spikes, both with pending immediate, B idle, A capture-error.
`WakeDue` → nil error, B pasted, A failure count 1.

Run → RED (currently returns err, B skipped). Commit test only.

## Task 6 — Isolate tryPaste + markWoken errors (always-nil tryPaste)

`internal/runtime/wake.go`:

- `tryPaste` logs Capture/PasteLine/markWoken failures with
  `fail=%d backoff=%s`, calls `recordWakeFailure`, returns nil always.
- `WakeDue`: native `Wake` error → log + `recordWakeFailure`, fall through
  (no return); native `markWoken` error → log + `recordWakeFailure`,
  `continue` (no return); `tryPaste` call ignores error, continues loop.
- Keep `Panes()` global error propagating (existing test pinned).

Run Task 5 test → GREEN. Run
`go test ./internal/runtime/ ./internal/adapter/ ./internal/spawn/` → GREEN.
`go vet ./...`, `gofmt -l .` clean. Commit.

## Task 7 — Full verification + report

1. `go test ./...` (or `./internal/...` if web/menubar toolchains unavailable
   in worktree; record which was run).
2. Re-read the request checklist: reliable native-or-paste, exponential
   backoff on failure — verify each against the new tests.
3. Correlate live evidence once more (no new manual `"check inbox"` expected
   post-deploy; deploy itself out of scope for this branch).
4. Final `verification-before-completion` gate, then report. Do not merge;
   leave branch + worktree for review.
