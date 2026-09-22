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
- Changing `isDescendant` to tolerate siblings, or any other loosening of
  the checkpoint scoping rule — the current strict ancestor rule is correct;
  the bug was the silent default, not the scoping check.
- Retroactive backfill or repair tooling for other stories that may have
  hit the same silent-default bug before this fix. If the user wants an
  audit query for that, it's a separate, explicit ask.
- Auto-fan-out (deriving which tasks a story-level `completed` should apply
  to and closing all of them at once) — considered and rejected: it would
  mark partial work as fully done on a guess.
- A reviewer's `completed` checkpoint counting toward a task's
  `completedCurrent` gate (any role's `completed` currently satisfies it,
  not just a gated one's) — real, but a separate concern from item-type
  targeting; not addressed here.

## Revision (same day, after commit 1): locked decisions 1, 2, 5 reversed

Commit 1 shipped the `WriteCheckpoint` guard above. The user then asked for
a "more wholistic and deterministic approach": the guard still leaves the
*choice* of target item to the worker at checkpoint time — a coder can still
be assigned to a story and has to remember to say `item: "<TASK-KEY>"`
correctly on every checkpoint. That's a should-remember, not a can't-get-
wrong; "deterministic" means the target is fixed by the assignment, not
chosen by the worker.

Verified before reversing (not assumed): `tryTransition`
(`checkpoint.go:124`) swallows `items.CodeTransitionDenied` and returns nil
(just logs it). `checkTask`'s `check()` (`transition.go:...`, `case Story:`)
denies every non-Done transition on a Story generically ("derived; only
reconciliation moves stories"). So a coder assigned to a Story writing
`accepted`/`progress`/`completed` was **already a total no-op for every
checkpoint kind**, not just `completed` — the multi-task-on-story mode never
worked in code, only in the skill doc that described it. There is no working
behavior this reversal takes away.

Reversed:

1. **Locked decision 1 (keep multi-task-worker-on-story) is dropped.** Every
   `coder`/`debugger`/`mechanical` worker is now spawned on exactly one task.
   Losing "one worker covers several small tasks" costs some efficiency, but
   the orchestrator's own concurrency budget (3 active subagents by default,
   `swarm-orchestrator` SKILL.md) already makes "spawn N workers for N tasks"
   the normal shape of the work; it was never actually saving spawns in
   practice given that ceiling.
2. **Locked decision 2 (fix lives in `WriteCheckpoint`, not `swarm_spawn`)
   is dropped.** The deterministic fix moves to spawn time:
   `runtime.Spawn` now refuses a gated role (`coder`/`debugger`/`mechanical`)
   on anything but an `items.Task`, and names the item's task children in
   the refusal. Once a gated role can only ever be assigned to a task,
   `completed`'s default-to-assignment is always correct — there is no
   longer a key to get wrong. `WriteCheckpoint`'s guard (commit 1) is kept as
   defense-in-depth: it still catches an ungated role (e.g. a `reviewer`,
   which *can* legitimately be spawned on a story for story-wide review)
   mistakenly reporting the whole story `completed`.
3. **Locked decision 5 is narrowed**: `swarm_control retry` and
   `isDescendant` are still unchanged (retry inherits an assignment that is
   now always correct by construction; the scoping rule was never the bug).
   `promoteDraft` is unchanged for the same reason as before. The
   **spawn-time role/item-type pairing**, previously locked as "no change,"
   is now the primary fix — see Model/API change below.
4. `internal/install/skills/swarm-orchestrator/SKILL.md` (and its canonical
   source `skills/swarm-orchestrator/SKILL.md`, kept in sync via
   `make skills-sync`) is updated to drop the multi-task-checkpoint
   instruction and state one-worker-per-task as the rule, since the doc's
   own guidance is what produced the STORY-27 assignment shape in the first
   place — leaving it as written would mean orchestrators keep attempting it
   and hitting the new spawn-time refusal.

### Model/API change (revision)

`runtime.Spawn` (`internal/runtime/agents.go`, after the existing
orchestrator-uniqueness check):

```go
if slices.Contains(gatedRoles, in.Role) && it.Type != items.Task {
    children, cerr := s.Items.Children(ctx, it.Key)
    if cerr != nil {
        return Agent{}, false, cerr
    }
    var keys []string
    for _, c := range children {
        if c.Type == items.Task {
            keys = append(keys, c.Key)
        }
    }
    msg := fmt.Sprintf("Spawn %s on a task, not %s.", in.Role, it.Key)
    if len(keys) > 0 {
        msg = fmt.Sprintf("%s Its tasks: %s.", msg, strings.Join(keys, ", "))
    }
    return Agent{}, false, &items.Error{Code: items.CodeBadRequest, Message: msg}
}
```

Reviewer/UI-reviewer/researcher/orchestrator are unaffected — they can still
be spawned on any item type, per the skill's story-wide-review case.

### Contention points audited (revision)

| Site | Risk | Disposition |
|---|---|---|
| `runtime.Spawn` (agents.go:559) | Gated role assigned to a non-task | **Fixed here** — refused, names the task keys |
| `WriteCheckpoint` guard (commit 1) | Now unreachable for gated roles (spawn blocks the fixture) | Kept as defense-in-depth for ungated roles (e.g. reviewer) mistargeting `completed`; commit-1 tests updated to spawn a `reviewer` instead of a `coder` to still exercise it |
| `promoteDraft` (orchestrator.go:49) | No-op on non-Task spawns | Unaffected — gated roles can no longer reach it on a non-Task at all |
| `swarm_control retry` | Inherits original assignment | Unaffected — the assignment is now always correct by construction |
| Reviewer's `completed` still counts toward `completedCurrent` | A reviewer assigned to a task (not just story-wide review) could satisfy the Done gate without a coder ever reporting | Real, pre-existing, unrelated to item-type targeting — logged in "Explicitly out of scope," not fixed here |

### Verification (revision, additive to the original section)

6. `go test ./internal/mcpserver/... -run TestSpawnRefusesACoderOnAStory` —
   refusal fires and names both sibling task keys.
7. `go test ./internal/mcpserver/... -run TestSpawnAllowsAReviewerOnAStory` —
   confirms the exemption isn't over-broad.
8. `go test ./... ` (full repo) green, including the three commit-1 checkpoint
   tests updated to spawn a `reviewer` fixture.

## Second revision (same day, before merge): the commit-1 guard over-blocked

The subagent that implemented commit 1 flagged, correctly, a gap its own
audit (and mine) missed: `completed` is not task-specific. It's the
universal session-terminal checkpoint — `skills/swarm/SKILL.md`: "Orchestrators,
debuggers and reviewers also consult [their advisor] before writing
`completed`" — and `terminalCheckpointKind` (`reconcile.go:219`) reads the
most recent `completed`/`failed` checkpoint on *any* agent's attempt to close
its session as `completed` instead of `crashed`. `checkRoot`'s audit row
("roots never reach Done via CompletedCkp") was true for *item status* but
irrelevant to *session status* — a different consumer of the same checkpoint
kind that neither the original spec nor the first revision considered.

Commit 1's guard, as shipped, refused `completed` on **any** Story/Epic/Bug
regardless of role. That would have broken:

- An orchestrator's own end-of-epic/bug `completed` checkpoint
  (`skills/swarm-orchestrator/SKILL.md`: "Write `completed` when the daemon
  reports the item accepted") — every orchestrator session end, turned into
  a false "crashed" instead of "completed".
- A reviewer's `completed` checkpoint ending a legitimate story-wide review.

**Fix:** narrow the `WriteCheckpoint` guard to `gatedRoles` only
(`slices.Contains(gatedRoles, a.Role)`, i.e. coder/debugger/mechanical) —
the only roles `runtime.Spawn` now restricts to tasks. For every other role,
`completed` on any item type it was actually assigned to is legitimate and
untouched.

**Why the guard is still worth keeping at all**, now that Spawn is airtight
for new spawns: an agent already running with a gated role on a
Story/Epic/Bug from *before* this fix deploys (mid-flight across the
upgrade) is not caught by the spawn-time gate — it was spawned under the
old code. The `WriteCheckpoint` guard is what still catches that one, purely
transitional case. It has no effect on a freshly-spawned gated worker, which
can never reach a non-task assignment in the first place.

**Tests corrected:** `assertRefusesCompleted` no longer spawns a `reviewer`
(which is no longer refused) — it spawns a `coder` legitimately on a real
task, then rewrites the agent's own `item_id` directly in the DB to simulate
"already assigned before the gate existed," and confirms `WriteCheckpoint`
still refuses. Two new regression tests
(`TestWriteCheckpointAllowsOrchestratorCompletedOnItsOwnEpic`,
`TestWriteCheckpointAllowsReviewerCompletedOnAStory`) pin the behavior this
revision restores, so it can't silently regress again.

No further code changes follow from this — `go test ./...` is green with
these five tests (three refusals + two allow-regressions) covering both
sides of the line.
