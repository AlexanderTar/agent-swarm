# P6 review of f97734e (Opus) — fix these

## Important. internal/workflow/next.go:274-276: pinnedFrom's new stale filter breaks the resume path after the new escalation
A stale changes_requested row can only exist at the previous round if the reviewed step re-completed in that same round — exactly the case that now escalates. So the only way to reach round+1 with such a row is an orchestrator `resume retry`.
Reproduced: two-pair spec (a, ra, b, rb). Round 1 escalates "reviewer reviewed b1, but b is now at b1new". After resume, with b's round-2 row active or completed, Next(…, 2, 1) returns Spawn a (coder) — a new coder on step a, which already passed review. Spec line ~737: resume re-runs the fix step. Before f97734e the stale row pinned from b and Next returned Wait b.
A stale row's sha can't be trusted, but which step it belongs to can.
Fix: drop the stale filter in pinnedFrom so a stale changes_requested/blocked row at the previous round pins from its fix step again. Flip the "clearly distinguishing" subtest of TestNextPinnedFromIgnoresStaleReviews to expect Wait b (rename the test to match). Remove completedSHAAt if unused.
Also: a stale *pass* row at the previous round (escalated via the stale path, then resumed) should pin at the review step's fix step too, instead of falling through to "pin everything" and returning Spawn a. Add a test.

## Minor (fix in the same round)
- next.go:186-192: a fresh blocked verdict is hidden when a stale sibling triggers the stale escalation. Run findBlocked on the fresh runs before returning the stale escalation; test it.
- next.go:408-425: stepSHA duplicates the currentRuns closure plus a completed-run scan; completedSHAAt is a third copy. Reuse one helper.
- next_test.go S12 "literal" subtest can't fail by its own comment — delete it or fold into the fixed distinguishing test.
- Spec gap: add the stale-review reason "<role> reviewed <sha7>, but <of> is now at <sha7>" to spec B4's Escalate row and to "All user-facing copy" in docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md, and state that `resume retry` after it re-runs the fix step (then the review at the fresh sha), consistent with line ~737.

Reviewer's probe file (scratch, may help): /private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/p6probe/workflow/probe_test.go
