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


### P3: Builder and reviewer skills
**Workflow:** `tdd-reviewed` (coder → reviewer; tests assert skill content) · **Units:** 5

**Files:**
- `skills/swarm/SKILL.md`
- `skills/swarm-{coder,reviewer,ui-reviewer,designer}/SKILL.md`
- `skills/swarm-batching/SKILL.md` (adopt the draft)
- `internal/install/skills_test.go`

**Acceptance:** Content per spec A4 and decisions 13–15.
- `swarm`: protocol only. Adds the "one step of a workflow" rule, lists `Workflow` among the disabled tools, and rule 9a points to `swarm-advisor`.
- `swarm-coder`:
  - covers package execution unit by unit, TDD per unit with `unit`-tagged entries, and one commit per unit
  - covers the `completed` git contract and handling fix rounds
  - references `ponytail`, with the precedence rule
  - references the superpowers skills `test-driven-development`, `verification-before-completion` and `receiving-code-review`
- `swarm-reviewer`:
  - references `superpowers:requesting-code-review`
  - defines the verdict and findings contract and walks the package unit by unit
  - treats a missing unit as `major`
  - uses `ponytail-review` as its second lens
- `swarm-ui-reviewer` adds the vendored UI and mobile skills.
- `swarm-designer` covers the design artifact contract.
- `swarm-batching` is adopted as drafted, and gains a pointer to the plan-registration warnings once P12 lands.
- Tests assert each skill names its required references, and that every `superpowers:<x>` reference is one of the 15 v6.4.1 skill names.

**Verify:**
- `make skills-sync`
- `go test ./internal/install/...`

#### Unit 3.1: Reference-checking tests
- [ ] Write `TestRoleSkillsReferenceTheirSkills` (a table from skill to required substrings, covering the five skills in this package) and `TestSuperpowersReferencesAreKnown`. Red. Commit with unit 3.2.

#### Unit 3.2: Core `swarm` rewrite
- [ ] Rewrite `skills/swarm/SKILL.md` as protocol only. TDD rule 11 moves to `swarm-coder` and `swarm-debugger`. Add the workflow-step rule and the disabled-tool list.
- [ ] Commit: `docs(skills): core swarm protocol for workflow steps`.

#### Unit 3.3: `swarm-coder`
- [ ] Write it per spec A4, including the ponytail precedence rule and package execution. Its test row goes green. Commit.

#### Unit 3.4: `swarm-reviewer` and `swarm-ui-reviewer`
- [ ] Write both. Their test rows go green. Commit.

#### Unit 3.5: `swarm-designer` and adopting `swarm-batching`
- [ ] Write `swarm-designer`. Review the draft `swarm-batching` against spec C4/C5 and fix any drift. Add its test row, which requires the role assignment table and the size-bounds table.
- [ ] All green. Commit: `docs(skills): designer skill; adopt swarm-batching`.

