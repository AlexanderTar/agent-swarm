# Orchestrator-writable role overrides (`swarm_role_overrides`)

## Context

`agents.role_overrides` (`map[runtime.Role]settings.RoleDefault`, JSON column) is a
per-orchestrator snapshot of role→agent/model/effort defaults, set ONCE at
orchestrator creation from the `roles` param of whatever created it (`swarm new`
CLI, or `orchestratorRequestBody.Roles`/`rolesFromBody` in
`internal/httpapi/runtime.go`). When an orchestrator spawns a child,
`internal/runtime/agents.go`'s `Spawn` (the resolution block at lines 746-755)
picks the child's agent/model in this order: the orchestrator's own
`RoleOverrides[role]` wins if present; only falls back to the live global
`Settings.Roles[role]` if the orchestrator has no override for that role.

This bit a real user: they changed the global `coder` role default from
claude/sonnet to muse, but an already-running orchestrator had
`role_overrides: {"coder":{"agent":"claude","model":"sonnet"},...}` baked in
from its own creation, so every coder it spawned kept using claude/sonnet
regardless of the global change. There was no way to change this after
creation — no MCP tool, no HTTP endpoint, no CLI command; the only fix was raw
SQL against a live orchestrator's `agents.role_overrides` column.

This spec adds a write path: an MCP tool, `swarm_role_overrides`, that lets a
running orchestrator set or clear entries in its own `role_overrides` row.

Repo: `/Users/alexandertar/GitHub/agent-swarm`. Implemented in a sibling
worktree, `../agent-swarm--orchestrator-role-overrides`, branch
`feat/orchestrator-role-overrides`. No collisions expected: the files touched
(`internal/settings/settings.go`, `internal/runtime/agents.go`,
`internal/mcpserver/orchestrator.go`, `internal/mcpserver/tools.go`, both
`skills/swarm-orchestrator/SKILL.md` copies) are not under concurrent edit by
any other known worktree as of 2026-09-23.

## Locked decisions

1. **Scope: self only.** The tool has no target-agent/agent-id parameter.
   `name` (the row to update) is always resolved from the caller's own MCP
   session (`callerAgent`), never taken from the request body. This is
   sufficient because a grep of `RoleOverrides` across `internal/runtime`
   shows it is read in exactly one place outside its own accessors/scans:
   `agents.go:711` (`parentRoleOverrides = parent.RoleOverrides`, inside
   `Spawn`) and consumed at `agents.go:748`. Nothing else — not a sibling's
   spawn, not a grandchild's, not any other code path — ever reads an
   orchestrator's `RoleOverrides` except that orchestrator's own future
   `Spawn` calls. There is therefore no user-visible difference between "self
   only" and a hypothetical "target any agent" version except attack surface,
   so self-only is both sufficient and the safer default.

2. **Operations: `set` and `clear`.**
   - `set` (`role`, `agent`, `model`, `effort?`): writes/replaces one role's
     entry in the map, after the same validation `settings.Store.Put` runs on
     a normal Settings write (agent enabled, model exists in that agent's
     catalog, model supports the given effort).
   - `clear` (`role`): removes the role's entry, so the next spawn on that
     role falls through to the live global `Settings.Roles[role]` default —
     this is exactly the fix the triggering incident needed and didn't have.
   - No `list`/read op on this tool: `swarm_read`'s existing `agentOut` now
     includes `role_overrides` (see Decision 5), matching this repo's own
     precedent that reading/listing is `swarm_read`'s job, not a mutation
     tool's (`itemsTool`'s own doc comment makes the same call for
     `swarm_items`).
   - No `clear-all`/bulk op: not requested, and `clear` one-at-a-time already
     lets an orchestrator undo a mistake without touching roles it set
     intentionally (the exact "don't clobber others it set on purpose"
     concern named in the request).

3. **Validation reuses `settings.Store`'s existing rule, not a reimplementation.**
   `settings.Store.validateDefault` (the same method `Put`'s `validate` calls
   for every `Settings.Roles[role]` entry) is exported by rename to
   `ValidateDefault` — its only call sites in the whole repo were the two
   internal ones inside `settings.go` (confirmed by grep), so the rename is
   the entire change needed to reuse it from `internal/runtime`. Called as
   `ValidateDefault(ctx, oldRD, rd, cfg.EnabledAgents, nil)`: `oldRD` is the
   role's current override (zero value if unset, matching how a slot that
   never had a value behaves elsewhere), `extra` is `nil` because Decision 4
   excludes the one role (`advisor`) that ever needed the `extra` hook
   (AdvisorCapable).

4. **Settable roles: `kinds.SettingsRoles` minus `RoleAdvisor`.**
   `RoleAdvisor` is excluded, not silently accepted-and-ignored, because it is
   provably dead on this path:
   - `kinds.go`'s own comment on the constant: `RoleAdvisor Role = "advisor" //
     Settings only; never an agent row` — it is never the `Role` of a spawned
     agent, so `Spawn`'s resolution loop (which is keyed on `in.Role`, the
     *spawned* agent's role) never even looks up
     `parentRoleOverrides[RoleAdvisor]`.
   - The actual advisor default is resolved by `resolveAdvisor`
     (`agents.go:573-603`), which reads `cfg.Roles[RoleAdvisor]` **straight
     off the live global `Settings`** — it takes no parent/orchestrator
     argument at all and never consults `RoleOverrides`.
   - `swarm_spawn`'s own `Advisor` field is accepted but "not yet wired" per
     its own comment in `internal/mcpserver/orchestrator.go` — a second,
     independent confirmation that nothing downstream of a spawn call ever
     threads an orchestrator's advisor override anywhere.

   A `RoleAdvisor` entry in `role_overrides` would therefore silently do
   nothing forever — indistinguishable from a bug to whoever set it. Refusing
   it with a clear error is one `slices.Contains` check
   (`OverridableRoles = kinds.SettingsRoles` minus `RoleAdvisor`, in
   `internal/runtime`) against accepting a permanently inert write.

   `RoleOrchestrator` **is** included: `Spawn`'s resolution block is
   role-agnostic, and spawning a sub-orchestrator via `swarm_spawn` is a real,
   already-supported path — `agents.go:664-671`'s own "This item already has
   an orchestrator" conflict check exists specifically because
   `Spawn(Role: RoleOrchestrator, ...)` is a real call shape (used for spike
   sub-orchestrators). An orchestrator overriding its own `orchestrator` role
   default changes what kind/model a *future* sub-orchestrator it spawns
   gets. Note this does **not** cascade: a newly spawned sub-orchestrator's
   own `role_overrides` column is written as `NULL` by `Spawn`'s INSERT
   (`agents.go`, the `role_overrides` column of the `swarm_spawn` INSERT is a
   literal `nil`, unlike `StartOrchestrator`'s INSERT, which threads
   `in.Roles`) — a sub-orchestrator starts with no inherited overrides of its
   own and must set any it wants via this same tool.

5. **`swarm_read`'s `agentOut` gains `role_overrides`.** Previously it
   returned only `{name, kind, model, role, state, parent}`. A grep of
   `internal/mcpserver/*_test.go` for assertions on this shape found none
   that decode the full map (`TestSwarmReadIncludesParentAgent` and similar
   decode into a struct naming only the fields they check, so Go's
   `json.Unmarshal` into that struct silently ignores the new field) —
   confirmed safe to add unconditionally. A `nil` map is normalized to `{}`
   on the wire (`roleOverridesOut`, shared by both `agentOut` and
   `swarm_role_overrides`' own return value), matching every other
   empty-collection field this package returns (e.g. `readTool`'s
   `out["items"] = []items.Item{}`).

6. **Idempotency and persistence follow existing house patterns.**
   - `runtime.Store.SetRoleOverride(ctx, name string, role Role, rd
     *settings.RoleDefault, sessionID, requestID string) (Agent, error)` is a
     pure-DB mutation (no external side effect like a tmux kill), so unlike
     `swarm_worktree`/`swarm_control`'s two-phase `PeekIdempotent` +
     after-the-fact `IdemTx` pattern, it uses the simpler single-`IdemTx`
     shape `Cancel` already uses for its own pure-DB `UPDATE agents SET
     state = ...`.
   - The read-modify-write of the JSON map happens **inside** the `IdemTx`
     transaction body (`SELECT ... FROM agents WHERE id = ? ` then `UPDATE
     ...`), not before it: `s.tx` is `BEGIN IMMEDIATE`, so this is
     race-free against two concurrent `set`/`clear` calls on different roles
     from the same orchestrator (a read taken before the transaction could
     let one clobber the other).
   - The row is stored as `NULL` when the resulting map is empty (mirrors
     `StartOrchestrator`'s own `rolesJSON any` — `nil` when `len(in.Roles) ==
     0`), not `"{}"`.
   - `s.publishAgentChanged` is called on every successful write (same as
     `Cancel`'s own tx body), so SSE subscribers see the change.

## Non-goals (explicitly out of scope)

- The global `Settings.Roles` mechanism itself — untouched.
- Letting an orchestrator modify another agent's `role_overrides` (parent,
  sibling, or child) — impossible by construction (no such parameter exists).
- A `swarm` CLI surface — MCP tool only, per the original ask.
- Retroactively fixing any specific live orchestrator's stale override row —
  not this task's concern.
- Making `role_overrides` inheritable by a spawned sub-orchestrator — not
  requested, and would be a behavior change to `Spawn`'s own INSERT
  (currently always `NULL`) rather than an addition; noted as a possible
  follow-up, not built here.

## DB / model changes

None. No migration. `agents.role_overrides` already exists (see the original
column in the CREATE TABLE / earlier migration); this only adds a second
write path onto it plus a rename (`settings.Store.validateDefault` →
`ValidateDefault`, purely a visibility change, no signature or behavior
change).

## API

### MCP tool: `swarm_role_overrides`

Available only to `runtime.RoleOrchestrator` (`Roles: orchestratorRole`, the
same restriction every other orchestrator-only tool in
`internal/mcpserver/orchestrator.go` uses).

Request schema:
```json
{
  "op": "set | clear",
  "role": "orchestrator | coder | reviewer | ui_reviewer | researcher | debugger | mechanical",
  "agent": "<AgentKind, required for set>",
  "model": "<model id, required for set>",
  "effort": "<optional, set only>",
  "request_id": "<optional idempotency key>"
}
```

- `set` requires `role`, `agent`, `model`; `effort` optional (`""` = agent
  default, same convention as `settings.RoleDefault.Effort`'s own doc
  comment).
- `clear` requires `role` only; `agent`/`model`/`effort` are ignored.
- `role` outside `OverridableRoles` (i.e. `advisor`, or any unknown string):
  `bad_request: role must be one of orchestrator, coder, reviewer,
  ui_reviewer, researcher, debugger, mechanical`.
- `set` with a disabled agent or unlisted model: the underlying
  `settings.ValidationError`'s own message (e.g. `bad_request: claude isn't
  enabled. Choose an enabled agent.`), matching the `bad_request` code the
  REST API already maps `ValidationError` to in `httpapi/server.go`.

Response (both ops):
```json
{"role_overrides": {"<role>": {"agent": "...", "model": "...", "effort": "..."}, ...}}
```
The full, current map after the write (not just the touched role), so a
caller can confirm state without a follow-up `swarm_read`.

### `swarm_read`'s `agentOut` (existing tool, extended)

Every agent object in `swarm_read`'s `agents` array now also carries:
```json
"role_overrides": {"<role>": {"agent": "...", "model": "...", "effort": "..."}, ...}
```
(`{}` when the agent has none set — never `null`).

## Files touched

- `internal/settings/settings.go` — rename `validateDefault` → `ValidateDefault`
  (export by rename; both internal call sites updated).
- `internal/runtime/agents.go` — add `OverridableRoles`, `joinRoles`, and
  `Store.SetRoleOverride`.
- `internal/mcpserver/orchestrator.go` — add `roleOverridesTool`, register it
  in `orchestratorTools`.
- `internal/mcpserver/tools.go` — add `roleOverridesOut` helper; `agentOut`
  now includes `role_overrides`.
- `skills/swarm-orchestrator/SKILL.md` and
  `internal/install/skills/swarm-orchestrator/SKILL.md` (kept byte-identical,
  as they already were) — one new bullet documenting the tool.
- Tests: `internal/runtime/agents_test.go`, `internal/mcpserver/orchestrator_test.go`,
  `internal/mcpserver/server_test.go` (tool-count fixture updated: 13→14
  orchestrator tools, 14→15 for a spike orchestrator).
- Reused unchanged: `runtime.IdemTx`/`PeekIdempotent`, `settings.Store.Get`,
  `kinds.SettingsRoles`, the existing `agents.role_overrides` column.

## Verification

```
go build ./...
go test ./internal/settings/...
go test ./internal/runtime/...
go test ./internal/mcpserver/...
go test ./internal/install/...
```

All pass as of this writing. `go test ./...` also shows a pre-existing,
unrelated failure in `internal/httpapi` (`TestBoardServedAtRoot`, 503) caused
by `web/dist` being an untracked, `pnpm build`-generated directory that a
fresh `git worktree add` never checks out (confirmed: `web/dist` doesn't
exist in this worktree; `git check-ignore -v web/dist` on the primary
checkout confirms it's gitignored; the Makefile's own `test-go: vet fmt
web-build` target exists specifically to build it first). This is an
environment-setup gap in any fresh worktree, not a regression from this
change — `internal/httpapi` has a zero-line diff against `main` in this
worktree, and the primary checkout's `internal/httpapi` suite passes
unchanged.

End-to-end scenarios covered by tests:
- Set an override, spawn a worker on that role with no explicit
  agent/model: the worker gets the override
  (`TestSetRoleOverrideAppliesAtSpawn`, `TestSwarmRoleOverridesSetThenSpawnUsesIt`).
- Clear a previously-set override: the next spawn on that role falls back to
  the live global default (`TestSetRoleOverrideClearFallsThroughToGlobalDefault`,
  `TestSwarmRoleOverridesClearRemovesTheEntry`) — this is the exact incident
  fix.
- Reject a disabled agent (`TestSetRoleOverrideRejectsDisabledAgent`,
  `TestSwarmRoleOverridesRejectsDisabledAgent`).
- Reject the `advisor` role (`TestSetRoleOverrideRejectsUnoverridableRole`,
  `TestSwarmRoleOverridesRejectsAdvisorRole`).
- A request_id replay does not double-apply
  (`TestSetRoleOverrideRequestIDReplaysWithoutASecondWrite`).
- Scope proof: one orchestrator's `SetRoleOverride`/`swarm_role_overrides`
  call never touches a second orchestrator's row
  (`TestSetRoleOverrideOnlyEverTouchesTheCallersOwnRow`,
  `TestSwarmRoleOverridesIsScopedToTheCallersOwnRow`).
- `swarm_read` surfaces the current overrides (`TestSwarmReadShowsRoleOverrides`).
