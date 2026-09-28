# Plan: the advisor belongs to the orchestrator only

Companion to `docs/specs/2026-09-28-advisor-orchestrator-only.md`. Strict TDD: red, watch
fail, minimal green, commit. One commit per task unless noted.

## Task 1 — `advisorAllowed` guard + Spawn no longer gives children an advisor

Files: `internal/runtime/agents.go`, `internal/runtime/agents_test.go`

1. Red: add `TestSpawnNeverPersistsAdvisorForACoder` in `agents_test.go`, modeled on
   `TestSpawnExplicitAdvisorChoiceOverridesSettings` (same fixture): `Spawn(SpawnInput{Role:
   RoleCoder, Kind: Fake, Model: "fake-1", Advisor: &AdvisorChoice{Kind: Codex, Model:
   "some-model"}, ...})`, assert `advisorCols` returns all-empty and, with `s.Advisor =
   fakeAdvisor{mode:"native"}` and `s.Adapters[Fake] = fa`, that `fa.LastSpec.AdvisorModel ==
   ""`. Run `go test ./internal/runtime/... -run TestSpawnNeverPersistsAdvisorForACoder -v`,
   confirm it fails (current code still sets codex/some-model).
2. Green:
   - Add near `resolveAdvisor` (agents.go, after line ~816):
     ```go
     // advisorAllowed reports whether role may be given an advisor. Only the
     // orchestrator role gets one (spec 2026-09-28): children are prohibited,
     // whatever Settings or a swarm_spawn/API advisor field says.
     func advisorAllowed(role Role) bool {
     	return role == RoleOrchestrator
     }
     ```
   - In `Spawn` (agents.go:1107), replace
     `advKind, advModel, advEffort, advMode, advRequestedEffort := s.resolveAdvisorAfterFallback(ctx, origKind, in.Kind, in.Advisor)`
     with:
     ```go
     var advKind AgentKind
     var advModel, advEffort, advMode, advRequestedEffort string
     if advisorAllowed(in.Role) {
     	advKind, advModel, advEffort, advMode, advRequestedEffort = s.resolveAdvisorAfterFallback(ctx, origKind, in.Kind, in.Advisor)
     }
     ```
3. Update the four pre-existing Spawn/RoleCoder tests that pinned "children get an advisor"
   (test/code mismatch created by this change — updated, not deleted, per CLAUDE.md test
   discipline):
   - `TestSpawnPersistsSimulatedAdvisorEffort`: change the final assertion to expect all four
     `advisorCols` empty regardless of `tc.advisor`/`tc.advisorModel` (rename the "persists"
     assertion comment to say children never get one now).
   - `TestSpawnUsesSettingsAdvisorDefaultWhenNoneChosen`: rename to
     `TestSpawnIgnoresSettingsAdvisorDefaultForAChild` and assert `advisorCols` empty.
   - `TestSpawnExplicitAdvisorChoiceOverridesSettings`: rename to
     `TestSpawnIgnoresExplicitAdvisorChoiceForAChild` and assert `advisorCols` empty.
   - `TestSpawnPassesAdvisorModelToSpecWhenNative`: rename to
     `TestSpawnNeverPassesAdvisorModelToSpecForAChild` and assert
     `fa.LastSpec.AdvisorModel == ""`.
   Leave `TestSpawnAdvisorNoneOverridesSettings` and `TestSpawnOmitsAdvisorModelWhenSimulated`
   as-is (already assert empty; still valid, now for a different reason — no code change).
4. Run `go test ./internal/runtime/... -run TestSpawn -v`; all green.
5. Commit: `fix(runtime): children never resolve an advisor (advisorAllowed guard)`.

## Task 2 — `startSession`'s `AdvisorModel` guard (defence in depth)

Files: `internal/runtime/agents.go`, `internal/runtime/agents_test.go`

1. Red: add `TestStartSessionOmitsNativeAdvisorModelForAPreChangeChildRow` — seed a coder
   agent row directly via SQL with `role='coder', advisor_mode='native',
   advisor_model='fable'` (the pre-change shape a stale DB row could have), then call
   `s.startSessionForTest(ctx, a, 1, 1)` (existing test helper, `helpers_test.go:189`) with
   `s.Adapters[a.Kind] = fa`, and assert `fa.LastSpec.AdvisorModel == ""`. Confirm it fails
   first (current code sets it whenever `AdvisorMode == "native"`, regardless of role).
2. Green: in `startSession` (agents.go:1451), change
   ```go
   if a.AdvisorMode == "native" {
   	spec.AdvisorModel = a.AdvisorModel
   }
   ```
   to
   ```go
   if advisorAllowed(a.Role) && a.AdvisorMode == "native" {
   	spec.AdvisorModel = a.AdvisorModel
   }
   ```
3. Run the new test plus `TestStartOrchestratorPassesAdvisorModelToSpecWhenNative` (must stay
   green — orchestrator unaffected).
4. Commit: `fix(runtime): startSession drops native AdvisorModel for non-orchestrator rows`.

## Task 3 — DrainQueue / `startQueued` re-resolution guard

Files: `internal/runtime/limits.go`, `internal/runtime/capacity_test.go` (or a new
`internal/runtime/limits_test.go` if none exists for `startQueued` — check first with
`grep -n startQueued internal/runtime/*_test.go`)

1. Red: add `TestStartQueuedNeverReResolvesAdvisorForAChild`. Reuse the existing
   fallback-substitution fixture pattern from `fallback_test.go` (`newStoreWithFallback`,
   `setFallback`, `s.Usage = fakeUsage{...}`) but spawn a **queued coder** (fill the pool with
   orchestrator holders first the way
   `TestRetryFallbackExhaustionOnAQueuedRetryBlocksAndNotifies` does, then `Spawn` a coder with
   an explicit `Advisor` choice so it queues), then call `s.DrainQueue(ctx)` once a slot frees
   and assert the coder's `advisorCols` stay empty after admission. Confirm red first.
2. Green: in `startQueued` (limits.go:220-222), guard the re-resolution block:
   ```go
   if substituted && advisorAllowed(a.Role) {
   	advKind, advModel, advEffort, advMode, advRequestedEffort := s.resolveAdvisor(ctx, a.Kind,
   		&AdvisorChoice{Kind: AgentKind(a.AdvisorKind), Model: a.AdvisorModel, Effort: a.AdvisorRequestedEffort})
   	a.AdvisorKind, a.AdvisorModel, a.AdvisorEffort, a.AdvisorMode, a.AdvisorRequestedEffort = string(advKind), advModel, advEffort, advMode, advRequestedEffort
   }
   ```
   (a child's `a.AdvisorKind` etc. are already empty from Task 1's Spawn guard, so this is
   belt-and-suspenders: it stops a stale pre-change row's advisor surviving a queue drain.)
3. Run the new test + existing `limits`/`capacity` advisor-adjacent tests.
4. Commit: `fix(runtime): DrainQueue never re-resolves an advisor for a child`.

## Task 4 — `applyRetryFallback` guard (Retry + startSuccessor)

Files: `internal/runtime/fallback.go`, `internal/runtime/fallback_test.go`

1. Red: add `TestApplyRetryFallbackNeverReResolvesAdvisorForAChild`, mirroring
   `TestRetryReResolvesAdvisorModeOnKindSwap` but spawning a coder (`Spawn`, not `StartSpike`)
   with `s.Advisor = kindSensitiveAdvisor{}`, crashing its session
   (`crashLatestSession`), setting `s.Usage = fakeUsage{Claude: true}`, calling
   `s.Retry(ctx, a.Name, "", "", "")`, and asserting `out.AdvisorMode == ""` and
   `advisorCols` all empty after. Confirm red (current code re-resolves via
   `kindSensitiveAdvisor` and would set `simulated`/non-empty for the coder too, since
   `applyRetryFallback` doesn't check role).
2. Green: in `applyRetryFallback` (fallback.go:90), guard both the resolve call and the
   persisted fields:
   ```go
   var advKind AgentKind
   var advModel, advEffort, advMode, advRequestedEffort string
   if advisorAllowed(a.Role) {
   	advKind, advModel, advEffort, advMode, advRequestedEffort = s.resolveAdvisor(ctx, fbKind,
   		&AdvisorChoice{Kind: AgentKind(a.AdvisorKind), Model: a.AdvisorModel, Effort: a.AdvisorRequestedEffort})
   }
   ```
   (the rest of the function — the UPDATE and the in-memory field assignment — is unchanged;
   it already just writes whatever the four/five values are, which are now correctly empty
   for a non-orchestrator `a.Role`.)
3. Run the new test + `TestRetryReResolvesAdvisorModeOnKindSwap` (must stay green — it spawns
   via `StartSpike`, i.e. role orchestrator, unaffected) + `TestStartSpikeImmediateFallbackRetainsNativeAdvisorSettingsEffort`.
4. Commit: `fix(runtime): applyRetryFallback never re-resolves an advisor for a child`.

## Task 5 — `swarm_advise` refuses non-orchestrator callers

Files: `internal/mcpserver/tools.go`, `internal/mcpserver/tools_test.go` (or nearest existing
advisor tool test file — check with `grep -rn advisorTool internal/mcpserver/*_test.go`)

1. Red: add `TestAdvisorToolRefusesNonOrchestratorCaller`: build a `Server` with `s.Advisor`
   set to a working fake, call the tool via `s.call(ctx, Caller{SessionID: ..., Role:
   runtime.RoleCoder}, "swarm_advise", `{"question":"x"}`)`, assert the error text equals the
   spec's exact refusal string and that no advice row exists afterward (query the advisor
   store, or assert `s.Advisor`'s fake wasn't invoked). Add
   `TestAdvisorToolAllowsOrchestratorCaller` (role `RoleOrchestrator`) asserting it still
   works (existing behavior, should already pass — write it anyway as the paired positive
   case). Confirm the refusal test fails first (today every role can call it).
2. Green: in `advisorTool` (tools.go:938), right after the `s.Advisor == nil` check, add:
   ```go
   if c.Role != runtime.RoleOrchestrator {
   	return nil, errors.New(`The advisor is only available to your orchestrator. Put the decision and your evidence in your checkpoint (blockers/next), or ask your parent with swarm_send kind:"question".`)
   }
   ```
3. Run both new tests plus the full `internal/mcpserver` advisor-related suite.
4. Commit: `fix(mcpserver): swarm_advise refuses any caller that is not an orchestrator`.

## Task 6 — `swarm_spawn` `advisor` field: description cleanup

Files: `internal/mcpserver/orchestrator.go`

No behavior to test (the field was already decoded-but-unused, per the existing comment at
orchestrator.go:634 and confirmed by `grep -n "in.Advisor" internal/mcpserver/orchestrator.go`
returning nothing beyond the struct field) — this task is pure copy/comment cleanup, not
covered by a new test:
1. `spawnTool`'s `Description` (line 612): drop "and advisor" —
   `"Spawn a worker agent on an item. Agent, model and effort come from the user's Settings
   role default. Pass agent/model/effort only when the user asked for them, with
   override_reason saying what the user asked for."`
2. Update the comment above the `Advisor json.RawMessage` field (lines ~634-639) to state it
   is now permanently ignored for children (spec Locked Decision 4), not just "not yet wired":
   `// Advisor is decoded for backward JSON compatibility but always ignored: children never
   // get an advisor (spec 2026-09-28), so nothing reads this field.`
3. `go build ./...` to confirm no compile break (the field itself is not removed from the
   Go struct — it stays decode-only, matching "removed from schema" since it was never in the
   JSON Schema string to begin with — verified: `grep -n '"advisor"' internal/mcpserver/orchestrator.go`
   only matches inside that comment/struct tag, not the `Schema:` string).
4. Commit: `docs(mcpserver): swarm_spawn description/comment reflect advisor is ignored for children`.

## Task 7 — Skills copy (both trees, byte-identical)

Files (each edited identically in both locations):
`skills/swarm/SKILL.md` + `internal/install/skills/swarm/SKILL.md`,
`skills/swarm-advisor/SKILL.md` + `internal/install/skills/swarm-advisor/SKILL.md`,
`skills/swarm-debugger/SKILL.md` + `internal/install/skills/swarm-debugger/SKILL.md`,
`skills/swarm-orchestrator/SKILL.md` + `internal/install/skills/swarm-orchestrator/SKILL.md`

No new Go test — `TestEmbeddedMirrorMatchesCanonicalTree`
(`internal/install/skills_test.go:288`) already asserts the two trees match, and
`TestApprovalSkillsDescribeCurrentContract` (line 106) may need its fixture string updated if
it embeds rule 9a's text verbatim — check first: `grep -n "9a\." internal/install/skills_test.go`.

1. `skills/swarm/SKILL.md` rule 9a (line 26): replace the whole line with the spec's exact
   copy:
   ```
   9a. Only orchestrators have an advisor. If you are not an orchestrator, do not look for or call one (swarm_advise refuses you): put hard decisions, with your evidence, in your checkpoint or ask your parent with swarm_send kind:"question". Orchestrators: follow the swarm-advisor skill; Claude orchestrators with a native advisor call their built-in advisor tool, others call swarm_advise. An orchestrator with neither tool reports it as a launch fault in its first checkpoint.
   ```
   Apply the identical edit to `internal/install/skills/swarm/SKILL.md`.
2. `skills/swarm-advisor/SKILL.md`:
   - Frontmatter `description`: append
     `…Referenced from the swarm skill's rule 9a; for orchestrators only — child agents have
     no advisor.` (replacing the existing trailing clause about being "installed for every
     agent, not just one role" — that clause is now false).
   - Insert as the new first paragraph under the `# Consulting your advisor` heading, before
     "Follow the `swarm` skill first; this adds to it.":
     `Only orchestrators have an advisor. If you are a child agent, stop here and escalate to
     your orchestrator instead.`
   Apply identically to `internal/install/skills/swarm-advisor/SKILL.md`.
3. `skills/swarm-debugger/SKILL.md` line 13, replace:
   `Consult your advisor before you commit to a root cause: this is exactly the "before
   committing to an approach" and "going in circles" case swarm-advisor calls out, and a wrong
   root cause wastes the rest of the task.`
   with the spec's exact replacement:
   `Before you commit to a root cause, put the candidate cause and its evidence in a progress
   checkpoint; your orchestrator weighs it (with its advisor) before you fix.`
   Apply identically to `internal/install/skills/swarm-debugger/SKILL.md`.
4. `skills/swarm-orchestrator/SKILL.md`: under the existing "Consult your advisor at each of
   these points:" bullet list (line 65, the Spikes section), add the new bullet verbatim from
   the spec after the existing four sub-bullets and before the "Note in the artifact's text…"
   line:
   ```
   - Consult your advisor when you review a complex worker report, before you accept it or re-dispatch:
     a completed or failed checkpoint with findings, a review with disagreements or blocking findings,
     a debug root cause, a design or spec from a designer, or any report you would otherwise accept on trust.
     Good briefing: "Coder says TASK-7 is done: red `go test ./auth -run TestExpiry` failed, green passes;
     diff touches token.go:40-88. Reviewer flags a race in refresh(). Accept, or send back?" plus the checkpoint refs.
     Bad briefing: "Is TASK-7 ok?"
   ```
   Apply identically to `internal/install/skills/swarm-orchestrator/SKILL.md`.
5. Verify: `diff -rq skills internal/install/skills` (must print nothing, aside from any
   preexisting `vendor/` asymmetry already present before this change — confirm with `git
   status` that only the four skill dirs changed on both sides).
6. `go test ./internal/install/... -run 'TestEmbeddedMirrorMatchesCanonicalTree|TestApprovalSkillsDescribeCurrentContract|TestSkillBodyCarriesTheSpecFrontmatterAndLastRule'`
   green.
7. Commit: `docs(skills): advisor is orchestrator-only in swarm/swarm-advisor/swarm-debugger/swarm-orchestrator`.

## Task 8 — Full verification sweep

1. `gofmt -l .` — empty output.
2. `go vet ./...` — clean.
3. `go test ./...` — clean.
4. Do NOT run `scripts/e2e.sh` (explicitly out of scope for this session).
5. No commit (verification only) unless a stray fix is needed, in which case fold it into the
   task it belongs to and re-run this sweep.

## Explicitly out of scope (restated from spec)

- Changing which model the advisor uses, or the advisor protocol itself.
- Killing or relaunching running children.
- Advisor accounting and cost reporting.
- Removing `Advisor *AdvisorChoice` from `SpawnInput`/`OrchestratorInput`/`SpikeInput` Go
  structs — spec Decision 4 only requires it be ignored for children and dropped from the
  `swarm_spawn` JSON Schema, and it was never in that Schema to begin with.
