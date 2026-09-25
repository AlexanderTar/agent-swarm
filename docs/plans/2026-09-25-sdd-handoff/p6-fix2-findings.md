# P6 fix round 2 — one regression from round 1 (Opus re-review)

- next.go:399-406 vs 412-427: pinnedFrom phase 2 (stale row) outranks phase 3 (failed/cancelled), so a crash escalation resumes at the wrong step when an earlier review in the same round is stale.
  Repro: round 1: `a` completed a1new, `ra` pass at a1 (stale), `b` failed with retries exhausted. Next escalates "mechanical failed after N auto-retries". Then Next(two, runs, 2, 1):
   - current code: Spawn a (coder) — wrong
   - base 23b5368: Spawn ra @ a1new — right per spec B7 ("when the escalation was a crash, re-runs the crashed step"), dropping the stale ra, re-reviewing, continuing to b.
  Fix: run the failed/cancelled scan before pinFromStale, skipping stale review rows the way the failure loop does at next.go:138-144. The stale-crashed-rb resume must still reach phase 2 and return Spawn b (existing test).
  Add a test pinning phase ordering for crash + stale.
  Reviewer probe: scratchpad/p6fix-r1probe/workflow/probe2_test.go
