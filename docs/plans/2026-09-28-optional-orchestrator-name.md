# Plan: optional Name in the New orchestrator dialog

Companion to docs/specs/2026-09-28-optional-orchestrator-name.md. Each task: failing test,
watch it fail, minimal implementation, pass, commit. Stage explicit paths only.

## Task 1 — migration 0018 title_pending

- `internal/db/schema/0018_title_pending.sql`: `ALTER TABLE items ADD COLUMN title_pending
  INTEGER NOT NULL DEFAULT 0;`
- `internal/db/schema_0018_title_pending_test.go` (pattern of `schema_0017_advisor_effort_test.go`):
  seed an item at version 17 via `openFixtureAtVersion`, migrate to 18 with
  `continueMigratingTo`, assert `title_pending` defaults to 0 for an existing row.
- `internal/db/db.go`: `SchemaVersion = 18`.
- Verify: `go test ./internal/db/...`.

## Task 2 — items.Item / CreateInput TitlePending

- `internal/items/store.go`: add `title_pending` to `itemCols`/`scanItem` (scan into an `int`,
  set `it.TitlePending = n != 0`, mirroring the `Waiting` bool pattern in `internal/runtime`);
  `CreateInput.TitlePending bool`; `CreateTx`'s INSERT gains the column.
- `internal/items/model.go`: `Item.TitlePending bool \`json:"title_pending,omitempty"\``.
- Test in `internal/items/store_test.go`: `Create` with `TitlePending: true` round-trips
  `Item.TitlePending == true`; default `Create` leaves it false.
- Verify: `go test ./internal/items/...`.

## Task 3 — placeholder helpers

- New `internal/runtime/placeholder.go`: `placeholderTitle(request string) string` and
  `placeholderAgentName(title string) string` per spec Locked Decision 2.
- `internal/runtime/placeholder_test.go`: the spec's exact example (68-rune request → 59-rune
  title ending "sign-in…", agent name "fix-the-login-redirect"); a single word > 60 runes hard
  cuts to 59 + "…"; empty/whitespace request → empty title → agent name "orchestrator".
- Verify: `go test ./internal/runtime/... -run Placeholder`.

## Task 4 — StartSpike wiring

- `internal/runtime/agents.go` `StartSpike`: trim Name/Request; empty+empty → 400
  `items.Error{Code: CodeBadRequest, Message: "Give a name or a request."}`; empty Name +
  non-empty Request computes placeholder title/agent name, sets `ci.Title`,
  `ci.TitlePending = true`, and resolves the agent name via
  `s.resolveName(ctx, trimmedName, generated)`; non-empty Name keeps today's behavior
  (`TitlePending` stays false). Kickoff: when `TitlePending`, append the exact spec copy line
  to the `Objective` passed to `RenderBrief` (not to the item's stored Brief/attachments text).
- Tests in `internal/runtime/agents_test.go`: empty name + request → item title/agent
  name/`TitlePending`/objective-contains-kickoff-line per spec scenario; typed name → flag
  false, no kickoff line; both empty → 400 `bad_request`.
- Verify: `go test ./internal/runtime/... -run StartSpike`.

## Task 5 — swarm_checkpoint title application

- `internal/runtime/checkpoint.go`: `CheckpointInput.Title string`; `CheckpointResult`
  gains `TitleApplied bool`, `TitleIgnored string`. In `WriteCheckpoint`, after the
  descendant check, when `in.Title` (trimmed) is non-empty: refuse (ignore) in order —
  caller isn't the orchestrator of the checkpoint's root item → "Only the orchestrator can
  name its item."; item's `title_pending` isn't set → "This item already has a name.";
  trimmed title outside 3–80 runes → "A title must be 3 to 80 characters."; else apply via a
  new `applyPendingTitle` helper (`UPDATE items SET title=?, title_pending=0, revision=revision+1,
  updated_at=? WHERE id=? AND title_pending=1`, 0 rows affected → ignored "already has a
  name" race), append `events.ItemChanged`, set `TitleApplied` and update the local `it.Title`
  for the notify args below it.
- Tests in `internal/runtime/checkpoint_test.go`: orchestrator applies once; second title
  ignored "already has a name"; child agent title ignored "Only the orchestrator..."; "ab"
  ignored with the length reason and flag stays 1.
- Verify: `go test ./internal/runtime/... -run Checkpoint`.

## Task 6 — MCP swarm_checkpoint wire

- `internal/mcpserver/tools.go`: schema adds `"title"`; decode `Title string`; pass through to
  `CheckpointInput`; result adds `title_applied` (omitted when false) and `title_ignored`
  (omitted when empty).
- Test in `internal/mcpserver/tools_test.go`: a title on an eligible checkpoint returns
  `title_applied: true`; an ineligible one returns `title_ignored`.
- Verify: `go test ./internal/mcpserver/...`.

## Task 7 — HTTP wire / 400

- `internal/httpapi/spawn.go`/`runtime.go`: no new code expected — `itemWire` embeds
  `items.Item`, so `title_pending` rides along automatically; the empty-name-and-request 400
  comes from `StartSpike`'s `*items.Error` through the existing `wrapPreflightErr`/`writeErr`
  path.
- Test in `internal/httpapi/spawn_test.go`: `POST /api/spikes` with both name and request empty
  → 400 `bad_request` "Give a name or a request."; a placeholder-titled spike's item JSON has
  `"title_pending":true`.
- Verify: `go test ./internal/httpapi/...`.

## Task 8 — Swift form

- `apps/menubar/Sources/SwarmBarKit/Copy.swift`: `nameOptionalPlaceholder`,
  `nameOrRequestRequired`.
- `apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift`: `canStart` drops the
  `kebab != nil` requirement, instead requiring trimmed Name or trimmed Request non-empty
  (`nameError == nil` and `errors.isValid` stay).
- `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift`: the Name `TextField` placeholder
  becomes `Copy.nameOptionalPlaceholder` (the `Text(Copy.name)` label above is unchanged).
- Tests in `apps/menubar/Tests/SwarmBarKitTests/NewOrchestratorFormTests.swift`: `canStart` true
  with empty name + non-empty request; false with both empty; `body()` sends `name: ""`.
- Verify: `cd apps/menubar && swift test`.

## Task 9 — skill docs

- `skills/swarm-orchestrator/SKILL.md` and `internal/install/skills/swarm-orchestrator/SKILL.md`:
  add the identical bullet, prefixed "If your kickoff says the item has no name yet:", with the
  spec's exact sentence.
- No automated test (doc-only); both copies must stay byte-identical to each other per existing
  convention.

## Final verification

`gofmt -l .`, `go vet ./...`, `go test ./...`, then `cd apps/menubar && swift test`.
