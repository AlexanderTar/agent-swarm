# Plan: worker defaults, native Gemini usage, kebab-case names

Spec: `docs/specs/2026-09-26-worker-defaults-gemini-usage-kebab-names.md`. Worktree `../agent-swarm-worker-defaults`, branch `fix/worker-defaults-gemini-names`. Every task: failing test → run, watch it fail → minimal code → run, pass → commit (explicit paths, no amend).

## Task 1: kebab-case generated names

- Test (`internal/runtime/agents_test.go`): `TestDefaultNameKebabsRole` — `defaultName(RoleUIReviewer, "Announce support and")` == `"announce-support-and-ui-reviewer"`; plus `TestSpawnUIReviewerNameIsKebab` spawning `RoleUIReviewer` on TASK-1 and asserting no `_` in `a.Name` and suffix `-ui-reviewer`.
- Run `go test ./internal/runtime -run 'DefaultNameKebab|UIReviewerNameIsKebab'` → FAIL (`...-ui_reviewer`).
- Code (`internal/runtime/agents.go` `defaultName`):
  ```go
  return ids.Kebab(slug + "-" + string(role))
  ```
- Run → PASS. Commit `fix(runtime): kebab-case the role suffix in generated agent names`.

## Task 2: agy usage shows native Gemini only

- Port `TestAgyBuckets` (`internal/usage/agy_test.go`): Claude & GPT meters must be absent and headline `gemini_5h` (was: cgpt labels present, headline `cgpt_5h`). Add `TestAgyHeadlineIsGeminiEvenWhenExtraModelsAreBusier` with the live shape (Gemini 5h 6 %, cgpt 5h 100 %).
- Add `internal/usagegate/usagegate_test.go` `TestAgyNotExhaustedByExtraModels`: `ParseAgyQuota` of that body → `Exhausted(snapshot)` false.
- Run → FAIL.
- Code (`internal/usage/agy.go`): `var agyExtraGroups = map[string]bool{"Claude and GPT models": true}`; skip those groups; headline = `gemini_5h` if emitted, else busiest 5h.
- Swift (`apps/menubar/Tests/...`): `MenuLabel`/`UsageSection` test with a Gemini-only agy snapshot → segment text `6%`, rows `["Gemini weekly","Gemini 5h"]`.
- Run `go test ./internal/usage ./internal/usagegate` and `(cd apps/menubar && swift test)` → PASS. Commit `fix(usage): agy reports native Gemini quota only`.

## Task 3: kind_reason column and Spawn provenance

- Migration `internal/db/schema/0016_agent_kind_reason.sql`: `ALTER TABLE agents ADD COLUMN kind_reason TEXT;`
- Tests (`internal/runtime/agents_test.go`):
  - `TestSpawnRoleDefaultHasNoKindReason`: no kind/model → role default, `KindReason == ""` (read back via `s.Agent`).
  - `TestSpawnExplicitOverrideRecordsReason`: `Kind: Fake, Model: "fake-1", OverrideReason: "user asked for fake"` → kept, `KindReason == "User override: user asked for fake"`.
  - `TestSpawnParentRoleOverrideRecordsReason`: parent with `role_overrides` → child uses it, `KindReason == "Role override set on <parent>"`.
  - `internal/runtime/fallback_test.go` `TestSpawnFallbackRecordsReason` and `TestStartQueuedFallbackRecordsReason`: reason contains `is out of usage`.
- Run → FAIL (field missing).
- Code: `Agent.KindReason`; `SpawnInput.OverrideReason`; `joinReason`/`fallbackReason`; compute in `Spawn` (explicit → user override; parent override applied → role override; substituted → append fallback); insert `kind_reason` (NULLIF ''); add `COALESCE(kind_reason,'')` to the 7 agent SELECTs + scans; `startQueued` and `Retry` update `kind_reason` when substituted; `s.logf("spawn: %s on %s/%s: %s", …)` when non-empty.
- Run `go test ./internal/runtime ./internal/db` → PASS. Commit `feat(runtime): record why a worker's kind differs from settings`.

## Task 4: MCP enforcement

- Tests (`internal/mcpserver/*_test.go`, next to existing swarm_spawn / swarm_role_overrides tests):
  - `swarm_spawn` with `agent` and no `override_reason` → error text equals spec copy.
  - with `override_reason` → spawned, agent row `KindReason` = `User override: …`.
  - no agent/model → spawned on role default.
  - `swarm_role_overrides set` without `reason` → spec copy; with `reason` → ok.
- Run → FAIL.
- Code (`internal/mcpserver/orchestrator.go`): schema fields; checks; pass `OverrideReason`; log role override set via `s.Log`.
- Run `go test ./internal/mcpserver` → PASS. Port any existing test that passed agent/model without a reason (add `override_reason`, never delete). Commit `feat(mcp): require a user reason for worker kind overrides`.

## Task 5: surface on board

- HTTP: `agentNodeWire.KindReason *string json:"kind_reason"` via `optStr`. Test in `internal/httpapi` asserting `kind_reason` present/null.
- Web: `types.ts` `kind_reason: string | null`; fixtures/mocks default `null`; `AgentRow.tsx` renders reason line when non-null; `AgentRow.test.tsx` asserts text shown and absent when null.
- Run `go test ./internal/httpapi`, `(cd web && pnpm test)` → FAIL then PASS. Commit `feat(board): show why a worker runs on a non-default agent`.

## Task 6: skill text

- `skills/swarm-orchestrator/SKILL.md` lines 28–30: leave agent/model/effort empty; only pass them (with `override_reason`) when the user asked; `swarm_role_overrides set` needs `reason` and only on user request; usage fallbacks are automatic.
- `make skills-sync && git diff --stat internal/install/skills`. Commit `docs(skills): orchestrators use the user's role defaults`.

## Final verification

```
go test ./... -count=1
go vet ./...
test -z "$(gofmt -l .)"
(cd web && pnpm test)
(cd apps/menubar && swift test)
make skills-sync && git diff --exit-code internal/install/skills
```
If `TestBoardServedAtRoot` 503s: `make web-build`.
