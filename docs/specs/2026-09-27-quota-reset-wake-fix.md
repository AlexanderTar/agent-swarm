# Spec: quota-reset wake actually reaches codex, agy, muse, cursor

## Context

User report: "codex doesn't seem to be receiving usage reset nudges, also
check muse, agy and cursor." Five independent root causes, all downstream of
`internal/runtime/wake.go`'s `WakeOnQuotaReset` and its daemon caller,
`cmd/swarm/daemon.go`'s `checkQuotaResets`.

### A — `WakeOnQuotaReset` never gives adapters a provider session id

`wake.go:540` (`WakeOnQuotaReset`'s row query) selects
`ses.id, a.id, a.name, ses.tmux_name, ses.state, ses.waiting, a.model,
a.effort` — never `ses.provider_session_id`. `wake.go:570`'s
`adapter.WakeTarget{...}` literal sets `SessionID`, `AgentID`, `TmuxName`,
`Notice`, `Model` — never `ProviderSessionID`. Contrast `wakeCandidates`
(`wake.go:62-66`, `WakeDue`'s row source), which does select and forward it.

Effect: `Codex.Wake` (`internal/adapter/codex.go:307-317`) runs
`codex queue --thread "" --message ...`, which fails live with `Error: No
active session found matching ''` (exit 1) — discarded by
`delivered, _ := ad.Wake(...)` at `wake.go:571`, never logged. `Agy.Wake`
(`internal/adapter/agy.go:404`) runs `agy --conversation ""`; `StartEnv`
still succeeds (it never validates the id), so `Wake` returns `true`, the
session is marked woken, and nothing was ever delivered to the real
conversation.

Confirmed live: `codex queue --thread <real-thread-id> --message ...`
delivers to a running `--no-daemon` TUI within ~6s, even with the
rate-limit/model-switch dialog open (see C).

### B — `checkQuotaResets` only ever sees the latest snapshot's `ResetsAt`

`daemon.go:460-479`: for each `usagesvc.Snapshot`/`Meter`, it takes
`cutoff := *m.ResetsAt` from the **current** poll and fires
`WakeOnQuotaReset` when `now` is in `(cutoff+1m, cutoff+1h)`. Codex's 5h
usage window restarts on first use after a reset (the user's own,
non-Swarm, codex use shares the same window), so as soon as anyone uses
codex past the reset instant, the next `usagesvc.Poller` fetch already
reports the **next** window's `ResetsAt` — potentially before
`checkQuotaResets` ever ticks with the old cutoff in view. The reset is
silently never observed, because nothing remembers it once the live value
moves on.

### C — codex's rate-limit model-switch picker eats the wake text, and `codexIdle` isn't bottom-anchored

Live codex TUI: hitting a rate limit shows a picker, `Switch to <model> for
lower credit usage?`, defaulting to option 1 (switch). Nothing in
`internal/adapter/codex.go`'s `PromptPatterns()` (`codex.go:216-221`)
matches it. `wake.go`'s idle-paste fallback (D) pastes text + `Enter` into
whatever the pane currently shows; against this picker, `Enter` alone
selects option 1 — switching the model to a different one (e.g.
`gpt-6-luna`) and dropping the pasted wake text entirely.

Separately, `codexIdle` (`codex.go:190`) is `(?m)^...$`, which matches an
empty-prompt line **anywhere** in the tmux capture, not only at the bottom.
A capture that still has an old empty-prompt render higher up in scrollback
(e.g. from before the rate-limit picker appeared) makes `Codex.Idle` return
`true` from stale history even though the pane is not actually idle. Two
downstream effects: `resolveAlive` (`internal/runtime/reconcile.go:~1131`)
only runs `PromptPatterns` when `!idle`, so a false-idle read means the new
picker pattern (once added) would never fire there either; and `wake.go`'s
own idle-paste gate (D) would pointlessly paste into a pane that is not
really at an empty composer.

### D — the paste fallback pastes the bare `IdleToken`, not the quota-reset notice

`wake.go:585`: `s.Tmux.PasteLine(ctx, tmuxName, IdleToken)`. `IdleToken`
(`internal/runtime/text.go:45`, `"swarm: inbox (call swarm_sync)"`) tells an
agent to go read its own inbox — it carries none of the
`QuotaResetNotice()`/`OpenRequestsReminder(n)` text already built two lines
above it and already used for the native-wake branch. A paste-only kind
(muse today; cursor has no native wake either, see below) gets a generic
"go check your inbox" instead of "your quota reset, resuming" plus the
open-requests reminder. `WakeDue`'s own paste path (`tryPaste`,
`wake.go:264`) already does this correctly — it receives the same rich
notice a native wake would get, never the bare token; `WakeOnQuotaReset`'s
paste branch is the one place still using `IdleToken`.

### E — `Agy.Wake`/`Codex.Wake` accept an empty id and lie about delivery; `WakeOnQuotaReset` throws away `Wake` errors

`Codex.Wake` and `Agy.Wake` never check `w.ProviderSessionID != ""` before
building their argv — codex's `codex queue --thread ""` fails (a real error,
at least visible if logged), but agy's `agy --conversation ""` **starts
successfully** (`StartEnv` has no way to know the conversation id is bogus)
and `Wake` returns `(true, nil)`: `WakeOnQuotaReset` marks the session woken
having delivered nothing. `wake.go:571`'s `delivered, _ := ad.Wake(...)`
discards whatever error a `Wake` call does return, so even codex's honest
failure is invisible in the daemon's own logs.

### Out of scope

- Muse and cursor: `Muse.Wake`/`Cursor.Wake` both unconditionally
  `return false, nil` — no native wake by design, paste-only. Fix D (the
  paste carries the real notice) is their fix; nothing else changes for
  them here.
- Muse's usage fetch failing since 09:43 ("host observed no subscription
  usage") — unrelated to wake delivery, not investigated further here.

## Locked decisions

1. `WakeOnQuotaReset`'s row query adds `COALESCE(ses.provider_session_id, '')`
   and forwards it as `WakeTarget.ProviderSessionID`, mirroring
   `wakeCandidates`/`WakeDue` exactly.
2. `checkQuotaResets` remembers every `ResetsAt` value it has ever observed
   per (agent kind, meter id) — not just the latest — and treats any
   remembered timestamp landing in `(now-1h, now-1m]` as a reset to fire on,
   regardless of what the live snapshot currently reports. Implemented as a
   pure function (`dueResets`) taking an explicit `seen` map plus the current
   snapshots and `now`, so it is unit-testable with no daemon, DB, or
   `usagesvc.Poller` involved; `quotaResetLoop` owns the single long-lived
   `seen` map (one goroutine, no locking needed). Entries older than
   `now-1h` are pruned each tick so the map cannot grow without bound.
   Firing `WakeOnQuotaReset` more than once for the same remembered cutoff
   is harmless and left unguarded here: `WakeOnQuotaReset`'s own
   `last_wake_at < cutoff` row filter already makes repeat calls with an
   already-passed cutoff a no-op for every session it already woke (a fresh
   `markWoken` call sets `last_wake_at` to "now", which is always `>=`
   any past cutoff).
3. `internal/adapter/codex.go` adds one `PromptPatterns()` entry:
   `Match: "Switch to \S+ for lower credit usage\?"`,
   `Require: "Keep current model"`, `Action: "Down+Enter"` (mirrors the
   existing `codexRetire` entry's reasoning: `Enter` alone accepts the
   highlighted first option, which is the switch, not the keep).
   `Codex.Idle` is anchored to the tail of the capture: raw lines are
   trimmed from the end while their ANSI-stripped content is blank, then
   only the last 3 raw lines are matched against `codexIdle` — a stale
   empty-prompt render further up in scrollback can no longer report a busy
   pane (dialog, or genuinely mid-turn) as idle.
4. `WakeOnQuotaReset`'s paste-fallback branch pastes the same `notice`
   string the native-wake branch already builds (`QuotaResetNotice()` plus,
   when there are open requests, `OpenRequestsReminder(n)`), not
   `IdleToken`.
5. `Agy.Wake` and `Codex.Wake` both return `(false, error)` immediately when
   `w.ProviderSessionID == ""`, before touching `StartEnv`/`RunEnv`.
   `WakeOnQuotaReset` keeps whatever error `ad.Wake` returns (native or the
   new empty-id guard) and logs it the same way `WakeDue` already does for
   its own native-wake branch.

## File list

- `internal/runtime/wake.go` — `WakeOnQuotaReset`: query + `WakeTarget`
  literal (A), paste-fallback notice (D), keep+log the `Wake` error (E).
- `internal/runtime/wake_test.go` — extend
  `TestWakeOnQuotaResetResolvesAgyLaunchModel` (seeded provider id + a
  codex twin); update `TestWakeOnQuotaReset`'s paste assertion (D).
- `internal/adapter/agy.go`, `internal/adapter/agy_test.go` — empty-id guard
  (E) + test.
- `internal/adapter/codex.go`, `internal/adapter/codex_test.go` — empty-id
  guard (E) + test; new `PromptPatterns` entry + `Idle` anchoring (C) +
  tests; new fixture `internal/adapter/testdata/codex/pane-dialog-rate-limit.txt`
  (synthesized from the reported dialog text, not a live capture — no probe
  captured this screen).
- `cmd/swarm/daemon.go` — `checkQuotaResets`/`quotaResetLoop`: introduce
  `dueResets` (pure) and the `seen` map (B).
- `cmd/swarm/quota_reset_test.go` (new) — unit tests for `dueResets` (B).

Nothing is deleted; no existing test's coverage is dropped (two existing
assertions change their expected *value*, not their existence: the
`IdleToken` paste assertion in `TestWakeOnQuotaReset`, and
`TestWakeOnQuotaResetResolvesAgyLaunchModel`'s scope grows, it isn't
replaced).

## Verification

```
go build ./...
go vet ./...
gofmt -l .
go test ./internal/adapter/... -run Codex -v
go test ./internal/adapter/... -run Agy -v
go test ./internal/runtime/... -run WakeOnQuotaReset -v
go test ./cmd/swarm/... -run DueResets -v
go test ./... -count=1 -timeout 40m
make e2e   # TestScenario12PreflightFailures may be pre-existing red
make skills-sync clean
```

Manual read-back: `git log` shows one commit per fix (A, E, D, C, B), each
with its failing-test-first shape still visible in the diff.

## Explicitly out of scope

- Muse's usage-fetch failure since 09:43 — noted, not fixed here.
- Any change to `Muse.Wake`/`Cursor.Wake` beyond what D already covers (the
  richer paste notice) — both stay paste-only by design.
- Generalizing the bottom-anchored idle check to other adapters'
  `IdlePrompt()` regexes — only codex's was reported and confirmed live.
