# Spec: optional Name in the New orchestrator dialog; the orchestrator titles its item

## Context

The New orchestrator dialog requires a Name today. `NewOrchestratorForm.canStart` needs a
valid `kebab`, and `nameError` reports `Kebab.emptyNameMessage`. The Name becomes both the
item title and, via `resolveName`, the kebab agent name (`internal/runtime/agents.go`
`StartSpike`). The user wants Name optional whenever a Request is given, with the name
inferred from the request.

User-approved design (2026-09-28): the daemon sets a deterministic placeholder right away,
and the orchestrator supplies the real title through a tool argument on its first accepted
checkpoint. This follows the standing rule "ask the agent, not a model": no local model and
no heuristics.

## Locked decisions

1. Start is enabled when the trimmed Name or the trimmed Request is non-empty (plus the
   existing conditions). A non-empty Name must still pass today's validation.
2. Empty Name with a non-empty Request:
   - **Item title:** the first non-blank line of the request, with whitespace collapsed,
     cut at a word boundary to at most 60 runes, with `…` appended if cut. A single word
     longer than 60 runes is hard-cut at 59 runes plus `…`.
   - **Agent name:** `resolveName(ctx, "", <first 4 words of the placeholder>)`, which uses
     `ids.Unique` on the kebab of those words. If that kebab is empty, use `orchestrator`.
   - **Item flag:** the item is marked `title_pending = 1`.
3. Empty Name and empty Request: `POST /api/spikes` returns 400 `bad_request` "Give a name or a request."
4. `swarm_checkpoint` gets an optional `title` string. It is applied only when all of these hold:
   - the caller is the orchestrator whose root item is the checkpoint's item;
   - that item has `title_pending = 1`;
   - the trimmed title is 3–80 runes.

   When applied, it sets the item title, clears the flag, and publishes the usual item-changed
   event. It applies at most once. Otherwise the title is ignored, and the tool result carries
   `"title_ignored": "<reason>"`. The reasons are:
   - "This item already has a name." (flag not set)
   - "Only the orchestrator can name its item."
   - "A title must be 3 to 80 characters."
5. The agent name never changes after launch; tmux sessions, refs and tokens are keyed on it.
6. A name the user typed is never overwritten (the flag stays 0).

## DB models

Migration `internal/db/schema` next number (0018): `ALTER TABLE items ADD COLUMN title_pending
INTEGER NOT NULL DEFAULT 0;`. Health `schema` becomes 18. The migration test follows the
existing `schema_00NN_*_test.go` pattern.

## Model / API types

- Go `items.CreateInput` gains `TitlePending bool`. `items.Item` gains `TitlePending bool`,
  which is read and written by the store.
- `internal/runtime`: `func placeholderTitle(request string) string` and
  `func placeholderAgentName(title string) string` (returns the words that `resolveName` receives).
- MCP `swarm_checkpoint` schema adds `"title":{"type":"string","description":"Only when your kickoff says the item has no name yet: a 3–6 word name for the work."}`.
  The result object gains `title_applied bool` (omitted when false) and `title_ignored string`
  (omitted when empty).
- The HTTP item wire gains `title_pending` (bool, omitted when false), so the board and
  menubar can style it later. No UI change in this spec.

## Screens

Dialog, Name row only:

```
Name   [ Optional if you write a request            ]
       agent: <preview when a name is typed; nothing when empty>
```

Nothing else changes. The empty-name error is shown only when both Name and Request are empty
**and** the user has typed in and then cleared Name (today's `nameError` behaviour when Name
is non-empty but invalid stays).

## User-facing copy

- Swift `Copy.nameOptionalPlaceholder` = "Optional if you write a request"
- Swift `Copy.nameOrRequestRequired` = "Give a name or a request."
- Daemon 400: "Give a name or a request."
- Kickoff/brief line, when `title_pending`, appended to the orchestrator objective:
  `This item has no name yet. In your first swarm_checkpoint kind:"accepted", pass title: a 3–6 word name for the work (good: "Fix login redirect loop"; bad: "Task from request").`
- Skill `swarm-orchestrator/SKILL.md` (both copies, kept identical) gets one bullet with the
  same sentence, prefixed "If your kickoff says the item has no name yet:".

## File list

- `internal/db/schema/0018_*.sql` plus a migration test.
- `internal/items`: model, store, create and scan (`TitlePending`).
- `internal/runtime/agents.go` (`StartSpike`), plus a new small file or functions for the placeholder helpers.
- `internal/runtime` checkpoint handling (where `swarm_checkpoint` lands) for `title`.
- `internal/mcpserver/tools.go`: schema and result fields.
- `internal/httpapi/runtime.go`: item wire `title_pending`; the 400 for an empty name and empty request (or return it from runtime as an `*items.Error`).
- `apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift` (`canStart`, `nameError`, `body()`), `Copy.swift`.
- `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift` (placeholder).
- Both `swarm-orchestrator/SKILL.md` copies.
- Tests beside each.

## Verification

1. `go test ./...`, `go vet ./...`, `gofmt -l .`. Then `cd apps/menubar && swift test`.
2. Scenarios, each a test:
   - Name empty with request "Fix the login redirect loop that happens after SSO sign-in on Safari" →
     title "Fix the login redirect loop that happens after SSO sign-in…" (≤60 runes, word
     boundary), agent `fix-the-login-redirect`, `title_pending` 1, and the objective contains the kickoff line.
   - Name typed → flag 0, no kickoff line.
   - Both empty → 400.
   - The orchestrator's accepted checkpoint with title "Fix login redirect loop" → title set, flag
     0, `title_applied` true; a second title → ignored "This item already has a name."
   - A child agent passing a title → ignored "Only the orchestrator can name its item."
   - Title "ab" → ignored with the length reason, and the flag stays 1.
   - Swift: `canStart` is true with an empty name and a request, and false with both empty; the body sends `name: ""`.

## Explicitly out of scope

- Renaming agents.
- Board or menubar styling for placeholder titles.
- Renaming items with a typed name.
- The CLI (`swarm spike`) flow, which keeps requiring a name.
