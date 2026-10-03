# CHORE-31 plan: open agent-swarm bugs + stale-root close

Spec: `docs/specs/2026-10-03-chore-31-open-bugs.md`. Integration branch
`chore-31-integration`. Each unit is strict TDD: failing test, watch it fail,
minimal fix, watch it pass, commit.

## Package A — root lifecycle (`chore-31-a-root-lifecycle`, tdd-reviewed)

1. **Widen resolve (BUG-33).** Test in `internal/items` (resolve test file next
   to `resolveTx`): In progress, In review and Blocked roots resolve to Done by a
   Done root; descendants not Done are cancelled; Done/Cancelled/Awaiting
   approval refused with "Only an open item can be resolved by another item; %s is %s."
   Fix: `transition.go` resolveTx eligibility (~line 1037).
2. **Refuse completed after approved finish (BUG-33).** Test in
   `internal/runtime` checkpoint tests: root orchestrator writes `integrated`,
   finish approved, then `completed` on the root is refused with the spec copy;
   after `finishing` it is not refused. Fix: `checkpoint.go` completed path.
3. **Integrated without git for a chore (BUG-31).** Test: chore root,
   orchestrator `integrated` with verification + waivers and no `git` is
   accepted and opens `accept_fix`; then a `finishing` with only `kept` (or empty
   for a no-repo chore) moves it to Done. Epic/bug without git still refused.
   Fix: `checkpoint.go` ~1381 and the finishing validation.
4. **Cross-root edits (EPIC-7).** Tests in `internal/mcpserver` / `internal/items`:
   a top-level orchestrator creates a task under a story of a Ready root with no
   live orchestrator, and updates its brief; refused when the foreign root is In
   progress or has a live orchestrator agent, and for status changes. Event
   `item_cross_root_edit` recorded. Fix: `store.go` ~579 scope check and
   `mcpserver/orchestrator.go`.

Verify: `go vet ./...`, `go test ./internal/items/... ./internal/runtime/... ./internal/mcpserver/...`.

## Package B — CLI, hooks, cleanup (`chore-31-b-cli-hooks-cleanup`, tdd-reviewed)

1. **BUG-32.** Test in `cmd/swarm`: stub daemon returns the real wrapped GET
   `{"item":{...,"repos_version":N}}`; `swarm start KEY --repo PATH` sends
   `repos_version` N. Update existing fakes to the wrapped shape. Fix:
   `runtime_cmds.go` ~185, mirroring 909bd43.
2. **BUG-29.** Test in `internal/hook`: Claude PreCompact output has no
   `hookSpecificOutput` and carries the reminder in top-level `systemMessage`;
   codex/agy outputs unchanged. Fix: `handler.go` ~712.
3. **BUG-30.** Debug first. Test reproducing a reclaim pass that fails mid-remove
   or runs concurrently with another pass: the record stays `retained` with the
   error and the result is not counted removed; two passes on one tree are
   serialized. Fix in `reconcile.go` ReclaimWorktreesWith / `workdirs.go`.

Verify: `go vet ./...`, `go test ./cmd/swarm/... ./internal/hook/... ./internal/runtime/...`.

## Package C — skills (`chore-31-c-skills`, mechanical, after A)

1. `skills/swarm-coder/SKILL.md`: BUG-28 continue-through-units rule (spec decision 8).
2. `skills/swarm-orchestrator/SKILL.md`: after an approved finish write
   `finishing`, never `completed`; resolve covers open roots; chores with no repo
   changes may write `integrated` without `git`; cross-root edits on Draft/Ready
   roots without a live orchestrator.
3. `make skills-sync`.

Verify: `make skills-sync && git diff --exit-code`, `go test ./internal/install/...`.

## Integration

Merge A, B, C into `chore-31-integration`; run `go vet ./...`, `go test ./...`,
`make skills-sync && git diff --exit-code`; final reviewer on the integrated sha.
Finish: merge to main, push, `make install`, restart daemon. Then resolve the
stale roots and fixed bugs, and add EPIC-7 context/subtasks via the new
cross-root edit.
