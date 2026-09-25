# P9 fix round 2 scoped re-review — remaining findings

Range `b749c63..4820c88`. Reviewer verdict: Needs fixes. Scratch probes at
`/tmp/p9-rereview2.KN9DGQ/internal/runtime/zz_rereview_probe_test.go`.

1. **Binding directive, Important:** `applyRetryFix` commits the round bump
   (`workflow.go:937`), then inserts an unowned waiting row (`:949`), and
   binds the old builder only at `:984`. A crash in either intermediate
   state lets recovery spawn a second live builder. Resolve the builder
   first; commit round bump and an already-bound run atomically. Test
   recovery after the round change and after insertion.
2. **Orphan active agent, Important:** after repeated `Retry`/`startSession`
   errors and escalation, the original agent remains `active`, its latest
   session is `failed`, and `liveDescendants` counts it, blocking worktree
   reclaim. Finalize exhausted failed agents with no live session; cover
   early `startSession` errors that leave `spawning` session rows. Keep
   agent-list visibility and descendant/reclaim checks consistent.
3. **Coverage:** add the stale `changes_requested` regression case from I5.
   Strengthen replay test to require successful reads, active state, a
   nonempty agent ID and worktree. The cancel-race test is sequential; either
   make an actual overlap or name it for the behavior it tests.

All other original I1-I9 mitigations and the failed-pane correction were
marked addressed. Preserve those fixes and the legacy-task behavior.
