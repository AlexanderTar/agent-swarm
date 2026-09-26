# MCP Top-Level Items — Implementation Plan

Companion to `docs/specs/2026-09-26-mcp-top-level-items.md`. TDD order: failing test → run/watch fail → minimal implementation → run/watch pass → commit. Worktree `agent-swarm-mcp-items`, branch `feat/mcp-top-level-items`. Stage explicit paths only; never `git add -A` / `--amend`.

## Task 1 — `items` package: top-level propose gate

**Files:** `internal/items/store.go`, `internal/items/store_test.go`

No `Actor` field change — `items.Actor` is untouched. `internal/items` only needs to stop refusing *every* orchestrator create with no parent; the child-vs-top-level distinction is enforced one layer up, in `internal/mcpserver` (Task 2), which already resolves `runtime.Agent.ParentAgentID`.

1. Write failing tests in `store_test.go`:
   - `TestOrchestratorCanProposeRoot`: `items.Orchestrator("agt_1", rootID)` creates `Type: Epic` with no parent → succeeds, `Status == Draft`, `SuggestedRepos == repos passed`, `Repos == []`, `OriginSpikeID == rootID`. Repeat for `Bug`, `Chore`, `Spike` (with a valid `SpikeIntent`).
   - `TestProposedRootCannotStartReady`: same actor, `Status: Ready`, no parent → error `"A proposed top-level item starts as Draft. The user starts it."`, `CodeBadRequest`.
   - `TestProposedStoryTaskStillNeedParent`: orchestrator actor, `Type: Story` (and `Task`), no parent → unchanged existing parent-required messages (`allowedParents`/`parentHint` path untouched).
   - Update `TestOrchestratorStaysInItsRoot` (store_test.go ~186): its "top-level err" assertion currently expects `"Orchestrators can only create items inside their own top-level item."` for `items.Orchestrator("agt_1", e1.ID)` creating a new `Epic` with no parent. That call now **succeeds** (this is exactly the new capability) — rewrite the assertion to expect success (Draft epic, `OriginSpikeID == e1.ID`) instead of deleting the test's other assertions (the "outside its root" checks above it stay as-is).
   - Repo-wide check: `grep -rn "Orchestrators can only create items inside their own top-level item" --include=*.go .` (quoted per shell hygiene) to find every other assertion of the old message and update each one (don't delete any test — see CLAUDE.md test discipline).
2. Run `go test ./internal/items/... -run 'TestOrchestratorCanProposeRoot|TestProposedRoot|TestProposedStoryTask|TestOrchestratorStaysInItsRoot' -v`, confirm failures.
3. Implement in `CreateTx` (`store.go`, the `if in.ParentKey == ""` block ~406-410):
   ```go
   if in.ParentKey == "" {
       if hint, ok := parentHint[in.Type]; ok {
           return Item{}, errf(CodeBadRequest, "%s", hint)
       }
       if by.isOrchestrator() {
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
   (Note: this removes the old `"Orchestrators can only create items inside their own top-level item."` refusal entirely — the top-level-only restriction is now enforced by the caller in `internal/mcpserver`, not here.)
4. Run the new/updated tests + full `go test ./internal/items/...`, confirm green.
5. `git add internal/items/store.go internal/items/store_test.go` (plus any other test file the repo-wide grep found) → commit: `feat(items): allow an orchestrator to propose a new top-level item`.

## Task 2 — `mcpserver`: `swarm_items create` child guard, intent, notification

**Files:** `internal/mcpserver/orchestrator.go`, `internal/runtime/materialize.go`, `internal/notify/notify.go`, `internal/notifyrules/notifyrules.go`, matching `_test.go` files (search first for the existing `swarm_items create` test coverage and `internal/notify/notify_test.go`, `internal/runtime/materialize_test.go`).

1. `internal/notifyrules/notifyrules.go`: add `"item.created.spike": {"info", "Spike ready", "{SPIKE-KEY} produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready.", "swarm.item"}`.
2. `internal/notify/notify.go`: failing test first — extend whatever `notify_test.go` coverage exists for the `.bug` trim (search `TrimSuffix|strings.HasPrefix.*item.created` there) to also assert `.chore` and `.spike` collapse to the stored kind `item.created`. Then implement: replace `kind := strings.TrimSuffix(in.Kind, ".bug")` with
   ```go
   kind := in.Kind
   if strings.HasPrefix(kind, "item.created.") {
       kind = "item.created"
   }
   ```
   Run `go test ./internal/notify/...`.
3. `internal/runtime/materialize.go`: rename `notifyItemCreated` → exported `NotifyItemCreated(ctx, tx, originKey, rootKey, rootTitle string, rootType items.Type)`, drop the `spike items.Item` param in favor of `originKey string`, add `case items.Spike: kind = "item.created.spike"`. Update `Materialize`'s one call site to pass `spike.Key`. `grep -rn "notifyItemCreated" --include=*.go .` first to catch every reference (tests included).
4. Write failing tests for the mcpserver `create` path (find the existing test file — likely `internal/mcpserver/orchestrator_test.go` or similar, search `swarm_items.*create` first):
   - Top-level orchestrator, `op:"create"`, no `parent`, `type:"epic"` → succeeds, notification raised with `SPIKE-KEY` = caller's own root key, `ROOT-KEY` = new item's key, kind `item.created`. Repeat for `bug`→`.bug`, `chore`→`.chore`, `spike`→`.spike` (with `intent`).
   - Child orchestrator (an `Agent` whose `ParentAgentID != ""`), same call → refused: `"Only a top-level orchestrator can propose a top-level item. Relay it to your parent."`, no item created, no notification raised.
   - `intent` on the wire lands in the created spike's `SpikeIntent` (assert via `swarm_read`/`Items.Get`).
   - The proposed item cannot be moved to Ready by the same orchestrator (`swarm_items update status:"ready"` on the new key from the *same* actor → refused by `orchestratorScope`, since the new item's root is itself and the caller's root differs — confirm this is naturally already true, or note why not, before asserting it).
5. Run, confirm failures (compile errors from the `materialize.go` rename count as failing — fix all call sites before the behavioral tests can even run).
6. Implement in `orchestrator.go`:
   - Add `Intent string \`json:"intent"\`` to the `swarm_items` input struct, and `"intent":{"type":"string"}` to the tool schema.
   - In `case "create":`, before the `IdemTx` call: `if in.Parent == "" && a.ParentAgentID != "" { return nil, &items.Error{Code: items.CodeBadRequest, Message: "Only a top-level orchestrator can propose a top-level item. Relay it to your parent."} }`.
   - Pass `SpikeIntent: in.Intent` into `CreateInput`.
   - Inside the same `IdemTx` closure, after `CreateTx` succeeds, when `in.Parent == ""`: `var originKey string; if err := tx.QueryRowContext(ctx, "SELECT key FROM items WHERE id = ?", a.RootItemID).Scan(&originKey); err != nil { return err }` then `if err := s.RT.NotifyItemCreated(ctx, tx, originKey, out.Key, out.Title, out.Type); err != nil { return err }`. (Use `tx`, not `s.RT.DB` / `rootKeyFor` — SQLite is single-writer and the caller's root row, while already committed from an earlier call, must be read through the same tx as the write for consistency with the rest of this codebase's convention.)
   - Update the tool `Description` to mention proposing a top-level item.
7. Run the new/updated tests, `go build ./...` (catches any other reference to the old unexported `notifyItemCreated` name), then full `go test ./internal/mcpserver/... ./internal/runtime/... ./internal/notify/... ./internal/notifyrules/...`.
8. Stage exact files, commit: `feat(mcpserver): swarm_items create can propose a top-level item, with notification`.

## Task 3 — `swarm_workflow start` promotes Draft

**Files:** `internal/mcpserver/workflow.go`, `internal/mcpserver/*_test.go` (whichever covers `swarm_workflow start` today — search first).

`promoteDraft` (orchestrator.go ~53) already does exactly this for `swarm_spawn`; reuse it verbatim rather than duplicating logic in `internal/runtime`.

1. Write a failing test: a Draft story with a Draft task carrying a workflow; `swarm_workflow start` on the task promotes both to Ready before starting (assert via `swarm_read`/`Items.Get` after the call).
2. Run, confirm the task is observed still `draft` after start (or that start fails some other way — check current behavior first; `StartWorkflow`, `internal/runtime/workflow.go:199`, has no explicit status gate today, so most likely it silently starts a workflow on a Draft item without promoting it).
3. Implement in `workflow.go`'s `case "start":`, right after resolving `orch` and before calling `s.RT.StartWorkflow`:
   ```go
   it, err := s.RT.Items.Get(ctx, in.Item)
   if err != nil {
       return nil, err
   }
   if err := promoteDraft(ctx, s, it, items.Orchestrator(orch.ID, orch.RootItemID)); err != nil {
       return nil, err
   }
   ```
   (`promoteDraft` only acts on `items.Task`; harmless no-op for a story-level start.)
4. Run the new test, then the full `internal/mcpserver` suite.
5. Stage, commit: `feat(mcpserver): swarm_workflow start promotes a Draft task and its Draft story`.

## Task 4 — skill docs

**Files:** `skills/swarm-orchestrator/SKILL.md`

1. Add a bullet to the "Owning an item" section: proposing a new top-level item with `swarm_items create` (no `parent`, `type: epic|bug|chore|spike`, `intent` required for spikes) is only available to a top-level orchestrator (a child orchestrator is refused and told to relay to its parent); it always lands Draft regardless of a passed `status`; the user starts it; any `repos` passed become `suggested_repos` only, never confirmed.
2. `make skills-sync`, confirm clean diff.
3. Stage, commit: `docs(swarm-orchestrator): document proposing a top-level item`.

## Task 5 — full verification pass

1. `go test ./... -count=1`
2. `go vet ./...`
3. `gofmt -l .`
4. `(cd apps/menubar && swift test)` — no menubar source change expected (Task 2's kind-trim fix means the existing `case "item.created":` in `Notifier.category(forKind:)` already covers `.bug`/`.chore`/`.spike`); this run is confirmation, not implementation.
5. `make skills-sync` (clean diff)
6. If `TestBoardServedAtRoot` 503s: `make web-build`, retry.
7. Fix anything red; do not commit until all pass. Final commit (if any cleanup needed) or confirm Task 1-4 commits already leave the tree clean.
