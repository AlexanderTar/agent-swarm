# Plan: gate `completed` checkpoints to the item types that can consume them

Spec: `docs/specs/2026-09-22-checkpoint-completed-item-type-gate.md`
Branch/worktree: `fix/checkpoint-completed-item-type` at
`../agent-swarm--checkpoint-item-gate`

## Task 1 — failing test: `WriteCheckpoint` refuses `completed` on Story/Epic/Bug

**File:** `internal/runtime/checkpoint_test.go`

1. Read the file's existing `WriteCheckpoint` test setup (look for an
   existing test that spawns a `coder` on a Task and writes a `completed`
   checkpoint — copy its fixture pattern rather than inventing a new one).
2. Add `TestWriteCheckpointRefusesCompletedOnAStory` (and, same shape,
   `...OnAnEpic`, `...OnABug` — three cases, same helper, table-driven if the
   file already uses a table for `WriteCheckpoint` variants, else three
   focused tests):
   - Seed a Story (or Epic/Bug) item, spawn a `coder` on it directly (the
     multi-task-worker case from the spec — assignment is the parent, not a
     task).
   - Call `WriteCheckpoint` with `Kind: CompletedCkp`, no `ItemKey` override.
   - Assert: error returned, `items.CodeBadRequest`, message contains
     `"Completed checkpoints attach to tasks"`.
3. Add `TestWriteCheckpointAllowsCompletedOnASpikeWithResolution` only if no
   existing test already covers a Spike + `completed` + `resolution` path —
   grep first (`grep -n "Spike" internal/runtime/checkpoint_test.go`); if one
   exists, extend it with a one-line comment noting it's the Spike exemption
   for this gate, don't duplicate the fixture.
4. Run: `cd internal/runtime && go test ./... -run WriteCheckpointRefuses` —
   confirm it fails (no guard exists yet) with a clear "expected error, got
   nil" or similar, not a panic. This is the required failing-test checkpoint
   before touching `checkpoint.go`.

**Produces:** a red test proving the gap.

## Task 2 — implement the guard

**File:** `internal/runtime/checkpoint.go`

1. In `WriteCheckpoint`, locate the existing `if in.Resolution != ""` block
   (around line 259, right after the `Integrated` kind check). Add the new
   check immediately after that block, still inside the same
   `IdemTx`/transaction closure, before the `ckpID := ids.New("ckp")` line:

   ```go
   if in.Kind == CompletedCkp && it.Type != items.Task && it.Type != items.Spike {
       return &items.Error{Code: items.CodeBadRequest,
           Message: fmt.Sprintf(
               "Completed checkpoints attach to tasks, not %ss. Pass item: \"<TASK-KEY>\" for the task you finished.",
               it.Type)}
   }
   ```

2. Confirm `items.Type`'s underlying value formats lowercase (`"story"`,
   `"epic"`, `"bug"` — check `internal/items/model.go:12-16`, already
   confirmed lowercase string consts) so `%ss` reads naturally ("not
   storys" — check this before committing; if the plural reads wrong for
   any value, e.g. it doesn't pluralize cleanly, hardcode a small
   `map[items.Type]string` instead of `%ss`. Verify with a quick `fmt.Println`
   or just inspect the consts — don't guess, this is user-facing copy).
3. `fmt` and `items` are already imported in `checkpoint.go` — no import
   changes needed (confirm with `goimports -l` after editing, should report
   nothing).

**Produces:** the guard exists; Task 1's tests should now pass.

## Task 3 — verify Task 1 goes green, no regressions

1. `cd internal/runtime && go test ./... -run Checkpoint` — all pass.
2. `cd internal/runtime && go test ./...` — full package, no regressions
   (a legitimate Task-scoped or Spike-scoped `completed` checkpoint must
   still work unchanged).

## Task 4 — deny-copy update in `checkTask`

**Files:** `internal/items/transition.go`, `internal/items/transition_test.go`

1. `grep -rn "No agent has reported it complete" internal/` — find every
   place (prod and test) that references the exact string. Expect at least
   `transition.go:179` and one or more assertions in `transition_test.go`
   (and possibly `internal/httpapi` or `internal/mcpserver` tests that
   assert on denial copy passed through — check those too).
2. Change `transition.go:179` from
   `"Couldn't move %s to Done. No agent has reported it complete."` to
   `"Couldn't move %s to Done. No agent has reported it complete on this task."`.
3. Update every test assertion the grep found to the new string. Do not
   delete a test to make it pass — update its expected string
   (per this repo's test-discipline rule: a test/copy mismatch is fixed by
   updating the test, never by removing it).

**Produces:** copy is accurate; no stale assertions.

## Task 5 — full verification

1. `go build ./...` from repo root.
2. `go test ./...` from repo root — full green.
3. `gofmt -l .` — no output (formatting clean).
4. Re-read the diff once (`git diff`) against the spec's File list — confirm
   nothing outside the listed files changed.

## Task 6 — commit

1. `git add internal/runtime/checkpoint.go internal/runtime/checkpoint_test.go internal/items/transition.go internal/items/transition_test.go docs/specs/2026-09-22-checkpoint-completed-item-type-gate.md docs/plans/2026-09-22-checkpoint-completed-item-type-gate.md`
   (explicit paths — this is a fresh worktree so there's no shared-worktree
   risk, but stay in the habit).
2. Commit message: summarize the root cause and the fix, reference that
   STORY-27's unstick is a separate operational step per the spec.
3. Do not merge/PR without the user's go-ahead — report back with the diff
   and test output first.

## Explicitly not doing

- No change to `swarm_spawn`, `runtime.Spawn`, `promoteDraft`,
  `swarm_control retry`, or `isDescendant` (spec's locked decisions 2, 5).
- No STORY-27 remediation in this branch (spec's locked decision 4) — that's
  a `swarm_spawn`/`swarm_checkpoint` operational sequence against
  TASK-78/79/80 directly, done live against the daemon, not a code change.
