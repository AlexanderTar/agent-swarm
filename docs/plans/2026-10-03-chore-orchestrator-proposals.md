# Chore Orchestrator Proposals — Implementation Plan

Spec: `docs/specs/2026-10-03-chore-orchestrator-proposals.md`. Item: BUG-26.
Root cause is known (deliberate guard), so this is one `tdd-reviewed` package, not a debug package.

## Unit 1 — Lift the guard (TDD)

1. In `internal/items/store_test.go`, rewrite `TestChoreRootCannotProposeTopLevel`
   as `TestChoreRootCanProposeTopLevel`: for epic, bug, chore, spike(feature),
   `s.Create(ctx, in, items.Orchestrator("agt_1", ch.ID))` succeeds with
   `Status == items.Draft` and `OriginSpikeID == ch.ID`; `COUNT(items) == 5`.
   Also assert `Status: items.Ready` from the chore orchestrator is refused with
   `A proposed top-level item starts as Draft. The user starts it.`
2. Run `go test ./internal/items/ -run TestChoreRootCanProposeTopLevel`; watch it fail (RED).
3. In `internal/items/store.go`, delete the `if own.Type == Chore {...}` guard and the
   now-unused `own` lookup if nothing else uses it; update the comment to note chores may propose (BUG-26).
4. Run the test; GREEN. Commit.

## Unit 2 — Skill text and spec pointer

1. Edit `skills/swarm-orchestrator/SKILL.md` Chores bullet per spec §6; run `make skills-sync`.
2. Add `> Superseded by docs/specs/2026-10-03-chore-orchestrator-proposals.md (BUG-26).` under decision 3 of `docs/specs/2026-09-27-chore-and-spike-output-ready.md`.
3. `git diff --exit-code internal/install/skills` after sync is clean relative to the staged copy; commit.

## Verify

- `go vet ./...`
- `go test ./...`
