# Unify agent concurrency limits — implementation plan

Spec: `docs/specs/2026-09-24-unify-agent-limits.md`. Worktree:
`../agent-swarm--unify-agent-limits`, branch `feat/unify-agent-limits`, off
`main` @ `49ba399`.

This was executed as one coordinated change rather than strict
per-file-commit TDD, because the rename touches ~30 call sites across three
languages that all share one contract (the wire shape of `Settings`) — a
half-renamed intermediate state doesn't compile in any of the three, so
there's no meaningful red/green step smaller than "the whole rename". Each
numbered task below was still verified red→green against the real test
suite before moving to the next; task 3's before/after is recorded as the
representative example.

## Tasks

1. **Backend struct + validation**
   - `internal/settings/settings.go`: remove `MaxOrchestrators`, add
     `MaxConcurrentAgents int \`json:"max_concurrent_agents"\``. `Defaults()`:
     `MaxConcurrentAgents: 4`. `validate()`: single range check, 1–32.
   - Consumes: nothing. Produces: `Settings.MaxConcurrentAgents`.

2. **Admission rewrite**
   - `internal/runtime/limits.go`, `Admit(ctx, tx, role, rootItemID) (bool,
     error)`: one combined `state = 'active' AND NotAZombieSlot` count vs
     `cfg.MaxConcurrentAgents`; early `return true, nil` for
     `role == RoleOrchestrator` once under that cap; unchanged
     `MaxAgentsPerRoot` check for every other role.

3. **Runtime test suite — the representative red→green step**
   - Before: `TestAdmitOrchestratorsUseTheirOwnLimit` asserted a coder child
     is admitted *despite* `max_agents=1`, because the orchestrator "does
     not occupy an agent slot". Running it against the task-2 code (before
     any test changes) with `go test ./internal/runtime/... -run
     TestAdmitOrchestratorsUseTheirOwnLimit` fails (a queued=true child no
     longer matches `queued=false` — this is the concrete proof the
     unification landed, not just a compile fix).
   - Rewritten as `TestAdmitOrchestratorsShareTheGlobalLimit`: same setup,
     inverted assertion (`!queued` → `queued`). Green.
   - `setLimits(t, s, orchestrators, agents, perRoot)` →
     `setLimits(t, s, agents, perRoot)` across `limits_test.go`,
     `agents_test.go`, `fallback_test.go`, `inbox_test.go`,
     `reconcile_test.go`, `pause_test.go` (~30 call sites). Every site that
     starts an orchestrator and then depends on remaining headroom for a
     following spawn had its `agents` value bumped by 1 (the orchestrator
     now holds a slot too) — found by running the full suite and fixing
     each failure by tracing what the test actually needs, not by pattern
     matching the diff.
   - `internal/httpapi/config_test.go`: `cur.MaxAgents` →
     `cur.MaxConcurrentAgents` (default assertion 8 → 4).
   - `internal/advisor/queue_test.go`: comment-only (`max_agents (8)` →
     `max_concurrent_agents (4)`).
   - Verify: `go build ./...`, `go vet ./...`, `go test ./...` — all green
     except the pre-existing `TestBoardServedAtRoot` gap.

4. **e2e harness (build-tag gated, not run live)**
   - `scripts/e2e/harness_test.go`: `enableFake`'s raw-SQL default-raise
     loop drops the `max_orchestrators` key; `setMaxAgents` →
     `setMaxConcurrentAgents`, writes `max_concurrent_agents`.
   - `scripts/e2e/concurrency_test.go`, `scripts/e2e/pause_test.go`:
     `countActiveAgents` counts every role now (was
     `role <> 'orchestrator'`); both call sites' headroom bumped from
     `before+1` to `before+2` (the orchestrator each test starts now also
     draws from the same pool as the worker it's testing).
   - Verify: `go vet -tags e2e ./scripts/e2e/...`,
     `go build -tags e2e ./scripts/e2e/...`.

5. **Swift wire type**
   - `Wire.swift`: `maxOrchestrators`/`maxAgents` fields → single
     `maxConcurrentAgents`; `CodingKeys` updated; decode via
     `decodeIfPresent(...) ?? 4`; defaults/memberwise init updated.
   - Verify: `swift build` in `apps/menubar/`.

6. **Swift `wouldQueue` + Limits tab**
   - `NewOrchestratorForm.swift`: `wouldQueue` drops its
     `role == .orchestrator` filter, counts every live/queued agent in the
     tree against `settings.maxConcurrentAgents`.
   - `SettingsModel.swift`: `Limit` enum gains `.agents` (range 1...32);
     `value`/`store`/`overLimit` switches extended (`overLimit(.agents, _)`
     mirrors `.subagents` minus the `parentName != nil` filter).
   - `Copy.swift`: `maxAgents` retitled `"Max concurrent agents"`, new
     `agentsLimitCaption`; `maxOrchestrators`/`orchestratorLimitCaption`
     deleted (dead, now false).
   - `SettingsView.swift`: new `LimitField(... limit: .agents)` row in
     `LimitsTab`, above the existing subagents row.
   - Verify: `swift build`.

7. **Swift tests + fixtures**
   - `Tests/Fixtures/{settings,state,state-empty}.json`,
     `Tests/Fixtures/events/stream.sse`: `max_orchestrators`/`max_agents` →
     `max_concurrent_agents: 4`.
   - `FixtureTests.swift`, `MockDaemonClientTests.swift`,
     `SettingsModelTests.swift`, `NewOrchestratorFormTests.swift`: field
     rename plus recomputed expectations where the widened `wouldQueue` /
     `overLimit` filters now count agents the old orchestrator-only filter
     didn't (documented inline at each call site — e.g.
     `NewOrchestratorFormTests.swift`'s `wouldQueue([state.agents[0]], max:
     2)` became `max: 4)` because that fixture agent carries 2 active
     children, now counted).
   - Verify: `swift test` — 196/196, 0 failures.

8. **Web (`web/`) — same contract, same bug class, found via a
   repo-wide grep the task's own instructions required**
   - `web/src/types.ts`: `Settings.max_orchestrators`/`max_agents` →
     `max_concurrent_agents`.
   - `web/src/logic/spawnForm.ts`: `orchestratorsBusy` drops its
     `role === "orchestrator"` filter (same unification as Swift's
     `wouldQueue`).
   - `web/src/mock/fixtures.ts`, `web/src/mock/daemon.ts`: fixture default,
     `newAgent`'s queue-prediction counts every role now.
   - Test files updated: `App.flows.test.tsx`, `mock/daemon.test.ts`,
     `panels/SpawnSheet.test.tsx`, `panels/NewSpikeSheet.test.tsx`,
     `logic/spawnForm.test.ts` (the last one's numeric expectations
     recomputed the same way the Swift fixture test's were: 6 live agents
     in the fixture tree once every role counts, not 4 orchestrators).
   - Verify: `npx tsc --noEmit`, `npx vitest run` (node_modules symlinked
     from the primary checkout for the run, removed after — nothing
     committed) — clean, 416 passed / 10 pre-existing skips.

9. **Docs**
   - This plan + `docs/specs/2026-09-24-unify-agent-limits.md`.

## Commits

Five real commits in the worktree, grouped by surface rather than one per
numbered task above (tasks 1–4 share a single backend commit; the docs
commit below is the fifth) — see `git log feat/unify-agent-limits` for the
exact sequence and messages:

1. `feat(settings,runtime): unify max_orchestrators/max_agents into one pool`
   — tasks 1–4.
2. `feat(menubar): expose the unified max_concurrent_agents limit` — tasks
   5–7.
3. `feat(web): mirror the unified max_concurrent_agents limit` — task 8.
4. `docs: spec and plan for unify-agent-limits` — task 9 (initial spec/plan).
5. `docs: add rollout section to unify-agent-limits spec` — advisor-prompted
   addition covering daemon/menubar deploy coupling and the deploy-time
   admission cliff, added after the rest of the work was verified green.
