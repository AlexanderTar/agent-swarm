# P9 fix round 1 re-review: partial, stopped by the coordinator

Head 9704206, base b749c63. The checkout was left untouched (git status clean, HEAD 9704206). All probes ran in scratch copies made with `git archive`: scratchpad/p9head, p9rev1, p9rev2, p9base.

## Per-finding verdicts reached

- **USER DIRECTIVE: NOT ADDRESSED (crash/cancel window still open).** workflow.go:929 inserts the round-2 build row as 'waiting' with no agent. :968 closes the old session, :974 calls Retry, and only :990-992 claims the row for prevAgentID. Two cases break this:
  - **Crash between the Retry and the claim.** After the restart, recoverWorkflows (:1350) does not exclude the workflow because no run is active with a live session. advance→fillWaitingRuns then spawns a fresh builder while the retried builder is still live. The probe `scratchpad/p9head/internal/runtime/zz_probe2_test.go` (TestProbeCrashBetweenRetryAndClaim) shows it: "live coder sessions=2, round2 agent=<new>".
  - **No crash needed.** agents.go:1530 IdemTx can fail after startSession (:1526) has already started the pane, for example when the ctx is cancelled. The checkpoint trigger runs advance on the MCP request ctx (checkpoint.go:1403; it is not WithoutCancel). Retry then returns an error, so :984 marks the row failed with no agent_id (or the UPDATE fails on the same ctx and the row stays waiting). AutoRetry's AgentID=="" branch (:1143) fresh-spawns while the retried session is live.
  - **Fix.** Claim the row with agent_id=prevAgentID before the close and the Retry. On Retry failure, mark it 'failed' but keep agent_id, so applyAutoRetry retries the same agent. Heal must then also treat an active run whose latest session is Completed and older than the run as stranded.
- **Addendum residual (old builder agent left 'active'): NOT harmless, fix now.** liveDescendants (reconcile.go:913-921) counts `state IN ('queued','active')` without NotAZombieSlot. It feeds two things:
  - owesNothing (:945). The orchestrator is then never "waiting": it gets spurious agent.stale alerts, and checkProgressDeadlock is skipped.
  - The worktree reclaim gate (:1059), which blocks reclaim.
  - swarm_read also shows the agent as active indefinitely.
  - startSession errors after the session row insert (agents.go Launch/PreRun/Kill paths) leave that session 'spawning' (live), so "latest session non-live" is not always true either.
  - Same one-line fix as above: keep agent_id on the failed row (workflow.go:984), so the SAME builder is retried and no orphan is created.
- **I1: ADDRESSED.** healStrandedActiveRuns is at workflow.go:463-499, and the recovery exclusion at :1350-1356. Scratch-revert check: disabling the heal and restoring the old SQL makes all three TestHealStrandedActiveRun* tests fail, so they are genuine RED tests.
- **I2: PARTIALLY ADDRESSED.**
  - Done: claim before Share (:872-890), Spawn error → 'failed' (:846-866), validateWorktrees (:121), and the one-live error mapping.
  - Scratch-revert check: moving the claim back after Share makes TestShareFailureDoesNotDoubleSpawn fail, so it is a genuine RED test.
  - Open: WithoutCancel was applied only at Start (:227) and Resume (:1527). The checkpoint trigger (checkpoint.go:1403) and the reconcile triggers still run under the caller ctx, so the ctx-cancel-after-Spawn/Retry window remains (see the directive).
- **I3: NOT ADDRESSED on the real replay path.** The re-check sits in applySpawn (:571-596). But Next only returns Spawn for *missing* roles (internal/workflow/next.go:219-226). If every role row exists, Next returns Wait, applySpawn never runs, and fillWaitingRuns spawns the reviewer with no worktree.
  - The probe `zz_probe3_test.go` (TestProbeReplayViaAdvanceAttachesReviewWorktree, gated spec) shows it: after the replay advance the reviewer was spawned with wt="".
  - The shipped test calls applySpawn directly with an action Next never produces in that state.
  - Fix: attach in spawnRunAgent/fillWaitingRuns when a review row has a SHA and review_worktree_id is NULL.
- **I4: ADDRESSED.** roundFindingLines (:788) reuses findFixStepsFor/fixRoundFindings, and the note persists via appendWorkflowContext (:1420). Minor: the resume note stays in context_json permanently, so it reaches every later brief (reviewers and later rounds), including the not-bumped case where deliverNote also sends it.
- **I5: ADDRESSED.** resumeBumpsRound is now a one-line call to workflow.Next (:1410-1412). I re-ran the previous probe with the new signature: the mixed failure, stale changes_requested and blocked cases all give bump=true, then Spawn{build, round 2}.
- **I6: ADDRESSED** (reconcile.go:635-665). TestPauseDeadlineLeavesWorkflowRunActive passes.
- **I7: ADDRESSED**, best-effort as documented (:1286-1330, :729-738). Minor: if an older sibling's advance keeps erroring before it reaches fillWaitingRuns, every newer workflow of the same owner yields forever.
- **I8: ADDRESSED.** Root checks are at :1471 and :1628. Cancel holds the lock, flips state first and marks runs cancelled; agents.go Cancel does not re-enter advance, so there is no deadlock. Resume is guarded with `AND state='escalated'`.
- **I9: ADDRESSED** (agents.go:1509-1514, workflow.go:1438-1456).

## New breakage (unconfirmed, probe interrupted)

- **Likely Important: checkpoint.go:1217-1227, removal of toCloseFailedSelf.**
  - After a workflow agent's FailedCkp, the session is marked 'failed' but its pane stays alive. Reconcile only scans live sessions (reconcile.go:98), so nothing kills that pane.
  - When AutoRetry runs Retry, startSession kills the pane by name. When the budget is spent and the workflow escalates, no Retry happens, so the failed agent's process keeps running in the rw worktree, unseen by reconcile.
  - A resume that bumps the round then spawns a fresh builder beside that still-running process, which is effectively the directive's two-builders case.
  - Unconfirmed: the probe `zz_probe4_test.go` needs a rw worktree (seedOwnedRepoWorktree) and was stopped before it ran.
  - The premise "startSession kills the stale pane" holds only when a Retry actually happens.

## Not done

- Legacy guarantee: the only pre-P9 test file touched is agents_test.go, which gains a `startErr` field on fakeTmux and a guard in Start. That is a helper change, not a test function change. I did not diff pre-P9 test functions exhaustively.
- Pass through the workflow_test.go diff (~1,200 lines) for asserting-nothing tests.
- The go test / vet / race runs. I only ran the round's 17 named tests at head, and all passed.
- Resume-bumped fresh builder spawn (spawnRunAgent) has no "previous-round builder not live" guard. This is an out-of-scope observation: I4 accepted the fresh spawn.

Verdict so far: **Needs fixes** (the directive's crash/cancel window, I3 on the real path, the residual). The toCloseFailedSelf regression is pending confirmation.
