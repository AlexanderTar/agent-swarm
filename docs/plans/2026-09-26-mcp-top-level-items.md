# MCP Top-Level Items — Implementation Plan

Companion to `docs/specs/2026-09-26-mcp-top-level-items.md`. TDD order: failing test → run/watch fail → minimal implementation → run/watch pass → commit. Worktree `agent-swarm-mcp-items`, branch `feat/mcp-top-level-items`. Stage explicit paths only; never `git add -A` / `--amend`.

## Task 1 — `items` package: top-level propose gate

**Files:** `internal/items/model.go`, `internal/items/store.go`, `internal/items/store_test.go`

1. Add `ParentAgentID string` to `Actor` (model.go). No constructor change (default `""`).
2. Write failing tests in `store_test.go`:
   - `TestTopLevelOrchestratorCanProposeRoot`: `items.Orchestrator("agt_1", rootID)` with `ParentAgentID: ""` creates `Type: Epic` with no parent → succeeds, `Status == Draft`, `SuggestedRepos == repos passed`, `Repos == []`, `OriginSpikeID == rootID`.
   - `TestChildOrchestratorCannotProposeRoot`: same actor but `ParentAgentID: "agt_parent"` → error `"Only a top-level orchestrator can propose a top-level item. Relay it to your parent."`, `CodeBadRequest`.
   - `TestProposedRootCannotStartReady`: top-level orchestrator, `Status: Ready`, no parent → error `"A proposed top-level item starts as Draft. The user starts it."`.
   - `TestProposedStoryTaskStillNeedParent`: top-level orchestrator, `Type: Story` (and `Task`), no parent → unchanged existing parent-required messages, for both a top-level and a child orchestrator actor.
   - Extend `TestOrchestratorStaysInItsRoot`'s "top-level err" assertion is now stale (an orchestrator *can* create top-level now) — update it, don't delete it: use a child-orchestrator actor there instead so it still asserts the child-relay refusal, or move that specific assertion into the new `TestChildOrchestratorCannotProposeRoot`. Keep the rest of that test (staying in its own root for a *child* create) intact.
3. Run `go test ./internal/items/... -run TestTopLevel -v` etc, confirm failures.
4. Implement in `CreateTx` (`store.go`, the `if in.ParentKey == ""` block ~406-410):
   ```go
   if in.ParentKey == "" {
       if hint, ok := parentHint[in.Type]; ok {
           return Item{}, errf(CodeBadRequest, "%s", hint)
       }
       if by.isOrchestrator() {
           if by.ParentAgentID != "" {
               return Item{}, errf(CodeBadRequest, "Only a top-level orchestrator can propose a top-level item. Relay it to your parent.")
           }
           if in.Status == Ready {
               return Item{}, errf(CodeBadRequest, "A proposed top-level item starts as Draft. The user starts it.")
           }
           in.Status = Draft
           if len(in.Repos) > 0 {
               in.SuggestedRepos = append(append([]string{}, in.SuggestedRepos...), in.Repos...)
               in.Repos = nil
           }
           if in.OriginSpikeID == "" {
               in.OriginSpikeID = by.RootID
           }
       }
   }
   ```
5. Run the new tests + full `go test ./internal/items/...`, confirm green.
6. `git add internal/items/model.go internal/items/store.go internal/items/store_test.go` → commit: `feat(items): allow a top-level orchestrator to propose a new root item`.

## Task 2 — `mcpserver`: `swarm_items create` wiring + notification

**Files:** `internal/mcpserver/orchestrator.go`, `internal/runtime/materialize.go`, `internal/mcpserver/orchestrator_test.go` (or existing items test file — check first), `internal/runtime/materialize_test.go`

1. In `materialize.go`: rename `notifyItemCreated` → exported `NotifyItemCreated(ctx, tx, originKey, rootKey, rootTitle string, rootType items.Type)`, drop the `spike items.Item` param in favor of `originKey string`, add `case items.Spike: kind = "item.created.spike"`. Update the one call site in `Materialize` to pass `spike.Key`.
2. Add `item.created.spike` row to `internal/notifyrules/notifyrules.go`: `{"info", "Spike ready", "{SPIKE-KEY} produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready.", "swarm.item"}`.
3. Write failing tests:
   - `internal/runtime/materialize_test.go`: existing bug-kind test still passes unchanged (signature change only internal — no test should reference the old unexported name directly; verify with `grep notifyItemCreated internal/runtime/*_test.go` first).
   - New mcpserver-level test (find the existing test file covering `swarm_items create`, e.g. `internal/mcpserver/*_test.go` — search first) asserting: a top-level orchestrator's `swarm_items create` with no `parent` raises an `item.created`/`.bug`/`.chore`/`.spike` notification (via whatever fake/spy the existing suite uses) with `SPIKE-KEY` = the caller's own root key, `ROOT-KEY` = the new item's key; a child orchestrator's attempt is refused with the relay message; `intent` on the wire lands in the created spike's `SpikeIntent`.
4. Run, confirm failures (compile errors from the rename count as "failing").
5. Implement in `orchestrator.go`:
   - Add `Intent string \`json:"intent"\`` to the `create`/`update` input struct, and `"intent":{"type":"string"}` to the tool schema.
   - Right after `actor := items.Orchestrator(a.ID, a.RootItemID)` (~line 148), add `actor.ParentAgentID = a.ParentAgentID`.
   - In the `case "create":` branch, pass `SpikeIntent: in.Intent` into `CreateInput`.
   - After `CreateTx` succeeds inside the same `IdemTx` closure, when `in.Parent == ""`: look up the caller's root key (`tx.QueryRowContext(ctx, "SELECT key FROM items WHERE id = ?", a.RootItemID).Scan(&originKey)`) and call `s.RT.NotifyItemCreated(ctx, tx, originKey, out.Key, out.Title, out.Type)`.
   - Update the tool `Description` to mention proposing a top-level item.
6. Run the new/updated tests, then `go build ./...` (catches any other reference to the old unexported name), then full `go test ./internal/mcpserver/... ./internal/runtime/...`.
7. Stage exact files, commit: `feat(mcpserver): swarm_items create can propose a top-level item, with notification`.

## Task 3 — `swarm_workflow start` promotes Draft

**Files:** `internal/runtime/workflow.go`, `internal/runtime/workflow_test.go`

1. Write failing test: `TestStartWorkflowPromotesDraftTaskAndStory` — a Draft story with a Draft task carrying a workflow; `StartWorkflow` on the task promotes both to Ready before starting (assert via `s.Items.Get`).
2. Run, confirm fail (item stays Draft / start refuses because of whatever gate currently exists — check first whether `StartWorkflow` even rejects a Draft item explicitly, since the item-status transitions may or may not block it silently).
3. Implement: in `StartWorkflow`, after loading `it` and before the "already has a running workflow" check, if `it.Status == items.Draft`, promote it (and its Draft parent story, if `it.Type == items.Task`) to Ready via `s.Items.TransitionTx` inside... note `StartWorkflow` is not itself inside a tx until the final insert; check whether an existing early `s.Items.Get`/plain (non-tx) `Transition` call is acceptable here (mirrors `promoteDraft`'s own non-tx `Transition` calls) or whether it must happen inside the same tx as the workflow row insert further down — read the rest of the function before deciding, and prefer doing it inside the existing write path if one already opens a tx to keep promotion and workflow-start atomic; otherwise call `s.Items.Transition` before the tx exactly like `promoteDraft` does for spawn, and re-fetch `it` after.
4. Run tests, confirm pass. Run full `internal/runtime` suite.
5. Stage, commit: `feat(runtime): swarm_workflow start promotes a Draft task and its Draft story`.

## Task 4 — menubar notifier + skill

**Files:** `apps/menubar/Sources/SwarmBarKit/Notifier.swift`, `apps/menubar/Tests/SwarmBarTests/NotifierTests.swift`, `skills/swarm-orchestrator/SKILL.md`

1. Add `"item.created.spike": "swarm.item"` to `testEveryKindMapsToItsCategory`'s table (failing).
2. Add `case "item.created", "item.created.spike": return "swarm.item"` in `Notifier.category(forKind:)` (was `case "item.created":` alone).
3. `swift test` (or `cd apps/menubar && swift test`), confirm pass.
4. Add a bullet to `skills/swarm-orchestrator/SKILL.md`'s "Owning an item" section describing: propose a new top-level item with `swarm_items create` (no `parent`, `type: epic|bug|chore|spike`, `intent` for spikes) only from a top-level orchestrator; it always lands Draft; the user starts it; repos passed become suggested only.
5. `make skills-sync`, confirm clean.
6. Stage, commit: `docs(swarm-orchestrator): document proposing a top-level item`.

## Task 5 — full verification pass

1. `go test ./... -count=1`
2. `go vet ./...`
3. `gofmt -l .`
4. `(cd apps/menubar && swift test)`
5. `make skills-sync` (clean diff)
6. If `TestBoardServedAtRoot` 503s: `make web-build`, retry.
7. Fix anything red; do not commit until all pass. Final commit (if any cleanup needed) or confirm Task 1-4 commits already leave the tree clean.
