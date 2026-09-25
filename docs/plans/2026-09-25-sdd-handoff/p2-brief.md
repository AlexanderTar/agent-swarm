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


### P2: Vendored skills
**Workflow:** steps `[{id: "vendor", run: "mechanical", gates: ["commit", "verify"]}, {id: "review", review: ["reviewer"], loop: {fix: "vendor", max_rounds: 2}}]` (a license/provenance check matters) · **Units:** 5 (same-shape edits)

**Files:**
- `skills/vendor/**`: 11 skills plus `README.md`
- `internal/install/skills_test.go`
- `README.md` (License section)

**Acceptance:**
- Sources, pinned commits, license files and modifications match spec A2 exactly. That includes the ponytail rows: fixed `full` level, and no slash-command persistence section.
- Each vendored skill has a `VENDORED.md` in the spec's format.
- A test enforces that every vendored skill has a license file and a `VENDORED.md`.
- The same test bans these strings anywhere under `skills/vendor/`: `CLAUDE_PLUGIN_ROOT`, `submit-expo-feedback`, `raw.githubusercontent.com`, `/ponytail `.
- The ui-ux-pro-max search script runs.

**Verify:**
- `make skills-sync`
- `go test ./internal/install/...`
- `python3 skills/vendor/ui-ux-pro-max/scripts/search.py "dashboard" --domain style | head -5`

#### Unit 2.1: Provenance test and vendor README
- [ ] Write the failing test `TestVendoredSkillsHaveLicenseAndProvenance`. It covers the expected set of 11 names, the license files, the `VENDORED.md` fields and the banned strings. Red.
- [ ] Write `skills/vendor/README.md` (name, source, license, which role skills use it) and the README License line. Commit: `test(skills): vendored skill provenance checks`.

#### Unit 2.2: web-design-guidelines and building-components
- [ ] Vendor web-design-guidelines together with `vercel-labs/web-interface-guidelines/command.md` saved as `references/rules.md`. Change SKILL.md to read the local file.
  - Upstream has no LICENSE file, so write the MIT text crediting Vercel and note that in `VENDORED.md`.
- [ ] Vendor building-components under Apache-2.0.
- [ ] Commit: `feat(skills): vendor web design and component skills`.

#### Unit 2.3: ui-ux-pro-max
- [ ] Vendor v2.13.0 without `scripts/tests/`. Rewrite the `search.py` paths to be relative to the skill dir, and keep the data provenance files.
- [ ] Run the search script (Verify line). Commit: `feat(skills): vendor ui-ux-pro-max`.

#### Unit 2.4: Mobile skills
- [ ] Vendor the mobile skills:
  - `expo-native-ui` and `expo-design-system`, with the "Submitting Feedback" sections removed
  - `vercel-react-native-skills`
  - `mobile-ios-design` and `mobile-android-design`
- [ ] Commit: `feat(skills): vendor mobile design skills`.

#### Unit 2.5: ponytail
- [ ] Vendor `ponytail`, `ponytail-review` and `ponytail-debt` from DietrichGebert/ponytail@`e3ba2aa`, with the MIT license.
  - In `ponytail`, replace "Persistence" and the intensity-switch commands with: "Swarm runs ponytail at **full** level for coders and mechanical agents; there is no mode switching in swarm sessions."
- [ ] Run the provenance test green. Commit: `feat(skills): vendor ponytail skills`.

