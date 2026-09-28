# Spec: auto-approve spec sections with nothing to review

## Context

User (2026-09-28): "If a section of the spike/spec doesn't have any meaningful content (like no new DB models, screens or anything for the user to review), skip approval step and treat it as auto-approved."

Today every required spec section goes through `swarm_ask kind:"approval"` and a native question (`internal/runtime/artifacts.go` `RequiredSpecSection` already exempts headings such as context/background/references/file list). Sections like "DB models: None." still interrupt the user.

The user approved the design: the agent declares it, and the daemon guards it. This follows the standing rule "ask the agent, not a model".

The same flow also has one bug to fix here (Opus review of 576a53d, minor 1). `ResolveQuestionReply` (`internal/runtime/requests.go` ~1426) re-binds a Codex async reply by question text with an empty header. With two children sending identical bodies, it can pick the wrong child and leave the right row open.

## Locked decisions

1. `swarm_ask` for a spec-section approval (the approve_section path) accepts an optional `nothing_to_review` string: a one-line reason, 3–200 runes.
2. The daemon auto-approves only when all hold (tightened post-review from the originally approved 300-rune bound):
   - the flag is set;
   - the section heading is at most 80 runes;
   - the section's stored body, with its heading line removed and trimmed, is a single short line (≤120 characters), no tables, lists or code: at most 120 runes, one non-empty line, and no table row (`|`), list item (a line starting with `-`, `*`, `+`, or a numbered marker like `1.`), or code fence (```` ``` ````).

   If the body fails any of those, it refuses with the copy below (using the body's real character count) and creates no request. Plans and reports never auto-approve; the field is refused on those kinds.
3. An auto-approval resolves the section exactly as a user approval would: same state transition, same `approval_result` delivery, same downstream behaviour, such as the spec becoming approved when all sections are. Its evidence is `auto_empty`, and `binding_json` records `nothing_to_review: <reason>`. `responded_via` is `auto`. No native question is issued; the result tells the agent what to print.
4. The user sees one line in chat, printed by the agent from the tool result's `next`:
   `Section "<title>": nothing to review (<reason>) — auto-approved.`
5. A section that was auto-approved and later revised with real content goes back to normal approval, under the existing stale-revision rules.
6. **Bug fix:** `ResolveQuestionReply` resolves through the already-bound question row: the newest open `kind='question'` row for the agent whose normalized prompt matches and whose `$.ref` is not null. It no longer re-binds by text.

## DB models

None. The `responded_via` value `auto` is new; check any CHECK constraint on `requests.responded_via`. If one exists, add a migration 0020 following the repo's table-rebuild pattern (and verify it on a copy of the live DB).

## User-facing copy

- Refusal:
  `This section has content to review (<n> characters); ask for approval normally.`
- Refusal on a plan or report:
  `nothing_to_review is only for spec sections.`
- Result `next`:
  `Print this line in chat: Section "<title>": nothing to review (<reason>) — auto-approved. Then continue with the next section.`
- `swarm_ask` schema description of the field:
  `Spec sections only: a one-line reason there is nothing for the user to review (e.g. "No DB changes: no tables, columns or migrations."). The section body must be a single short line (≤120 characters), no tables, lists or code.`
- `swarm-orchestrator` and `swarm-spike` skills: one bullet (both copies byte-identical):
  `A spec section with nothing for the user to review (e.g. "DB models: none") is asked with nothing_to_review: "<one-line reason>" instead of a native question; print the line the result gives you. Never use it to skip a section that has content.`

## File list

- `internal/runtime` (approve_section ask path, resolve path, `ResolveQuestionReply`).
- `internal/mcpserver/tools.go` (schema).
- Skills (both copies).
- A migration only if needed.
- Tests beside each.

## Verification

1. `gofmt -l .`, `go vet ./...`, `go test ./...`
2. Scenarios, each a test:
   - A "None." body with the flag → approved, evidence `auto_empty`, `approval_result` delivered, no question row.
   - A 301-rune body with the flag → refused, no request.
   - The flag on a plan → refused.
   - All sections approved, some of them automatically → spec approved as today.
   - An auto-approved section revised with content → a normal approval is required.
   - Codex reply with two children sending identical bodies → the right child's row resolves.

## Explicitly out of scope

- Daemon-side content heuristics.
- Board display of auto-approvals.
