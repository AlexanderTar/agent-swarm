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


### P4: Support-role skills and kickoff
**Workflow:** `tdd-reviewed` · **Units:** 4

**Files:**
- `skills/swarm-{debugger,mechanical,researcher,advisor}/SKILL.md`
- stubs for `skills/swarm-{spike,workflows}/SKILL.md`
- `internal/advisor/*` (prompt)
- `internal/runtime/{text.go,text_test.go,agents.go}`
- `internal/install/skills_test.go`

**Interfaces (produces):**
- `func RoleSkills(role Role, itemType items.Type) []string`
- `Kickoff(name, role, itemType, key, title)` and `ResumeKickoff(...)`, both with the new `itemType` parameter

**Acceptance:**
- The skills match spec A4:
  - `swarm-debugger`: systematic-debugging, and a regression test first.
  - `swarm-mechanical`: ≤ 60 lines, and references `ponytail`.
  - `swarm-researcher`: brainstorming on its own sub-question, the deep-research loop, the notes format.
  - `swarm-advisor`: original text, a mermaid diagram, and a CC BY-NC inspiration credit.
- The simulated advisor prompt uses the same decision rules.
- The kickoff lists the skills in spike table A3, including `swarm-batching` for orchestrators. Spikes get `swarm-spike`.
- Every role gets the mandate.
- Every name `RoleSkills` returns exists in `install.Skills()`. That includes `swarm-spike` and `swarm-workflows`, created here as valid stubs and filled in by P12.

**Verify:**
- `make skills-sync`
- `go test ./internal/install/... ./internal/advisor/... ./internal/runtime/ -run 'Kickoff|RoleSkills'`
- `go build ./... && go vet ./...`

#### Unit 4.1: `swarm-debugger` and `swarm-mechanical`
- [ ] Add the test rows (red), write both skills (green), commit.

#### Unit 4.2: `swarm-researcher`
- [ ] Add the test row (red), write the skill (green), commit.

#### Unit 4.3: `swarm-advisor` and the advisor prompt
- [ ] Write the failing tests `TestAdvisorSkillHasMermaidAndCredit` and `TestAdvisorSystemPromptDecisionRules`. Red.
- [ ] Write the skill and update the prompt builder in `internal/advisor`. Green. Commit.

#### Unit 4.4: Kickoff role-skill table
- [ ] Write the failing tests `TestKickoffNamesRoleSkill` (a table over roles plus the spike case), `TestRoleSkillsExist` and `TestKickoffMandateForEveryRole`. Red.
  - Old kickoff-string assertions in `text_test.go` are updated. This is an intentional change.
- [ ] Implement the table, the new signatures and the call sites. Add the `swarm-spike` and `swarm-workflows` stubs (valid frontmatter, body "Filled in by P12").
- [ ] Green. Commit: `feat(runtime): kickoff names each role's skill`.

---

