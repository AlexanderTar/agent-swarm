# Chore Orchestrators May Propose Top-Level Items — Specification

- **Date**: 2026-10-03
- **Item**: BUG-26
- **Status**: Locked.
- **Repos**: `agent-swarm` (`internal/items`, `skills/swarm-orchestrator`, `internal/install/skills` via `make skills-sync`)
- **Companion plan**: `docs/plans/2026-10-03-chore-orchestrator-proposals.md`
- **Supersedes**: decision 3 of `docs/specs/2026-09-27-chore-and-spike-output-ready.md`

## 1. Context

A top-level chore orchestrator (CHORE-23, 2026-10-03; earlier BUG-18) called
`swarm_items {op:create}` with no `parent` to propose out-of-scope follow-ups
(spikes, a chore). Swarm refused with `A chore works only on its own scope.
It can't propose top-level items.` The user had to create the follow-ups by
hand. The user said: "add feedback to swarm that this limitation should be lifted".

Root cause: a deliberate guard in `CreateTx`, `internal/items/store.go:470-473`,
added by 2026-09-27 decision 3. There is no second chore refusal in
`internal/mcpserver` (the top-level-only check there is root-type agnostic),
and the board's "Started from KEY" link (`web/src/panels/Details.tsx:257`)
does not depend on the origin's type.

## 2. Locked decisions

1. A top-level chore orchestrator may propose top-level items (epic, bug, chore,
   spike) exactly like an epic, bug or spike orchestrator. Proposals land Draft,
   `status: "ready"` is still refused, repos are suggestions only, and
   `origin_spike_id` is the chore root.
2. A chore orchestrator still works only on its own scope: proposals are
   separate Draft items the user decides on; the chore itself stays tasks-only.
3. A child orchestrator under a chore is still refused by the existing
   top-level-only check in `internal/mcpserver`. Unchanged.

## 3. DB models

None.

## 4. Model / API types

None. `CreateTx` loses the chore guard; no signature changes.

## 5. Screens

None.

## 6. All user-facing copy

Removed: `A chore works only on its own scope. It can't propose top-level items.`

`skills/swarm-orchestrator/SKILL.md`, Chores section, the bullet
"Never propose top-level items from a chore — Swarm refuses it. Put out-of-scope findings in your final summary instead." becomes:

> Out-of-scope work you find stays out of the chore's own tasks. Propose it as a brand-new top-level item (see "Found out-of-scope work" above); it lands Draft and the user decides whether to start it.

## 7. File list

- `internal/items/store.go`: delete the chore guard; update the comment.
- `internal/items/store_test.go`: rewrite `TestChoreRootCannotProposeTopLevel` as `TestChoreRootCanProposeTopLevel` (not deleted).
- `skills/swarm-orchestrator/SKILL.md` + `internal/install/skills/...` (via `make skills-sync`).
- `docs/specs/2026-09-27-chore-and-spike-output-ready.md`: one "Superseded" pointer on decision 3.

## 8. Verification

1. `go test ./internal/items/ -run 'TestChoreRootCanProposeTopLevel|TestOrchestratorCanProposeRoot'`
2. `make skills-sync && git diff --exit-code internal/install/skills`
3. `go vet ./... && go test ./...`

Scenarios: chore orchestrator proposes epic/bug/chore/spike → each Draft with
origin = chore; `status: ready` → refused; unknown repo → refused.

## 9. Explicitly out of scope

- Changing who may propose (child orchestrators stay refused).
- Any board or menubar change.
