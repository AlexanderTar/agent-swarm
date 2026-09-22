# Spec: gate `completed` checkpoints to the item types that can consume them

## Context

STORY-27 was stuck: TASK-78/79/80 (S7.1/7.2/7.3) had merged, gated work, but
the story wouldn't reach Done. Root cause traced (systematic-debugging +
advisor review, both completed before this spec) to a real bug, not an
operator mistake:

- `swarm_checkpoint` (`internal/runtime/checkpoint.go:227`, `WriteCheckpoint`)
  defaults `itemKey` to the calling agent's own assignment when the caller
  omits `item`.
- `swarm-orchestrator` SKILL.md documents a legitimate mode: "if one worker
  covers several tasks, tell it to checkpoint each with `item: "<TASK-KEY>"`."
  That mode requires the worker be **assigned to the story** (its assignment
  must be an ancestor of every task it checkpoints — `isDescendant`,
  `checkpoint.go:138`, is strict ancestor→descendant; sibling tasks are not
  descendants of each other, so a worker assigned to one task cannot
  checkpoint a sibling task at all).
- The moment such a worker's `completed` checkpoint omits `item` (forgets it,
  or believes "the story is done" is the right thing to report), `itemKey`
  silently defaults to the story. `WriteCheckpoint` accepts it: the row is
  written, `CompletedCkp`'s switch (`checkpoint.go:337`) only transitions
  `it.Type == items.Task` to `InReview` — for a Story it's a no-op that looks
  like it worked (200 OK, checkpoint ID returned) but changes nothing.
- Later, `items.checkTask`'s Done gate (`internal/items/transition.go:177`)
  checks `completedCurrent` scoped to the **task's own item ID**
  (`transition.go:268`). It correctly finds nothing and denies with "No agent
  has reported it complete" — true, but it doesn't say a `completed`
  checkpoint exists one level up, so the generic denial reads as "nobody did
  the work" when the real defect is "the report landed on the wrong item."
  `Story→Done` (`transition.go:163`) then also denies, correctly, because its
  tasks aren't Done.

Both gate denials are functioning exactly as designed — they're the correct
fail-closed behavior, not the bug. The bug is upstream: nothing stops a
`completed` checkpoint from landing on an item type that has no consumer for
it, so the mistake is invisible until someone goes looking for it, generically,
possibly days later.

Affected repo: `agent-swarm` only (this repo). No other repo's worktrees are
implicated. `internal/runtime/checkpoint.go` is not currently owned by any
other in-flight worktree per `git worktree list` at spec time; confirm before
merge if that has changed.

## Locked decisions

1. **Keep the multi-task-worker-on-story assignment mode.** SKILL.md
   documents it as supported and it's a real efficiency win (one worker,
   several small tasks). We do not force one worker per task.
2. **The fix lives in `WriteCheckpoint`, not in `swarm_spawn`.** A spawn-time
   check that forbids assigning `coder`/`debugger`/`mechanical` to a Story
   would break decision 1 — a multi-task worker's assignment *must* be the
   story. The spawn path stays as-is. This is the "one guard in the shared
   function beats a guard in every caller" root-cause fix: every route to a
   bad `completed` checkpoint (a forgotten `item:`, a `swarm_control retry`
   that inherits the bad key, a hand-written MCP call) passes through
   `WriteCheckpoint`.
3. **The guard applies to `CompletedCkp` only, on item types with no
   consumer for it: `items.Story`, `items.Epic`, `items.Bug`.**
   `items.Task` is the only type whose transition switch acts on
   `CompletedCkp` (`checkpoint.go:337-341`, moves it to `InReview`).
   `items.Spike` also has a real, separate consumer: a `completed` checkpoint
   there carries `resolution` (`checkpoint.go:259-270`) and is how a spike
   reports `no_change`/`duplicate_of:<KEY>` — Spike is therefore exempted,
   not gated.
4. **STORY-27 itself is out of code scope.** Its tasks are still sitting at
   whatever pre-completion status they were left at; nothing in this
   codebase can retroactively attach a `completed` checkpoint to a past
   attempt. Unsticking it is an operational step (spawn a `coder` against
   each of TASK-78/79/80, have it record `completed` with the already-merged
   sha as evidence), done separately, not part of this change.
5. **No change to `swarm_control retry`, `promoteDraft`, or the spawn-time
   role/item-type pairing.** They were audited (see "Contention points
   audited" below) and don't need a change once `WriteCheckpoint` closes the
   choke point.

## Contention points audited

Every place a role↔item-type or item-key mismatch of this shape could occur,
checked against this fix:

| Site | Risk | Disposition |
|---|---|---|
| `WriteCheckpoint` default `itemKey = assignment` (checkpoint.go:227) | A `completed` checkpoint silently lands on a Story/Epic/Bug | **Fixed here** — rejected for `CompletedCkp` when `it.Type` is Story/Epic/Bug |
| `swarm_spawn` / `runtime.Spawn` (orchestrator.go:466, agents.go:558) | No role×item-type validation at spawn time | Not changed — see locked decision 2; would break the documented multi-task-on-story mode |
| `promoteDraft` (orchestrator.go:49) | No-ops when spawned on non-Task; the task itself never gets promoted to Ready/InProgress from this path | Working as designed for story-level spawns (story-wide review, not a coder); no gate depends on it |
| `checkTask` deny copy (transition.go:179) | "No agent has reported it complete" doesn't point at a misdirected report | **Improved here** — see Model/API section; cosmetic, not a behavior change |
| `swarm_control retry` | Retries inherit the original (possibly wrong) assignment | No change needed — once `WriteCheckpoint` refuses the bad `completed`, a retried worker gets the same clear refusal and corrects its `item:`, it doesn't silently repeat the mistake |
| `checkSpike` (`transition.go:230`) | Could a Spike get wrongly gated by this change? | No — Spike is explicitly exempted (locked decision 3) |
| `checkRoot` (Epic/Bug, `transition.go:196`) | Could this change block a legitimate root-level checkpoint? | No — roots never reach Done via `CompletedCkp`; they use `integrated` + `accept_epic`/`accept_fix` (`rootState`, `approvedCurrent`). A `completed` checkpoint on an Epic/Bug was already dead weight; refusing it loses nothing |

## API / behavior change

`WriteCheckpoint` (`internal/runtime/checkpoint.go`), inside the existing
`in.Kind == CompletedCkp` handling, before the INSERT:

```go
if in.Kind == CompletedCkp && it.Type != items.Task && it.Type != items.Spike {
    return &items.Error{Code: items.CodeBadRequest,
        Message: fmt.Sprintf(
            "Completed checkpoints attach to tasks, not %ss. Pass item: \"<TASK-KEY>\" for the task you finished.",
            it.Type)}
}
```

Placed alongside the existing `in.Resolution` block (both are `CompletedCkp`-
scoped item-type checks; keep them adjacent for readability, don't merge them
— resolution is Spike-only, this new check is Story/Epic/Bug-only, and a
single combined condition would read backwards).

`checkTask`'s deny message (`internal/items/transition.go:179`), copy only,
no signature change:

- Before: `"Couldn't move %s to Done. No agent has reported it complete."`
- After: `"Couldn't move %s to Done. No agent has reported it complete on this task."`

("on this task" is the whole fix to this copy — it's now accurate to say,
since a `completed` checkpoint can no longer exist anywhere else for that
work to have gone missing.)

## Screens / UI

None. This is an MCP tool (`swarm_checkpoint`) and daemon-internal gate
message; no web UI surface changes. The error message above is what a coder
agent sees verbatim in its tool result when it mistargets a `completed`
checkpoint — that IS the user-facing copy for this change.

## File list

- `internal/runtime/checkpoint.go` — add the item-type guard in
  `WriteCheckpoint`.
- `internal/runtime/checkpoint_test.go` — new test(s), see plan.
- `internal/items/transition.go` — deny-message copy tweak in `checkTask`.
- `internal/items/transition_test.go` — update the assertion(s) that match
  the old deny copy verbatim (grep first; don't guess which ones).
- Everything else (spawn path, `promoteDraft`, `swarm_control`, reconcile,
  web/) — unchanged, per the audit above.

## Verification

1. `cd internal/runtime && go test ./... -run Checkpoint` — new tests pass,
   no existing checkpoint test regresses.
2. `cd internal/items && go test ./... -run Transition` — deny-copy
   assertions updated and passing.
3. `go build ./...` from repo root — compiles.
4. `go test ./...` from repo root — full suite green (the copy change is a
   verbatim string a few tests may assert on; the plan's step to grep for
   the old string before editing is required, not optional).
5. Manual scenario, once merged: spawn a `coder` on a Story, have it
   checkpoint `kind: completed` without `item:` → expect the new refusal
   message, not a silent 200.

## Explicitly out of scope

- Unsticking STORY-27 itself (locked decision 4 — operational, separate
  from this code change).
- Any spawn-time role/item-type validation (locked decision 2).
- Changing `isDescendant` to tolerate siblings, or any other loosening of
  the checkpoint scoping rule — the current strict ancestor rule is correct;
  the bug was the silent default, not the scoping check.
- Retroactive backfill or repair tooling for other stories that may have
  hit the same silent-default bug before this fix. If the user wants an
  audit query for that, it's a separate, explicit ask.
