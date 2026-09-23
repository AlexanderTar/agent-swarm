# Worktree cleanup enforcement

Date: 2026-09-23
Status: proposed — awaiting review, nothing implemented
Slug: `2026-09-23-worktree-cleanup-enforcement`

---

## 1. Context

### The symptom

`~/.swarm/worktrees` reached 70 directories / 14 GB, several of them multi-GB
because each carries its own `node_modules`. It surfaced while chasing an
unrelated full-disk incident (`df`: 581 Mi free of 926 Gi, resolved by deleting
stale DB backups). The worktree pile was not the cause of that incident, but it
is a real and growing leak.

### What the daemon actually does today

`internal/worktree/worktree.go` already implements a complete, conservative
removal routine. `Service.remove` refuses to delete anything it cannot prove is
safe:

| Condition | Outcome |
| --- | --- |
| `git status --porcelain` non-empty, or the command fails | `retain(reason="dirty")` |
| Branch not merged into `base_ref` and not pushed (`@{u} == HEAD`) | `retain(reason="unmerged")` |
| `git worktree remove` itself fails | `retain(reason="remove_failed")` |
| Otherwise | `git worktree remove`, row → `state='removed'` |

`retain` sets `state='retained'` and fires `OnRetained`, which raises the
`worktree.retained` notification ("Worktree kept — the worktree for {ROOT-KEY}
has {detail} and was kept."). So the *deletion rules* are not the gap.

The gap is the **trigger**. There is exactly one automatic caller:

```go
// internal/runtime/reconcile.go:862
func (s *Store) sweepFinishedRoots(ctx context.Context) error {
    rootIDs, err := s.queryIDs(ctx, `SELECT id FROM items WHERE root_id = id AND status IN ('done','cancelled')`)
    ...
        `SELECT COUNT(*) FROM agents WHERE root_item_id = ? AND state NOT IN ('finished','acknowledged')`
        if stuck > 0 { continue }
        s.Worktree.Sweep(ctx, rootID)
}
```

Reclamation is gated at **root-item granularity**: nothing is reclaimed until an
entire top-level Epic/Bug reaches `done`/`cancelled` *and* every agent in its
tree is terminal.

### Measured state of the live database (2026-09-23, `~/.swarm/swarm.db`, read-only)

```
worktrees by state:     active 226 | removed 15 | retained 0
root items by status:   ready 4 | in_progress 2 | blocked 2 | done 1 | cancelled 1
active worktrees by owning root status:   in_progress 226  (2 distinct roots; one owns 214)
```

Three facts fall out:

1. **The sweep has never fired for any of these worktrees.** All 226 active rows
   hang off two `in_progress` roots — multi-day epics with live agents. The gate
   cannot open until the epic ends, which is weeks.
2. **`retained` has never been reached in production** (0 rows). The
   retain/notify path is untested against real data.
3. **The DB is lying about disk.** Of 226 `active` rows, only 14–16 paths still
   exist. The other ~210 point at directories that are gone.

### Who is actually deleting the worktrees

During this investigation the on-disk count fell 70 → 63 → 17 → 14 in roughly
twenty minutes, while:

- `worktrees.removed_at` gained **zero** new rows (latest is 17:41),
- `grep -c worktree ~/.swarm/logs/daemon.{out,err}.log` returns **0**,
- `git worktree list --porcelain | grep -c '^prunable'` returns **0** in
  `endurio-chat` — the removals were clean, so `git worktree remove`, not `rm -rf`.

Something outside the daemon is removing worktrees correctly-but-invisibly. The
cause is a path collision with a superpowers skill:

`superpowers:finishing-a-development-branch` (6.3.0) Step 6 decides ownership by
path prefix:

> **If `WORKTREE_PATH` is under `.worktrees/` or `worktrees/`:** Superpowers
> created this worktree — we own cleanup:
> ```bash
> git worktree remove "$WORKTREE_PATH"
> git worktree prune
> ```

Swarm worktrees live at `~/.swarm/**worktrees**/<repo>--<slug>`
(`worktree.go:44`, `:112`). The prefix matches, so the skill's heuristic claims
them, and an agent following it runs raw `git worktree remove` — bypassing
`Service.Remove`'s owner check, its reservation check, its dirty/unmerged
refusal, and its DB bookkeeping.

Meanwhile the daemon's own skills never close the loop.
`skills/swarm/SKILL.md:22` says *"Do not create worktrees or branches"* — it says
nothing about removing them. `skills/swarm-orchestrator/SKILL.md:16` says
*"Remove worktrees when merged"* without naming `swarm_worktree`, and `:49` says
*"remove your worktrees"*. Neither forbids raw git. `skills/swarm/SKILL.md:27`
explicitly points agents at `superpowers:test-driven-development`, establishing
superpowers skills as in-bounds.

### Root cause (two distinct gaps)

**Gap 1 — disk leak. Wrong granularity, not missing code.** The only automatic
reclaim is root-scoped and will not fire during a long-lived epic. Hypothesis
(b) from the brief, precisely: the code exists, it is correct for its stated
purpose, and its trigger never opens.

**Gap 2 — truth leak. Skill collision, not agent forgetfulness.** Agents *do*
clean up, diligently and correctly, via a superpowers skill whose ownership
heuristic mis-claims the daemon's directory. Every such removal leaves a stale
`state='active'` row. Hypothesis (a) from the brief, with a sharper cause than
"agents forget": they are following a skill that tells them to.

### Why the two gaps need different fixes

Fixing the skill text stops new stale rows and keeps future removals inside the
daemon's safety net — but does nothing for an agent that crashed, was cancelled,
or died mid-task. Measured: **96 of 226 rows** have a `finished` owner with no
live session; those owners will never run a cleanup step of any kind. A
daemon-side backstop is required. Conversely, a backstop alone leaves agents
racing the daemon on the same paths with weaker guarantees. Both, or neither
works.

### Constraint discovered while designing

`ReconcileLoop` runs **every 5 seconds** (`cmd/swarm/daemon.go:317`). Any
reclaim that shells out to `git status` per worktree cannot live inside
`Reconcile`: 226 candidates × 12 ticks/minute is ~45 `git` subprocesses per
second, permanently. The reclaim needs its own, much slower loop.

### How an agent learns a worktree path (this is the safety basis)

Measured, because the obvious gates turn out not to work:

- `sessions.cwd_kind` is `neutral` for **all 290 session rows**; zero are
  `worktree`. Every agent is spawned in `~/.swarm/work/<agent-name>` and told
  worktree paths as text. So `sessions.cwd` can never identify who is inside a
  worktree — a gate built on it would silently never fire.
- `BriefWorktree` (`internal/runtime/text.go:131`) has no production
  construction site, and `Service.ForAgent` (`worktree.go:270`) has no
  production caller. Confirmed against data:
  `full-go-api-migration-orchestrator-2`'s brief is 806 characters and names
  **no** worktree path. So a brief-text gate protects nothing either.

That leaves these production channels by which a path reaches an agent:

| Channel | Who receives it | DB trace |
| --- | --- | --- |
| `swarm_worktree op:"create"` / `"review"` return value | the creating orchestrator | `worktrees.owner_agent_id` |
| `swarm_worktree op:"share"` → `assignment_update` message | the shared-with agent | `worktree_reservations` row |
| Free text in a spawned child's brief (`context`, `scope_in`) | the child | **none** — but the child is a descendant of the spawner |

**This is the invariant the design rests on:** the daemon reveals a worktree
path only to its owner, to a reservation holder, or to a descendant of one of
those. "Owner terminal, and no other unreleased reservation, and no live
descendant" is therefore precisely "the daemon has told nobody still running
about this path."

#### Latent bug found while establishing this — named, not fixed here

`swarm_spawn` accepts a documented `worktrees: [{worktree, mode}]` argument
(`orchestrator.go:473`, `:515`). It is decoded into the request struct and then
**never read**: `runtime.SpawnInput` (`agents.go:30`) has no worktree field, and
`grep` finds no other use of `in.Worktrees`. So an orchestrator that passes it
gets silence — no reservation row, no `assignment_update`, nothing.

Two consequences for this design:

1. The third row above is the only way a spawned child actually learns a path
   today, which is why `3-tiny-lows-from-task-coder` is working in a worktree
   with no reservation row of its own. **The live-descendant check is therefore
   load-bearing, not belt-and-braces** — it is the sole gate covering spawned
   children. Treat it as such when reviewing §4.3.
2. It bounds the residual risk in §2.11 precisely: a child spawned by agent `B`
   into a worktree owned by agent `A` is a descendant of `B`, not of `A`, so
   neither `A`'s descendant check nor any reservation sees it.

Fixing `swarm_spawn` to honour `worktrees` (creating the reservation row the
gate already checks) would close that hole properly and is the right follow-up.
It is **out of scope here** (§9) — it changes spawn semantics and deserves its
own blast-radius review rather than riding along in a cleanup change.

### Collision warnings

- Live agents are working in `~/.swarm/worktrees` right now
  (`endurio-chat--task-227-tool-loop-lows-cleanup`, owner `active`, live
  session, 3 dirty files). Implementation must not run against the live
  `~/.swarm/swarm.db`; use a fixture DB.
- **Sibling orchestrators share a root.** `full-go-api-migration-orchestrator`
  is `finished` and owns 91 worktree rows; `full-go-api-migration-orchestrator-2`
  is `active` and `running` on the **same** `root_item_id`. Both have
  `parent_agent_id = NULL`, so `-2` is *not* a descendant of `-1` and a
  descendant-only gate would not see it. The invariant above is what covers
  this case: `-2` owns none of `-1`'s worktrees and holds no reservation on
  them, so the daemon never gave it those paths. See §2.11 for the residual
  risk this leaves and why it is accepted.
- The on-disk set is changing continuously; any measurement taken during
  implementation will differ from the numbers above. The *row* counts are
  stable; the *directory* counts are not.
- `internal/runtime/reconcile.go` and `internal/runtime/wake.go` have had
  recent concurrent work (`fix/wake-delivery-reliability`, merged 9855945).

---

## 2. Locked decisions

These are settled. Do not reopen during implementation.

1. **The deletion rules do not change.** `Service.remove`'s dirty →
   `retain("dirty")` and unmerged → `retain("unmerged")` behaviour is the safety
   contract and stays exactly as written. This work adds a *trigger* and
   hardens three edge cases; it never makes removal more permissive. There is no
   `--force`, no "delete anyway after N days", no size-based heuristic.

2. **Reclaim is gated on the owning agent's subtree, not on the root item.** A
   worktree is a candidate only when all of the following hold:
   - `worktrees.state IN ('active','retained')`
   - owner agent `state IN ('finished','acknowledged')`
   - owner agent `finished_at <= now - reclaimGrace`
   - no live session for the owner (the existing `agentHasLiveSession` state
     list: `spawning, running, pause_requested, quiescing, stopping`)
   - no `queued`/`active` agent anywhere in the owner's descendant subtree
   - no unreleased `worktree_reservations` row held by an agent other than the
     owner

3. **`reclaimGrace = 1 hour`, a package constant, not configurable.**
   `Retry` (`agents.go:1333`) gates on *session* state
   (`Completed|Failed|Crashed|Interrupted`) and flips a `finished` agent back to
   `active`. A finished agent therefore stays retryable indefinitely, so
   "finished" is not permanently terminal and a grace window is genuinely
   needed. One hour is long enough to cover a human or orchestrator noticing a
   failure, and this is disk hygiene — latency costs nothing.

4. **Reservations are a veto, never a liveness signal.** Measured: `open_others
   = 0` for all 226 rows, and 91 rows have a `finished` owner still holding an
   unreleased `rw` reservation, because the self-completion path in
   `WriteCheckpoint` releases reservations only for *siblings*
   (`checkpoint.go:334`), never for the completing agent itself. So
   "zero unreleased reservations" would gate out almost everything. The
   *owner's own* reservation is ignored (as `Service.Remove` already does with
   `agent_id <> callerAgentID`); any *other* agent's unreleased reservation
   blocks.

5. **Reclaim runs on its own loop at 10-minute intervals, not inside
   `Reconcile`.** See the 5-second constraint above.

6. **A missing directory is `removed`, never `retained`.** Today,
   `DirtyStrict` on a vanished path fails, `dirty` comes back `true`, and the
   worktree is retained as "dirty" — which on first run would fire ~210
   spurious "Worktree kept" notifications for directories that no longer exist.
   The path-existence check runs before any `git` call.

7. **`retain` only notifies on a genuine transition.** At a 10-minute cadence
   the 30-second notification dedup window (`notify.go:33`) does not suppress
   repeats, so an unchanged dirty worktree would raise "Worktree kept" six times
   an hour forever. `OnRetained` fires only when `(state, retained_reason)`
   actually changes.

8. **Enforcement is a `PreToolUse` deny, not a `Stop` hook.** A `PreToolUse`
   block on `git worktree add|remove` targeting a path under
   `~/.swarm/worktrees` maps to `permissionDecision: "deny"` with a reason the
   agent reads and can act on. Skill text alone is advisory and now contradicts
   a skill the daemon itself installs (§1), which ships a rationalization table
   defending its own behaviour. A deny is not advisory. The `Stop` hook is
   researched and rejected — see §7.

9. **No schema change.** Every predicate above is answerable from the existing
   tables.

10. **Skill text is part of this change, not a follow-up.** The daemon-side
    backstop without the skill fix leaves agents and daemon racing on the same
    paths with the weaker of the two guarantees winning.

11. **Residual risk, accepted and named.** An agent can still discover a
    worktree path out of band — a handoff note, a checkpoint summary, an `ls`,
    or a brief written by an orchestrator that is not the worktree's owner
    (see §1's latent `swarm_spawn` bug: a child spawned by `B` into `A`'s
    worktree is a descendant of `B`, so `A`'s descendant check does not see
    it). The design does not try to detect that. It is bounded by the rules in
    decision 1: a dirty tree retains, an unmerged tree retains, so the only
    thing ever deleted is a tree that is clean *and* fully merged or pushed —
    and a coder actively working in a tree has uncommitted changes essentially
    by definition.
    The worst case is a live agent getting `ENOENT` on a directory whose
    contents exist in git — recoverable with `swarm_worktree op:"create"`, and
    **not** data loss. We accept ENOENT to avoid the alternative, which is
    decision 2 collapsing back into `sweepFinishedRoots`'s root-wide gate and
    reclaiming nothing at all while an epic runs (measured: both live roots
    have a live orchestrator, so a same-root gate reclaims 0 of 226 rows —
    that gate *is* the bug).

---

## 3. DB models

**No migration. No new table, column, index, enum value or relation.**

The design reads existing tables only. For reference, the rows it consults
(`internal/db/schema/0001_init.sql`):

```sql
CREATE TABLE worktrees (
  id             TEXT PRIMARY KEY,
  repo_id        TEXT NOT NULL REFERENCES repos(id),
  path           TEXT NOT NULL UNIQUE,
  branch         TEXT,
  detached_sha   TEXT,
  base_ref       TEXT NOT NULL,
  base_sha       TEXT NOT NULL,
  owner_agent_id TEXT NOT NULL REFERENCES agents(id),
  root_item_id   TEXT NOT NULL REFERENCES items(id),
  state          TEXT NOT NULL CHECK (state IN ('active','retained','removed')),
  retained_reason TEXT,
  created_at     INTEGER NOT NULL,
  removed_at     INTEGER
);

CREATE TABLE worktree_reservations (
  worktree_id TEXT NOT NULL REFERENCES worktrees(id),
  agent_id    TEXT NOT NULL REFERENCES agents(id),
  mode        TEXT NOT NULL CHECK (mode IN ('rw','ro')),
  created_at  INTEGER NOT NULL,
  released_at INTEGER,
  PRIMARY KEY (worktree_id, agent_id)
);
```

Also read: `agents(id, state, parent_agent_id, finished_at)`,
`sessions(agent_id, state)`.

Note the existing `state` CHECK constraint already permits every value this
design writes. `retained_reason` continues to take the three existing string
values `dirty | unmerged | remove_failed` and no new ones.

---

## 4. API and type signatures

### 4.1 Package boundary

The existing split is deliberate: `runtime` decides *who is done*, `worktree`
does *git*. `Sweep`'s own doc comment says so — "the caller has checked the
whole tree is finished." The new code keeps that split.

- **`internal/runtime`** owns the gate (it reads `agents` and `sessions`) and
  the loop.
- **`internal/worktree`** gains one thin exported method that takes a worktree
  and applies the existing rules.

### 4.2 `internal/worktree/worktree.go` — new exported surface

```go
// ReclaimOne applies Remove's rules to one worktree the caller has already
// decided is eligible. It is Sweep's per-worktree body, exported: the same
// lockFor(wt.ID) window and the same call into remove, with no eligibility
// opinion of its own.
//
// It exists because Sweep's *own* eligibility rule — a whole root item
// finished — cannot fire during a multi-week epic (see the spec's §1), and
// runtime needs a way to act on one worktree at a time.
func (s *Service) ReclaimOne(ctx context.Context, wt Worktree) (Worktree, error)

// Candidates returns the worktrees matching an eligibility clause the caller
// supplies, so runtime can express a gate over agents and sessions without
// this package taking a view on either. It is the existing unexported
// query(), exported.
func (s *Service) Candidates(ctx context.Context, where string, args ...any) ([]Worktree, error)
```

`Remove`, `Sweep` and the unexported `remove`/`retain` are unchanged in shape;
`Sweep`'s loop body is refactored to call `ReclaimOne` so there is one copy of
the lock-and-remove sequence, not two.

**Known trade, flagged for review:** `Candidates` exports a raw SQL fragment
across a package boundary, coupled to the `w.` table alias. It is the smallest
change that keeps the eligibility policy in `runtime` where it belongs, and it
is the existing unexported `query()` with no new logic. If a reviewer objects
to the coupling, the alternative is `runtime` computing eligible
`owner_agent_id`s and `worktree` exposing `ForOwners(ids []string)` — same
behaviour, one more round trip, no SQL crossing the boundary. Either is
acceptable; do not spend time relitigating it mid-implementation.

### 4.3 `internal/runtime/reconcile.go` — the gate and the loop

```go
// reclaimGrace is how long an owner agent must have been finished before its
// worktrees become reclaimable. Retry (agents.go) gates on *session* state
// (Completed|Failed|Crashed|Interrupted) and flips a finished agent back to
// active, so "finished" is never permanently terminal and this window is
// real, not decorative.
const reclaimGrace = time.Hour

// ReclaimWorktrees is the per-agent backstop sweepFinishedRoots cannot be.
// sweepFinishedRoots waits for a whole root item to reach done/cancelled,
// which a multi-week epic never does; this waits only for one worktree's own
// owner to be genuinely finished.
//
// It never returns early on a per-worktree failure: one unreadable repo must
// not strand every other worktree behind it. sweepFinishedRoots' fail-fast
// loop is not inherited.
func (s *Store) ReclaimWorktrees(ctx context.Context) error

// ReclaimWorktreesLoop runs ReclaimWorktrees every `every` until ctx is
// cancelled. Same shape as ReconcileLoop, deliberately.
func (s *Store) ReclaimWorktreesLoop(ctx context.Context, every time.Duration)
```

**The gate**, passed to `Candidates` as a `WHERE` clause:

```sql
WHERE w.state IN ('active', 'retained')
  AND EXISTS (
        SELECT 1 FROM agents a
        WHERE a.id = w.owner_agent_id
          AND a.state IN ('finished', 'acknowledged')
          AND a.finished_at IS NOT NULL
          AND a.finished_at <= ?)                      -- now - reclaimGrace, millis
  AND NOT EXISTS (
        SELECT 1 FROM sessions s
        WHERE s.agent_id = w.owner_agent_id
          AND s.state IN ('spawning','running','pause_requested','quiescing','stopping'))
  AND NOT EXISTS (
        SELECT 1 FROM worktree_reservations r
        WHERE r.worktree_id = w.id
          AND r.agent_id <> w.owner_agent_id
          AND r.released_at IS NULL)
ORDER BY w.created_at
```

Each surviving candidate then gets the live-descendant check, run as a
**separate statement per candidate** — the exact query already at
`reconcile.go:818`, reused verbatim rather than inlined as a correlated
recursive CTE (SQLite's support for a correlated `WITH RECURSIVE` inside
`NOT EXISTS` is not something this design needs to bet on, and the candidate
set is small):

```sql
WITH RECURSIVE d(id) AS (
      SELECT id FROM agents WHERE parent_agent_id = ?
      UNION ALL SELECT a.id FROM agents a JOIN d ON a.parent_agent_id = d.id)
SELECT COUNT(*) FROM agents WHERE id IN (SELECT id FROM d) AND state IN ('queued','active')
```

Non-zero means skip. This covers a child still working in the worktree its
parent created — `worktree_reservations` cannot answer that, because
measurement shows `share` is unused in practice (`open_others = 0` on all 226
rows) and children work in the parent's worktree with no reservation row.

Note `finished_at IS NULL` costs nothing here: measured
`SELECT COUNT(*) FROM agents WHERE state='finished' AND finished_at IS NULL`
returns **0**, so the `IS NOT NULL` guard excludes no real rows today and fails
safe (toward keeping) if that ever changes.

### 4.4 `Service.remove` — three hardening edits, same file

```go
func (s *Service) remove(ctx context.Context, wt Worktree) (Worktree, error) {
    // (a) NEW: a path that is already gone is 'removed', not 'dirty'. Without
    // this, DirtyStrict's git call fails, dirty comes back true, and ~210
    // vanished worktrees each raise a "Worktree kept" notification on the
    // first pass. Runs before any git call.
    if !fileExists(wt.Path) {
        return s.markRemoved(ctx, wt)
    }

    dirty, err := s.DirtyStrict(ctx, wt.Path)
    if dirty { /* unchanged */ }

    // (b) NEW: a detached worktree whose HEAD has moved off detached_sha holds
    // commits reachable from no ref anywhere else. Today `wt.Branch == ""`
    // skips mergedOrPushed entirely, so those commits would be deleted with
    // the worktree.
    if wt.Branch == "" && !s.atDetachedSHA(ctx, wt) {
        return s.retain(ctx, wt, "unmerged")
    }
    if wt.Branch != "" && !s.mergedOrPushed(ctx, wt) { /* unchanged */ }
    // ... unchanged from here
}

// markRemoved closes the row without touching git or the filesystem.
//
// ponytail: it does not run `git worktree prune`. Measured today,
// `git worktree list --porcelain | grep -c '^prunable'` is 0 in both repos,
// because the agents doing raw removals prune after themselves. Ceiling: a
// future `rm -rf` with no prune would leave `.git/worktrees/<name>`
// registered, and since PathFor checks the filesystem and not the registry,
// the next Create for the same slug would pick the same base path and
// `git worktree add` would fail with "missing but already registered".
// Upgrade path if that is ever observed: one `git worktree prune` in the repo
// here.
func (s *Service) markRemoved(ctx context.Context, wt Worktree) (Worktree, error)

// atDetachedSHA reports whether a detached worktree's HEAD is still the sha it
// was created at. A command failure reads as "moved" — the same
// cannot-prove-it-is-safe posture DirtyStrict takes.
func (s *Service) atDetachedSHA(ctx context.Context, wt Worktree) bool
```

```go
// (c) retain gains a transition guard. OnRetained raises a user-visible
// notification; at a 10-minute reclaim cadence the 30-second dedup window
// (notify.go:33) does not suppress a repeat, so an unchanged dirty worktree
// would notify six times an hour forever.
func (s *Service) retain(ctx context.Context, wt Worktree, reason string) (Worktree, error) {
    changed := wt.State != "retained" || wt.RetainedReason != reason
    // ... UPDATE as today; call s.OnRetained only when changed
}
```

### 4.5 `internal/hook` — the `PreToolUse` deny (the enforcement half)

This is what stops Gap 2 at the source. `handler.go:447` already has a
shell-command gate in `PreToolUse` with two checks in it (`isClaudeCommand`,
`AttrCheck`); this is a third, in the same block, in the same shape:

```go
// internal/hook/worktreeguard.go (new file, ~25 lines)

// swarmWorktreeMutation matches an agent trying to add or remove a worktree
// under the daemon's directory itself. It is deliberately narrow: `remove` and
// `add` are the two operations that desynchronize the worktrees table, and
// `remove` under ~/.swarm/worktrees is the measured actor behind ~210 stale
// rows (see the spec's §1). `prune` and `list` are read-only-ish bookkeeping
// and are not blocked.
var swarmWorktreeMutation = regexp.MustCompile(`\bgit\b[^|;&]*\bworktree\b[^|;&]*\b(remove|add)\b`)

// worktreeGuardOrchestrator is the reason an orchestrator sees: it can see and
// call the swarm_worktree tool.
const worktreeGuardOrchestrator = "[swarm] ~/.swarm/worktrees belongs to the daemon. " +
    "Use the swarm_worktree tool (op: \"create\" / \"remove\") instead — its remove refuses a dirty " +
    "or unmerged tree and keeps the daemon's records straight, which raw git does not. " +
    "If superpowers:finishing-a-development-branch told you a path under worktrees/ is yours to " +
    "clean up: for a swarm worktree that is wrong, skip that step."

// worktreeGuardWorker is the reason a parented agent sees. swarm_worktree is
// Roles: orchestratorRole (mcpserver/orchestrator.go:18), so a coder cannot
// see or call it: naming that tool to a worker is a dead end, and workers
// following superpowers Step 6 are the population most likely to hit this
// guard. Same ParentAgentID != "" test nativeQuestionRelay already uses.
const worktreeGuardWorker = "[swarm] ~/.swarm/worktrees belongs to the daemon, not to you — " +
    "your orchestrator and the daemon remove worktrees. " +
    "If superpowers:finishing-a-development-branch told you a path under worktrees/ is yours to " +
    "clean up: for a swarm worktree that is wrong. Skip that step and write your " +
    "`completed` checkpoint instead."

// blocksWorktreeMutation reports whether cmd mutates a worktree under
// worktreesDir. worktreesDir empty disables the check.
//
// The directory must appear *after* the remove/add verb, not merely somewhere
// in the command: `git -C ~/.swarm/worktrees/x commit -m "worktree add fix"`
// contains both the directory and a matching verb, and blocking a legitimate
// commit would be worse than the leak this guard prevents.
func blocksWorktreeMutation(cmd, worktreesDir string) bool {
    if worktreesDir == "" {
        return false
    }
    m := swarmWorktreeMutation.FindStringIndex(cmd)
    return m != nil && strings.Contains(cmd[m[1]:], worktreesDir)
}
```

Wired into the existing `if in.Command != ""` block in `decide`'s `PreToolUse`
case, after `isClaudeCommand` and before `AttrCheck`:

```go
if blocksWorktreeMutation(in.Command, h.WorktreesDir) {
    reason := worktreeGuardOrchestrator
    if s.ParentAgentID != "" {
        reason = worktreeGuardWorker
    }
    return adapter.HookDecision{Block: true, Reason: reason}, nil
}
```

`Handler` gains one field, `WorktreesDir string`, set at its single
construction site (`cmd/swarm/daemon.go:266`) from the value
`install.Config.Worktrees()` already computes (`internal/install/config.go:71`).
Empty disables the guard, so every existing `hook.Handler` literal in tests
keeps compiling and keeps its current behaviour.

A `Block` on `PreToolUse` maps to `hookSpecificOutput.permissionDecision:
"deny"` with `permissionDecisionReason` (`internal/adapter/claude.go`), so the
agent reads the reason and can redirect to `swarm_worktree` in the same turn.
That is the difference between this and the skill text: skill text is advisory
and currently contradicted by a skill the daemon itself installs; a deny is
not.

**Deliberately not blocked:** `rm -rf` on a worktree path. Measured
`prunable = 0` says nobody is doing it, and a general-purpose `rm` blocker is a
regex arms race with real false-positive cost inside a worktree. The guard
targets the one measured actor.

### 4.6 `cmd/swarm/daemon.go` — two lines

```go
// in the loops slice, beside ReconcileLoop and WakeLoop:
func(ctx context.Context) { dm.rt.ReclaimWorktreesLoop(ctx, 10*time.Minute) },

// at line 266:
hookH := &hook.Handler{..., WorktreesDir: cfg.Worktrees()}
```

`Service.Log` is already wired (`daemon.go:228`), as is
`OnRetained = rt.OnWorktreeRetained` (`daemon.go:247`).

### 4.7 No changes to

`Service.Share`, `Service.Release`, `Service.Create`, `Service.Review`,
`DirtyStrict`, `mergedOrPushed`, `sweepFinishedRoots`, the `swarm_worktree`
MCP tool, `AttrCheck`, or `runtime.Store.OnWorktreeRetained`. `Remove` and
`Sweep` inherit the `remove` hardening for free, which is the point of putting
it there. `Sweep`'s loop body is refactored to call `ReclaimOne`; its behaviour
is identical.

**One deliberate behaviour change reaches `Remove`:** its *shape* is unchanged,
but via the `retain` transition guard (§4.4c) an orchestrator calling
`swarm_worktree op:"remove"` twice on a still-dirty tree now raises one
"Worktree kept" notification instead of two. This is correct — the second call
changes nothing — but check `internal/worktree/worktree_test.go` for a test
asserting the old repeat-notify behaviour and update it rather than deleting
it.

---

## 5. Screens and user-facing copy

No new screens. This subsystem has no UI of its own; its only user-visible
output is the existing notification, which the menubar renders.

### 5.1 Existing notification — unchanged

`internal/notifyrules/notifyrules.go:41`:

```
worktree.retained | attention | "Worktree kept"
  "The worktree for {ROOT-KEY} has {detail} and was kept."
  sound: swarm.info
```

`{detail}` is `uncommitted changes` or `unmerged commits`
(`agents.go:1750`). Note `remove_failed` currently renders as
`uncommitted changes`, which is wrong but pre-existing; **out of scope** (§9).

### 5.2 Daemon log lines — new, operator-facing only

Exact strings, all via the existing `s.logf`:

```
worktree: reclaimed %s                                  // path
worktree: %s is gone, closing its row                   // path
worktree: keeping %s (%s)                               // path, reason
worktree: reclaim of %s failed, keeping it: %v          // path, err
worktree: reclaim pass: %d reclaimed, %d kept, %d failed
```

### 5.3 Skill copy — `skills/swarm/SKILL.md`

Replace line 22's `Do not create worktrees or branches.` with:

> Do not create, move or remove worktrees or branches. `~/.swarm/worktrees`
> belongs to the daemon, which removes a worktree only after proving it is
> clean and merged. `superpowers:finishing-a-development-branch` Step 6 will
> tell you that a path under `worktrees/` is yours to clean up — for a swarm
> worktree that is wrong. Skip that step and write your `completed` checkpoint
> instead; your orchestrator and the daemon handle the rest.

### 5.4 Skill copy — `skills/swarm-orchestrator/SKILL.md`

Replace line 16's `Remove worktrees when merged.` with:

> Remove worktrees with `swarm_worktree` `op: "remove"` — never
> `git worktree remove`. The daemon's remove refuses a dirty or unmerged tree
> and keeps its own records straight; raw git silently bypasses both and
> strands the record. If the result comes back `state: "retained"`, the tree
> had uncommitted or unmerged work: deal with that, then remove again.

Replace line 49's `remove your worktrees` with:

> remove your worktrees with `swarm_worktree` `op: "remove"`

---

## 6. File list

### Changed

| Path | Change |
| --- | --- |
| `internal/worktree/worktree.go` | `ReclaimOne`, `Candidates`, `markRemoved`, `atDetachedSHA`; `remove` gains the path-missing and detached-sha branches; `retain` gains the transition guard; `Sweep`'s loop body calls `ReclaimOne` |
| `internal/runtime/reconcile.go` | `reclaimGrace`, `ReclaimWorktrees`, `ReclaimWorktreesLoop` |
| `internal/hook/worktreeguard.go` | **New**, ~35 lines: `swarmWorktreeMutation`, `worktreeGuardOrchestrator`, `worktreeGuardWorker`, `blocksWorktreeMutation` |
| `internal/hook/handler.go` | `Handler.WorktreesDir` field; three lines in `PreToolUse`'s existing `in.Command != ""` block |
| `cmd/swarm/daemon.go` | One line in `loops`; `WorktreesDir:` on the `hook.Handler` literal (line 266) |
| `internal/worktree/worktree_test.go` | Tests 1–7, 12–16 per §8.2 |
| `internal/runtime/reconcile_test.go` | Tests 8–11 (the gate) per §8.2 |
| `internal/hook/worktreeguard_test.go` | **New**: guard cases G1–G6 per §8.2b |
| `skills/swarm/SKILL.md` | Line 22 copy per §5.3 |
| `skills/swarm-orchestrator/SKILL.md` | Lines 16 and 49 copy per §5.4 |

### Reused unchanged

`Service.remove`'s dirty/unmerged/remove_failed rules, `DirtyStrict`,
`mergedOrPushed`, `lockFor`, `scanWorktree`, `worktreeCols`,
`runtime.Store.OnWorktreeRetained`, `notifyrules` entry `worktree.retained`,
`reconcile.go:818`'s live-descendant recursive CTE (called, not copied),
`handler.go`'s `PreToolUse` shell-command block, `install.Config.Worktrees()`,
`internal/db` helpers, `execx.Runner`.

### Deleted

Nothing.

### Deliberately not touched

`internal/runtime/reconcile.go` — `sweepFinishedRoots` stays exactly as it is.
It is correct for its own purpose (whole-root teardown) and Reclaim subsumes it
in practice without needing it removed. Deleting it would be a second,
independent behaviour change riding along in this one.

---

## 7. Hooks: what was taken, and what was rejected

The brief asked to weigh a hook-based mechanism. Both candidate events were
researched against `internal/hook/handler.go` and `internal/adapter/claude.go`.

**What the hook system can express.** A handler returns
`adapter.HookDecision{Context string; Block bool; Reason string}`
(`adapter.go:62`). There is no `systemMessage`. On `Stop`, `Block: true` maps to
`{"decision":"block","reason":...}`; elsewhere `Block: true` maps to
`hookSpecificOutput.permissionDecision: "deny"` plus
`permissionDecisionReason`; a non-blocking decision maps to
`hookSpecificOutput.additionalContext`. Events wired for Claude: SessionStart,
UserPromptSubmit, PreToolUse, PostToolUse, PreCompact, Stop.

**Taken: `PreToolUse`.** See §4.5. It is the only mechanism that actually
prevents the collision rather than asking agents not to fall into it, and it
reuses a shell-command gate that already exists in that exact code path.

### Rejected: a `Stop` hook

1. **It cannot reach the population that leaks.** 96 of 226 rows have a
   `finished` owner with no live session — crashed, cancelled, or completed and
   gone. A Stop hook needs a live agent that is still stopping. It addresses
   only the agents that were going to clean up anyway.
2. **Blocking Stop is a hang risk on the exact path that matters.**
   `maxStopBlocks = 3` is already spent by inbox delivery (`handler.go`). A
   fourth reason to block a finishing agent risks stalling it, and a worktree
   is a disk-space concern, not a correctness one. Never trade a hang for
   disk.
3. **Non-blocking Stop context arrives too late to act on.**
   `additionalContext` on a session that is ending is a message nobody reads.
4. **The human-facing channel already exists.** `worktree.retained` fires
   whenever the daemon keeps a worktree it could not safely delete, with the
   reason, through the existing notification path to the menubar. That is the
   right surface for "a human must look at this," and it needs no new wiring —
   it just needs a trigger that actually fires, which §4 provides.

A `Stop` hook is the wrong end of the problem: reclamation matters most
precisely when no agent is alive to hook. Prevention belongs at `PreToolUse`,
where the agent is still running and still about to do the wrong thing.

### Rejected: gating on `sessions.cwd`

The obvious "is anyone inside this directory" gate. Measured dead:
`cwd_kind = 'neutral'` for all 290 session rows, zero `'worktree'`, and no live
session's `cwd` equals any worktree path. Agents are spawned in
`~/.swarm/work/<name>` and given worktree paths as text. A gate on `cwd` would
compile, pass review, and never once fire — worse than no gate, because it
would look like protection.

### Rejected: gating on the owner's brief text

Same class of failure. `full-go-api-migration-orchestrator-2`'s brief is 806
characters and contains no worktree path; `BriefWorktree` has no production
construction site and `Service.ForAgent` has no production caller. Zero
worktrees are protected by a brief-text gate.

### Also considered and rejected

- **Age or size heuristics** ("remove anything over 7 days / 1 GB"). Rejected:
  disk size and age carry no information about whether work is at risk. The
  measured pile includes a clean, merged, finished worktree and a live, dirty
  one at nearly the same age.
- **`git worktree prune` after each removal.** Measured `prunable = 0` in both
  repos: agents doing raw removals are already pruning. No leak to fix, so no
  code. (The 48 `git worktree list` entries across the two repos are other
  tooling's worktrees under `~/GitHub` and `~/.ao`, not swarm's.)
- **Lowering `sweepFinishedRoots`'s gate in place** rather than adding
  `Reclaim`. Rejected: `Sweep` is called from `Reconcile` at 5-second
  intervals and force-releases every reservation in the root before removing.
  Both behaviours are right for whole-root teardown and wrong for a
  continuous backstop.

---

## 8. Verification

### 8.1 Command order

```bash
cd /Users/alexandertar/GitHub/agent-swarm
go build ./...
go test ./internal/worktree/... -run 'Reclaim|Retain|Remove|Sweep' -v
go test ./internal/runtime/... -run 'Reclaim' -v
go test ./internal/hook/... -run 'BlocksWorktreeMutation|WorktreeGuard' -v
go test ./internal/worktree/... ./internal/runtime/... ./internal/hook/... ./cmd/...
go vet ./...
```

Never against `~/.swarm/swarm.db`. Tests build their own fixture DB and
fixture repos, as `internal/worktree/worktree_test.go` already does.

### 8.2 Unit scenarios

Each is one test. The named real-world worktrees are the fixtures to model.

| # | Scenario | Expected |
| --- | --- | --- |
| 1 | Owner `finished` > 1 h, no live session, no live descendants, clean, merged branch — *models `endurio-app--epic-3-task-96`* | removed; `state='removed'`, `removed_at` set; directory gone |
| 2 | Owner `finished`, clean, **unmerged and unpushed** — *models `epic-3-task-102`* | retained `unmerged`; directory intact; one notification |
| 3 | Owner `active` with a live session, dirty tree — *models `endurio-chat--task-227-tool-loop-lows-cleanup`, the live session running during this investigation* | **not a candidate**; no git command runs against it at all |
| 4 | Owner `finished`, tree dirty (untracked file only) | retained `dirty`; directory intact; file still present |
| 5 | Row `active`, directory already deleted — *models the ~210 stale rows* | `state='removed'`; **no** git command; **no** notification |
| 6 | Detached review worktree, owner `finished`, clean, `HEAD == detached_sha` — *models `endurio-app--review-0d2d79d`* | removed |
| 7 | Detached review worktree with a local commit (`HEAD != detached_sha`) | retained `unmerged` — commits are not orphaned |
| 8 | Owner `finished` 10 minutes ago (inside `reclaimGrace`) | not a candidate this pass; candidate after the window |
| 9 | Owner `finished`, but a descendant agent is `active` | not a candidate |
| 10 | Owner `finished`, another agent holds an unreleased `rw` reservation | not a candidate |
| 11 | Owner `finished`, the **owner's own** unreleased reservation — *91 real rows look like this* | candidate (owner's own reservation never blocks) |
| 12 | Already `retained` with reason `unmerged`, branch has since merged | re-evaluated and removed — retained rows are not permanently stranded |
| 13 | Already `retained` `dirty`, still dirty | stays retained; `OnRetained` **not** called a second time |
| 14 | Three candidates, the middle one's repo is unreadable | first and third processed; middle recorded as an error; pass returns all three results |
| 15 | `ReclaimOne` racing `Share` on the same worktree | existing `lockFor` behaviour holds: `Share` either lands before (and blocks the removal) or refuses after |
| 16 | **Sibling orchestrator.** Owner `A` finished > 1 h, no descendants; a *separate* top-level agent `B` (`parent_agent_id = NULL`, same `root_item_id`) is `active` with a running session — *models `full-go-api-migration-orchestrator` / `-2`, live during this investigation* | **is** a candidate. `B` owns no row and holds no reservation, so the daemon never revealed the path to it (§1). Asserts the accepted-risk boundary of §2.11 explicitly, so a future change that widens it has to change this test on purpose |
| 17 | `Sweep` on a finished root, unchanged fixtures | identical outcomes to before the refactor — `ReclaimOne` extraction changes nothing |

### 8.2b `PreToolUse` guard tests (`internal/hook`)

| # | `in.Command` | `WorktreesDir` | Expected |
| --- | --- | --- | --- |
| G1 | `git worktree remove /Users/x/.swarm/worktrees/repo--slug` | set | `Block` |
| G2 | `git -C /some/repo worktree add /Users/x/.swarm/worktrees/repo--new main` | set | `Block` |
| G3 | `git worktree remove /Users/x/GitHub/other-worktree` | set | no block — outside the daemon's directory |
| G4 | `git worktree list` / `git worktree prune` | set | no block |
| G5 | `git worktree remove /Users/x/.swarm/worktrees/repo--slug` | `""` | no block — guard disabled, existing `hook.Handler` literals unaffected |
| G6 | `git status` inside a swarm worktree | set | no block — the path appears but no mutation verb |
| G7 | G1's command, session has `ParentAgentID == ""` | set | `Block`, reason `worktreeGuardOrchestrator` |
| G8 | G1's command, session has `ParentAgentID != ""` | set | `Block`, reason `worktreeGuardWorker` — must **not** name `swarm_worktree`, which a worker cannot call |
| G9 | `git -C /Users/x/.swarm/worktrees/repo--slug commit -m "worktree add fix"` | set | no block — the directory precedes the verb; blocking a legitimate commit is worse than the leak |

### 8.3 End-to-end scenario

1. Build a fixture `$SWARM_HOME` with a fixture repo and **seven** worktrees
   matching scenarios 1, 2, 3, 4, 5, 6 and 13.
2. Run one `ReclaimWorktrees` pass.
3. Assert on disk: scenario 1 and 6 directories gone; 2, 3, 4 and 13 present.
4. Assert in DB: 1, 5, 6 → `removed`; 2 → `retained/unmerged`; 3 → untouched
   `active`; 4 and 13 → `retained/dirty` with `removed_at` still NULL.
5. Assert notifications: exactly two raised (scenario 2's and 4's transitions).
   Not 5's, not 13's repeat.
6. Run a **second** pass with nothing changed. Assert: no new notifications, no
   new git commands against already-`removed` rows, DB byte-identical.

### 8.4 Skip / decline / error / exhaust paths

- **Skip:** every "not a candidate" row must produce zero subprocess calls.
  Assert against the fake `execx.Runner`'s recorded argv, not just the outcome —
  a design whose safety depends on *not* shelling out must prove it did not.
- **Decline:** `git worktree remove` exits non-zero → `retain("remove_failed")`,
  directory untouched.
- **Error:** `git status` fails (corrupt repo) → treated as dirty → retained.
  Existing `DirtyStrict` contract; assert it still holds through `Reclaim`.
- **Exhaust:** 500 candidates in one pass → all processed, no early return, one
  summary log line.
- **Cancellation:** `ctx` cancelled mid-pass → returns promptly with the
  results collected so far; no partial DB state (each worktree's row write is
  its own transaction, as today).

### 8.5 Manual smoke, after tests pass

Read-only inspection of the live DB — **never** a write, never a removal:

```bash
sqlite3 "file:$HOME/.swarm/swarm.db?mode=ro" \
  "SELECT state, retained_reason, COUNT(*) FROM worktrees GROUP BY 1,2;"
du -sh ~/.swarm/worktrees
```

Expected within one hour of the daemon restart: `active` falls sharply as the
~210 vanished rows close, `removed` rises correspondingly, `retained` becomes
non-zero for the first time, and `du` drops. The live dirty worktree must still
be on disk.

---

## 9. Explicitly out of scope

- **Any change to what makes removal safe.** Dirty and unmerged still retain.
  No force flag, no age override, no size override, ever.
- **Deleting or changing `sweepFinishedRoots`.** It stays.
- **Schema changes**, including a `last_reclaim_attempt_at` column. If the
  10-minute cadence proves too chatty against a large retained set, that is a
  follow-up with evidence behind it.
- **Fixing `remove_failed` rendering as "uncommitted changes"** in
  `OnWorktreeRetained` (`agents.go:1750`). Real, pre-existing, unrelated.
- **Releasing the completing agent's own `rw` reservation** in
  `WriteCheckpoint`. The asymmetry against `closeSiblings` is real and is
  documented in §2.4, but this design is explicitly built not to depend on it.
  Fixing it is a separate change with its own blast radius.
- **`git worktree prune`** — measured unnecessary; the ceiling and its upgrade
  path are recorded as a `ponytail:` comment on `markRemoved` (§4.4).
- **Blocking `rm -rf` on worktree paths.** The `PreToolUse` guard covers
  `git worktree add|remove` only — the measured actor. A general `rm` blocker
  is a regex arms race with real false-positive cost inside a worktree.
- **Blocking raw git in the *other* worktree directories** agents legitimately
  own (`.worktrees/`, `~/GitHub/*`). The guard is scoped to
  `~/.swarm/worktrees` and must stay that way; superpowers' cleanup step is
  correct everywhere else.
- **The one orphan directory with no DB row**
  (`~/.swarm/worktrees/endurio-chat--review-task209`). The daemon should not
  delete filesystem objects it has no record of; that is exactly the
  "looks old, must be stale" reasoning this design refuses. Report it, do not
  reclaim it. A `swarm doctor`-style reporter is a separate idea.
- **Making `swarm_spawn` honour its `worktrees` argument** (§1's latent bug).
  It is the right fix for the last uncovered path channel and it would make the
  live-descendant check belt-and-braces again instead of load-bearing — but it
  changes spawn semantics and needs its own review.
- **A `swarm worktree list/gc` CLI surface.**
- **Upstreaming a fix to `superpowers:finishing-a-development-branch`** so its
  ownership heuristic stops claiming `~/.swarm/worktrees`. Worth doing; not
  this change, and the skill text in §5.3 defends against it regardless of
  whether upstream ever moves.
- **Anything touching the currently-running sessions' worktrees.**

---

## 10. Open questions carried as assumptions

None are blocking; each is stated as a decision so implementation never stalls.

1. **10 minutes is the right cadence.** Assumed. It bounds worst-case waste at
   ten minutes of disk and costs at most a few dozen `git status` calls per
   pass. Revisit only with a measurement.
2. **One hour is the right grace.** Assumed, from `Retry`'s indefinite
   retryability. If a retry-after-reclaim is ever observed, the fix is to make
   `Retry` refuse when the worktree is gone, not to lengthen the window.
3. **`acknowledged` is as terminal as `finished`.** Assumed; it is strictly
   later in the lifecycle and `sweepFinishedRoots` already treats the two
   identically.
4. **The `PreToolUse` deny is honoured by every agent kind.** Verified for
   Claude in code (`internal/adapter/claude.go` maps `Block` to
   `permissionDecision: "deny"`). The existing comment at `handler.go:168`
   records that codex/cursor/agy block shapes are UNVERIFIED against a live
   agent — that caveat applies to this guard exactly as it already does to the
   native-question and native-fork blocks beside it. Assumed equivalent; the
   daemon-side reclaim in §4.3 is the backstop for any kind where it is not,
   which is the reason both halves ship together.
5. **A crashed agent's worktree is never reclaimed automatically, on purpose.**
   `reconcile.go`'s `default: // crashed` branch sets the *session* to
   `crashed` but leaves the *agent* `active` — that is the measured population
   of 7 rows with an `active` owner and no live session. The gate excludes
   them, which is right (a crash means work is most likely at risk), but it
   means they age out only when a human retries or cancels the agent. Accepted:
   the alternative is reclaiming exactly the trees most likely to hold
   unrecovered work.
6. **Blocking `git worktree add` under `~/.swarm/worktrees` breaks nothing.**
   Assumed from `skills/swarm/SKILL.md:22` ("Do not create worktrees or
   branches") — agents are already forbidden from creating them, and the
   daemon's own `Create` runs in-process, not through an agent's shell, so it
   never passes through a hook.
