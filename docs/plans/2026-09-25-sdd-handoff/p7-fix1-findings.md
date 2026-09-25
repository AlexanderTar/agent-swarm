# P7 follow-up (Opus review: Approved, spec ✅). Controller rules these load-bearing minors in, since P8/P9 build on them:

1. internal/items/store.go CreateTx/UpdateTx: steps, units, solo, verify (and workflow's task-level Validate) are accepted on Story/Epic/Bug items; spec C1 marks them task-only (a story/epic may carry only its own `workflow` level per B2/B8 — check the spec: story `after_tasks`, root `integration`; keep whatever the spec allows for `workflow` on those levels via workflow.Level, but refuse steps/units/solo/verify on non-task types). Copy: "Only tasks can set steps, units, solo or verify." Add it to spec "All user-facing copy". Test it.
2. validateStepsUnits: refuse a unit with an empty title or no steps, and an empty step string. Copy: "Unit <n> needs a title and at least one step." Add to spec copy. Test it.
3. Add the string "Only an orchestrator or a plan can set workflow, steps, units, solo or verify." to the spec's "All user-facing copy" section (next to the tdd_exempt refusal).
4. Tests for coverage gaps: a user-actor Patch setting Workflow is refused; tdd_exempt + tdd-reviewed stores the spec without the `tdd` gate.

Deferred (don't do): role_hint mismatch refusal, error title quoting, helper consolidation, web optional fields.
Commits: one `fix(items): …` for 1+2+4 (test-first), one `docs(spec): …` for copy. Verify: go test ./internal/items/... ./internal/mcpserver/... ./internal/httpapi/... ; go build ./... && go vet ./... (no ./cmd, no ./...).
