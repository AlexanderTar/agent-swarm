# Unify agent concurrency limits

## Context

The daemon's global agent-concurrency ceiling was split into two settings,
counted against two separate pools:

- `Settings.MaxOrchestrators` (default 3) — counted only `role = 'orchestrator'`
  agents, in `internal/runtime/limits.go`'s `Admit`.
- `Settings.MaxAgents` (default 8) — counted every OTHER role, globally.

A third, unrelated knob, `Settings.MaxAgentsPerRoot` (default 4, per-epic
fairness), and a fourth, `Settings.MaxConcurrentSubagents` (default 3, how
many children one parent may run at once, enforced outside `Admit`), are not
part of this change and keep their exact current semantics.

The bug that triggered this: the menubar Settings → Limits tab only exposed
`MaxConcurrentSubagents` and `PauseDeadlineSec`. `MaxOrchestrators`,
`MaxAgents` and `MaxAgentsPerRoot` existed in the backend with zero UI
exposure — a queued muse agent silently never started because `MaxAgents`
was saturated, with no UI surface to even see the limit existed.

`main` had moved since this task was scoped (new merged work:
`feat/muse-keychain-api-usage`, `feat/hold-relays-while-exhausted`); this
branch is cut from current `main` at commit `49ba399`.

Superseded by this change (left alone, historical record only):
`docs/specs/2026-09-19-usage-fallback-agent.md`,
`docs/specs/2026-09-20-subagent-budget-limits.md`,
`docs/specs/2026-09-22-isolated-mcp-and-custom-instructions.md` — each
mentions `MaxOrchestrators`/`MaxAgents` as they existed before this change.

## Locked decisions

Already confirmed with the user via AskUserQuestion before this spec was
written; not reopened here:

- Replace `MaxOrchestrators` and `MaxAgents` with **one single global
  setting** — every role (orchestrator, coder, reviewer, ui_reviewer,
  researcher, debugger, mechanical) counted against ONE shared pool, default
  **4**.
- `MaxAgentsPerRoot` and `MaxConcurrentSubagents` stay exactly as they are
  today, as separate, finer-grained knobs — not touched, not folded in.
- The menubar Limits tab gets a new field for the unified limit, following
  the exact visual/structural pattern of the existing "Max concurrent
  subagents" field. No UI for `MaxAgentsPerRoot` in this change (a real gap,
  explicitly left for a separate follow-up).

### Field name: `MaxConcurrentAgents` / `max_concurrent_agents` (new name, not a reuse)

Two options were on the table: reuse `MaxAgents` (redefine its semantics,
minimal wire/API churn) or introduce a new name (`MaxAgentsTotal` /
`MaxConcurrentAgents`, clearer, but touches more call sites and needs
`MaxOrchestrators` fully removed either way).

**Decision: new name, `MaxConcurrentAgents` (Go), `max_concurrent_agents`
(JSON), `maxConcurrentAgents` (Swift/TS).**

Why: `Settings.Get` only keeps a DB row whose key exists in the current
`Defaults()` (`internal/settings/settings.go`: `if _, known := fields[k]`).
Reusing the `max_agents` key means a daemon that already has a live
`max_agents` row (set under the OLD non-orchestrator-only semantics) would
have that same number silently reinterpreted as the new, broader combined
cap the instant this ships — a value someone set to "8 non-orchestrator
workers" becomes "8 agents total, orchestrators included" with no signal
that anything changed. A new key avoids that: any pre-existing
`max_orchestrators`/`max_agents` row becomes an inert orphan (harmless —
`Settings.Get` ignores unknown keys), and every daemon starts fresh at the
agreed default of 4 under the new, unified semantics. This also parallels
the existing `MaxConcurrentSubagents` naming.

No DB migration needed or written: orphaned `max_orchestrators`/`max_agents`
rows are simply never read again. No code deletes them (matches this
codebase's existing pattern — `fillMissing` never prunes unknown keys
either).

### Admission semantics

`internal/runtime/limits.go`'s `Admit`:

- One combined count: `SELECT COUNT(*) FROM agents WHERE state = 'active'
  AND NotAZombieSlot` — every role, orchestrators included — compared
  against `MaxConcurrentAgents`.
- If that count is at or over the limit, refuse (queue) immediately,
  whatever the role.
- Otherwise, for `role == RoleOrchestrator`, admit — orchestrators are never
  checked against `MaxAgentsPerRoot` (they never were, and this change
  doesn't touch that).
- For every other role, the existing `MaxAgentsPerRoot` check runs exactly
  as it did before (`role <> 'orchestrator' AND root_item_id = ? AND state =
  'active' AND NotAZombieSlot`, compared against `cfg.MaxAgentsPerRoot`).

### Range and default

`MaxConcurrentAgents` keeps the old `MaxAgents` range, 1–32 (the wider of
the two old ranges; `MaxOrchestrators` was 1–8). No narrower range is
justified — a fleet running mostly orchestrators should be able to set the
same ceiling a fleet running mostly workers could. Default: 4 (locked
decision).

## Wire / model types

**Go** (`internal/settings/settings.go`, `Settings` struct):

```go
// MaxConcurrentAgents is the single global admission ceiling shared by
// every role, orchestrator included. Replaces MaxOrchestrators/MaxAgents.
MaxConcurrentAgents int `json:"max_concurrent_agents"`
MaxAgentsPerRoot     int `json:"max_agents_per_root"` // unchanged
```

`MaxOrchestrators` field removed entirely (not deprecated, not a no-op).

**Swift** (`apps/menubar/Sources/SwarmBarKit/Wire.swift`, `Settings`):

```swift
public var maxConcurrentAgents: Int
public var maxAgentsPerRoot: Int
```

`CodingKeys.maxConcurrentAgents = "max_concurrent_agents"`. Decode uses
`decodeIfPresent(...) ?? 4` (same tolerance pattern as
`maxConcurrentSubagents`/`instructions`) so a daemon or fixture that
predates this rename doesn't crash the app; `maxAgentsPerRoot` keeps its
existing required `decode`.

**TypeScript** (`web/src/types.ts`, `Settings` interface):

```ts
max_concurrent_agents: number;
max_agents_per_root: number;
```

## Screens

Menubar Settings → Limits tab, following the exact `LimitField` pattern the
existing "Max concurrent subagents" field uses (label, numeric input, range
hint, one-line caption underneath):

```
┌─ Limits ──────────────────────────────────────────────┐
│ Max concurrent agents        [  4]  (1–32)             │
│ Maximum agents running at once, every role including    │
│ orchestrators.                                          │
│                                                           │
│ Max concurrent subagents     [  3]  (1–16)              │
│ Maximum subagents each parent agent may run concurrently.│
│ [N agents are running above the new limit... ] [Apply]  │  ← shown when
│                                                              lowering
│                                                              either limit
│                                                              would strand
│                                                              running agents
│ Pause deadline                [120] seconds (30–600)     │
└───────────────────────────────────────────────────────┘
```

The "N agents running above the new limit" notice/Apply row is generic over
`SettingsModel.Limit` already (`pendingLimit`/`limitNotice`/`applyLimit`) —
no new plumbing needed for it to also cover the new `.agents` case.

`MaxAgentsPerRoot` gets no UI in this change (explicit follow-up gap, not
fixed here).

## Copy

- Label: `"Max concurrent agents"` (`Copy.maxAgents`, existing constant,
  previously dead/unused — its text already matched this exact field, just
  never wired to a view).
- Caption: `"Maximum agents running at once, every role including
  orchestrators."` (new constant, `Copy.agentsLimitCaption`).
- Removed: `Copy.maxOrchestrators` ("Maximum concurrent orchestrators") and
  `Copy.orchestratorLimitCaption` ("Orchestrators have their own limit and
  don't use agent slots.") — both dead, and the caption is now false under
  the new semantics.
- Kept, untouched, still unused (out of scope): `Copy.maxAgentsPerItem`.

## File list

**Backend (Go):**
- `internal/settings/settings.go` — field rename/removal, `Defaults()`,
  `validate()`.
- `internal/runtime/limits.go` — `Admit()` rewrite, doc comments.
- `internal/settings/settings_test.go`, `internal/runtime/limits_test.go`,
  `internal/runtime/agents_test.go`, `internal/runtime/fallback_test.go`,
  `internal/runtime/inbox_test.go`, `internal/runtime/reconcile_test.go`,
  `internal/runtime/pause_test.go`, `internal/httpapi/config_test.go`,
  `internal/advisor/queue_test.go` (comment only) — updated, not deleted,
  per this repo's test-discipline rule.
- `scripts/e2e/harness_test.go`, `scripts/e2e/concurrency_test.go`,
  `scripts/e2e/pause_test.go` — raw-SQL settings keys and headroom math
  updated for the unified pool (not run live; `go vet -tags e2e` /
  `go build -tags e2e` only).

**Menubar (Swift):**
- `apps/menubar/Sources/SwarmBarKit/Wire.swift` — wire type.
- `apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift` —
  `wouldQueue` widened from orchestrator-only to every role (the same
  unification bug the backend fixes; this client-side predictor would
  otherwise silently mispredict "Start" vs "Queue orchestrator").
- `apps/menubar/Sources/SwarmBarKit/SettingsModel.swift` — new `.agents`
  `Limit` case, `value`/`store`/`overLimit`.
- `apps/menubar/Sources/SwarmBarKit/Copy.swift` — label/caption changes.
- `apps/menubar/Sources/SwarmBarUI/SettingsView.swift` — new `LimitField`
  row in `LimitsTab`.
- `apps/menubar/Tests/Fixtures/{settings,state,state-empty}.json`,
  `apps/menubar/Tests/Fixtures/events/stream.sse` — fixture wire updates.
- `apps/menubar/Tests/SwarmBarTests/{FixtureTests,MockDaemonClientTests,
  NewOrchestratorFormTests,SettingsModelTests}.swift` — updated/extended.

**Web (`web/`, the separate kanban/hierarchy board — in scope because its
wire type and its own `wouldQueue`-equivalent (`orchestratorsBusy` in
`spawnForm.ts`, and `mock/daemon.ts`'s `newAgent`) mirror the exact same
contract and the exact same orchestrator-only counting bug):**
- `web/src/types.ts`, `web/src/logic/spawnForm.ts`, `web/src/mock/
  {fixtures,daemon}.ts` — wire field rename, unified role counting.
- `web/src/{App.flows,logic/spawnForm,mock/daemon,panels/SpawnSheet,
  panels/NewSpikeSheet}.test.ts(x)` — updated.

**Not touched:** `internal/httpapi/config.go` and `server.go` (pass
`settings.Settings` through generically, no field references),
`internal/httpapi/contract_test.go` (reads the menubar fixtures directly —
covered by the fixture updates above), the three historical specs named in
Context.

## Verification

1. `go build ./...`, `go vet ./...` — clean.
2. `go test ./...` from repo root — clean except the pre-existing,
   documented, unrelated `TestBoardServedAtRoot` gap (missing gitignored
   `web/dist` in a fresh worktree).
3. `go vet -tags e2e ./scripts/e2e/...` and `go build -tags e2e
   ./scripts/e2e/...` — clean (not run live; needs a running daemon per
   `make e2e`, out of scope for this change per its own "do not redeploy"
   boundary).
4. `swift build` and `swift test` in `apps/menubar/` — clean, 196/196.
5. `npx tsc --noEmit` and `npx vitest run` in `web/` — clean, 416 passed / 10
   pre-existing skips (node_modules symlinked from the primary checkout for
   this run only, removed afterward; nothing committed).
6. Manual scenarios covered by the updated Go suite: orchestrator now shares
   the global pool with every other role (`TestAdmitOrchestratorsShareTheGlobalLimit`);
   lowering the limit never kills a running agent
   (`TestLoweringALimitQueuesTheNextSpawnAndLeavesRunningAgentsAlone`);
   zombie/self-terminal-checkpoint exclusions still apply to the unified
   count; `MaxAgentsPerRoot` still only gates non-orchestrator roles.

## Rollout

Not performed as part of this change (see Explicitly out of scope), but
load-bearing for whoever deploys it:

- **Daemon and menubar app must redeploy together, not independently.** The
  Swift `Wire.swift` shipped in this change decodes `max_concurrent_agents`
  with `decodeIfPresent(...) ?? 4`, which protects a *new* menubar build
  against an *old*, not-yet-upgraded daemon. The reverse direction is not
  protected: today's already-installed menubar app decodes with `try
  c.decode(Int.self, forKey: .maxOrchestrators)` (required, no fallback). If
  the daemon deploys first, its `GET /api/settings` response no longer has a
  `max_orchestrators` key at all, so the *old, still-running* menubar app's
  Settings window fails to decode and any settings PUT it sends omits
  `max_concurrent_agents` entirely, which `validate()` reads as 0 and
  rejects with a 400. `make install-daemon` and `make install-app` are two
  separate steps on this machine (per the `swarm-local-deploy` memory) —
  they need to run back to back for this change, not independently.
- **`web/dist` is embedded at daemon build time** (`web/embed.go`), so
  `make install-daemon` (which does `web build` + `go build` in one step,
  per the same memory) already keeps the daemon and the web board in sync —
  no extra step needed there, just confirming the existing `make
  install-daemon` flow covers it; a manual `go build` of only `cmd/swarm`
  without rebuilding `web/` first would ship a stale bundle still reading
  `settings.max_orchestrators` (now `undefined`), silently pinning
  `orchestratorsBusy` to `false`.
- **Deploy-time admission cliff.** The bug report that triggered this task
  describes the live system already at `max_agents=8` (plus orchestrators)
  and saturated. The moment this ships, the pool becomes `max_concurrent_agents=4`
  total. Already-running agents are never stopped (`TestLoweringALimitQueuesTheNextSpawnAndLeavesRunningAgentsAlone`
  covers exactly this), but if more than 4 agents are active at deploy time,
  *no new agent admits* — spawns and new orchestrators queue — until enough
  of the existing fleet finishes to drop the count under 4. That's the
  user's locked default working as designed, not a bug, but it means
  deploying this while the fleet is busy will visibly stall new work until
  it drains; worth deploying at a quiet point, or raising the limit via the
  new Settings UI field right after deploy if that stall isn't acceptable.

## Explicitly out of scope

- UI for `MaxAgentsPerRoot` (real gap, left for a follow-up — see Locked
  decisions).
- Any change to `MaxAgentsPerRoot` or `MaxConcurrentSubagents` semantics.
- A DB migration or cleanup of orphaned `max_orchestrators`/`max_agents`
  settings rows.
- Merging, pushing, redeploying the daemon, or rebuilding/reinstalling the
  menubar app — this branch is for independent review before any of that.
