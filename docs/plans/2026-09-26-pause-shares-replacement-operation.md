# Pause shares the replacement operation (follow-up plan)

**Goal:** Pause records a `mode: pause` operation, assembles the handoff
manifest from its handoff checkpoint, and passes the same ready gate as
Handoff, closing the deferral recorded in
`docs/specs/2026-09-25-agent-continuity-and-handoff.md` §1/§3.

**Current state:** Pause already shares the notice path, deadline and hook
policy with Handoff (`requestPreservationTx`, `TickPause`,
`PreservationCommandAllowed`). It lacks the operation row, manifest and ready
gate. Safe meanwhile: nothing auto-launches after Pause, and Resume re-checks
HEAD/status and durable state.

**Why it was deferred:** session-scope Pause is easy, but subtree Pause has
its own state machine in `internal/runtime/pause.go` (cascade, deepest-first
promotion, combined handoff, daemon fallback checkpoint at the deadline).
Wiring operations through it needs one operation per paused descendant plus
a root operation whose ready gate waits on the children, without breaking
`subtreeRoleOf`'s invariants.

## Tasks (strict TDD: failing test, watch it fail, minimal code, pass, commit)

1. RED `TestSessionPauseRecordsOperation` (`internal/runtime/pause_test.go`):
   `Pause(name, "session")` on a running worker creates one nonterminal
   `mode: pause` operation in `preserving`; a duplicate Pause is a no-op, a
   Handoff during it returns 409 naming the operation.
   GREEN: `pause()` inserts the operation in the same transaction as
   `requestPreservationTx` (roleIdle only); `ModePause` in
   `stopPredecessor` parks the session `paused` and lands `succeeded`.
2. RED `TestPauseHandoffCheckpointBindsManifest`: the handoff checkpoint
   writes the manifest and runs `ValidatePreservationReady`; a dirty tracked
   tree blocks with the path named, and the session still parks paused
   (never launches). GREEN: `bindHandoffCheckpoint` accepts `ModePause`.
3. RED `TestResumeAfterPauseCarriesRecoveryBundle`: Resume after a pause
   operation serves the manifest in the first sync's recovery bundle.
   GREEN: `SyncRecovery` reads the latest pause operation too.
4. RED `TestSubtreePauseRecordsPerAgentOperations`: `Pause(orch,
   "subtree")` records one operation per cascaded descendant and one for the
   root; the root's operation stays `preserving` until every descendant's
   operation is terminal; the deadline fallback checkpoint marks the root
   operation blocked with "orchestrator did not respond", never succeeded.
   GREEN: `cascadeSubtreePause` and `promotePendingSubtreePauses` write the
   operations; `subtreeRoleOf` is unchanged.
5. RED `TestPauseOperationSurvivesRestart`: close and reopen the store
   mid-pause; `ResumeOperations` continues without launching a successor.
6. e2e: extend `scripts/e2e/pause_test.go` scenario 06 to assert the pause
   operation, manifest path, and `succeeded` phase; scenario 08 asserts one
   operation per paused agent.

## Verification

`go test ./internal/runtime/ -run 'Pause|Replacement' -count=1`,
`go test -race ./internal/runtime/...`, `go test ./... -count=1`,
`scripts/e2e.sh -run 'Pause|Handoff'` (isolated home and port).

## Out of scope

Changing Pause's user-facing copy or the menubar; auto-launch after Pause.
