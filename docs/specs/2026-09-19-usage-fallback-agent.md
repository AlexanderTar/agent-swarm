# Usage-triggered fallback agent

## Context

Agent Swarm spawns/retries agents for a task role (orchestrator, coder,
reviewer, ui_reviewer, researcher, debugger, mechanical, advisor) using
whatever `{Agent, Model}` the caller resolves — today, on the machine we
dogfood on, that resolution happens client-side (web UI's `AgentFields`/
`spawnForm.ts`, or the orchestrator LLM through `swarm_spawn`'s `agent`/
`model` args), seeded from `Settings.roles[role]`. **Investigation finding
that corrects the ticket's premise**: `internal/runtime`'s own `Spawn`,
`StartOrchestrator` and `StartSpike` never read `cfg.Roles` themselves (the
only runtime code that reads `cfg.Roles` is `resolveAdvisor`, for the advisor
role). Whatever `{Kind, Model}` a caller passes in (or the "" default, which
falls back to the first enabled agent) is what actually gets spawned. So this
feature does not intercept "role resolution" — there is no such choke point
in Go today — it intercepts the **resolved `{Kind, Model}`** immediately
before every place the daemon turns that pair into a live tmux session:
`StartSpike`, `StartOrchestrator`, `Spawn`, `Retry`, and `DrainQueue`'s
`startQueued` (a queued agent's persisted kind/model, admitted later, is
exactly the same kind of decision, just deferred).

The daemon already tracks per-agent-kind usage in `internal/usage`
(`Poller`, `Snapshot{Meters, Error, Stale}`, `Meter{ID, Label, UsedPct,
ResetsAt}`), gated off by default (`SWARM_USAGE` must be `"live"`; in
dev/test `Poller.Sources` is empty and `Snapshots` returns nothing for a kind
that was never polled). That data currently only feeds the usage-widget
display (`GET /api/usage`, `GET /api/state`). Nothing in the spawn/retry path
consults it, so an out-of-quota agent keeps getting (re)spawned against
itself indefinitely — the actual bug this feature fixes.

Affected repos/worktrees: this worktree only
(`.claude/worktrees/agent-ae9d0fd7ee4d107aa`, branch
`worktree-agent-ae9d0fd7ee4d107aa`). `internal/usage/*.go` is being reworked
by other work in the primary checkout this session — this spec reads that
package but never modifies it. The unrelated orchestrator-crash
investigation is a separate, already-assigned task; not touched here.

## Locked decisions

1. **Interception point is the resolved `{Kind, Model}`, not "role
   resolution".** One new method, `Store.resolveUsageFallback`, is called at
   five sites (`StartSpike`, `StartOrchestrator`, `Spawn`, `Retry`,
   `startQueued`) right before the point each site already turns
   `{Kind, Model}` into a live session (`Preflight` for the first three,
   directly before `startSession` for `Retry`/`startQueued`).
2. **`Resume` is explicitly excluded.** `Resume` continues an existing
   provider session (`resume := ses.ProviderSessionID != ""`); switching
   agent kind mid-resume cannot continue a Claude conversation on Codex.
   Usage exhaustion on resume is a pre-existing, unrelated gap — out of
   scope.
3. **Exhaustion is per agent kind, not per model or per role.** A fallback
   configured to the *same* kind as the one that's exhausted is a no-op by
   construction (`fb.Agent == kind` short-circuits to "no fallback
   available"). With the shipped defaults (every role's agent is Claude, and
   the fallback default is also Claude), Claude running out of usage has no
   escape until the user points the fallback at a second installed agent.
   This is expected and stated plainly, not silently masked.
4. **No fallback chain.** One fallback, one hop. If the fallback is also
   confirmed exhausted, or none is configured/enabled, that's terminal: no
   further search, no infinite loop. See "Terminal behavior" below.
5. **Substitution is visible**: the spawned/retried agent's `agents.kind`/
   `agents.model` columns reflect the fallback (not the originally-configured
   one) once it's used, and a new `agent.fallback_used` notification fires.
   This mirrors how every other daemon decision the user didn't type
   themselves (`agent.queued`, `agent.preflight_failed`) is already surfaced
   — no new UI concept, no new event type beyond the existing
   `agent.changed`/notification machinery.
6. **Terminal ("no agent available") behavior reuses the existing
   Preflight-failure plumbing, not a new refusal path.** When the configured
   agent is exhausted and no usable fallback exists,
   `resolveUsageFallback` returns an error. `StartSpike` and `startQueued`
   already have a `preflightErr`-shaped "create the agent row anyway, mark
   `preflight_error`, raise `agent.preflight_failed`, don't start a session"
   branch — the fallback error is fed into that same variable, so no new
   notification kind is needed for this case. `StartOrchestrator` and
   `Spawn` already refuse outright with a plain Go error before creating any
   row when Preflight fails — the fallback error is returned the same way.
   `Retry` has no Preflight concept today; it returns the error directly
   (same shape as its existing `notRetryable` refusal).
7. **Settings default**: `FallbackDefault = {Agent: claude, Model: "sonnet"}`
   per the user's explicit ask. `"sonnet"` is confirmed (by reading
   `internal/catalog/parse.go`'s `aliasOrder`/`ClaudeAliasFallback`) to be a
   rolling alias — `catalog.Find` matches it against whichever real model ID
   currently carries the `"sonnet"` alias — exactly the same "latest Sonnet"
   convention `roleDefaults` already uses for `RoleCoder`/`RoleResearcher`.
   No new convention invented.
8. **A `PUT /api/settings` body that omits the new key is a validation
   error, not a silent reset-to-default**, matching how every other
   scalar/struct top-level field in `Settings` already behaves (e.g.
   omitting `max_orchestrators` zeroes it and `validate()` rejects the whole
   PUT; nothing in `Put` special-cases a missing key). `Get()` needs no
   special handling either: its existing raw-JSON-merge-against-`Defaults()`
   logic (settings.go lines 97-130) already fills in any top-level key
   absent from the `settings` table — which is how every previously-added
   field (`UsagePollSec`, `PauseDeadlineSec`, ...) already gets a correct
   default for existing installs with no code path needing to change.

## Exhaustion heuristic

`kind` is confirmed exhausted iff **all** of:

- `Store.Usage != nil` (usage tracking wired at all — false in every test and
  in any environment where `SWARM_USAGE` isn't `"live"`), and
- a `usage.Snapshot` exists for `kind` (it was polled at least once), and
- `!snapshot.Stale` (a stale reading is "we don't currently know" — never
  "exhausted"; this also covers `recordAttemptOnly` rows, which are always
  stale since `fetched_at` stays 0), and
- `snapshot.Error == ""` (a fetch error — network blip, expired token, rate
  limit on our own manual-refresh gate — is not evidence of quota
  exhaustion; string-matching provider error text was explicitly rejected as
  a primary signal per the task brief, since Claude's real exhaustion
  response now returns valid meters instead of an error), and
- `len(snapshot.Meters) > 0`, and
- its **headline meter** (`snapshot.Meters[i]` where `Meters[i].ID ==
  HeadlineID`, or `Meters[0]` when `HeadlineID` is empty or matches nothing
  — the same fallback the menubar's own `UsageSnapshot.headline` computed
  property already uses) has `UsedPct >= 100` **and** (`ResetsAt == nil` or
  `ResetsAt.After(now)`).

**Revised during implementation** (was originally spec'd as "any meter", per
an earlier reading of this section) after re-reading `internal/usage/claude.go`
in full: several meters a real source emits are *not* whole-kind caps.
Claude's own `seven_day_opus`, `seven_day_fable`, and the per-model
`seven_day_<model>` entries built from `parsed.Limits` are **per-model**
weekly caps; Cursor's `cursor_auto` vs `cursor_api` are separate billing
pools. Under this codebase's own shipped defaults — every role but coder
defaults to Claude/opus, the fallback also defaults to Claude/sonnet — an
"any meter" rule would have refused (or, with a different fallback
configured, silently rerouted) a coder spawn the moment the *Opus* weekly
cap alone was hit, even though Sonnet still had full capacity. That is a
regression this feature must never cause: it would make a working spawn
fail where none was failing before. Headline-only avoids it: `HeadlineID`
is each source's own choice of "the representative reading" (Claude sets it
to `five_hour`, a whole-account, not per-model, window), so this is
source-agnostic and needs no per-kind special-casing in `usagegate`. The
accepted cost is a false negative when a genuine whole-account cap (e.g.
Claude's `seven_day`, "all models") is hit while the headline still reads
low — that reads as "available" when it technically isn't, which is exactly
today's pre-feature stall behavior, not a new one, and is preferred over the
false-positive risk above.

Threshold is `>= 100`, not a softer "close to 100" cutoff: this session's
other work (`internal/usage/claude.go`) confirmed Claude's real
quota-exhausted response now reports a genuine `100.0` `UsedPct`, so a lower
threshold would be an unverified guess and would risk diverting tasks off an
agent that still has real, usable capacity left (e.g. at 92% used with hours
until reset). The `ResetsAt` guard exists so a snapshot that reports 100%
but whose window has already rolled over (stale-but-not-yet-refetched) isn't
treated as a live block.

`nil`/never-polled/disabled `SWARM_USAGE` always resolves to "available" —
this is fail-open by design (see task brief: "must NOT treat unknown as
exhausted"). A future `Fail`-dialog signal (none of `claude.go`/`codex.go`'s
`StartupDialogs()` currently mark a usage-limit prompt as `Fail`; `codex.go`
has one unrelated `Fail: true` dialog for a hook-trust prompt) could sharpen
this later; not built now — out of scope.

## Package/import-cycle design

`internal/usage` already imports `internal/runtime` (for `AgentKind` on
`Snapshot.Agent`), so `internal/runtime` can never import `internal/usage`
back. New leaf package **`internal/usagegate`** (imports both) holds the
heuristic and a thin adapter:

```go
// internal/usagegate/usagegate.go
package usagegate

// exhaustionPct is the UsedPct at/above which a meter counts as exhausted;
// see the spec's "Exhaustion heuristic" for why this is 100, not a softer cutoff.
const exhaustionPct = 100.0

// Exhausted is the pure heuristic, independent of any I/O, directly unit-testable.
func Exhausted(snap usage.Snapshot, now time.Time) bool

// Gate implements runtime.UsageReader against a live *usage.Poller.
type Gate struct {
    Poller *usage.Poller
    Now    func() time.Time // nil means time.Now
}

func (g *Gate) Exhausted(ctx context.Context, kind runtime.AgentKind) bool
```

`internal/runtime` (new file `internal/runtime/fallback.go`, plus one field
on `Store` in `model.go`):

```go
// UsageReader reports whether an agent kind is confirmed to be out of usage
// right now. nil disables the fallback feature entirely — matching every
// existing test and every environment where usage polling is off.
type UsageReader interface {
    Exhausted(ctx context.Context, kind AgentKind) bool
}
```
`Store.Usage UsageReader` (new field, model.go).

```go
// resolveUsageFallback is the fallback-resolution step (docs/specs/2026-09-19-usage-fallback-agent.md).
// It returns (kind, model) unchanged with a nil error when kind isn't
// confirmed exhausted, or a substituted (fallback kind, fallback model) with
// substituted=true when it is and a usable fallback exists. When kind is
// exhausted and no usable fallback exists (none configured, fallback ==
// kind, fallback not enabled, or fallback itself also exhausted), it
// returns a non-nil error the caller must treat as a Preflight-style
// refusal (see Locked Decision 6) — it must NOT use the returned kind/model
// to spawn.
func (s *Store) resolveUsageFallback(ctx context.Context, kind AgentKind, model string) (AgentKind, string, bool, error)
```

`cmd/swarm/daemon.go` wires it after constructing both `rt` and `up`:
```go
rt.Usage = &usagegate.Gate{Poller: up, Now: now}
```

## Settings model change

`internal/settings/settings.go`:

```go
type Settings struct {
    ...
    Roles           map[kinds.Role]RoleDefault `json:"roles"`
    FallbackDefault RoleDefault                `json:"fallback_default"`
    ...
}
```

`Defaults()`: `FallbackDefault: RoleDefault{Agent: kinds.Claude, Model: "sonnet"}`
(no `Effort` — "" means agent default, same as every other `roleDefaults`
entry with unset effort).

`switchDisabled` (I18): when `FallbackDefault.Agent` is the agent being
disabled, reassign it the same way a role is reassigned — to
`roleDefaults[...]`-shaped default if `first == Claude`, else to
`{Agent: first, Model: def}` from `ModelsFor(first)`. Implemented by
factoring the existing per-role reassignment body into a helper both the
`Roles` loop and this one new case call (see plan).

`validate()`: `FallbackDefault` is validated exactly like a role default
(enabled-agent check, model-exists check, effort-supported check), with a
shared helper carved out of the existing per-role loop body so the ~20 lines
aren't duplicated. Unlike `RoleAdvisor`, there is no `NoAdvisor`/"none"
carve-out — a fallback with no real agent configured would defeat the
feature, so it's always required to resolve to an enabled agent + real
model.

## Notification

New row in `internal/notifyrules/notifyrules.go`'s `Rules` map (leaf
package, safe to touch — explicitly not `internal/usage`):

```go
"agent.fallback_used": {"info", "Fallback agent used",
    "{name} switched to {agent} because {from} is out of usage.", "swarm.info"},
```

Raised (best-effort, `_ = s.Notify.Raise(...)`, same pattern as every
existing call site) with
`Args: map[string]string{"name": a.Name, "agent": <fallback kind>.Display(), "from": <original kind>.Display()}`
after the agent row is committed and, for the immediate-start paths, after
`startSession` succeeds — mirroring exactly where `agent.queued` is already
raised. No new notification kind for the "no agent available" terminal case
(Locked Decision 6 — it reuses `agent.preflight_failed`, or a plain returned
error for `StartOrchestrator`/`Spawn`/`Retry`).

## Call-site wiring (all five)

- **`StartSpike`**: replace `preflightErr := s.Preflight(...)` with:
  resolve fallback on `(in.Kind, in.Model)` first; if it errors, that error
  becomes `preflightErr` (skip calling the real `Preflight`, since the
  original kind is still installed/signed-in and a real `Preflight` call
  would return nil, masking the exhaustion refusal); else run the real
  `Preflight` against the (possibly substituted) kind/model. `in.Kind`/
  `in.Model` are reassigned before the `Agent{}` struct is built, so the
  `INSERT` naturally persists the substituted values — no extra `UPDATE`
  needed. Notify `agent.fallback_used` after the session starts, only when
  substituted and no preflight error.
- **`StartOrchestrator`**, **`Spawn`**: same substitution before their
  existing `s.Preflight(...)` call; on a fallback error, `return ..., err`
  exactly like today's `if err := s.Preflight(...); err != nil { return
  ..., err }` (no new branch shape). `Spawn` guards the post-commit notify
  with `ran` (mirrors the existing "a replay must not re-raise agent.queued"
  comment) so an idempotent replay never double-notifies.
- **`Retry`**: after loading `a` and before `startSession`, resolve fallback
  on `(a.Kind, a.Model)`. On success+substitution: run `Preflight` against
  the fallback (Retry never validated the *original* kind at retry time —
  it trusted the agent row — but a freshly-substituted kind has never been
  vetted, so it must be); if that Preflight also fails, return that error
  (don't spawn a broken substitute); else `UPDATE agents SET kind = ?, model
  = ? WHERE id = ?` in the same transaction that already sets `state =
  'active'`, update the in-memory `a`, and notify `agent.fallback_used`
  after commit. On a fallback error (no usable fallback), return it directly
  — same shape as the existing `notRetryable` refusal.
- **`startQueued`** (`internal/runtime/limits.go`): resolve fallback on
  `(a.Kind, a.Model)` before computing `preflightErr`; on substitution,
  update the local `a.Kind`/`a.Model` (both existing `UPDATE agents SET
  state = 'active' WHERE id = ?` statements become `UPDATE agents SET state
  = 'active', kind = ?, model = ? WHERE id = ?`, bound to the possibly-new
  values, harmless no-op when unchanged); move the existing
  `ad, ok := s.Adapters[a.Kind]` lookup (used later for `watchStartup`) to
  *after* the fallback resolution, since it must resolve against the
  substituted kind. Notify `agent.fallback_used` after commit, only in the
  no-preflight-error branch.

## API / type signatures (exact)

```go
// internal/usagegate/usagegate.go
package usagegate

func Exhausted(snap usage.Snapshot, now time.Time) bool

type Gate struct {
    Poller *usage.Poller
    Now    func() time.Time
}
func (g *Gate) Exhausted(ctx context.Context, kind runtime.AgentKind) bool

// internal/runtime/model.go (addition to Store)
type UsageReader interface {
    Exhausted(ctx context.Context, kind AgentKind) bool
}
// Store gains: Usage UsageReader

// internal/runtime/fallback.go
func (s *Store) resolveUsageFallback(ctx context.Context, kind AgentKind, model string) (AgentKind, string, bool, error)

// internal/settings/settings.go
type Settings struct {
    // ... existing fields ...
    FallbackDefault RoleDefault `json:"fallback_default"`
}
```

## Web UI

Investigation finding: **the web app (`web/`) has no Settings-editing screen
at all** — `grep` for `PUT /api/settings` and `putSettings` across
`web/src` returns nothing; `Settings` only appears read-side (prefilling
`AgentFields`/`spawnForm.ts` choices and in `types.ts`). The
**menubar app is the only place Settings, including role defaults, is
edited.** This inverts the task brief's assumption ("wire web, treat
menubar as optional") — done: `web/src/types.ts`'s `Settings` interface
gets the new field for type-accuracy/contract parity (`fallback_default:
RoleDefault`), since it's part of the wire shape web code already types
against; no editing control is added because there is no existing pattern
to extend and none was requested to be built from scratch. Web fixtures used
by `web/src/mock/fixtures.ts` get the field too, for shape parity with the
real daemon response.

## Menubar UI (apps/menubar) — in scope, done

`apps/menubar/Sources/SwarmBarKit/Wire.swift`:
- `SettingsRole` gains `case fallback`.
- `Settings` gains `public var fallbackDefault: RoleDefault =
  RoleDefault(agent: .claude, model: "sonnet")` (default value keeps every
  existing memberwise `Settings(...)` test/production call site compiling
  unchanged) with `CodingKeys` case `fallbackDefault = "fallback_default"`.
- `Settings.defaults` static literal explicitly sets it.
- `subscript(role:)` special-cases `.fallback` to read/write
  `fallbackDefault` directly instead of the `roles` dictionary — this is
  what lets every existing `SettingsModel` method (`defaultsRows`,
  `setAgent`, `setModel`, `setEffort`) work for the new row with **zero**
  changes to their bodies.

`apps/menubar/Sources/SwarmBarKit/SettingsModel.swift`: `defaultsOrder`
gains `.fallback` at the end.

`apps/menubar/Sources/SwarmBarKit/Copy.swift`: `defaultsRowLabel` gains
`case .fallback: return "Fallback"`.

`apps/menubar/Sources/SwarmBarUI/SettingsView.swift`: no change — its
`ForEach(model.defaultsRows)` is already generic over every row.

Fixtures: `Tests/Fixtures/settings.json`, `state.json`, `state-empty.json`
(the only three fixtures with a `"roles"` block) get a `"fallback_default"`
key. No custom `Decodable` init added for backward-compat with a
hypothetical daemon that predates this field — this is a single-user,
single-machine dogfood tool where daemon and menubar are rebuilt together;
per the task's own YAGNI instruction, that robustness is not requested and
not built.

## File list

**New:**
- `internal/usagegate/usagegate.go`, `internal/usagegate/usagegate_test.go`
- `internal/runtime/fallback.go`, `internal/runtime/fallback_test.go`
- `docs/specs/2026-09-19-usage-fallback-agent.md` (this file)
- `docs/plans/2026-09-19-usage-fallback-agent.md`

**Changed:**
- `internal/settings/settings.go` (+ `settings_test.go`)
- `internal/runtime/model.go` (`UsageReader` interface + `Store.Usage` field)
- `internal/runtime/agents.go` (`StartSpike`, `StartOrchestrator`, `Spawn`,
  `Retry`) + `agents_test.go`
- `internal/runtime/limits.go` (`startQueued`) + `limits_test.go`
- `internal/notifyrules/notifyrules.go` (one new rule)
- `cmd/swarm/daemon.go` (wire `rt.Usage`)
- `web/src/types.ts`, `web/src/mock/fixtures.ts`
- `apps/menubar/Sources/SwarmBarKit/Wire.swift`,
  `apps/menubar/Sources/SwarmBarKit/SettingsModel.swift`,
  `apps/menubar/Sources/SwarmBarKit/Copy.swift`
- `apps/menubar/Tests/Fixtures/settings.json`,
  `apps/menubar/Tests/Fixtures/state.json`,
  `apps/menubar/Tests/Fixtures/state-empty.json`

**Explicitly not touched:**
- `internal/usage/*.go` (read-only per task boundary)
- `apps/menubar/Sources/SwarmBarUI/SettingsView.swift` (already generic,
  needs no edit)
- `internal/httpapi/*` (no route or shape change needed — `GET`/`PUT
  /api/settings` already round-trip whatever `settings.Settings` marshals
  to; `s.Usage *usage.Poller` in httpapi is unrelated to `runtime.Store.Usage`)
- `internal/adapter/*` (the `StartupDialogs` "future signal" noted in the
  heuristic section is not built)
- Any orchestrator-crash investigation

## Out of scope (explicit)

- A fallback *chain* (fallback-of-fallback). One hop only.
- Per-model (rather than per-agent-kind) usage tracking.
- A persisted `fallback_from`/audit column distinct from the existing
  `agent.fallback_used` notification and the agent's own now-updated
  `kind`/`model` columns.
- Using a `StartupDialogs` `Fail` dialog as a corroborating signal.
- A custom backward-compatible JSON decoder on the menubar's `Settings` for
  a daemon that predates this field.
- Any change to `internal/usage/*.go` itself.
- The unrelated orchestrator-crash bug noted separately this session.
- **The Notion Specs-database page** this repo's CLAUDE.md asks every spec
  to get: this worktree has no Notion access from its own sandbox boundary.
  Not done — the coordinator should publish it (or ask a session that has
  Notion access to) from this file once the work is reviewed.

## Verification plan

Commands (run from this worktree):
```
go build ./...
go vet ./...
gofmt -l .
go test ./... -race
cd web && pnpm test
cd apps/menubar && swift test
```

Scenarios (all as real tests, using this package's existing fake-adapter /
fake-tmux / dbtest harness — no manual/live-daemon steps, since `SWARM_USAGE`
is off in every test environment by construction):

1. `usagegate.Exhausted` unit tests: fresh snapshot at 100% → true; stale →
   false; `Error != ""` → false; no meters → false; a meter at 99.9% → false;
   the headline meter at 100% whose `ResetsAt` is in the past → false; a
   *non-headline* (per-model) meter at 100% while the headline reads low →
   false (`TestNonHeadlineMeterAtCapDoesNotCount` — proves the corrected
   "headline only" contract); the headline meter at 100% while a
   non-headline meter reads low → true
   (`TestExhaustedWhenTheHeadlineMeterIsAtCap`); an empty/non-matching
   `HeadlineID` falls back to the first meter
   (`TestHeadlineIDMissingFallsBackToFirstMeter`).
2. `usagegate.Gate.Exhausted` seeded via a real `usage.Poller` +
   `RefreshOne` with a `stubSource`-style 100%-meter source (never touching
   unexported `storeSuccess`) → true; never-refreshed kind → false.
3. `runtime` tests, with a `fakeUsage map[AgentKind]bool` implementing
   `UsageReader`: exhausted kind + configured, enabled, non-exhausted
   fallback → agent spawns under the fallback kind/model,
   `agent.fallback_used` notified with correct Args, agent row's
   `kind`/`model` reflect the fallback.
4. Exhausted kind + fallback also exhausted → `StartOrchestrator`/`Spawn`
   return a plain error and create no agent row; `StartSpike`/`startQueued`
   create the agent row with `PreflightError` set and raise
   `agent.preflight_failed` (not a new kind), no session starts.
5. Exhausted kind + no `FallbackDefault` configured to a *different*, usable
   agent (e.g. fallback == the exhausted kind itself) → same terminal
   behavior as (4). Covered explicitly with the *shipped default* fallback
   left untouched (`TestStartSpikeDefaultFallbackSameAsExhaustedKindRefuses`)
   — this is the branch every fresh install actually hits, not a corner
   case: with the shipped defaults, Claude exhaustion has no escape until
   the user points the fallback at a second installed agent.
6. Not exhausted (nil `Store.Usage`, or a fresh low-usage snapshot) → every
   existing test in `agents_test.go`/`limits_test.go` is unaffected (`Usage`
   stays nil in `newStore`, so this is definitionally green — the falsification
   check below proves the new logic is actually exercised, not just inert).
7. `Retry` on a session whose agent kind is now exhausted: substitutes,
   re-Preflights the fallback, persists the new kind/model, notifies. Retry
   on a session whose kind is exhausted *and* the substituted fallback also
   fails its own re-Preflight (e.g. not installed) → `Retry` returns that
   error and does not spawn.
8. `Resume` is unaffected by an exhausted kind — explicit regression test
   proving Locked Decision 2 (it starts a session under the *original* kind
   even when `Store.Usage` reports it exhausted).
9. `settings` package: `Defaults()` includes the new field with the right
   value; `Put` rejects a `FallbackDefault` pointing at a disabled agent, a
   nonexistent model, or an unsupported effort (mirrors the existing
   per-role table-driven tests); `switchDisabled` reassigns
   `FallbackDefault` when its agent is disabled, same as a role.
10. Falsification check (see report): temporarily invert the `>= 100`
    comparison (or force `Exhausted` to always return `false`), confirm the
    new fallback-substitution tests go red, then restore and confirm green.

## Explicitly out of scope, restated

Everything under "Out of scope (explicit)" above, plus: no change to how
`Roles` (per-role defaults) are resolved by callers today — that resolution
staying client-side/orchestrator-side is a pre-existing architecture choice
this feature does not revisit.
