# CHORE-27: Fix open agent-swarm bugs BUG-13..BUG-25

## Context

Thirteen Draft bug reports filed by agents between 2026-10-03 06:44 and 13:38Z.
All live in `agent-swarm`. They cluster into three areas, each delivered as one
work package on branch `chore-27-open-bugs`:

| Package | Bugs | Area |
|---|---|---|
| A — checkpoint gates | 14, 15, 19, 20, 21, 22 | `internal/runtime/checkpoint.go` and item status |
| B — workflow engine & messaging | 16, 17, 23 | `internal/workflow`, `internal/runtime/workflow.go`, messaging |
| C — isolated contract fixes | 13, 18, 24, 25 | `adapter/agy.go`, `items/store.go`, `advisor/transcript.go`, `mcpserver/orchestrator.go`, skills |

Collision warning: packages A and B both live in `internal/runtime`; A owns
`checkpoint.go`, B must not edit it. C is file-disjoint from both.

## Locked decisions

- **BUG-14/15/19**: the commit gate accepts a checkpoint `sha` that is a
  unique prefix of HEAD of at least 7 hex chars; the stored run sha is always
  the full HEAD. When the sha mismatches, the message prints full shas.
- **BUG-20/21**: the `required_artifact` gate (plan for epic, debug_report for
  bug) applies only when the checkpointing agent is the root's orchestrator.
  Reviewers and other children are never refused by it.
- **BUG-22**: a `blocked` checkpoint from a non-orchestrator child on a root
  does not leave the root stranded: that child's later `completed` restores
  `status_before_block`, and an `integrated` checkpoint opens `accept_fix` /
  `accept_epic` even when the root is Blocked (restoring it first).
- **BUG-17**: extra rounds granted by `swarm_workflow resume retry` stay (by
  design). The defect is the rendered brief: "round N of at most M" must use
  the effective max (`max_rounds + extra rounds`).
- **BUG-23**: `resume retry` must deliver the review findings and the
  orchestrator note to the fix-step agent. Re-use of the same agent is
  preferred where the engine supports it; otherwise the fresh agent's brief
  must carry findings + note. Skill text must match the shipped behaviour.
- **BUG-16**: a `swarm_send` body of any size up to the send limit must be
  retrievable in full by the recipient via `swarm_sync` (and `swarm_read` of
  the msg id if supported). Inbox *notice* truncation stays.
- **BUG-13**: AGY `PostToolUse` expects an empty JSON object (`{}`) per the
  CLI's own hook contract; `injectSteps` is only valid for `PreInvocation`.
  `HookOutput` becomes event-aware: PostToolUse context returns `{}`.
- **BUG-18** (user decision 2026-10-03): chore orchestrators *may* propose
  top-level items. Remove the chore refusal in `items/store.go`; keep the
  top-level-only check in `mcpserver/orchestrator.go`. Update the Chores
  section of `skills/swarm-orchestrator/SKILL.md` and re-sync the mirror.
- **BUG-24**: Claude's transcript dir encodes every non-alphanumeric char of
  the cwd as `-` (e.g. `/Users/x/.swarm/work/a` →
  `-Users-x--swarm-work-a`). Cursor's encoding is left unchanged unless
  verified to differ.
- **BUG-25**: `swarm_items create` with `blocked_by` links the dependency in
  the same transaction (same code path as `op: link`).

## DB models

None. No schema or migration changes.

## Model / API types

No new wire types. Behaviour changes only:
- `swarm_items create` now honours the existing `blocked_by` string field.
- `swarm_checkpoint` git `sha` accepts an abbreviated (≥7) prefix.

## Screens

None.

## All user-facing copy

- Commit gate mismatch: `Commit your work before completing: <repo> HEAD is <40-char head>, checkpoint says <given sha>. Pass the full sha of your committed HEAD.`
- Rendered review brief: `round <n> of at most <effective max>`.

## File list

Changed: `internal/runtime/checkpoint.go` (+tests), item-status code it calls,
`internal/workflow/render.go`/`next.go`, `internal/runtime/workflow.go`,
messaging/sync code for BUG-16, `internal/adapter/agy.go`,
`internal/items/store.go`, `internal/advisor/transcript.go`,
`internal/mcpserver/orchestrator.go`, `skills/swarm-orchestrator/SKILL.md`,
`internal/install/skills/**` (mirror). Deleted: none.

## Verification

1. `make skills-sync && git diff --exit-code internal/install/skills`
2. `go vet ./...`
3. `go test ./...`

## Explicitly out of scope

- Any change to workflow round budgets or the resume extra-round mechanism.
- Inbox notice truncation length.
- Menubar / web UI.
