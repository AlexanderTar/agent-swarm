## Global Constraints

- Everything ships together in one release; no compatibility shims between daemon, web and menubar.
- Legacy behaviour is preserved exactly for tasks whose `workflow_json` is NULL (existing epics, board-created tasks). Every change to checkpoint, transition or spawn code keeps today's legacy-task tests green.
- Never delete or weaken an existing test to make a new one pass. Tests whose expectations change intentionally are named in the unit that changes them.
- After any edit under `skills/`, run `make skills-sync`; `go test ./internal/install/...` enforces the mirror.
- **One commit per unit** (conventional message, e.g. `feat(install): …`, signed, repo style). Review fix rounds add commits; never amend.
- Every Go unit keeps `go build ./... && go vet ./...` green. Web units also run `cd web && pnpm typecheck && pnpm test`. Menubar units also run `cd apps/menubar && swift test`.
- Hot files: `internal/runtime/checkpoint.go`, `internal/runtime/agents.go`, `internal/items/transition.go`. Pull or rebase before starting P7–P10.
- **Package review loop** (every package, once, after all its units are committed):
  1. Run the package Verify set.
  2. Request review with `superpowers:requesting-code-review` against the package Acceptance, walking the diff unit by unit.
  3. Handle findings with `superpowers:receiving-code-review`. Fix, re-run Verify, and commit.
  4. Repeat until the review passes. Stop after the workflow's `max_rounds` and escalate to the human.
  5. If one unit collects all the findings two rounds in a row, split it into a follow-up package instead of re-running the whole package (`swarm-batching` split trigger).

## Review Focus

Spec inputs most likely to be under-tested. Reviewers check these explicitly:

1. Legacy tasks (no workflow) behave byte-for-byte as before across checkpoint, transition and spawn paths.
2. Engine idempotency: a repeated `advance` (daemon restart, duplicate trigger) never double-spawns (`UNIQUE (workflow_id, step_id, round, role)`).
3. `closeCompletedSiblings` no longer tears down a builder when a reviewer completes on the same task, and vice versa.
4. Symlinked skills are actually discovered by each agent CLI. The P1 empirical check decides where the copy fallback is used.
5. Relay suppression: the orchestrator receives exactly one `workflow_succeeded` per successful package. It still receives `blocked`/`failed` checkpoints and questions from step agents.
6. No role is ever inferred. A task without a workflow is rejected at plan registration and on orchestrator `swarm_items create`.


### P6: Workflow model (`internal/workflow`)
**Workflow:** `tdd-reviewed` · **Units:** 4 · This package is the contract every later package consumes.

**Files:** `internal/workflow/{spec.go,templates.go,validate.go,resolve.go,render.go,next.go}` and their tests, plus `testdata/`.

**Interfaces (produces):**
- Spec B2 types: `Gate`, `Loop`, `Step`, `Integration`, `Spec`.
- `Level`, `Validate`, `Resolve`, `RunRole`, `Render`, `Templates`.
- `Run`, `Finding{Severity, File string; Line, Unit int; Summary string}`, `Action`, `Next`, defined as follows:

```go
type Run struct {
	StepID      string
	Round       int
	Role        string
	State       RunState // waiting|active|completed|failed|cancelled
	Verdict     Verdict  // ""|pass|changes_requested|blocked
	Findings    []Finding
	SHA         string
	AutoRetries int
}
type Action struct {
	Kind     ActionKind // spawn|retry_fix|auto_retry|wait|succeed|escalate
	StepID   string
	Roles    []string
	Round    int
	Findings []Finding
	Run      *Run
	SHA      string
	Reason   string
}
func Next(s Spec, runs []Run, round, extraRounds int) Action
```

**Acceptance:**
- The six templates resolve to spec B2's table.
- Every validation rule has a test with the spec's exact copy.
- `Resolve` expands templates, applies the `max_rounds` override, defaults `retries` to 1, drops `tdd` when the task is exempt, and fills `of`.
- `RunRole` returns the first run step's role.
- The `Render` golden output matches spec B6.
- `Next` is total and deterministic over every case in spec B4's table, including:
  - merged findings in a stable order
  - escalation copy for rounds exhausted, a blocked verdict, a double failure, or a missing sha

**Verify:** `go test ./internal/workflow/... -count=1 && go vet ./internal/workflow/...`

#### Unit 6.1: Types and templates
- [ ] Write the failing test `TestTemplatesResolve`. Red.
- [ ] Implement the types and the `Templates` map using a `reviewed(id, role, gates, reviewers, rounds)` helper:
  - `tdd-reviewed`
  - `ui-tdd-reviewed`
  - `design-reviewed` (2 rounds)
  - `debug`
  - `mechanical`
  - `research`
- [ ] Green. Commit.

#### Unit 6.2: Validation
- [ ] Write the failing test `TestValidateErrors`: a table with one row per spec B2 rule, each checked against the exact error. Red. Implement. Green. Commit.

#### Unit 6.3: Resolve, RunRole, Render
- [ ] Write the failing tests `TestResolveDropsTDDWhenExempt`, `TestResolveMaxRoundsOverride`, `TestRunRole` and `TestRenderBuildStepGolden` (golden file `testdata/render_build_r2.txt`). Red. Implement. Green. Commit.

#### Unit 6.4: `Next` planner
- [ ] Write the failing test `TestNext` with at least 14 rows covering spec B4. Build the runs with a helper `r(step, round, role, state, verdict)`. Red.
- [ ] Implement `Next`:
  - Failures are handled first.
  - Then the first step with no runs in this round gets a spawn.
  - Any step that isn't finished yet → wait.
  - Review verdicts are aggregated.
  - Single-step templates succeed once their run completes.
- [ ] Green. Commit: `feat(workflow): pure next-action planner`.

