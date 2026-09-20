# tmux-detected usage-limit hit → immediate fallback eligibility

## Context

`docs/specs/2026-09-19-no-stalled-work.md` deferred this explicitly: "the ask
[is] to pattern-match a real Claude Code usage-limit screen in a captured
pane, the same way a trust-folder dialog is matched, and invoke the
already-merged usage-fallback mechanism (`internal/runtime/fallback.go`'s
`resolveUsageFallback`)... nobody has captured what a real usage-limit screen
actually looks like in a live pane." This spec is that dedicated brainstorming
pass, anchored to real captured evidence found on this machine (see
"Evidence" below), not a guess.

`docs/specs/2026-09-19-usage-fallback-agent.md` (merged to `main` the same
day) already built the substitution mechanism: `Store.resolveUsageFallback`,
called at five spawn/retry sites, substitutes a configured fallback
`{Kind, Model, Effort}` when `Store.Usage.Exhausted(ctx, kind)` reports the
current kind out of quota. That check reads the last **polled** snapshot
(`internal/usage.Poller`, default interval `UsagePollSec = 300`s) and,
critically, **only inspects the headline meter** (Claude's `five_hour`
window) — a deliberate, already-documented choice to avoid false-positives
from per-model weekly caps. That spec's own "Exhaustion heuristic" section
states the accepted cost in writing: *"a false negative when a genuine
whole-account cap (e.g. Claude's `seven_day`, 'all models') is hit while the
headline still reads low... is preferred over the false-positive risk
above."* This spec's tmux-detected signal is the fix for exactly that
accepted gap — not a new fallback system, a second detection signal feeding
the same `resolveUsageFallback`/`UsageReader` machinery.

Affected: `internal/adapter` (new detection regexes/method on the Claude
adapter), `internal/runtime/reconcile.go` (`resolveAlive`, the mid-session
per-tick pane check), `internal/runtime/model.go` (`UsageReader` interface),
`internal/usagegate` (the concrete override), `internal/notifyrules.go` (one
new rule). No DB schema change. No web/menubar change (this is a daemon-only
detection + state-transition + notification change; the existing
`agent.fallback_used` UI path is unchanged and already covers what the user
sees once a human/orchestrator calls `swarm_control retry`).

**Collision note**: two sibling agents in this session are working
`docs/specs/2026-09-19-no-stalled-work.md`'s other two deferred items
(`failure_text` persistence, `ackTimeout`) in their own worktrees
(`../agent-swarm--failure-text`, `../agent-swarm--ack-timeout`). Both touch
`internal/runtime/agents.go`/`reconcile.go`. This spec's own future
implementation will also touch `reconcile.go`'s `resolveAlive` — expect a
merge-time line-shift conflict with the `ackTimeout` item, not a design
collision (different code path: `ackTimeout` guards "no checkpoint since
`startSession`"; this guards "the pane shows a usage-limit message", checked
after that timeout would already have expired for a long-running session).
This document is research + design only; nothing here is implemented.

## Evidence

**What was found, real and captured on this machine** — grepped from
`~/.claude/projects/*/*.jsonl` (real Claude Code transcripts from this user's
own unrelated work: `endurio-app`, `endurio-chat`, and one from this very
`agent-swarm` repo). Over 30 real, independent instances across many
sessions and CLI versions (2.1.246 through 2.1.278+). Two independently
confirmed shapes:

1. **The exact on-screen text.** When an API call inside a running Claude
   Code session gets rejected with HTTP 429, the CLI renders a **synthetic
   assistant turn** (`"model":"<synthetic>"`, `"stop_reason":"stop_sequence"`,
   `"isApiErrorMessage":true`, `"apiErrorStatus":429`, `"error":"rate_limit"`)
   whose entire visible content is one of three literal strings, verbatim
   across every captured instance of each kind:
   - `You've hit your session limit · resets <time> (<tz>)` — the 5-hour
     rolling window (`"quotaLimits":{"rateLimitType":"five_hour", ...}`).
   - `You've hit your weekly limit · resets <time-or-date+time> (<tz>)` — the
     7-day, whole-account/model cap (`"rateLimitType":"seven_day"`).
   - `You've hit your org's monthly spend limit · run /usage-credits to ask
     your admin for a higher limit · your session limit resets <time>
     (<tz>)` — an org billing cap, distinct from both usage windows
     (`"overageDisabledReason":"out_of_credits"`, `"upgradePaths":
     ["upgrade_plan"]`).
   `<time>` renders as `H:MMam/pm`, `Hpm` (no minutes), or, for a weekly
   reset more than a day out, `"Sep 4 at 11pm"` — three distinct formats
   confirmed, not necessarily exhaustive.
2. **The CLI does not exit, and for the 5-hour case it self-heals.** In one
   fully-captured real sequence
   (`~/.claude/projects/-Users-alexandertar-GitHub-endurio-app/5c07fe14-fdae-4cca-81de-2a5e7621eba0.jsonl`,
   2026-08-31, v2.1.251, mid-task — not startup, the session was deep into an
   audit — timestamps below are exact from that file):
   - `14:28:47.237` — the synthetic `"You've hit your session limit ·
     resets 6:10pm (Europe/London)"` turn.
   - `14:28:52.748` — **automatically**, with no user keystroke
     (`"origin":{"kind":"auto-continuation"}`), the CLI enqueues a
     resume message ("You can continue now. Continue the task you were
     working on when the usage limit was reached...") and itself runs the
     `/low-priority` local command, whose stdout is: `"Continuing now at
     lower priority until your limit resets at 6:10pm. Your weekly limit
     still applies, and responses may pause while waiting for spare
     capacity. Run /low-priority to stop."`
   - `14:28:52.869` — the queued resume message is dequeued and delivered;
     the session keeps working, unattended.
   This is a real, already-shipped Claude Code resilience feature for the
   **5-hour session limit specifically**: it does not exit the process, does
   not need a human, and self-recovers by trading latency (low-priority
   queue) for continuity. The stdout text explicitly says **the weekly limit
   is not covered** by this recovery ("Your weekly limit still applies").
   `quotaLimits.lowPriorityMaxWaitSeconds: 1200` (20 minutes) was the
   observed cap on how long low-priority mode itself will wait for spare
   capacity before (presumably) giving up — not confirmed what happens if it
   does give up, no captured instance of that outcome exists.
3. **What was checked and found absent**: `internal/adapter/testdata/claude/`
   has no usage-limit fixture (only trust/dev-channel/idle/busy panes).
   `~/.swarm/logs/daemon.{out,err}.log` have zero matches for "session
   limit"/"rate_limit"/"429" — this daemon's own `Reconcile` loop has never
   actually observed one of these live, in any swarm-managed tmux pane, on
   this machine. Every one of the 30+ real instances above came from a
   **top-level interactive `claude` process** (exactly the kind of process
   `internal/adapter.Claude.Launch` spawns into a swarm tmux pane — not a
   `Task`-tool sub-agent; a `Task` sub-agent that hits the same 429
   terminates immediately with "Agent terminated early due to an API error",
   a different, non-self-healing shape this spec does not need since
   agent-swarm never launches Claude via the `Task` tool).

**What was NOT found, and is a real gap, stated plainly rather than
guessed**: no instance above is a raw `tmux capture-pane -e` capture — every
one is the CLI's own JSONL transcript log of the message it rendered, not a
screenshot of the terminal. The transcript's `content[0].text` is almost
certainly what gets drawn (Claude Code renders assistant message content
directly), but the exact **surrounding chrome** — leading bullet glyph,
color/SGR wrapping, indentation, whether it survives inside the last 15
lines `resolveAlive`'s `Tmux.Capture(ctx, name, 15)` already requests — is
unconfirmed. This is exactly the class of mistake the sibling
`no-stalled-work` spec's own item 2 (`claudeBusy`'s ANSI-escape miss) shows
the cost of: a regex written against assumed rendering can silently fail to
match the real pane. **A live capture (`tmux capture-pane -e -p -t <pane>`
against a real Claude Code session actually mid-limit) is a precondition for
implementation**, not optional polish — see "Explicitly out of scope" and
the companion plan's first task.

Public/Anthropic documentation was not searched — the real, dated, versioned
evidence above (30+ instances, exact strings, exact JSON shape) is stronger
than anything a doc search would add, and the one gap that remains (terminal
rendering) is not something Anthropic's docs would answer either.

## Answers to the open design questions

1. **StartupDialogs-shaped one-time screen, or mid-session?** Mid-session,
   confirmed (evidence #2: the captured hit happened deep into a running
   audit task, not at spawn). It is emitted by the CLI whenever an API call
   returns 429, which can happen on any turn, so it must be checked
   continuously. `internal/runtime/reconcile.go`'s `resolveAlive` — which
   already runs every reconcile tick and already calls
   `s.Tmux.Capture(ctx, r.TmuxName, 15)` right before `ad.Idle(capture)` — is
   the right site, not `watchStartup` (bounded to the `Spawning` state and a
   10-minute ceiling; a limit hit hours into a task would never be seen
   there).
2. **Does the process exit, or sit waiting?** Sits waiting — confirmed, not
   guessed (evidence #2). Worse for detection purposes: for the 5-hour case,
   the CLI's own auto-low-priority run returns the pane to a normal idle
   prompt indistinguishable, to `internal/adapter`'s existing `Idle()`
   regex, from a session that finished its turn cleanly. Tracing
   `resolveAlive`'s existing logic confirms the concrete stall this causes
   today, with **zero code changes needed to reproduce it**: `ad.Idle(capture)`
   → true (idle prompt is back); `owesNothing` → true for a normal
   kickoff-only agent (no pending messages/requests); `waiting = idle &&
   owesNothing` → true; and `resolveAlive` explicitly returns before the
   staleness check with the comment `// M6: a waiting session is never
   stale`. The session shows "Waiting" in the menubar, forever, and nothing
   ever notifies anyone — the exact silent-stall class of bug the sibling
   `no-stalled-work` spec's item 3 (`ackTimeout`) fixes for the
   "no-checkpoint-yet" case, but `ackTimeout` only fires before the first
   checkpoint of an attempt; a session hours into real, checkpointed work is
   already past that guard and would never trip it.
3. **Does `Retry`/`resolveUsageFallback` actually help, precisely?** No, not
   as-is, for two independent reasons confirmed by reading the code (not
   assumed):
   - `Retry`'s own state guard (`retryableStates = [Completed, Failed,
     Crashed, Interrupted]`, `internal/runtime/agents.go`) refuses a session
     that is still `Running` — which a rate-limited-but-not-exited session
     is. `swarm_control retry` on it today returns `notRetryable` ("This
     agent isn't in a state that can be retried."), full stop. The tmux
     signal must force the session into a terminal state before `Retry` can
     do anything at all.
   - Even once retryable, `resolveUsageFallback` → `Store.Usage.Exhausted`
     reads the last **polled** snapshot. For the `five_hour` case this is
     merely stale (up to `UsagePollSec` = 300s old) — a real but bounded
     race. For the `seven_day`/weekly case it is **structurally never
     detected at all** by the existing heuristic, regardless of poll
     freshness: `usagegate.Exhausted` checks only the **headline** meter
     (`HeadlineID`, which Claude sets to `five_hour`), by the original
     spec's own explicit, accepted design choice. A weekly cap can be fully
     exhausted while the headline (5-hour) meter reads 0% — `Exhausted`
     would return `false` even against a perfectly fresh poll. The
     tmux-detected signal therefore cannot just "poll sooner" (e.g. calling
     `Poller.RefreshOne` synchronously) — it must **override** the
     exhaustion answer directly for the affected kind, independent of what
     any snapshot says.

## Locked decisions

1. **React only to the `weekly` and `org spend` variants, not the plain
   `session` (`five_hour`) variant.** The 5-hour case already self-heals
   (Evidence #2) without agent-swarm's help, typically within
   `lowPriorityMaxWaitSeconds` (observed: 1200s/20min). Reacting to it
   immediately would race the CLI's own recovery: killing the pane the
   moment the message appears would discard a session that was, per
   directly observed behavior, about to resume on its own — the specific
   failure mode the advisor review of this spec flagged ("kill+retry
   destroys in-progress state to save a wait"). The weekly and org-spend
   variants have no such self-heal (the CLI's own stdout says so:
   "Your weekly limit still applies") and can block a session for days, not
   minutes — that is the actual, unrecoverable-without-a-fallback case this
   feature exists for, and it is exactly the gap the usage-fallback-agent
   spec already named and accepted. **Assumption the human should confirm**:
   no captured evidence exists of what happens if 5-hour low-priority mode
   itself fails to recover within its 1200s ceiling (account has no spare
   capacity at all, or `lowPriorityOffer` isn't offered for this plan) — if
   that turns out to be a real, observed failure mode, it would need the
   same reaction as weekly, gated on "still showing the session-limit text
   after ~20 minutes of no capture change," not built here for lack of
   evidence.
2. **Detection site: `internal/runtime/reconcile.go`'s `resolveAlive`**,
   immediately after its existing `capture, err := s.Tmux.Capture(ctx,
   r.TmuxName, 15)` call (already runs on every reconcile tick for every
   live session) and before the existing `idle = ad.Idle(capture)` line —
   not `watchStartup` (see design question 1) and not a new poll loop.
3. **Adapter surface: a new, small, optional interface, not a change to the
   big `Adapter` interface.** Every existing adapter kind (Claude, Codex,
   Cursor, agy) would otherwise need a method for a screen only Claude is
   confirmed to show:
   ```go
   // internal/adapter/adapter.go — new, alongside Dialog
   // UsageLimitKind classifies a detected usage-limit message so the caller
   // can react differently per kind (see spec Locked Decision 1).
   type UsageLimitKind string

   const (
       UsageLimitNone   UsageLimitKind = ""
       UsageLimitWeekly UsageLimitKind = "weekly" // no self-heal; react
       UsageLimitOrgSpend UsageLimitKind = "org_spend" // no self-heal; react
       UsageLimitSession UsageLimitKind = "session" // self-heals; do not react (Locked Decision 1)
   )

   // UsageLimitDetector is implemented by an Adapter that can recognize its
   // own CLI's usage-limit message in a captured pane. Not part of Adapter
   // itself: only Claude has real captured evidence (see spec "Evidence");
   // an adapter that doesn't implement it is never checked.
   type UsageLimitDetector interface {
       UsageLimitHit(capture string) UsageLimitKind
   }
   ```
   `internal/adapter/claude.go` implements it:
   ```go
   var (
       claudeSessionLimit  = regexp.MustCompile(`You've hit your session limit`)
       claudeWeeklyLimit   = regexp.MustCompile(`You've hit your weekly limit`)
       claudeOrgSpendLimit = regexp.MustCompile(`You've hit your org's monthly spend limit`)
   )

   func (c *Claude) UsageLimitHit(capture string) UsageLimitKind {
       plain := StripANSI(capture) // same helper claudeBusy/claudeIdle already use
       switch {
       case claudeWeeklyLimit.MatchString(plain):
           return UsageLimitWeekly
       case claudeOrgSpendLimit.MatchString(plain):
           return UsageLimitOrgSpend
       case claudeSessionLimit.MatchString(plain):
           return UsageLimitSession
       default:
           return UsageLimitNone
       }
   }
   ```
   Order matters: the org-spend message's stdout also contains the
   substring "session limit resets" (evidence #1's third variant), so
   `claudeSessionLimit` is checked last, and only the more specific two are
   matched first — precisely the ambiguity the advisor review flagged.
   **Assumption**: `StripANSI` (already exported, used by `claudeBusy`'s own
   ANSI fix earlier the same day) is sufficient; unconfirmed against a real
   capture (see "Evidence" gap above).
4. **Reaction: mark the kind exhausted immediately, then treat exactly like
   an existing crash** — reuse `resolveDead`'s already-correct "no terminal
   checkpoint → crashed, notify, relay to parent" branch, rather than invent
   a new `SessionState` or a new relay code path:
   - Call a new `Store.Usage`-reachable override (`MarkExhausted`, Locked
     Decision 5) synchronously, so any `resolveUsageFallback` call from here
     on sees this kind as exhausted regardless of the poll.
   - Raise a new `agent.usage_limit` notification (distinct from
     `agent.crashed`, so the user/orchestrator sees the real reason, not a
     misleading "crashed") with the matched `UsageLimitKind` in `Args`.
   - `s.Tmux.Kill(ctx, r.TmuxName)`. The *next* reconcile tick sees
     `p.Dead == true`, and `resolveDead`'s existing, unmodified logic runs:
     no terminal checkpoint for this attempt (a genuinely mid-task session,
     by construction) → `crashed` branch → sets `state = 'crashed'`, raises
     `agent.crashed` (in addition to this spec's own `agent.usage_limit`,
     both fire — accepted duplication, cheaper than adding a new branch to
     `resolveDead`'s already-carefully-commented state table), relays
     `{event: "crashed", ...}` to the parent if one exists. `Crashed` is in
     `retryableStates`, so `swarm_control retry` now works.
   - **Explicitly not auto-called by the daemon.** `Retry` stays a
     human/orchestrator-invoked action (`swarm_control retry`), same as
     every other retry today; this mirrors the sibling `no-stalled-work`
     spec's own Locked Decision for its item 3 ("the daemon pushes an
     event; it does not self-schedule/self-act") and the
     `swarm-orchestrator` skill's existing guidance ("Fixes after review:
     `swarm_control retry`..."). No new auto-retry policy question (chain
     depth, backoff, loop-prevention) needs answering here — `Retry`'s
     existing one-hop-only fallback rule already covers it once invoked.
     Considered and rejected: the daemon calling `Retry` itself the moment
     it kills the pane — rejected because it would be the *first* place in
     this codebase where the daemon initiates a state-changing
     `swarm_control` action on its own, a bigger, separately-decidable
     design question this spec does not need to answer to close the actual
     gap (Retry not working at all today).
5. **`UsageReader` gains one new method; the override lives in
   `usagegate.Gate`, not `runtime.Store`.**
   ```go
   // internal/runtime/model.go
   type UsageReader interface {
       Exhausted(ctx context.Context, kind AgentKind) bool
       MarkExhausted(kind AgentKind) // NEW: force Exhausted(kind) true from now on
   }
   ```
   ```go
   // internal/usagegate/usagegate.go
   type Gate struct {
       Poller *usage.Poller
       Now    func() time.Time
       overrideMu sync.Mutex
       override   map[runtime.AgentKind]bool // NEW
   }

   func (g *Gate) MarkExhausted(kind runtime.AgentKind) {
       g.overrideMu.Lock()
       defer g.overrideMu.Unlock()
       if g.override == nil {
           g.override = map[runtime.AgentKind]bool{}
       }
       g.override[kind] = true
   }

   func (g *Gate) Exhausted(ctx context.Context, kind runtime.AgentKind) bool {
       g.overrideMu.Lock()
       forced := g.override[kind]
       g.overrideMu.Unlock()
       if forced {
           return true
       }
       // ... existing snapshot-based logic, unchanged
   }
   ```
   **Locked, corrected after review: the override needs a bounded TTL, not
   an indefinite one.** An earlier draft of this decision claimed an
   indefinite (restart-only) override was safe because "a fresh poll would
   still allow a non-fallback spawn later" — **that claim is false and has
   been removed.** `resolveUsageFallback`'s guard is `if s.Usage == nil ||
   !s.Usage.Exhausted(ctx, kind)` (`internal/runtime/fallback.go` line 37),
   consulted at all five substitution sites with no bypass. Once
   `MarkExhausted(Claude)` is called, `Exhausted(ctx, Claude)` returns `true`
   unconditionally — there is no code path that ignores the override. With
   an indefinite override and the shipped defaults (`FallbackDefault =
   Claude/sonnet`), **one tmux-detected weekly hit makes every Claude spawn
   in the whole daemon fail Preflight (or fall back, if a real non-Claude
   fallback is configured) until the daemon process restarts** — including
   hours after the real weekly window has actually reset. That is a
   swarm-wide outage of the default agent, not "the fallback gets used a
   little longer than necessary." Traced and confirmed against the code,
   not assumed.

   **Fix: `MarkExhausted` takes a fixed TTL, not a permanent flag.**
   ```go
   const forcedExhaustionTTL = time.Hour // assumption, see below — not derived
                                          // from a confirmed reset-time format

   func (g *Gate) MarkExhausted(kind runtime.AgentKind) {
       g.overrideMu.Lock()
       defer g.overrideMu.Unlock()
       if g.override == nil {
           g.override = map[runtime.AgentKind]time.Time{}
       }
       g.override[kind] = g.now().Add(forcedExhaustionTTL)
   }

   func (g *Gate) Exhausted(ctx context.Context, kind runtime.AgentKind) bool {
       g.overrideMu.Lock()
       until, forced := g.override[kind]
       g.overrideMu.Unlock()
       if forced && g.now().Before(until) {
           return true
       }
       // ... existing snapshot-based logic, unchanged
   }
   ```
   **`forcedExhaustionTTL = 1 hour` is a stated assumption the human should
   confirm, not evidence-derived** — no captured instance confirms every
   reset-time phrasing Claude Code renders (parsing "resets 6:10pm"/"resets
   Sep 4 at 11pm" into an exact expiry was considered and rejected for the
   same reason as before: unconfirmed format list, unconfirmed timezone
   abbreviations). A fixed TTL trades a different, smaller, self-correcting
   risk for the outage above: if the TTL expires before the real weekly
   window resets, `resolveAlive` simply re-detects the same still-showing
   pane message on its very next reconcile tick (the session was killed and
   is presumably being retried by then, or — if not yet retried — the
   original pane is gone and a *fresh* spawn against Claude would fail
   Preflight/hit the wall again, get re-detected, and re-mark) — one bounded
   extra failed-or-wasted attempt per TTL window, visible via a fresh
   `agent.usage_limit` notification each time, never a silent wrong answer.
   That is a strictly better failure mode than "unavailable until someone
   remembers to restart the daemon," which is why the fixed-TTL version is
   locked over the indefinite one, not the reverse. `fakeUsage` (the
   `map[AgentKind]bool` test double in
   `internal/runtime/fallback_test.go`) gains a real (not no-op)
   `MarkExhausted` that sets the map entry — existing fallback tests are
   unaffected since none of them call it, and it makes the double honest
   for the new reconcile tests that do.

## Model / API types (exact)

```go
// internal/adapter/adapter.go
type UsageLimitKind string

const (
    UsageLimitNone     UsageLimitKind = ""
    UsageLimitSession  UsageLimitKind = "session"
    UsageLimitWeekly   UsageLimitKind = "weekly"
    UsageLimitOrgSpend UsageLimitKind = "org_spend"
)

type UsageLimitDetector interface {
    UsageLimitHit(capture string) UsageLimitKind
}
```

```go
// internal/adapter/claude.go
func (c *Claude) UsageLimitHit(capture string) UsageLimitKind
```

```go
// internal/runtime/model.go — UsageReader gains one method
type UsageReader interface {
    Exhausted(ctx context.Context, kind AgentKind) bool
    MarkExhausted(kind AgentKind)
}
```

```go
// internal/usagegate/usagegate.go
func (g *Gate) MarkExhausted(kind runtime.AgentKind)
// Gate.Exhausted's existing signature is unchanged; only its body gains the
// override check.
```

```go
// internal/runtime/reconcile.go — resolveAlive. capture only exists inside
// the existing "if ok { ... }" block (ok from "ad, ok := s.Adapters[r.Kind]"),
// so this nests inside it, right after the existing "idle = ad.Idle(capture)"
// line, not as a sibling block:
if ok {
    capture, err := s.Tmux.Capture(ctx, r.TmuxName, 15) // existing line
    if err != nil {
        return err
    }
    idle = ad.Idle(capture) // existing line
    if lim, ok := ad.(adapter.UsageLimitDetector); ok { // NEW
        if k := lim.UsageLimitHit(capture); k == adapter.UsageLimitWeekly || k == adapter.UsageLimitOrgSpend {
            return s.handleUsageLimitHit(ctx, r, k)
        }
    }
}
```

```go
// internal/runtime/reconcile.go — new function
// handleUsageLimitHit reacts to a tmux-detected usage-limit message that has
// no self-heal path (spec docs/specs/2026-09-19-usage-limit-tmux-detection.md,
// Locked Decision 1: the plain "session" (5-hour) variant is never passed
// here — it self-heals via the CLI's own low-priority mode). It marks kind
// exhausted immediately (so the next resolveUsageFallback call — whenever a
// human or orchestrator runs swarm_control retry — substitutes correctly
// instead of waiting on a poll that, for a weekly/org cap, would never
// report exhaustion at all), notifies, and kills the pane so the next
// reconcile tick's resolveDead call makes the session retryable.
func (s *Store) handleUsageLimitHit(ctx context.Context, r liveRow, k adapter.UsageLimitKind) error {
    if s.Usage != nil {
        s.Usage.MarkExhausted(r.Kind)
    }
    if err := s.notify(ctx, nil, NotifyInput{Kind: "agent.usage_limit", AgentName: r.AgentName, ItemKey: r.ItemKey,
        Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey, "kind": string(k)}}); err != nil {
        return err
    }
    return s.Tmux.Kill(ctx, r.TmuxName)
}
```

```go
// internal/notifyrules/notifyrules.go — one new row
"agent.usage_limit": {"attention", "Usage limit hit",
    "{name} hit its {kind} usage limit on {KEY} and was stopped. Retry to use the fallback agent.", "swarm.agent"},
```

## File list

**New:**
- `internal/adapter/testdata/claude/pane-usage-limit-weekly.txt`,
  `pane-usage-limit-org-spend.txt` — real `tmux capture-pane -e` captures
  (see "Explicitly out of scope": capturing these is a precondition, not
  part of this spec). A `pane-usage-limit-session.txt` fixture is also
  useful even though the session variant is never *reacted* to, to prove
  `UsageLimitHit` correctly returns `UsageLimitSession` (not `None`, not
  `Weekly`) and that `resolveAlive` correctly ignores it.
- `docs/specs/2026-09-19-usage-limit-tmux-detection.md` (this file)
- `docs/plans/2026-09-19-usage-limit-tmux-detection.md`

**Changed:**
- `internal/adapter/adapter.go` — `UsageLimitKind`, `UsageLimitDetector`
- `internal/adapter/claude.go` — the three regexes, `Claude.UsageLimitHit`
- `internal/adapter/claude_test.go` — new test(s) against the new fixtures
- `internal/runtime/model.go` — `UsageReader.MarkExhausted`
- `internal/runtime/reconcile.go` — `resolveAlive`'s new check,
  `handleUsageLimitHit`
- `internal/runtime/reconcile_test.go` — new test(s) (see Verification)
- `internal/runtime/fallback_test.go` — `fakeUsage.MarkExhausted` (real
  setter, not a no-op — see Locked Decision 5's correction)
- `internal/usagegate/usagegate.go` — `Gate.override`, `Gate.MarkExhausted`,
  `Gate.Exhausted`'s override check
- `internal/usagegate/usagegate_test.go` — override tests
- `internal/notifyrules/notifyrules.go` — `agent.usage_limit` row

**Explicitly not touched:**
- `internal/usage/*.go` (read-only, per the original fallback spec's own
  boundary — this feature only *calls* the already-public `UsageReader`
  surface, never modifies the poller/sources)
- `internal/runtime/fallback.go`'s `resolveUsageFallback` itself — its
  signature and logic are correct as merged; this feature only changes what
  feeds `Store.Usage.Exhausted`'s answer, not how the answer is consumed
- `web/*`, `apps/menubar/*` — `agent.usage_limit` rides the existing generic
  notification stream every consumer already renders (same as
  `agent.crashed`/`agent.stale` today); no new UI concept
- `internal/runtime/agents.go`'s `Retry`, `StartSpike`, `StartOrchestrator`,
  `Spawn`, `startQueued` — unchanged; they already correctly consult
  `resolveUsageFallback`, which now (via the override) gets a correct answer

## Verification plan

Commands (once implemented):
```
go build ./...
go vet ./...
gofmt -l .
go test -race ./...
```

Scenarios:
1. `internal/adapter`: `Claude.UsageLimitHit` against each of the three new
   fixtures returns the right `UsageLimitKind`; against every *existing*
   fixture (`pane-idle.txt`, `pane-busy.txt`, `pane-dialog-trust.txt`, etc.)
   returns `UsageLimitNone` (no false positives on unrelated panes); the
   org-spend fixture does not get misclassified as `UsageLimitSession` (the
   ordering fix in Locked Decision 3).
2. `internal/usagegate`: `Gate.Exhausted` returns `true` for a kind after
   `MarkExhausted(kind)`, independent of any snapshot state (including no
   snapshot at all, and a snapshot whose headline meter reads 0%); a
   different, non-marked kind is unaffected; `MarkExhausted` is safe to call
   concurrently with `Exhausted` (existing `overrideMu` pattern, matching
   `Poller`'s own `fetchMu`/`backoffMu` style).
3. `internal/runtime` (`reconcile_test.go`), using the existing fake-tmux/
   fake-adapter harness: a live session whose fake adapter's `UsageLimitHit`
   returns `UsageLimitWeekly` → `resolveAlive` calls `MarkExhausted`, raises
   `agent.usage_limit`, and kills the pane (assert via the fake tmux's kill
   log) instead of running the normal idle/staleness logic; same for
   `UsageLimitOrgSpend`; a session whose adapter returns `UsageLimitSession`
   is left completely alone (falls through to the existing idle/waiting
   logic unchanged — proves Locked Decision 1's self-heal carve-out); an
   adapter that doesn't implement `UsageLimitDetector` at all (a fake
   registered without the method) is skipped with no panic (the `ok`
   type-assertion guard).
4. End-to-end (`runtime` + `usagegate` together, mirroring the original
   fallback spec's own scenario 3): a fake session detected as
   `UsageLimitWeekly` → pane killed → next reconcile tick's `resolveDead`
   marks it `crashed` → `swarm_control retry` (test calls `Store.Retry`
   directly) now succeeds (no longer `notRetryable`) and substitutes the
   configured fallback kind/model, because `MarkExhausted` already ran —
   this is the falsification check: temporarily skip the `MarkExhausted`
   call and confirm this same test's `Retry` call spawns under the
   *original*, still-exhausted-per-real-account kind instead (proving the
   override, not incidental poll timing, is what makes the test pass).
5. Regression: every existing `reconcile_test.go`/`fallback_test.go` test is
   unaffected (`UsageLimitDetector` is an optional interface nothing existing
   implements yet outside the new Claude case; `fakeUsage.MarkExhausted`
   is a no-op that doesn't change any existing assertion).

## Explicitly out of scope

- **Capturing the real `tmux capture-pane -e` fixtures is a precondition,
  not part of this spec.** This document establishes exact message text
  from real transcripts; it does not establish exact terminal rendering
  (glyph/color/wrapping/line-position inside the 15-line capture window).
  The companion plan's first task is a blocking, manual "go capture a real
  one" step — implementation should not proceed past it on assumed
  rendering, mirroring the sibling spec's own item 2 lesson.
- **Reacting to the plain 5-hour `session` limit variant.** Deliberately
  not built (Locked Decision 1) — it self-heals. Revisit only if a real,
  captured instance shows the CLI's own low-priority recovery failing to
  return the session to real progress within some bounded window; no such
  instance exists today.
- **Expiring `MarkExhausted`'s override at the message's own stated reset
  time**, instead of a fixed `forcedExhaustionTTL`. Rejected for lack of a
  confirmed-exhaustive format list and timezone table (Locked Decision 5).
  A future pass could revisit this with more captured examples covering
  every reset-time phrasing Claude Code actually renders, and tighten the
  1-hour default into something derived from the real reset time instead of
  a flat guess.
- **The daemon auto-calling `Retry` itself.** Deliberately left to a human
  or orchestrator (Locked Decision 4), matching the sibling
  `no-stalled-work` spec's own design philosophy for its `ackTimeout` item.
- **Any change to `internal/usage/*.go`, or to `resolveUsageFallback`'s own
  logic.** Both are correct as merged; this feature only supplies a second,
  faster input to the same `UsageReader.Exhausted` answer they already
  consult.
- **Non-Claude adapters (Codex, Cursor, agy).** No captured evidence exists
  for any of their usage-limit screens (if they even have an equivalent
  concept exposed the same way); `UsageLimitDetector` is optional precisely
  so this ships for Claude alone without guessing at the others.
- **A `Fail: true`-style `Dialog`/`StartupDialogs` entry.** Considered and
  rejected: that mechanism only runs while `SessionState == Spawning`,
  bounded by `startupCeiling` (10 minutes); a usage-limit hit hours into a
  real task would never be seen there (see design question 1).
- **Reading `internal/adapter.HookInput.TranscriptPath` / tailing the
  session's own JSONL transcript for a structured `quotaLimits` signal**
  (considered as an alternative to pane-regex matching: it would give an
  exact `resetsAt` epoch instead of unparsed text). Rejected for this pass:
  the pane text alone already disambiguates `session`/`weekly`/`org_spend`
  without it, the transcript path is only available via `ParseHook`'s
  per-hook-event `HookInput` (not something `reconcile.go`'s per-tick loop
  already has plumbed through), and it would make this feature depend on
  parsing an internal, versioned JSONL format on top of an already-real
  regex-on-pane-text approach the rest of `internal/adapter` already uses
  for every other detection (trust dialog, dev-channels dialog, idle,
  busy). Worth reconsidering later if the pane-regex approach proves too
  fragile against real rendering once captured.
