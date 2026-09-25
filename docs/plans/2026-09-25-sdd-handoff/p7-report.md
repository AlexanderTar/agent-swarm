# P7 report — Roles, guards and item fields

Worktree: `/Users/alexandertar/GitHub/agent-swarm--p7`, branch `pkg/p7`.
Commits (oldest first):
- `d4948a7` feat(roles): add the designer role
- `224fd45` feat(hook): block the native Workflow tool in swarm sessions
- `01740cd` feat(items): add workflow/steps/units/solo/verify to tasks
- `79e34e4` feat(mcp,http): wire workflow/steps/units/solo/verify onto the wire
- `405543d` test(items): cover the update path for workflow fields

Hot-files drift check (Global Constraints: "pull or rebase before starting P7–P10"; `agents.go`
is on the hot list and this package touches it): `git fetch origin && git log --oneline
HEAD..origin/main` — empty. `origin/main` is an ancestor of this branch's `HEAD`; no rebase
needed.

## Unit 7.1 — Designer role plumbing (Go)

**Built:** `kinds.RoleDesigner`, added to `kinds.SettingsRoles`; `runtime.RoleDesigner` alias;
`runtime.OverridableRoles` includes it; `runtime.titles.go` `roleEmoji[RoleDesigner] = "🎨"`;
`settings.roleDefaults[RoleDesigner] = {Claude, "opus", ""}`. Not added to `checkpoint.go`'s
`gatedRoles` (designer never has a TDD gate, per spec A5). No role-enum validation added to
`Spawn` — that's out of scope (B7/a later package); designer just passes today's DB `CHECK`
(migration 0010, already merged) like every other role string.

**TDD:**
- RED: `go test ./internal/settings/... -run TestSettingsDefaultsIncludeDesigner` →
  `undefined: kinds.RoleDesigner` (compile failure). `go test ./internal/runtime/... -run
  'TestSpawnDesignerRoleAccepted|TestOverridableRolesIncludeDesigner'` → same, `undefined:
  RoleDesigner`.
- GREEN: both pass after implementing; `go test ./internal/settings/... ./internal/kinds/...
  ./internal/runtime/...` all pass.

**Existing test updated (intentional, named by the unit):** `internal/settings/settings_test.go`
`TestDefaults` — `len(d.Roles)` 8→9 and `want` gains the designer entry, since defaults now
include a ninth role.

**Files:** `internal/kinds/kinds.go`, `internal/runtime/{types.go,agents.go,titles.go}`,
`internal/settings/settings.go`, `internal/settings/settings_test.go`,
`internal/runtime/agents_test.go`.

## Unit 7.2 — Block the native Workflow tool

**Built:** `internal/hook/handler.go`'s `PreToolUse` branch denies `tool_name` `"Workflow"` or
`"workflow"` with the spec A6 copy verbatim: `"[swarm] The Workflow tool is disabled in Swarm
sessions. Use swarm_spawn or swarm_workflow."` — a separate `if` before the native-fork block
(distinct reason string, not folded into `isNativeFork`). `skills/swarm/SKILL.md` rule 5 gained
a bullet alongside the existing native-fork-tools one; `make skills-sync` ran and the mirror
under `internal/install/skills/swarm/SKILL.md` is committed in the same commit.

**TDD:**
- RED: `go test ./internal/hook/... -run
  'TestPreToolUseBlocksWorkflowTool|TestWorkflowToolAllowedOutsideSwarm' -v` →
  `TestPreToolUseBlocksWorkflowTool` failed (`unexpected end of JSON input`, i.e. no block
  emitted yet); `TestWorkflowToolAllowedOutsideSwarm` was already green — it exercises a
  session id with no DB row, and `Handle`'s existing `h.load` → `sql.ErrNoRows` → `nil, nil`
  path already makes any tool a no-op outside a session Swarm manages. Noted as a pre-existing
  regression guard rather than forced into failing first.
- GREEN: both pass after adding the block.

**Files:** `internal/hook/handler.go`, `internal/hook/handler_test.go`,
`skills/swarm/SKILL.md`, `internal/install/skills/swarm/SKILL.md`.
Verify: `go test ./internal/hook/... ./internal/install/...` pass.

## Unit 7.3 — Item fields in the store

**Built:**
- `items.Unit{Title, Steps}` (matches swarm-tree `TreeUnit`/spec C1 exactly).
- `items.Item`, `CreateInput`, `Patch` gain `Workflow *workflow.Spec`, `Steps []string`,
  `Units []Unit`, `Solo string`, `Verify []string`. `Patch.Workflow` is a single pointer
  (present = "set to this"), matching how `Item.Workflow` is already optional; `Steps`/`Units`/
  `Verify` are `*[]T` like the existing `Acceptance *[]string`; `Solo` is `*string` like
  `TddExempt`.
- `itemCols`/`scanItem` read the new `workflow_json`/`steps_json`/`units_json`/`solo`/
  `verify_json` columns (migration 0011, already merged); `steps_json`/`units_json`/
  `verify_json` are `COALESCE`d to `'[]'` so a legacy row (`workflow_json IS NULL`) still scans
  clean, non-nil slices.
- `CreateTx`/`UpdateTx`: validate the given `workflow.Spec` (`workflow.Validate` by level —
  `workflowLevel(t)` maps `Story→LevelStory`, `Epic|Bug→LevelRoot`, everything else (task, and
  spike/chore, unspecified by the spec) `→LevelTask`), resolve it (`workflow.Resolve`, dropping
  the TDD gate when the item is `tdd_exempt`), store the **resolved** spec, and set
  `role_hint = workflow.RunRole(resolved)` — overwriting any caller-given `role_hint`, per spec
  B3's literal wording ("CreateTx runs workflow.Resolve ... and sets role_hint").
- `validateStepsUnits`: a task can't have both `steps` and `units`, and `units` is capped at 8.
- `workflowFieldsPermitted`: only an orchestrator or the daemon may set
  workflow/steps/units/solo/verify.
- **Locked decision 15**: an orchestrator-created `Task` with no `Workflow` is refused; a
  board-created (`items.User`) or daemon-materialized task may still omit it. The check sits
  *after* the parent-resolution block (so `TestOrchestratorStaysInItsRoot`'s
  `"STORY-1 is outside EPIC-1."` still fires first for that fixture) and is gated to
  `in.Type == Task && by.isOrchestrator()`.

**Copy — spec-given vs. judgment calls (both discussed with the advisor before writing tests):**
- `"Task %s has no workflow. Plans assign every role: pick a template or write steps."` and
  `"Task %s has both steps and units; use one."` / `"Task %s has %d units (max 8)."` reuse the
  spec's "Batching/role copy" group verbatim (`<ref>` in the spec is a layer-relative
  placeholder, same convention as `<KEY>` in "Start errors"). **Judgment call:** on `Create`
  there is no key yet (`ids.NextKey` runs after validation, deliberately not moved earlier to
  avoid naming a key that never commits) — the trimmed `Title` substitutes for `<ref>`; on
  `Update`, `it.Key` substitutes.
- Invalid-workflow errors pass `workflow.Validate`'s own message through unwrapped
  (`errf(CodeBadRequest, "%s", err)`) — those strings are themselves spec copy (B2's validation
  rules section).
- **Judgment call, no spec copy found for this case:** the "only an orchestrator or the daemon"
  refusal — `"Only an orchestrator or a plan can set workflow, steps, units, solo or verify."` —
  mirrors `tdd_exempt`'s existing message exactly ("a plan" = the daemon materializing, per that
  precedent). Flagged rather than escalated to NEEDS_CONTEXT since it's the "small, clearly-
  scoped judgment call" the contract allows, not a load-bearing behavioural ambiguity.
- On `Update`, the permission check reads `p.Workflow != nil` etc. (what *this patch* asks to
  set) rather than the merged `it.*` state that `tdd_exempt`'s own adjacent check uses — a
  deliberate divergence from that precedent, noted inline in the code: the only real caller
  (`swarm_items`) is already orchestrator-gated at the MCP layer, but a plain board edit of an
  already-workflowed task's *title* must not spuriously trip a workflow-permission error just
  because the task already carries one.

**TDD:**
- RED: `go test ./internal/items/...` → compile failures (`in.Workflow undefined`,
  `unknown field Workflow`, `undefined: items.Unit`, etc.) for all six new tests.
- GREEN: `go test ./internal/items/... -v -run
  'TestCreateTaskStoresResolvedWorkflowAndRoleHint|TestCreateRejectsInvalidWorkflow|
  TestCreateRejectsStepsAndUnits|TestOrchestratorTaskNeedsWorkflow|
  TestBoardTaskMayOmitWorkflow|TestUserCannotSetWorkflow'` — all 6 PASS. Full
  `go test ./internal/items/...` — PASS.

**Existing test updated (intentional, named by the unit):**
`internal/items/store_test.go` `TestTddExemptRules` — the orchestrator-actor loop now sets
`in.Workflow = &workflow.Spec{Template: "tdd-reviewed"}` before iterating `TddExempt` values,
since an orchestrator-created task now requires one; the earlier `user`-actor assertion (which
must keep getting the tdd_exempt-specific message) runs *before* `Workflow` is added to `in`, so
ordering between the two new checks and the pre-existing `tdd_exempt` checks was not an issue
there. `TestOrchestratorStaysInItsRoot` and `TestUpdateTddExempt` needed no changes (Daemon-
actor `mk()` fixtures, or the out-of-root error firing first by design, as above).

**Fix round (post-report advisor pass):** the Update path (`Patch.Workflow`/`Steps`/`Units`/
`Solo`/`Verify` merge, resolve, and the deliberately-diverging permission check) had no direct
test. Added `TestUpdateSetsWorkflowAndKeepsItOnBoardEdits` (commit `405543d`): sets `Workflow`
via `Patch` on an existing daemon-created task and confirms the resolved spec and derived
`role_hint` survive the post-update `getByID` re-read (this is exactly the class of bug where
`role_hint` would've been silently dropped had it been missing from the `UPDATE`'s own `SET`
clause — it isn't, but now it's locked in), then does a board (`user`-actor) title-only edit on
that same now-workflowed task and confirms it succeeds without tripping the set-workflow-fields
permission check. Green on first run, as expected — it documents behaviour already built rather
than driving new implementation.

**Known cross-package ripple (fixed in Unit 7.4, not here):** five `internal/mcpserver`
fixtures create an orchestrator task via `swarm_items` without a `workflow`; until Unit 7.4
wires `workflow` onto `swarm_items`, those five tests failed. `internal/mcpserver` is not in
Unit 7.3's file list, so this was left for 7.4 rather than reaching into mcpserver mid-unit.

**Files:** `internal/items/{model.go,store.go,store_test.go}`.

## Unit 7.4 — Wire the fields through MCP and HTTP

**Built:**
- `swarm_items` (`internal/mcpserver/orchestrator.go`): `create`/`update` decode `workflow`,
  `steps`, `units`, `solo`, `verify` and pass them to `items.CreateInput`/`Patch` (schema gained
  the matching properties).
- `swarm_read`: needed **no code change** — it already appends raw `items.Item` values and
  returns them; once `items.Item` carries the new JSON-tagged fields, they come along for free.
  Confirmed via `TestSwarmReadReturnsWorkflowFields`.
- `GET /api/items/:key` (`internal/httpapi/items.go`): `itemWire` gained shadow fields
  `Workflow *workflow.Spec` and `Solo *string` (populated via the existing `orNull` pattern) so
  an unset workflow/solo serializes as explicit `null` rather than being omitted; `orEmpty` was
  made generic (`orEmpty[T any]`) and is now also applied to `Steps`/`Units`/`Verify` so they're
  never-null arrays, matching the `acceptance`/`repos` convention (contracts §3.1). On
  `items.Item` itself, `Steps`/`Units`/`Verify`'s json tags dropped `omitempty` (mirroring
  `Acceptance`'s un-tagged convention) — with `omitempty`, an empty-but-non-nil slice would
  still have been *omitted* from `swarm_read`'s raw output, not emitted as `[]`.
- `web/src/types.ts`: added `Workflow`/`WorkflowStep`/`WorkflowLoop`/`WorkflowIntegration`/
  `Unit` types (mirroring `internal/workflow.Spec`'s JSON shape) and the five new `Item` fields,
  all optional (`?:`) so `web/src/mock/fixtures.ts` needed no changes, per the brief's "Item
  fields only" scoping.

**TDD:**
- RED: `go test ./internal/mcpserver/... -run
  'TestSwarmItemsAcceptsWorkflowUnitsVerify|TestSwarmReadReturnsWorkflowFields'` →
  both failed with `"Task ... has no workflow..."` (the field wasn't parsed yet, so `CreateTx`
  saw `Workflow: nil`). `go test ./internal/httpapi/... -run TestItemJSONIncludesWorkflowFields`
  → failed (`workflow`/`solo` absent instead of null; `steps`/`units`/`verify` absent instead of
  `[]`).
- GREEN: all three pass after implementing. Same run also fixed the five pre-existing
  `internal/mcpserver` fixtures flagged in Unit 7.3 (added `"workflow":{"template":
  "tdd-reviewed"}` to each body): `TestItemsIsScopedToTheCallersRoot`,
  `TestItemsCreateRequestIDReplaysInsteadOfCreatingTwice`,
  `TestItemsCreateWithoutOrDistinctRequestIDsEachCreate`,
  `TestSpawnOnReadyTaskPromotesDraftParentStory`, `TestItemsCreateStatusReady`.
  `TestItemsCreateRejectsInProgressStatus` needed no change: its `in_progress`-status
  validation fires before the workflow-requiredness check, so its (unrelated) expected error
  still fires first.

**Files:** `internal/mcpserver/{orchestrator.go,orchestrator_test.go}`,
`internal/httpapi/{items.go,items_test.go}`, `web/src/types.ts`.

## Verify (brief's set, run at the end of P7)

```
go test ./internal/kinds/... ./internal/settings/... ./internal/hook/... ./internal/items/... \
  ./internal/mcpserver/... ./internal/httpapi/... ./internal/runtime/...
```
Result: every package `ok` except `internal/httpapi`, which has exactly the one documented
pre-existing baseline failure (`TestBoardServedAtRoot`, web bundle not built) — the same failure
named in the implementer contract's Safety note, not caused by this package.

```
go build ./... && go vet ./...
```
Clean, no output.

```
cd web && pnpm typecheck && pnpm test
```
`pnpm typecheck`: clean. `pnpm test`: 48 files / 416 tests passed, 1 file / 10 tests skipped
(pre-existing skips, unrelated to this change).

`go test ./internal/install/...` (skills-sync mirror check, from Unit 7.2's `skills/` edit):
pass.

Did **not** run `go test ./...` or `go test ./cmd/...`, per the brief's Safety note.

## Self-review notes / judgment calls (summary)

1. `workflowLevel(t Type)` defaults Spike/Chore to `LevelTask` — the spec's `Level` doc comment
   only names task/story/root ("epics and bugs"), leaving Spike/Chore unspecified. Untested in
   this package (no acceptance criterion exercises it); low risk since nothing in P7 sets
   `Workflow` on a Spike/Chore item.
2. The `Create`-time identifier substituted for the spec's `<ref>` placeholder is the trimmed
   title (no key exists yet at validation time); `Update` uses the item's `Key`.
3. `"Only an orchestrator or a plan can set workflow, steps, units, solo or verify."` has no
   spec-given copy; mirrors `tdd_exempt`'s existing refusal text.
4. `Update`'s permission check for these five fields is based on *patch presence*
   (`p.Workflow != nil`, etc.), not merged item state — a deliberate, noted divergence from
   `tdd_exempt`'s own (merged-state) check on the same code path, to avoid a plain board title
   edit tripping a workflow-permission refusal on a task that already has one.
5. `Patch.Workflow` has no "explicit clear" representation (a single `*workflow.Spec`, matching
   `Item.Workflow`'s own optionality) — nothing in P7's acceptance criteria needs clearing a
   task's workflow via `swarm_items update`, so this wasn't built.

## Concerns

None blocking.

- The mcpserver fixture fixes are cross-unit (7.3 → 7.4) rather than self-contained within 7.3's
  own file list; documented above and in the 7.3 commit message rather than silently folded in.
- If `tdd_exempt` is set via `swarm_items update` on a task whose `workflow` was resolved
  earlier (in a prior call), the already-stored resolved spec still carries the `tdd` gate —
  `Resolve` (which drops that gate when exempt) only runs when `Workflow` itself is part of the
  same patch, not merely when `TddExempt` changes. This is not a behavioural bug: spec B5's gate
  check independently skips the `tdd` gate whenever the item is `tdd_exempt`, regardless of
  what the stored spec's `gates` list says (that check is out of this package's scope —
  checkpoint/gate consumption is a later package). Flagging it here so a reviewer doesn't need
  to re-derive that it's already covered.

## Fix round 1 (Opus review: Approved, spec-compliant; controller-folded load-bearing minors
for P8/P9)

Findings source:
`/private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/sdd/p7-fix1-findings.md`.
Deferred items (role_hint mismatch refusal, error title quoting, helper consolidation, web
optional fields) were explicitly out of scope for this round and not touched.

New commits (oldest first):
- `f34416a` fix(items): restrict steps/units/solo/verify to tasks, validate units
- `e1c7762` docs(spec): record P7's item-store copy in All user-facing copy

### Finding 1 — steps/units/solo/verify are task-only

**Built:** `onlyTasksSet(t Type, steps, units, solo, verify)` in `internal/items/store.go`,
called from both `CreateTx` and `UpdateTx` (on the merged `it.*` state for Update) right after
the existing `workflowFieldsPermitted` check. Refuses any of the four fields on a non-`Task`
item with `"Only tasks can set steps, units, solo or verify."` A story/root may still carry its
own level of `Workflow` (its own `after_tasks`/`integration` shape, via the pre-existing
`workflowLevel`/`workflow.Validate` path) — only the task-execution-script fields are blocked.

**TDD:**
- RED: `go test ./internal/items/... -run TestOnlyTasksCanSetStepsUnitsSoloVerify -v` →
  `steps on story: err = <nil>, want "Only tasks can set steps, units, solo or verify."`
- GREEN: same command → PASS. New test covers `Steps` and `Verify` on `Create` (Story) and
  `Solo` on `Update` (Story).

### Finding 2 — malformed units

**Built:** `validateStepsUnits` (same function, extended) now also rejects a unit whose title is
empty/whitespace-only, has zero steps, or has any empty/whitespace-only step string, with
`"Unit %d needs a title and at least one step."` (1-based index).

**TDD:**
- RED: `go test ./internal/items/... -run TestCreateRejectsMalformedUnits -v` →
  `empty title: err = <nil>, want "Unit 1 needs a title and at least one step."`
- GREEN: same command → PASS. New test covers empty title, nil steps, whitespace-only step, and
  that the index in the message tracks the actual offending unit (`"Unit 2 ..."` for a second,
  malformed unit after a valid first one).

### Finding 3 — spec copy

**Built:** `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md`'s "All user-facing
copy" section, "Batching/role copy" bullet, gained `"Unit <n> needs a title and at least one
step."` and `"Only tasks can set steps, units, solo or verify."`; a new bullet right after it
records `"Only an orchestrator or a plan can set workflow, steps, units, solo or verify."`
(items.Store's own judgment call from Unit 7.3, parallel to `tdd_exempt`'s). No code change;
separate `docs(spec):` commit per the controller's instruction.

### Finding 4 — coverage gaps

**Built:** two new tests, no production code change (both were already green on first run,
confirming behaviour already shipped in Units 7.3/7.4 rather than driving new implementation):
- `TestUpdateWorkflowRefusedForUser`: a `user`-actor `Patch{Workflow: ...}` on `UpdateTx` is
  refused with the same permission copy `CreateTx` uses.
- `TestCreateTddExemptWorkflowDropsTddGate`: creating a task with `TddExempt: "docs"` and
  `Workflow: {Template: "tdd-reviewed"}` stores a resolved spec whose steps carry no
  `workflow.GateTDD` (confirms `resolveWorkflow` correctly threads `in.TddExempt != ""` into
  `workflow.Resolve`'s `tddExempt` param).

**TDD:** both ran green immediately (`go test ./internal/items/... -run
'TestUpdateWorkflowRefusedForUser|TestCreateTddExemptWorkflowDropsTddGate' -v` → `PASS`), which
is the expected outcome for a pure-coverage addition — reported as such rather than manufactured
as a RED/GREEN pair.

### Fix round 1 Verify

```
go test ./internal/items/... ./internal/mcpserver/... ./internal/httpapi/...
```
`internal/items` and `internal/mcpserver`: `ok`. `internal/httpapi`: only the same documented
pre-existing baseline failure (`TestBoardServedAtRoot`).

```
go build ./... && go vet ./...
```
Clean, no output.

Did **not** run `go test ./...` or `go test ./cmd/...`, per the coordinator's explicit
instruction for this round and the original Safety note.

Working tree clean after both commits; no other files touched.
