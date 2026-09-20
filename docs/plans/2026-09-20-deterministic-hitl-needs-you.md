# Deterministic HITL "Needs you" Implementation Plan

## Task 1: Schema Migration & Model Definition
Files to touch:
- `internal/db/schema/0004_hitl_requests.sql`
- `internal/runtime/requests.go`
- `internal/runtime/requests_test.go`

Instructions:
1. Create `internal/db/schema/0004_hitl_requests.sql`:
   ```sql
   ALTER TABLE requests ADD COLUMN is_hitl INTEGER NOT NULL DEFAULT 0;
   UPDATE requests SET is_hitl = 1 WHERE kind IN ('question', 'prompt', 'blocker');
   CREATE INDEX requests_hitl_open ON requests(is_hitl, state, created_at);
   ```
2. In `internal/runtime/requests.go`:
   - Add `IsHITL bool` to `RequestWire` and `Request` structs.
   - Add constants:
     ```go
     const (
         KindPrompt  RequestKind = "prompt"
         KindBlocker RequestKind = "blocker"
     )
     ```
   - Update `requestTx` and `RequestWireTx` to scan `is_hitl`.
   - Update `askQuestion` to set `is_hitl = 1`.
3. Add unit test `TestHITLRequestWire` in `internal/runtime/requests_test.go`.
4. Run `go test -v ./internal/runtime/... -run TestHITLRequestWire`.

## Task 2: Checkpoint Blocker Auto-Request
Files to touch:
- `internal/runtime/checkpoint.go`
- `internal/runtime/checkpoint_test.go`

Instructions:
1. In `internal/runtime/checkpoint.go`:
   - In `WriteCheckpoint`, when `in.Kind == BlockedCkp` and `len(in.Blockers) > 0`:
     - Open a request with `kind: "blocker"`, `is_hitl: 1`, `prompt: in.Summary`, `options: in.Blockers`.
     - Enqueue `events.RequestOpened` and raise notification.
2. In `internal/runtime/checkpoint_test.go`:
   - Add `TestBlockedCheckpointOpensHITLRequest`.
   - Verify that recording a blocked checkpoint with blockers creates a request in `requests` table with `is_hitl = 1`.
3. Run `go test -v ./internal/runtime/... -run TestBlockedCheckpointOpensHITLRequest`.

## Task 3: Question Tool & Permission Hook Interception
Files to touch:
- `internal/hook/handler.go`
- `internal/hook/handler_test.go`

Instructions:
1. In `internal/hook/handler.go`:
   - In `case "PreToolUse":`:
     - Detect native question tools:
       ```go
       isQuestionTool := in.ToolName == "ask_question" ||
           in.ToolName == "AskUserQuestion" ||
           in.ToolName == "request_user_input" ||
           in.ToolName == "experimental_request_user_input"
       ```
     - When `isQuestionTool`:
       - Extract question text and options from `in.ToolInput`.
       - Create an open request with `kind: "question"`, `is_hitl: 1`.
       - Allow the tool to proceed in the agent session (do NOT block).
   - In `case "PermissionRequest":` (Codex hook event):
     - Create an open request with `kind: "prompt"`, `is_hitl: 1`, `prompt: in.Command`.
2. In `internal/hook/handler_test.go`:
   - Add `TestQuestionToolInterceptionCreatesHITLRequest`.
   - Verify calling `ask_question` (AGY) and `AskUserQuestion` (Claude) opens an `is_hitl=1` request.
3. Run `go test -v ./internal/hook/... -run TestQuestionToolInterception`.

## Task 4: Runtime Reconciler Live Prompt Detection
Files to touch:
- `internal/adapter/adapter.go`
- `internal/adapter/claude.go`
- `internal/adapter/codex.go`
- `internal/adapter/agy.go`
- `internal/adapter/cursor.go`
- `internal/runtime/reconcile.go`
- `internal/runtime/reconcile_test.go`

Instructions:
1. In `internal/adapter/adapter.go`:
   - Define `PromptMatcher` struct:
     ```go
     type PromptMatcher struct {
         Match   *regexp.Regexp
         Title   string
         Action  string // e.g. "Enter", "Down+Enter", "y"
     }
     ```
   - Add `PromptPatterns() []PromptMatcher` to `Adapter` interface.
2. Implement `PromptPatterns()` in each adapter:
   - Claude: trust prompt, local dev warning.
   - Codex: directory trust, hook trust.
   - Agy: project trust.
   - Cursor: permission prompt.
3. In `internal/runtime/reconcile.go`:
   - In `resolveAlive`:
     - If not idle, check pane capture against `ad.PromptPatterns()`.
     - If matched and not already recorded:
       - Insert a request with `kind: "prompt"`, `is_hitl: 1`, `prompt: matcher.Title`.
4. In `internal/runtime/reconcile_test.go`:
   - Add `TestPromptDetectedInRunningSessionOpensHITLRequest`.
5. Run `go test -v ./internal/runtime/... -run TestPromptDetected`.

## Task 5: Dedicated MCP Tool `swarm_blocker`
Files to touch:
- `internal/mcpserver/tools.go`
- `internal/mcpserver/tools_test.go`

Instructions:
1. In `internal/mcpserver/tools.go`:
   - Register `swarm_blocker` tool.
   - Takes `reason` (string, required) and `options` ([]string, optional).
   - Calls `s.AskBlocker(ctx, sessionID, reason, options)`.
2. In `internal/mcpserver/tools_test.go`:
   - Add `TestSwarmBlockerOpensHITLRequest`.
3. Run `go test -v ./internal/mcpserver/... -run TestSwarmBlocker`.

## Task 6: Menubar Wire, AppModel, and NeedsYouSection
Files to touch:
- `apps/menubar/Sources/SwarmBarKit/Wire.swift`
- `apps/menubar/Sources/SwarmBarKit/AppModel.swift`
- `apps/menubar/Sources/SwarmBarUI/Popover/NeedsYouSection.swift`
- `apps/menubar/Tests/SwarmBarTests/AppModelTests.swift`

Instructions:
1. In `Wire.swift`:
   - Add `isHITL: Bool = false` to `SwarmRequest`.
   - Update `RequestKind` enum to include `case prompt, blocker`.
2. In `AppModel.swift`:
   - Update `openRequests`:
     ```swift
     public var openRequests: [SwarmRequest] {
         state.requests.filter { $0.state == "open" && $0.isHITL }.sorted { $0.createdAt < $1.createdAt }
     }
     ```
3. In `NeedsYouSection.swift`:
   - Handle `.prompt` and `.blocker` rows with distinct icons and unblock actions.
4. Run `cd apps/menubar && swift test`.

## Task 7: Web Board NeedsYou Filter
Files to touch:
- `web/src/types.ts`
- `web/src/logic/inbox.ts`
- `web/src/panels/NeedsYou.tsx`
- `web/src/logic/inbox.test.ts`

Instructions:
1. In `web/src/types.ts`:
   - Add `is_hitl: boolean` to `Request`.
   - Add `prompt` and `blocker` to `RequestKind`.
2. In `web/src/logic/inbox.ts`:
   - Update `filterRequests` to filter `is_hitl === true` on default "all" / "questions" views, and provide a "reviews" filter for non-HITL requests.
3. In `web/src/panels/NeedsYou.tsx`:
   - Add "reviews" to Segmented filter options.
4. Run `cd web && pnpm test`.
