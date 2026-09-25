# P8 fix round 1 (Opus review) + controller rulings. Legacy guarantee verified intact (test files insertions-only) — keep it that way.

Controller rulings (amend spec B5 accordingly in one docs(spec) commit, then implement):
R1. closeCompletedSiblings: legacy tasks (workflow_json NULL) — an orchestrator caller keeps today's behaviour (closes other live sessions on the item); other legacy callers: same role. Workflow tasks: same role + same step, no orchestrator exemption.
R2. completedCurrent's workflow branch counts the most recent `completed` among build roles = any role except reviewer, ui_reviewer, orchestrator (so designer/researcher templates can finish). Legacy branch unchanged.
R3. tdd gate "fix round" = a run whose step is the fix target of a review step's loop (Loop.Fix, falling back to Of) AND that review step has reviewer rows at round-1 with changes_requested/blocked. Findings are read only from those reviewer rows. Otherwise (first run of the step at any round) → every unit. Zero findings in a genuine fix round (e.g. blocked + resume retry) → the package-wide rule (≥1 red→green pair). Unit-tagged findings on a non-batched task → treated as package-wide.

Findings to fix:
1. (Important) checkpoint.go:376-395 tddGate — implement R3. Tests (red first): round 2 with a named unit missing → units error; package-wide finding with no pair → round copy; round-1 evidence doesn't carry into round 2; multi-loop first-run case (design→review-design→build→review-build: build's first run at round 2 needs every unit); findings from multiple reviewers of the same review step merge.
2. (Important) checkpoint.go:832 — implement R1 (`callerRole == RoleOrchestrator && it.Workflow == nil`). Test: orchestrator completed on a workflow task leaves the builder Running.
3. (Important) transition.go:295 — implement R2. Test: design-reviewed designer completed makes completedCurrent true.
4. (Important) checkpoint.go:212 applyGates fails open when run.StepID isn't in the item's workflow (or it.Workflow nil with a run): return an error (spec has no copy — use "workflow step <id> not found on <KEY>; ask your orchestrator." and add it to the spec copy list).
Minors (fold in):
5. Commit gate copy (:544): no git entry for an rw repo → say so ("no git entry for <repo>"); drop the trailing period to match the spec; zero rw worktrees → refuse ("no rw worktree shared with you") — check the spec copy list first and match it if present.
6. Validate verdict value whenever non-empty (any checkpoint kind) with the spec copy instead of a raw SQL CHECK error; refuse pass+major/critical regardless of hasRun only on completed with a run (keep legacy lenient behaviour otherwise? — no: legacy never sent verdicts; refusing an invalid enum value everywhere is fine).
7. tools.go findings schema: severity enum critical|major|minor|nit; `file` optional (package-wide findings); server-side validate severity.
8. Artifact gate: filepath.Clean before HasPrefix; expand a leading "~/"; error copy uses the real home path, not a hardcoded ~/.swarm when SWARM_HOME differs; register on every completed (fix-round revisions recorded) — dedupe identical path.
9. Surface json.Unmarshal errors at :251/:276.
10. Tests: same role different step → not closed; OnDepUnblocked dedup with two agents under one parent.
11. Export workflow's sha7 and reuse it; fix workflow/spec.go:13 GateTDD comment ("same attempt" → round scope); remove `_ = it` dead code.
Verify: go test ./internal/runtime/... ./internal/items/... ./internal/mcpserver/... ./internal/workflow/... -count=1 ; go build ./... && go vet ./... ; go test ./... once.
