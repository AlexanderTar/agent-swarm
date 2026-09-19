# Plan: usage-triggered fallback agent

Companion to `docs/specs/2026-09-19-usage-fallback-agent.md`. Each step:
failing test → run, watch it fail → minimal implementation → run, watch it
pass → commit. Commits are small and scoped to one step.

**Revised after implementation** (kept for history; do not re-derive from
this section): Steps 1 and 6 below describe an "any meter" heuristic and a
4-return `resolveUsageFallback(ctx, kind, model)` signature. Both were
corrected during review — see `docs/specs/2026-09-19-usage-fallback-agent.md`'s
"Exhaustion heuristic" and "Locked decisions" §5 for why: "any meter" was a
regression risk (Claude's per-model weekly meters), and the 4-return
signature dropped effort on substitution, which broke the exact scenario
the feature exists for whenever a role used a non-default effort. The real
heuristic checks only the headline meter, and `resolveUsageFallback` is
`(ctx, kind, model, effort string) (AgentKind, string, string, bool, error)`.

## Step 1 — `internal/usagegate`: pure heuristic

Files: `internal/usagegate/usagegate.go` (new), `internal/usagegate/usagegate_test.go` (new).

Test first (`usagegate_test.go`):
```go
package usagegate

import (
    "testing"
    "time"

    "github.com/AlexanderTar/agent-swarm/internal/usage"
)

func TestExhaustedAtCap(t *testing.T) {
    now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
    later := now.Add(time.Hour)
    snap := usage.Snapshot{Meters: []usage.Meter{{ID: "five_hour", UsedPct: 100, ResetsAt: &later}}}
    if !Exhausted(snap, now) {
        t.Fatal("want exhausted")
    }
}

func TestNotExhaustedBelowCap(t *testing.T) { /* UsedPct: 99.9 -> false */ }
func TestNotExhaustedWhenStale(t *testing.T)  { /* Stale: true, UsedPct: 100 -> false */ }
func TestNotExhaustedOnFetchError(t *testing.T) { /* Error: "boom", UsedPct: 100 -> false */ }
func TestNotExhaustedNoMeters(t *testing.T)   { /* Meters: nil -> false */ }
func TestNotExhaustedResetAlreadyPassed(t *testing.T) { /* UsedPct:100, ResetsAt: past -> false */ }
func TestExhaustedOnNonHeadlineMeter(t *testing.T) {
    // HeadlineID points at a low meter; a second, non-headline meter is at 100%.
    // Must still report exhausted -- proves "any meter", not "headline only".
}
```
Run `go test ./internal/usagegate/...` — fails (package doesn't exist / `Exhausted` undefined).

Implement `usagegate.go`:
```go
package usagegate

import (
    "context"
    "time"

    "github.com/AlexanderTar/agent-swarm/internal/runtime"
    "github.com/AlexanderTar/agent-swarm/internal/usage"
)

const exhaustionPct = 100.0

func Exhausted(snap usage.Snapshot, now time.Time) bool {
    if snap.Stale || snap.Error != "" || len(snap.Meters) == 0 {
        return false
    }
    for _, m := range snap.Meters {
        if m.UsedPct < exhaustionPct {
            continue
        }
        if m.ResetsAt != nil && !m.ResetsAt.After(now) {
            continue
        }
        return true
    }
    return false
}

type Gate struct {
    Poller *usage.Poller
    Now    func() time.Time
}

func (g *Gate) now() time.Time {
    if g.Now != nil {
        return g.Now()
    }
    return time.Now()
}

func (g *Gate) Exhausted(ctx context.Context, kind runtime.AgentKind) bool {
    snaps, err := g.Poller.Snapshots(ctx)
    if err != nil {
        return false
    }
    for _, s := range snaps {
        if s.Agent == kind {
            return Exhausted(s, g.now())
        }
    }
    return false
}
```
Run `go test ./internal/usagegate/...` — green. Commit: "add internal/usagegate: usage-exhaustion heuristic".

## Step 2 — `Gate` against a real `Poller` (end-to-end, no unexported access)

Add to `usagegate_test.go`:
```go
func TestGateExhaustedAfterRefresh(t *testing.T) {
    // real usage.Poller + settings.Store (dbtest) + a Source whose Fetch
    // returns a single meter at UsedPct: 100, no ResetsAt; RefreshOne(ctx, kind);
    // Gate{Poller: p}.Exhausted(ctx, kind) -> true.
}
func TestGateFalseWhenNeverPolled(t *testing.T) {
    // Gate{Poller: p}.Exhausted(ctx, someOtherKind) -> false, no source configured at all.
}
```
Mirror `internal/usage/helpers_test.go`'s `settingsWith`/`stubSource` shape
(own local copies in this package — `usagegate` doesn't import `usage`'s
`_test.go` helpers, it writes its own minimal versions of the same pattern).
Run, watch fail (package wiring), implement is already done in Step 1 — this
step should already pass once the test compiles; if not, fix `Gate`. Commit:
"usagegate: cover Gate against a real Poller.RefreshOne".

## Step 3 — `settings.FallbackDefault` field, defaults, validation

Files: `internal/settings/settings.go`, `internal/settings/settings_test.go`.

Test first, in `settings_test.go`, extend `TestDefaults` to assert
`d.FallbackDefault == RoleDefault{kinds.Claude, "sonnet", ""}`, and add:
```go
func TestPutRejectsFallbackOnDisabledAgent(t *testing.T) {
    s := newStore(t, kinds.Claude)
    cfg, _ := s.Get(ctx)
    cfg.FallbackDefault = RoleDefault{Agent: kinds.Codex, Model: "gpt-6-astra"}
    _, err := s.Put(ctx, cfg)
    verr(t, err, "Codex isn't enabled. Choose an enabled agent.")
}
func TestPutRejectsFallbackUnknownModel(t *testing.T) { /* mirrors an existing per-role case */ }
func TestSwitchDisabledReassignsFallback(t *testing.T) {
    // enable claude+codex, FallbackDefault -> codex, then disable codex via
    // switchDisabled's path (Put with codex removed from EnabledAgents) ->
    // FallbackDefault reassigned to claude's roleDefaults-shaped default.
}
```
Run `go test ./internal/settings/...` — new tests fail (field doesn't exist
yet, or isn't validated/reassigned).

Implement:
- Add `FallbackDefault RoleDefault \`json:"fallback_default"\`` to `Settings`.
- `Defaults()`: add `FallbackDefault: RoleDefault{Agent: kinds.Claude, Model: "sonnet"}`.
- Extract `validateDefault(ctx, prev, next RoleDefault, enabled []kinds.AgentKind) error`
  from the existing per-role loop body (lines ~230-260), used both by the
  `Roles` loop (which keeps its own `RoleAdvisor`/`NoAdvisor` and
  `AdvisorCapable` carve-outs wrapped around the call) and by one new
  `validateDefault(ctx, prev.FallbackDefault, next.FallbackDefault, next.EnabledAgents)`
  call in `validate()`.
- `switchDisabled`: after the existing `for role, rd := range next.Roles`
  loop, add the same reassignment logic for `next.FallbackDefault` when its
  `Agent` is the one being disabled (factor the "reassign one RoleDefault"
  body into a small helper `reassignDefault(ctx, first AgentKind) (RoleDefault, error)`
  reused by both the loop and this one case).
Run `go test ./internal/settings/...` — green, and confirm the *existing*
`TestDefaults`/role-validation tests still pass unchanged (regression check
on the refactor). Commit: "settings: add FallbackDefault, validated and
reassigned like a role default".

## Step 4 — `internal/notifyrules`: `agent.fallback_used`

Files: `internal/notifyrules/notifyrules.go`. No dedicated test file exists
for this leaf package's data (it's exercised via `runtime`'s `fakeNotifier`
in Step 6) — add the row now so Step 6 can use it immediately:
```go
"agent.fallback_used": {"info", "Fallback agent used",
    "{name} switched to {agent} because {from} is out of usage.", "swarm.info"},
```
`go build ./...`. Commit: "notifyrules: add agent.fallback_used".

## Step 5 — `runtime.UsageReader` + `Store.Usage` field

Files: `internal/runtime/model.go`.
```go
type UsageReader interface {
    Exhausted(ctx context.Context, kind AgentKind) bool
}
```
Add `Usage UsageReader` to the `Store` struct (doc comment: nil disables the
feature). No behavior change yet — `go build ./...`, `go test ./internal/runtime/...`
stay green (nothing reads the new field yet). Commit: "runtime: add
UsageReader interface and Store.Usage field".

## Step 6 — `resolveUsageFallback` + wire into `StartSpike`

Files: `internal/runtime/fallback.go` (new), `internal/runtime/agents.go`,
`internal/runtime/fallback_test.go` (new) or additions to `agents_test.go`
(prefer a new `fallback_test.go` so the fallback-specific fixtures — a
second enabled+cataloged fake agent kind, a `fakeUsage` — don't bloat
`agents_test.go`'s existing `newStore`).

Test first, in `fallback_test.go`:
```go
package runtime

// fakeUsage implements UsageReader for tests.
type fakeUsage map[AgentKind]bool
func (f fakeUsage) Exhausted(_ context.Context, k AgentKind) bool { return f[k] }

// newStoreWithFallback extends newStore(t): seeds a model_catalog row for a
// second real kind (Codex, reusing the Fake adapter under s.Adapters[Codex]
// so no real CLI is invoked), adds it to enabled_agents, and returns the
// Store plus setters for Settings.FallbackDefault and Store.Usage.
func newStoreWithFallback(t *testing.T) (*Store, *fakeTmux) { ... }

func TestStartSpikeSubstitutesExhaustedFallback(t *testing.T) {
    s, tm := newStoreWithFallback(t)
    setFallback(t, s, RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
    s.Usage = fakeUsage{Claude: true} // Claude exhausted, Codex not mentioned -> available
    _, a, queued, err := s.StartSpike(ctx, SpikeInput{Name: "x", Intent: "feature", Kind: Claude, Model: "sonnet"})
    // assert err == nil, !queued, a.Kind == Codex, a.Model == "gpt-6-astra"
    // assert notified(t, s, "agent.fallback_used").Args == {"name": a.Name, "agent": "Codex", "from": "Claude"}
}
func TestStartSpikeNotExhaustedNoSubstitution(t *testing.T) {
    // s.Usage = fakeUsage{} (Claude not marked exhausted) -> a.Kind == Claude, no notification raised.
}
func TestStartSpikeBothExhaustedRefuses(t *testing.T) {
    // s.Usage = fakeUsage{Claude: true, Codex: true} -> preflightErr set, agent row has
    // PreflightError != "", notified(t, s, "agent.preflight_failed"), no session/tmux start (tm.started empty).
}
func TestStartSpikeNilUsageNeverSubstitutes(t *testing.T) {
    // s.Usage left nil (as newStore already leaves it) -> Claude, unaffected. This is the
    // regression proof that every pre-existing StartSpike test stays correct un-modified.
}
```
Run — fails (`resolveUsageFallback` undefined / `StartSpike` doesn't call it).

Implement `fallback.go`:
```go
package runtime

import (
    "context"

    "github.com/AlexanderTar/agent-swarm/internal/catalog"
)

// resolveUsageFallback is docs/specs/2026-09-19-usage-fallback-agent.md's
// fallback-resolution step, called immediately before every place the
// daemon turns a resolved {kind, model} into a live session. See the spec's
// "Locked decisions" §1 and §6 for why this intercepts the resolved pair
// rather than "role resolution", and what the returned error means.
func (s *Store) resolveUsageFallback(ctx context.Context, kind AgentKind, model string) (AgentKind, string, bool, error) {
    if s.Usage == nil || !s.Usage.Exhausted(ctx, kind) {
        return kind, model, false, nil
    }
    cfg, err := s.Settings.Get(ctx)
    if err != nil {
        return kind, model, false, nil // can't read Settings: fail open, unrelated to usage
    }
    fb := cfg.FallbackDefault
    unusable := fb.Agent == "" || fb.Agent == kind ||
        !slices.Contains(cfg.EnabledAgents, fb.Agent) ||
        s.Usage.Exhausted(ctx, fb.Agent)
    if unusable {
        return kind, model, false, fmt.Errorf("%s is out of usage, and no other agent is available right now.", kind.Display())
    }
    fbModel := fb.Model
    if models, _, err := s.Catalog.ModelsFor(ctx, fb.Agent); err == nil {
        if _, ok := catalog.Find(models, fbModel); !ok && len(models) > 0 {
            fbModel = models[0].ID
        }
    }
    return fb.Agent, fbModel, true, nil
}
```
In `StartSpike` (agents.go), replace:
```go
preflightErr := s.Preflight(ctx, PreflightInput{Kind: in.Kind, Model: in.Model, Effort: in.Effort, Role: RoleOrchestrator, RepoPaths: in.RepoPaths})
```
with:
```go
fbKind, fbModel, substituted, ferr := s.resolveUsageFallback(ctx, in.Kind, in.Model)
in.Kind, in.Model = fbKind, fbModel
var preflightErr error
if ferr != nil {
    preflightErr = ferr
} else {
    preflightErr = s.Preflight(ctx, PreflightInput{Kind: in.Kind, Model: in.Model, Effort: in.Effort, Role: RoleOrchestrator, RepoPaths: in.RepoPaths})
}
```
and, after the successful `startSession`+`watchStartup` dispatch at the
bottom of `StartSpike` (right before `return it.Key, a, false, nil`), add:
```go
if substituted && s.Notify != nil {
    _ = s.Notify.Raise(ctx, nil, NotifyInput{Kind: "agent.fallback_used", AgentName: a.Name,
        Args: map[string]string{"name": a.Name, "agent": a.Kind.Display(), "from": string(kind)}})
}
```
(capture the original `kind` — i.e. `in.Kind` *before* reassignment — in a
local before the substitution call, e.g. `origKind := in.Kind`, and use
`origKind.Display()` for the `{from}` arg.)

Run `go test ./internal/runtime/... -run Fallback` then the full package —
green. Commit: "runtime: resolveUsageFallback, wired into StartSpike".

## Step 7 — wire `StartOrchestrator` and `Spawn`

Tests (in `fallback_test.go`): same four-shape matrix
(substitutes / not-exhausted / both-exhausted / nil-Usage) for
`StartOrchestrator` and for `Spawn`, with the both-exhausted case asserting
a returned `error` and **no agent row created**
(`SELECT COUNT(*) FROM agents` unchanged) rather than a `PreflightError`
row — matching Locked Decision 6's split. `Spawn`'s substituted case also
asserts idempotent-replay safety: calling `Spawn` twice with the same
`SessionID`/`RequestID` after a substitution notifies `agent.fallback_used`
exactly once (`notifiedCount`).

Implement: same substitution-before-Preflight edit in both functions,
returning `ferr`/`err` directly on failure (no new branch shape — mirrors
today's `if err := s.Preflight(...); err != nil { return Agent{}, false, err }`).
For `Spawn`, guard the post-commit notify with `ran` (see the existing
comment above the `agent.queued` re-raise-guard) so a replay never
double-notifies; the notify goes right before the final
`return result.Agent, false, nil` (and, if reachable, before the
`result.Queued` early return too — a substitution the caller should learn
about whether or not the agent started immediately).

Run, green. Commit: "runtime: wire usage fallback into StartOrchestrator and Spawn".

## Step 8 — wire `Retry`

Tests: substitutes-and-re-preflights-ok; substitutes-but-fallback-fails-its-own-preflight
(assert `Retry` returns that error, `agents.kind`/`model` unchanged in the
DB, no new session row); both-exhausted (plain error, no new session);
nil-Usage (regression, unaffected); **`Resume` regression test** — mark the
agent's kind exhausted via `fakeUsage`, call `Resume`, assert the new
session's `Kind` is still the original (read back via
`s.Adapters`/`tm.started` last entry) and no `agent.fallback_used` was
raised (Locked Decision 2).

Implement in `Retry` (agents.go), after `ses, err := s.LatestSession(...)`
and the `retryableStates` check, before computing `nextAttempt`:
```go
fbKind, fbModel, substituted, ferr := s.resolveUsageFallback(ctx, a.Kind, a.Model)
if ferr != nil {
    return Agent{}, ferr
}
if substituted {
    if err := s.Preflight(ctx, PreflightInput{Kind: fbKind, Model: fbModel, Effort: a.Effort, Role: a.Role}); err != nil {
        return Agent{}, err
    }
    if err := s.tx(ctx, func(tx *sql.Tx) error {
        _, err := tx.ExecContext(ctx, `UPDATE agents SET kind = ?, model = ? WHERE id = ?`, string(fbKind), fbModel, a.ID)
        return err
    }); err != nil {
        return Agent{}, err
    }
    a.Kind, a.Model = fbKind, fbModel
}
```
then after the existing `IdemTx`/publish block succeeds, before the
`s.go_(func() { ... watchStartup ... })` dispatch:
```go
if substituted && s.Notify != nil {
    _ = s.Notify.Raise(ctx, nil, NotifyInput{Kind: "agent.fallback_used", AgentName: out.Name,
        Args: map[string]string{"name": out.Name, "agent": a.Kind.Display(), "from": ...}})
}
```
(thread the pre-substitution kind through the same way as Step 6.)

Run, green. Commit: "runtime: wire usage fallback into Retry, excluding Resume".

## Step 9 — wire `startQueued` (`DrainQueue`)

Tests in `limits_test.go`: queued agent whose kind is exhausted by the time
`DrainQueue` runs it → starts under the fallback, `agents.kind`/`model`
updated, `agent.fallback_used` notified; both-exhausted → the existing
"queued agent whose preflight fails when taken off the queue" branch fires
(agent goes `active` with a `failed` session and a `spawn_failed` relay to
its parent, if any) with the fallback error as the reason — reuse the
existing test in that file as a template rather than writing the DB
assertions from scratch.

Implement in `startQueued` (limits.go): move the
`ad, ok := s.Adapters[a.Kind]` lookup down, insert the fallback resolution
before computing `preflightErr`:
```go
fbKind, fbModel, substituted, ferr := s.resolveUsageFallback(ctx, a.Kind, a.Model)
var preflightErr error
if ferr != nil {
    preflightErr = ferr
} else {
    a.Kind, a.Model = fbKind, fbModel
    preflightErr = s.Preflight(ctx, PreflightInput{Kind: a.Kind, Model: a.Model, Effort: a.Effort, Role: a.Role, RepoPaths: repoPaths})
}
ad, ok := s.Adapters[a.Kind]
if !ok {
    return false, fmt.Errorf("no adapter for %s", a.Kind)
}
```
(the original top-of-function `ad, ok := ...` line is deleted; this
replaces it). Update both `UPDATE agents SET state = 'active' WHERE id = ?`
statements to `UPDATE agents SET state = 'active', kind = ?, model = ?
WHERE id = ?` bound to `a.Kind, a.Model`. After the successful branch
(`preflightErr == nil`, right where `admitted = true` already led to
`startSession` further down — check the exact post-tx flow in the file),
add the `agent.fallback_used` notify when `substituted`.

Run `go test ./internal/runtime/... -run Queue` then full suite. Commit:
"runtime: wire usage fallback into DrainQueue's startQueued".

## Step 10 — `cmd/swarm/daemon.go` wiring

```go
rt.Usage = &usagegate.Gate{Poller: up, Now: now}
```
placed right after `up := &usagesvc.Poller{...}` (which is itself after
`rt := &runtime.Store{...}`). Add the `usagegate` import.
`go build ./...`. No new daemon-level test (this is one assignment line;
covered indirectly by every `runtime`-level test already exercising
`Store.Usage`, and by `go build` proving the wiring compiles without a
cycle). Commit: "cmd/swarm: wire usagegate.Gate into runtime.Store".

## Step 11 — web type parity

Files: `web/src/types.ts`, `web/src/mock/fixtures.ts`.
Add `fallback_default: RoleDefault;` to the `Settings` interface (next to
`roles`). Add a matching `fallback_default: { agent: "claude", model: "sonnet" }`
to whatever fixture object in `mock/fixtures.ts` builds a full `Settings`
value (grep `roles:` there first). Run `cd web && pnpm test` — green (no
runtime code path reads the new field, so this is pure type/fixture
parity — confirm `pnpm typecheck`/`tsc` if the repo has that script, since
that's the actual thing this step protects). Commit: "web: add
fallback_default to the Settings type for wire parity".

## Step 12 — menubar UI

Files: `Wire.swift`, `SettingsModel.swift`, `Copy.swift`, three fixture
JSONs.

- `Wire.swift`: `SettingsRole` gets `case fallback`; `Settings` gets
  `public var fallbackDefault: RoleDefault = RoleDefault(agent: .claude, model: "sonnet")`
  with `CodingKeys` entry `fallbackDefault = "fallback_default"`;
  `Settings.defaults` literal sets it explicitly; `subscript(role:)`
  special-cases `.fallback`.
- `SettingsModel.swift`: `defaultsOrder` appends `.fallback`.
- `Copy.swift`: `defaultsRowLabel` gets `case .fallback: return "Fallback"`.
- `Tests/Fixtures/settings.json`, `state.json`, `state-empty.json`: add
  `"fallback_default": {"agent": "claude", "model": "sonnet"}` next to
  `"roles"`.

Test: extend `SettingsModelTests.swift` with a case asserting
`m.defaultsRows.last?.role == .fallback` and its `label == "Fallback"` after
loading the fixture, plus a `setAgent(.fallback, "codex")` /
`setModel(.fallback, ...)` round-trip against the mock client (mirrors
whatever existing test already does this for `.mechanical` or similar —
find and copy that test's shape).

Run `cd apps/menubar && swift build && swift test`. Commit: "menubar: add
the Fallback row to Settings, reusing the existing Defaults-tab machinery".

## Step 13 — falsification check (report only, no commit)

Temporarily change `usagegate.Exhausted`'s `m.UsedPct < exhaustionPct` to
`m.UsedPct < 1000` (always false → nothing is ever exhausted). Run the full
`internal/runtime` and `internal/usagegate` suites: every
substitution/both-exhausted test from Steps 6-9 must go red (proves they
actually exercise the new code path, not a vacuous no-op). Revert. Run
again: green. Report the exact before/after test names and counts in the
final report; this step itself is never committed.

## Step 14 — full verification sweep

```
go build ./...
go vet ./...
gofmt -l .
go test ./... -race
cd web && pnpm test
cd apps/menubar && swift test
```
Fix anything red (shape-assertion tests flagged in the spec's "Verification
plan" — `internal/httpapi/config_test.go`, `web/src/contract.test.ts` — by
inspection first, since they may not actually assert a fixed field count).
No commit for this step alone unless something needed fixing, in which case
that fix is its own small commit.
