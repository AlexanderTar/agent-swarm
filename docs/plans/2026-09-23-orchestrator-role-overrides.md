# Implementation plan: orchestrator-writable role overrides

Spec: `docs/specs/2026-09-23-orchestrator-role-overrides.md`. Bounded
extension (existing flow already reads `RoleOverrides`; this adds a write
path onto the same column) — this plan is a short TDD checklist, not a full
architectural breakdown.

All steps executed in `../agent-swarm--orchestrator-role-overrides`, branch
`feat/orchestrator-role-overrides`.

## 1. Export `settings.Store.ValidateDefault`
- Rename `validateDefault` → `ValidateDefault` in `internal/settings/settings.go`
  (`replace_all`, two internal call sites in `validate`).
- No test needed: pure rename, existing `internal/settings` suite proves no
  behavior change (`go test ./internal/settings/...`).

## 2. `runtime.Store.SetRoleOverride` (TDD)
- **Red**: add to `internal/runtime/agents_test.go`:
  - `TestSetRoleOverrideAppliesAtSpawn`
  - `TestSetRoleOverrideClearFallsThroughToGlobalDefault`
  - `TestSetRoleOverrideRejectsDisabledAgent`
  - `TestSetRoleOverrideRejectsUnoverridableRole`
  - `TestSetRoleOverrideRequestIDReplaysWithoutASecondWrite`
  - `TestSetRoleOverrideOnlyEverTouchesTheCallersOwnRow`
  - Confirm `go vet ./internal/runtime/...` fails (`SetRoleOverride undefined`).
- **Green**: add `OverridableRoles`, `joinRoles`, `Store.SetRoleOverride` to
  `internal/runtime/agents.go` (signature: `(ctx, name string, role Role, rd
  *settings.RoleDefault, sessionID, requestID string) (Agent, error)`).
  Validate role membership, then (if `rd != nil`)
  `s.Settings.ValidateDefault`, then a single `IdemTx` whose body does the
  `SELECT role_overrides ... ` / mutate map / `UPDATE ...` /
  `publishAgentChanged` inside the transaction.
- Run `go test ./internal/runtime/... -run TestSetRoleOverride` — confirm
  green.

## 3. `swarm_role_overrides` MCP tool + `swarm_read` extension (TDD)
- **Red**: add to `internal/mcpserver/orchestrator_test.go`:
  - `TestSwarmRoleOverridesSetThenSpawnUsesIt`
  - `TestSwarmRoleOverridesClearRemovesTheEntry`
  - `TestSwarmRoleOverridesRejectsAdvisorRole`
  - `TestSwarmRoleOverridesRejectsDisabledAgent`
  - `TestSwarmRoleOverridesIsScopedToTheCallersOwnRow`
  - `TestSwarmReadShowsRoleOverrides`
  - Confirm they fail (`swarm_role_overrides: not available to this caller`).
- **Green**:
  - `internal/mcpserver/tools.go`: add `roleOverridesOut`, wire into `agentOut`.
  - `internal/mcpserver/orchestrator.go`: add `roleOverridesTool`, register in
    `orchestratorTools`.
- Run `go test ./internal/mcpserver/... -run 'TestSwarmRoleOverrides|TestSwarmReadShowsRoleOverrides'` —
  confirm green.

## 4. Fix the tool-count fixture
- `internal/mcpserver/server_test.go`'s `TestToolListsByRole` hardcodes the
  orchestrator tool count. Update 13→14 (regular orchestrator) and 14→15
  (spike orchestrator, `+swarm_materialize`); add `swarm_role_overrides` to
  the required-tools list.
- Run `go test ./internal/mcpserver/...` — full package green.

## 5. Docs
- Add one bullet to `skills/swarm-orchestrator/SKILL.md` documenting
  `swarm_role_overrides`. Copy byte-identical into
  `internal/install/skills/swarm-orchestrator/SKILL.md` (the two were
  byte-identical before this change; `internal/install/skills_test.go`
  doesn't assert on the new bullet's text, only pre-existing anchors, so no
  test change needed there).
- `go test ./internal/install/...` — confirm still green.

## 6. Full verification
- `go build ./...`
- `go test ./internal/settings/... ./internal/runtime/... ./internal/mcpserver/... ./internal/install/...`
- `go test ./...` — expect everything green except the pre-existing,
  unrelated `internal/httpapi` `TestBoardServedAtRoot` failure (`web/dist`'s
  real build output is gitignored, `dist/*` + `!dist/.gitkeep`, so a fresh
  worktree only has the placeholder, not a `pnpm build` — see spec's
  Verification section). Do not attempt to fix this as part of this task.
- `gofmt -l internal/` — compare against the same command on `main`; confirm
  no *new* files appear (this repo carries pre-existing gofmt debt on files
  this change doesn't touch; don't fix unrelated files here).
- Commit in small, real steps (not one giant commit): (1) settings rename,
  (2) runtime `SetRoleOverride` + its tests, (3) mcp tool + `swarm_read`
  extension + their tests + the tool-count fixture fix, (4) SKILL.md docs.
