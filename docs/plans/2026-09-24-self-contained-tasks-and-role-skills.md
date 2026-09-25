# Self-contained Tasks, Workflow Engine and Role Skills — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan package-by-package. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **This plan dogfoods `skills/swarm-batching/SKILL.md`.** Work is grouped
> into **work packages** (tasks). Each package holds 3–5 **units**, where a
> unit is what superpowers calls a task: its own red → green cycle and its
> own commit. Each package has one workflow, which means one build and one
> review loop, plus one Verify set. Every package states its workflow, so
> its roles are assigned here and not by whoever executes the plan. A
> package is finished only when all of the following are true:
> - every unit's steps are checked
> - every unit is committed
> - the package's Verify commands pass
> - its review loop ended in a pass

**Goal:** Replace micro-task plans and orchestrator-driven review loops with work packages whose build → review → fix → verify loop is run by the daemon from a declarative, planner-assigned workflow. Give every agent role its own skill, plus a new `designer` role and vendored UI/design/mobile/ponytail skills. Make spikes do deep research and emit batched, role-assigned packages.

**Architecture:** See `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md`.
- **(A) Skills and roles.** Recursive skill packaging with one on-disk copy under `~/.swarm/skills`, linked into each agent kind. Role skills are named in each agent's kickoff. Eleven vendored skills, the `swarm-batching` rules, a `designer` role, and the native `Workflow` tool blocked.
- **(B) Workflow engine.**
  - A pure `internal/workflow` package: DSL, templates, validation, and a `Next` planner.
  - An engine in `internal/runtime/workflow.go` backed by new tables `workflows` and `workflow_runs`.
  - Review verdicts, and per-step gates (tdd per unit, commit, verify, artifact).
  - Task semantics that are safe with several agents on one task.
- **(C) Planning.** swarm-tree packages carry `workflow`, `units`, `solo` and `verify`. Registering a plan enforces role assignment and warns on unbatched or split work. The spike skill runs research → design → plan → critic.

**Tech Stack:** Go (stdlib `embed`, `io/fs`, `database/sql` on SQLite, `testing`), React + TypeScript + Vitest + Biome (`web/`), Swift + XCTest (`apps/menubar/`), Go e2e harness (`scripts/e2e`).

**Spec:** `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md`

## Batching summary (dogfood check)

- **Units:** 52 superpowers-sized units in 12 work packages, an average of 4.3 units per package.
- **Review loops:**
  - The first draft of this plan had 22 tasks, so 22 build/review loops.
  - Treating every unit as a task would mean 52 loops.
  - This plan has 12, plus one story review and one final integration review.
- **Packages below 3 units:** only P5 (schema migrations, 2 units), with `solo: "table-rebuilding migrations"`. Batching test: keep risky or irreversible work apart.
- **Split decisions:**
  - UI (P11) is separate because it needs a different reviewer (`ui_reviewer`).
  - Vendoring (P2) is separate because it needs a license/provenance review, not a code review.
  - The DSL (P6) lands before its consumers (contract first).
  - Checkpoint semantics (P8) and the engine (P9) are separate because together they exceed the ~500 changed-line bound.
- **Folded in, not separate:**
  - README and docs changes go into the package whose behaviour they describe.
  - The final smoke test is the epic's `integration` step.
  - Skill mirror syncs happen inside each unit.

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

---

## Story A — Skills and roles

### P1: Skill distribution
**Workflow:** `tdd-reviewed` (coder → reviewer) · **Units:** 4

**Files:**
- `internal/install/{skills.go,skills_test.go,claude.go,codex.go,agy.go,cursor.go,muse.go,doctor.go,uninstall.go}`
- `internal/adapter/claude.go` and its test
- `internal/migrate/{integrations.go,recover_test.go}`
- `Makefile`
- `cmd/swarm/daemon.go`

**Interfaces (produces):**
- `type Skill struct{ Name, Dir string; Vendored bool }`
- `func Skills() ([]Skill, error)`
- `func SkillFS() fs.FS`
- `func SkillNames() []string`
- `func SyncSkills(home string) ([]string, error)` (`home` is the swarm home, e.g. `~/.swarm`; writes `<home>/skills`)
- `const ManagedMarker = ".swarm-managed"`
- `func LinkSkills(root, skillsHome string, mode LinkMode) (skipped []string, err error)`
- `func WriteSkills(c Config, k Kind) (changed, skipped []string, err error)`
- `func CheckSkills(c Config, k Kind) Check`

**Acceptance:**
- The whole `skills/` tree is embedded (nested dirs, LICENSE, scripts, data) and extracted to `~/.swarm/skills/<name>/`, with vendored skills flattened by name.
- Stale files are pruned, and each extracted dir gets a `.swarm-managed` marker.
- The daemon syncs skills at startup.
- `make skills-sync` mirrors the tree with deletes, and the drift test compares every file.
- Every kind's skills root links to `~/.swarm/skills/<name>`, or holds a managed copy where the CLI can't follow symlinks.
- A user-owned skill with the same name is left untouched and reported.
- Claude spawns get links in `<cwd>/.claude/skills/`.
- Uninstall removes only swarm entries.
- Doctor checks skills for every kind, and warns when `python3` is missing.

**Verify:**
- `make skills-sync`
- `go test ./internal/install/... ./internal/adapter/... ./internal/migrate/...`
- `go build ./... && go vet ./...`

#### Unit 1.1: Embed, registry, sync
- [x] Write the failing tests in `internal/install/skills_test.go`:

```go
func TestSkillsRegistryMatchesTree(t *testing.T) {
	sk, err := Skills()
	if err != nil { t.Fatal(err) }
	names := map[string]bool{}
	for _, s := range sk {
		if names[s.Name] { t.Fatalf("duplicate skill name %q", s.Name) }
		names[s.Name] = true
		body, err := fs.ReadFile(SkillFS(), path.Join(s.Dir, "SKILL.md"))
		if err != nil { t.Fatalf("%s: %v", s.Name, err) }
		fm := parseFrontmatter(t, body)
		if fm["name"] != s.Name || path.Base(s.Dir) != s.Name { t.Errorf("%s: frontmatter/dir mismatch", s.Dir) }
		if strings.TrimSpace(fm["description"]) == "" { t.Errorf("%s: empty description", s.Name) }
	}
	for _, want := range []string{"swarm", "swarm-orchestrator", "swarm-batching"} {
		if !names[want] { t.Errorf("missing %s", want) }
	}
}

func TestSyncSkillsWritesNestedFilesAndPrunes(t *testing.T) {
	home := t.TempDir()
	if _, err := SyncSkills(home); err != nil { t.Fatal(err) }
	marker := filepath.Join(home, "skills", "swarm", ManagedMarker)
	if _, err := os.Stat(marker); err != nil { t.Fatalf("marker: %v", err) }
	stale := filepath.Join(home, "skills", "swarm", "stale.md")
	os.WriteFile(stale, []byte("x"), 0o644)
	if _, err := SyncSkills(home); err != nil { t.Fatal(err) }
	if _, err := os.Stat(stale); !os.IsNotExist(err) { t.Fatalf("stale file survived sync") }
}

func TestEmbeddedMirrorMatchesCanonicalTree(t *testing.T) { /* walk ../../skills vs SkillFS(); same file set, same bytes */ }
```

- [x] Run `go test ./internal/install/ -run 'TestSkillsRegistry|TestSyncSkills|TestEmbeddedMirror'`. Expect a compile failure and record it as red (unit 1).
- [x] Implement the embed and sync:
  - Replace the current embed with `//go:embed all:skills`. `SkillFS()` returns `fs.Sub(..., "skills")`.
  - `Skills()` treats every dir holding a `SKILL.md` as a skill; `Vendored` is set when the dir is under `vendor/`.
  - `SyncSkills` writes each file with `WriteIfChanged`, keeps the exec bit under `scripts/`, writes the marker, and prunes files that are no longer embedded.
  - `SkillNames()` becomes a function. Update its callers in `WriteSkills`, `adapter/claude.go`, `uninstall.go` and `migrate/integrations.go`.
  - Update the count assertions in `skills_test.go:66` and `migrate/recover_test.go:479-517` to derive from `len(SkillNames())`. This is an intentional change.
- [x] Change `Makefile` `skills-sync` to mirror with deletes (`rm -rf internal/install/skills && cp -R skills internal/install/skills`; rsync isn't always installed) and run it.
- [x] In `cmd/swarm/daemon.go`, call `install.SyncSkills(cfg.Home)` (the swarm home) before the reconcile loop, then relink kinds that already hold swarm-owned skills. Log failures; they are not fatal.
- [x] Run green and record it (unit 1). Commit: `feat(install): embed and sync the full skills tree`.

#### Unit 1.2: Per-kind links, user-owned safety, uninstall
- [x] **Empirical check first.** For each installed CLI (claude, codex, agy, cursor-agent, muse), symlink a throwaway skill into its skills root and confirm whether the CLI lists or uses it. Set `skillLinkMode[kind]` from the result. A CLI that isn't installed defaults to `Copy`. Put the findings in the commit message.
- [x] Write the failing tests and record red:
  - `TestWriteSkillsSymlinksEveryManagedSkill`
  - `TestWriteSkillsSkipsUserOwnedSameName`
  - `TestWriteSkillsReplacesStaleSwarmCopy`
  - `TestUninstallRemovesOnlySwarmSkills`
- [x] Implement `LinkSkills` and `WriteSkills`. An entry counts as swarm-owned if it is a symlink into `skillsHome`, or a dir containing the marker.
- [x] Run green. Commit: `feat(install): link swarm skills into every agent kind`.

#### Unit 1.3: Claude per-spawn links
- [x] Write the failing test `TestClaudeProjectConfigLinksSkills` in `internal/adapter/claude_test.go`. Red.
- [x] Make `writeProjectSwarmConfig` call `LinkSkills(<cwd>/.claude/skills, ~/.swarm/skills, skillLinkMode[Claude])`.
- [x] Run green. Commit: `feat(adapter): link skills into claude spawn dirs`.

#### Unit 1.4: Doctor
- [x] Write the failing tests `TestDoctorChecksSkillsForEveryKind` and `TestDoctorWarnsWithoutPython3`. Red.
- [x] Add `CheckSkills` to every kind's `Check*`, and add a warn-level `python3` base check. Refresh the README's `swarm doctor` line (folded docs).
- [x] Run green. Commit: `feat(doctor): skills check per kind and python3 warning`.

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
- [x] Write the failing test `TestVendoredSkillsHaveLicenseAndProvenance`. It covers the expected set of 11 names, the license files, the `VENDORED.md` fields and the banned strings. Red.
- [x] Write `skills/vendor/README.md` (name, source, license, which role skills use it) and the README License line. Commit: `test(skills): vendored skill provenance checks`.

#### Unit 2.2: web-design-guidelines and building-components
- [x] Vendor web-design-guidelines together with `vercel-labs/web-interface-guidelines/command.md` saved as `references/rules.md`. Change SKILL.md to read the local file.
  - Upstream has no LICENSE file, so write the MIT text crediting Vercel and note that in `VENDORED.md`.
- [x] Vendor building-components under Apache-2.0.
- [x] Commit: `feat(skills): vendor web design and component skills`.

#### Unit 2.3: ui-ux-pro-max
- [x] Vendor v2.13.0 without `scripts/tests/`. Rewrite the `search.py` paths to be relative to the skill dir, and keep the data provenance files.
- [x] Run the search script (Verify line). Commit: `feat(skills): vendor ui-ux-pro-max`.

#### Unit 2.4: Mobile skills
- [x] Vendor the mobile skills:
  - `expo-native-ui` and `expo-design-system`, with the "Submitting Feedback" sections removed
  - `vercel-react-native-skills`
  - `mobile-ios-design` and `mobile-android-design`
- [x] Commit: `feat(skills): vendor mobile design skills`.

#### Unit 2.5: ponytail
- [x] Vendor `ponytail`, `ponytail-review` and `ponytail-debt` from DietrichGebert/ponytail@`e3ba2aa`, with the MIT license.
  - In `ponytail`, replace "Persistence" and the intensity-switch commands with: "Swarm runs ponytail at **full** level for coders and mechanical agents; there is no mode switching in swarm sessions."
- [x] Run the provenance test green. Commit: `feat(skills): vendor ponytail skills`.

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
- [x] Write `TestRoleSkillsReferenceTheirSkills` (a table from skill to required substrings, covering the five skills in this package) and `TestSuperpowersReferencesAreKnown`. Red. Commit with unit 3.2.

#### Unit 3.2: Core `swarm` rewrite
- [x] Rewrite `skills/swarm/SKILL.md` as protocol only. TDD rule 11 moves to `swarm-coder` and `swarm-debugger`. Add the workflow-step rule and the disabled-tool list.
- [x] Commit: `docs(skills): core swarm protocol for workflow steps`.

#### Unit 3.3: `swarm-coder`
- [x] Write it per spec A4, including the ponytail precedence rule and package execution. Its test row goes green. Commit.

#### Unit 3.4: `swarm-reviewer` and `swarm-ui-reviewer`
- [x] Write both. Their test rows go green. Commit.

#### Unit 3.5: `swarm-designer` and adopting `swarm-batching`
- [x] Write `swarm-designer`. Review the draft `swarm-batching` against spec C4/C5 and fix any drift. Add its test row, which requires the role assignment table and the size-bounds table.
- [x] All green. Commit: `docs(skills): designer skill; adopt swarm-batching`.

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
- [x] Add the test rows (red), write both skills (green), commit.

#### Unit 4.2: `swarm-researcher`
- [x] Add the test row (red), write the skill (green), commit.

#### Unit 4.3: `swarm-advisor` and the advisor prompt
- [x] Write the failing tests `TestAdvisorSkillHasMermaidAndCredit` and `TestAdvisorSystemPromptDecisionRules`. Red.
- [x] Write the skill and update the prompt builder in `internal/advisor`. Green. Commit.

#### Unit 4.4: Kickoff role-skill table
- [x] Write the failing tests `TestKickoffNamesRoleSkill` (a table over roles plus the spike case), `TestRoleSkillsExist` and `TestKickoffMandateForEveryRole`. Red.
  - Old kickoff-string assertions in `text_test.go` are updated. This is an intentional change.
- [x] Implement the table, the new signatures and the call sites. Add the `swarm-spike` and `swarm-workflows` stubs (valid frontmatter, body "Filled in by P12").
- [x] Green. Commit: `feat(runtime): kickoff names each role's skill`.

---

## Story B — Workflow engine and multi-agent tasks
**Story workflow:** `after_tasks: {id: "story-review", review: ["reviewer"]}`. These packages interact through shared runtime state, so one cross-package review runs before the story is done.

### P5: Schema migrations
**Workflow:** `tdd-reviewed`, `max_rounds: 4` · **Units:** 2 · **Solo:** `"table-rebuilding migrations"` (irreversible work gets its own package)

**Files:**
- `internal/db/schema/0010_designer_and_artifact_kinds.sql`
- `internal/db/schema/0011_workflows.sql`
- `internal/db/*_test.go`
- `internal/db/testdata` (a fixture copy of a populated v2 DB)

**Acceptance:**
- `0010` rebuilds `agents`, adding `'designer'` to its role CHECK. It rebuilds `artifacts`, adding `'design'` and `'research'` to its kind CHECK. Both rebuilds preserve every row, index and foreign key, following the `0008` pattern.
- `0011` adds everything from spec B1:
  - on `items`: `workflow_json`, `steps_json`, `units_json`, `solo`, `verify_json`
  - on `checkpoints`: `verdict` and `findings_json`
  - on `workflows`: `extra_rounds`
  - the `workflows` and `workflow_runs` tables, the unique live-workflow index and the runs agent index
- Both migrations apply on a fresh DB and on the populated fixture, and row counts are unchanged.

**Verify:** `go test ./internal/db/... -count=1 && go build ./...`

#### Unit 5.1: Migration 0010
- [x] Write the failing test `TestMigration0010PreservesRowsAndWidensChecks`: populate the fixture, migrate, then assert counts and that an insert of role `designer` / kind `design` succeeds. Red.
- [x] Write the migration. Green. Commit.

#### Unit 5.2: Migration 0011
- [x] Write the failing tests `TestMigration0011Schema` (columns, tables, indexes and CHECKs) and `TestOneLiveWorkflowPerItem` (the second `running` row fails). Red.
- [x] Write the migration. Green. Commit.

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
- [x] Write the failing test `TestTemplatesResolve`. Red.
- [x] Implement the types and the `Templates` map using a `reviewed(id, role, gates, reviewers, rounds)` helper:
  - `tdd-reviewed`
  - `ui-tdd-reviewed`
  - `design-reviewed` (2 rounds)
  - `debug`
  - `mechanical`
  - `research`
- [x] Green. Commit.

#### Unit 6.2: Validation
- [x] Write the failing test `TestValidateErrors`: a table with one row per spec B2 rule, each checked against the exact error. Red. Implement. Green. Commit.

#### Unit 6.3: Resolve, RunRole, Render
- [x] Write the failing tests `TestResolveDropsTDDWhenExempt`, `TestResolveMaxRoundsOverride`, `TestRunRole` and `TestRenderBuildStepGolden` (golden file `testdata/render_build_r2.txt`). Red. Implement. Green. Commit.

#### Unit 6.4: `Next` planner
- [x] Write the failing test `TestNext` with at least 14 rows covering spec B4. Build the runs with a helper `r(step, round, role, state, verdict)`. Red.
- [x] Implement `Next`:
  - Failures are handled first.
  - Then the first step with no runs in this round gets a spawn.
  - Any step that isn't finished yet → wait.
  - Review verdicts are aggregated.
  - Single-step templates succeed once their run completes.
- [x] Green. Commit: `feat(workflow): pure next-action planner`.

### P7: Roles, guards and item fields
**Workflow:** `tdd-reviewed` · **Units:** 4

**Files:**
- `internal/kinds/kinds.go`
- `internal/runtime/{types.go,titles.go,agents.go}` (`OverridableRoles`)
- `internal/settings/settings.go`
- `internal/hook/handler.go` and its tests
- `internal/items/{model.go,store.go}` and their tests
- `internal/mcpserver/{orchestrator.go,tools.go}` and their tests
- `internal/httpapi/items.go`
- `web/src/types.ts` (Item fields only)

**Interfaces (produces):**
- `kinds.RoleDesigner`
- On `items.Item`, `CreateInput` and `Patch`: `Workflow *workflow.Spec`, `Steps []string`, `Units []Unit`, `Solo string`, `Verify []string`
- The same fields on the wire

**Acceptance:**
- `designer` is a valid role across kinds, settings (default `claude`/`opus`), overrides and the catalog. Its emoji is 🎨.
- `Workflow` and `workflow` tool names are blocked in swarm sessions with the spec copy. They are still allowed outside swarm.
- Items store the resolved workflow and set `role_hint = RunRole(workflow)`.
- A task may not have both `steps` and `units`, and may have at most 8 units.
- Only an orchestrator or the daemon may set these fields.
- An orchestrator `swarm_items create` of a task without a `workflow` is refused with the spec copy. A task the user creates on the board may omit it.
- `swarm_items` accepts the new fields, and `swarm_read` and `GET /api/items/:key` return them.

**Verify:**
- `go test ./internal/kinds/... ./internal/settings/... ./internal/hook/... ./internal/items/... ./internal/mcpserver/... ./internal/httpapi/... ./internal/runtime/...`
- `go build ./... && go vet ./...`
- `cd web && pnpm test`

#### Unit 7.1: Designer role plumbing (Go)
- [x] Write the failing tests `TestSettingsDefaultsIncludeDesigner`, `TestSpawnDesignerRoleAccepted` and `TestOverridableRolesIncludeDesigner`. Red. Implement. Green. Commit.

#### Unit 7.2: Block the native Workflow tool
- [x] Write the failing tests `TestPreToolUseBlocksWorkflowTool` and `TestWorkflowToolAllowedOutsideSwarm`. Red. Implement. Green. Commit.

#### Unit 7.3: Item fields in the store
- [x] Write the failing tests:
  - `TestCreateTaskStoresResolvedWorkflowAndRoleHint`
  - `TestCreateRejectsInvalidWorkflow`
  - `TestCreateRejectsStepsAndUnits`
  - `TestOrchestratorTaskNeedsWorkflow`
  - `TestBoardTaskMayOmitWorkflow`
  - `TestUserCannotSetWorkflow`
- [x] Red. Implement the model, `scanItem` and the create/update validation. Green. Commit.

#### Unit 7.4: Wire the fields through MCP and HTTP
- [x] Write the failing tests `TestSwarmItemsAcceptsWorkflowUnitsVerify`, `TestSwarmReadReturnsWorkflowFields` and `TestItemJSONIncludesWorkflowFields`. Red.
- [x] Implement, and update the web `Item` type. Green. Commit.

### P8: Checkpoint semantics
**Workflow:** `tdd-reviewed` · **Units:** 5 · Every unit touches `internal/runtime/checkpoint.go`, so they share context.

**Files:**
- `internal/runtime/{checkpoint.go,model.go,reconcile.go,artifacts.go}`
- `internal/items/transition.go`
- `internal/mcpserver/tools.go` (`swarm_checkpoint` schema)
- tests: `checkpoint_test.go`, `transition_test.go`, `reconcile_test.go`, `artifacts_test.go`

**Interfaces (produces):**
- `CheckpointInput.Verdict` and `.Findings`
- `Verify.Unit`
- `workflowRunFor(ctx, tx, agentID)`, whose run-row type is defined here
- `tddOK(entries, units)`, `verifyDeclaredOK`, `commitOK`, `artifactGate`, `registerArtifactAsDaemon`

**Acceptance:**
- Verdict rules and copy follow spec B5.
- Siblings are closed only when they have the same role and the same step.
- `completedCurrent` is per agent. This fixes the cross-agent attempt bug, which a test reproduces.
- `OnDepUnblocked` wakes every distinct parent.
- For workflow agents the step's gates apply:
  - tdd: per unit for batched tasks, and skipped when the task is exempt
  - verify: declared commands, matched by containment and normalised whitespace
  - commit: clean, sha equals HEAD, sha stored on the run
  - artifact: registers `design`/`research`
- Legacy agents keep `verifyOK`, and existing legacy tests stay untouched and green.
- Any existing test asserting that a coder's `completed` closes a reviewer is updated to the new rule. This is an intentional change.

**Verify:**
- `go test ./internal/runtime/... ./internal/items/... ./internal/mcpserver/... -count=1`
- `go build ./... && go vet ./...`

#### Unit 8.1: Verdicts and findings
- [x] Write the failing tests `TestVerdictRequiredForWorkflowReviewer`, `TestVerdictRefusedForCoder` and `TestPassVerdictRefusesMajorFindings`. Red. Implement storage, validation and the schema. Green. Commit.

#### Unit 8.2: Close siblings by role and step
- [x] Write the failing tests `TestReviewerCompletedDoesNotCloseBuilder`, `TestBuilderCompletedDoesNotCloseReviewer` and `TestSameRoleSiblingStillClosed`. Red.
- [x] Implement the query change. It filters on the caller's role and on the step from that agent's latest `workflow_runs` row. Green. Commit.

#### Unit 8.3: Per-agent completion and dependency wake-ups
- [x] Write the failing tests `TestCompletedCurrentIsPerAgent` and `TestDepUnblockedWakesAllParents`. Red. Implement. Green. Commit.

#### Unit 8.4: tdd and verify gates
- [x] Write the failing tests:
  - `TestTDDGateNeedsRedBeforeGreen` (green only; red then green across checkpoints; red and green in different attempts)
  - `TestTDDGatePerUnit` (unit 2 missing → error names unit 2)
  - `TestTDDGateSkippedWhenExempt`
  - `TestVerifyGateMatchesDeclaredCommands`
  - `TestLegacyCoderKeepsVerifyOK`
- [x] Red. Implement. Green. Commit.

#### Unit 8.5: Commit and artifact gates
- [x] Write the failing tests `TestCommitGateRefusesDirtyWorktree`, `TestCommitGateRefusesShaMismatch`, `TestCommitGateStoresSha` and `TestDesignArtifactGateRegistersArtifact`. They use a real temp git repo via `runtime/helpers_test.go`. Red. Implement. Green. Commit.

### P9: Workflow engine
**Workflow:** `tdd-reviewed`, `max_rounds: 4` · **Units:** 5

**Files:**
- `internal/runtime/{workflow.go,workflow_test.go,limits.go,agents.go,text.go,checkpoint.go,reconcile.go}`
- `internal/items/transition.go`
- `internal/hook/handler.go` (uses `SubagentSlots`)

**Interfaces (produces):**
- `StartWorkflowInput`, `WorkflowState`
- `StartWorkflow`, `WorkflowFor`, `advance`, `ResumeWorkflow`, `CancelWorkflow`, `recoverWorkflows`
- `SubagentSlots(ctx, parentID) (used, max int, err error)`
- `BriefForStep(it, spec, stepID, round, ctxLines) BriefInput`
- `RenderBrief` gains the `Units`/`Steps` and `Workflow` sections

**Acceptance:**
- **Briefs.** Engine-spawned agents get daemon-rendered briefs per spec B6, collapsing unit steps when over the length cap.
- **Start.** Start is validated per spec B4.
- **Advance.** Advance applies every `Next` action:
  - Build agents get the rw worktree shared to them. Reviewers get a review worktree at the sha, which is created, shared and then removed.
  - A fix round retries the builder with the rendered findings, grouped by reviewer and unit.
  - The task moves InReview ↔ InProgress, and to Done only by the daemon.
  - `workflow_succeeded` and `workflow_escalated` relays are sent, plus the `workflow.escalated` notification.
- **Idempotency.** Repeated advances are no-ops: `ON CONFLICT DO NOTHING` on the run key, and no spawn unless a row was inserted.
- **Budget.** When the budget is full, runs wait and start in FIFO order. The hook uses `SubagentSlots`, and its existing tests stay green.
- **Relays.** The engine suppresses relays of `accepted`, `progress` and `completed` checkpoints to the orchestrator.
- **Triggers.** Advance runs after a checkpoint, after a session death, after a slot is released, and on start/resume. A stall older than 30 s is recovered.
- **Done gating.** An orchestrator cannot mark a workflow task Done.
- **Resume and cancel** follow spec B7, with extra rounds recorded.

**Verify:**
- `go test ./internal/runtime/... ./internal/items/... ./internal/hook/... -count=1`
- `go build ./... && go vet ./...`

#### Unit 9.1: Slots and briefs
- [ ] Write the failing tests `TestSubagentSlotsMatchesHookCount` and `TestRenderBriefUnitsAndWorkflowSections` (including the cap collapse). Red.
- [ ] Move the hook query into `limits.go`, and implement `BriefForStep` and the new `RenderBrief` sections. Green. Commit.

#### Unit 9.2: Start and spawning steps
- [ ] Write the failing tests:
  - `TestStartWorkflowValidates` (no workflow, one already running, no rw worktree, dependencies open)
  - `TestWorkflowSpawnsBuilderWithSharedWorktree`
  - `TestWorkflowSpawnsReviewersOnReviewWorktree`
  - `TestWorkflowParallelReviewers`
  - `TestAdvanceIsIdempotent`
- [ ] Red. Implement `StartWorkflow` and the spawn half of `advance`, using a per-workflow mutex, one tx per action, and side effects after commit. Green. Commit.

#### Unit 9.3: Rounds, success, escalation, relays
- [ ] Write the failing tests `TestWorkflowHappyPath`, `TestWorkflowFixRound`, `TestWorkflowEscalatesWhenRoundsExhausted`, `TestWorkflowAutoRetryOnCrash` and `TestWorkflowRelaysSuppressed`. Red. Implement. Green. Commit.

#### Unit 9.4: Triggers and recovery
- [ ] Write the failing tests `TestCheckpointTriggersAdvance`, `TestCrashTriggersAdvance`, `TestSlotReleaseSpawnsWaitingRun` and `TestRecoverStalledWorkflow`. Red. Implement. Green. Commit.

#### Unit 9.5: Resume, cancel, Done gating
- [ ] Write the failing tests:
  - `TestOrchestratorCannotMarkWorkflowTaskDone`
  - `TestResumeRetryGrantsExtraRound`
  - `TestResumeAccept`
  - `TestResumeFail`
  - `TestCancelWorkflow`
  - `TestResumeRefusedWhenNotEscalated`
- [ ] Red. Implement. Green. Commit.

### P10: Orchestrator surface, story and integration gates, e2e
**Workflow:** `tdd-reviewed` · **Units:** 4

**Files:**
- `internal/mcpserver/{workflow.go,orchestrator.go,tools.go,server.go}` and their tests
- `internal/runtime/{workflow.go,checkpoint.go}`
- `internal/items/transition.go` (`deriveStory`)
- `scripts/e2e/{workflow_test.go,tdd_test.go,harness_test.go}`

**Acceptance:**
- `swarm_workflow` supports start, status, resume and cancel. It is orchestrator-only, has `required` set in its schema, and is idempotent via `request_id`.
- `swarm_spawn`:
  - validates the role enum with the spec copy
  - refuses build/design/research roles on workflow tasks
  - honours `worktrees`, sharing them and putting them in the brief header
  - drops `advisor`/`cwd` from its schema
- `swarm_read` returns `workflow_state` and `crew`.
- Story `after_tasks` follows spec B8:
  - a `story_ready_for_review` relay
  - start with a read-only worktree
  - `deriveStory` waits for the review
  - changes requested → escalate
- The `integrated` gate checks the integration verify commands and a passing final review.
- The e2e scenarios pass:
  - happy path, one fix round, escalation, `resume accept`
  - a batched package with per-unit evidence
- `tdd_test.go` uses the new copy for workflow tasks and the legacy copy for legacy tasks.

**Verify:**
- `go test ./internal/mcpserver/... ./internal/runtime/... ./internal/items/...`
- `make e2e`

#### Unit 10.1: `swarm_workflow`
- [ ] Write the failing tests `TestSwarmWorkflowToolsOnlyForOrchestrators`, `TestSwarmWorkflowStartStatusResumeCancel` and `TestSwarmWorkflowIdempotentStart`. Red. Implement. Green. Commit.

#### Unit 10.2: Spawn, checkpoint and read changes
- [ ] Write the failing tests:
  - `TestSwarmSpawnRejectsUnknownRole`
  - `TestSwarmSpawnRefusesWorkflowTask`
  - `TestSwarmSpawnSharesWorktrees`
  - `TestSwarmCheckpointVerdictSchema`
  - `TestSwarmReadCrew`
- [ ] Red. Implement. Green. Commit.

#### Unit 10.3: Story and integration gates
- [ ] Write the failing tests:
  - `TestStoryReadyForReviewRelay`
  - `TestStoryDoneWaitsForAfterTasksReview`
  - `TestStoryReviewChangesEscalates`
  - `TestIntegratedNeedsIntegrationVerify`
  - `TestIntegratedNeedsFinalReviewPass`
  - `TestStoryWithoutAfterTasksUnchanged`
- [ ] Red. Implement. Green. Commit.

#### Unit 10.4: e2e
- [ ] Write the scenarios and the harness helpers for `swarm_workflow`, reviewer verdicts and unit-tagged verification. Run `make e2e` and record the red.
- [ ] Fix any real bug in its owning package, test first. Green. Commit.

---

## Story C — Planning, spikes and surfaces

### P11: Board and menubar
**Workflow:** `ui-tdd-reviewed` (coder → reviewer + ui_reviewer) · **Units:** 5

**Files:**
- `web/src/{types.ts,copy.ts,api.ts,components/AgentFields.tsx,components/WorkflowSection.tsx,panels/Details.tsx,mock/fixtures.ts}`
- the kanban card component under `web/src/views/`
- tests for the above
- `internal/httpapi` (item detail `workflow_state`/`crew`; `step` in the agents payload)
- `apps/menubar/Sources/SwarmBarKit/{Wire.swift,Copy.swift,SettingsModel.swift}` and their tests

**Acceptance:**
- Spec Screens and copy, exactly:
  - Designer and chore labels in web and menubar, including the Settings defaults row.
  - A Workflow section in Details (hidden for legacy tasks). It shows findings with a unit tag.
  - A kanban crew row and round badge.
  - "Plan warnings" on the plan review screen.
  - A menubar step suffix.
- Screenshots of the mock board in light and dark are checked with the `run` skill or Playwright.

**Verify:**
- `go test ./internal/httpapi/...`
- `cd web && pnpm typecheck && pnpm test`
- `cd apps/menubar && swift test`

#### Unit 11.1: Designer and chore labels
- [ ] Write the failing tests: web `copy.test.ts` (`ROLE_LABEL.designer`, `ITEM_TYPE_LABEL.chore`), Swift `testRoleDecodesDesigner` and `testDefaultsOrderIncludesDesigner`. Red. Implement. Green. Commit.

#### Unit 11.2: HTTP payload fields
- [ ] Write the failing tests `TestItemDetailIncludesWorkflowState` and `TestAgentsPayloadIncludesStep`. Red. Implement. Green. Commit.

#### Unit 11.3: `WorkflowSection`
- [ ] Write the failing tests: `WorkflowSection.test.tsx` (runs, verdicts, unit-tagged findings, escalation; hidden when there is no workflow) and the `Details.test.tsx` addition. Red. Implement. Green. Commit.

#### Unit 11.4: Kanban crew and plan warnings
- [ ] Write the failing tests for the kanban card (crew row, "+n", "R2" badge) and for the plan review warnings list. Red. Implement. Green. Commit.

#### Unit 11.5: Menubar step suffix
- [ ] Write the failing test `testAgentRowStepSuffix`. Red. Implement. Green. Take the screenshots. Commit.

### P12: Planning emits role-assigned packages
**Workflow:** `tdd-reviewed` · **Units:** 5

**Files:**
- `internal/runtime/{artifacts.go,materialize.go}` and their tests
- `internal/mcpserver/orchestrator.go` (`swarm_artifact` warnings)
- `skills/swarm-{workflows,spike,orchestrator,batching}/SKILL.md`
- `internal/install/skills_test.go`
- `README.md`

**Interfaces (produces):**
- `TreeNode.Workflow`, `.Steps`, `.Units []TreeUnit`, `.Solo`, `.Verify`
- `RegisterArtifactResult.Warnings`
- `func lintTree(Tree) (errs []error, warnings []string)`

**Acceptance:**
- Trees carrying the new fields parse (still `DisallowUnknownFields`) and validate per level.
- Materialize copies the resolved workflow, steps/units, `solo` and verify, and derives `role_hint`.
- Plan registration returns errors and warnings with the exact spec C2 copy. The errors cover:
  - a missing workflow
  - a `role_hint` mismatch
  - review tasks
  - steps and units together
  - more than 8 units
  - reviewer roles on stories or roots
- The warnings cover:
  - split TDD titles
  - no test step
  - single-unit tasks with no `solo`
  - more than 5 units
  - stories made entirely of single-unit tasks
- `swarm-workflows` has the DSL reference, the role assignment table and a worked example that a test parses and validates.
- `swarm-spike` covers the seven-step method, batching via `swarm-batching`, and pre-assigning roles for research and design tasks.
- `swarm-orchestrator`:
  - covers delivery via `swarm_workflow`
  - covers escalation handling, folding follow-ups with no micro-tasks, story reviews, and root integration (including a `ponytail-debt` pass)
  - references `dispatching-parallel-agents`, `subagent-driven-development` and `finishing-a-development-branch`
- `swarm-batching` points to the plan-registration warnings.
- The README reflects the release: roles, skills, vendored licenses, `swarm_workflow`, and how work flows.

**Verify:**
- `make skills-sync`
- `go test ./internal/runtime/... ./internal/install/... ./internal/mcpserver/...`
- `cd web && pnpm test`

#### Unit 12.1: Tree fields and materialize
- [ ] Write the failing tests `TestParseTreeWithWorkflowUnitsSolo`, `TestMaterializeCopiesWorkflowUnitsVerify` and `TestMaterializeDerivesRoleHint`. Red. Implement. Green. Commit.

#### Unit 12.2: Plan lint
- [ ] Write the failing tests:
  - `TestTreeRequiresWorkflow`
  - `TestTreeRejectsRoleHintMismatch`
  - `TestTreeRejectsReviewTasks`
  - `TestTreeRequiresStepsAndVerifyForTDDTasks`
  - `TestTreeWarnsOnSplitTDDTitles` (positives from the spec regex; negatives such as "Add retry to failing uploads")
  - `TestTreeWarnsOnUnbatchedTasks`
  - `TestSwarmArtifactReturnsWarnings`
- [ ] Red. Implement `lintTree`. Green. Commit.
  - Fixtures in `runtime/testdata` that relied on role_hint-only tasks or had review tasks are updated to carry workflows. This is an intentional change.

#### Unit 12.3: `swarm-workflows`
- [ ] Write the failing test `TestWorkflowsSkillExampleValidates`: extract the fenced example, run it through `ParseTree`, `lintTree` and `Validate`, and expect no errors and no warnings. Red.
- [ ] Write the skill. Green. Commit.

#### Unit 12.4: `swarm-spike`
- [ ] Add the test row (references to `superpowers:brainstorming`, `writing-plans`, `systematic-debugging` and `swarm-batching`, plus the phrase "assign every role"). Red.
- [ ] Write the skill, including a bad → good example: split RED/GREEN/review tasks → one package with units. Green. Commit.

#### Unit 12.5: `swarm-orchestrator` and README
- [ ] Write the failing test `TestOrchestratorSkillNoLongerHandRollsReviews`: the old "After a worker's `completed`, spawn a `reviewer`" sentence is gone, and the three superpowers references and `ponytail-debt` are present. Red.
- [ ] Rewrite the skill, add the pointer to `swarm-batching`, and update the README. Green. Commit.

### PA: agy skills root

Filed 2026-09-25 (PR #20), after P1 shipped: agy's real skills location
turned out to differ from `Config.SkillsDir(KindAgy)`, and every
swarm-spawned agy session's first run chained the *real*
`~/.gemini/antigravity-cli/skills` one hop deeper into that session's own
launch folder (see `docs/plans/2026-09-24-skill-symlink-probe.md`,
"Follow-up"). See spec A7.

**Workflow:** `tdd-reviewed` (coder → reviewer) · **Units:** 4

**Files:**
- `internal/install/{config.go,config_test.go,agy.go,agy_test.go,skills.go,skills_test.go,skills_internal_test.go,plugins.go}`
- `internal/adapter/{agy.go,agy_test.go}`
- `docs/plans/2026-09-24-skill-symlink-probe.md`

**Interfaces (produces):**
- `Config.SkillsDir(KindAgy)` now returns `~/.gemini/config/skills`.
- `legacyAgySkillsChain(c Config) (resolved string, ok bool)`, `repairAgySkillsRoot(c Config) error`, `sameDir(a, b string) (bool, error)` (internal/install/agy.go).
- `underDir(target, root string) bool` (internal/install/skills.go, fix round 1: shared by `isSwarmOwned` and `legacyAgySkillsChain`).
- `copyTree` (internal/install/plugins.go) dereferences every symlink in the tree into real content (fix round 3, controller ruling). It skips and logs links back to active ancestors before creating their destinations, using `copyTreeGuarded`'s linked-root visited set and ordinary directory ancestry check; `maxCopyTreeDepth` remains a backstop.

**Acceptance:**
- `Config.SkillsDir(KindAgy)` is `~/.gemini/config/skills`; doctor, uninstall and `WriteSkills` follow it automatically (they all derive the path from this method).
- A spawned agy's `setupEnv` links `<agy-home>/.gemini/config/skills` straight at the real `~/.gemini/config/skills` (creating it if missing), and gives agy-home its own `.migrated` marker (a byte copy of the real one, never a symlink to it) so a spawned agy has no first-run skills migration left to perform and never gets a write path back into the real `~/.gemini/config` tree.
- `copyTree` dereferences symlinks into real content everywhere (a file link → a real file, a directory link → a real directory holding a recursive copy), so cursor's local plugin copy stays a real folder tree with none, and content `repairAgySkillsRoot` salvages out of `run/launch` survives that session later being reaped. A cycle (a link back to an active ancestor) is skipped and logged before its destination is created; independent siblings still copy. The one exception: a top-level legacy entry that is itself a symlink pointing outside `run/launch` is preserved as a symlink to its resolved target, not deep-copied.
- `swarm install` (and `swarm migrate`'s step 9, the same `install.Agents` → `WriteAgy` path) repairs an existing `~/.gemini/antigravity-cli/skills` that chains into a swarm session's `run/launch/` folder: salvage its entries into the new root (never overwriting a user-owned same-named entry there), then repoint the old path at the new root. Nothing under `run/launch` is ever deleted. This never runs from the daemon or from a test against the real home.
- Doctor warns (never fails) when `~/.gemini/antigravity-cli/skills` still resolves into a `run/launch/` session folder.
- The agy symlink-following verdict is re-probed in a sealed scratch `HOME` with no symlink back into any real directory, including the actual two-hop production shape (`setupEnv`'s directory-level symlink plus a `Symlink`-mode entry inside it), and `skillLinkMode[KindAgy]` is set from the result (verified `Symlink`, agy 1.2.11).

**Verify:**
- `go test ./internal/install/... ./internal/adapter/... ./cmd/...`
- `go build ./... && go vet ./...`
- `go test ./...` once

#### Unit PA.1: `SkillsDir(KindAgy)` → `~/.gemini/config/skills`
- [x] Update the failing assertion in `TestSkillsDirPerAgent` (`internal/install/config_test.go`) to expect `/fake/home/.gemini/config/skills`. Red.
- [x] Point `Config.SkillsDir(KindAgy)` at `c.Gemini("config", "skills")`. Green. Commit.

#### Unit PA.2: `setupEnv` links `config/skills` + `.migrated`
- [x] Write the failing tests `TestAgySetupEnvLinksConfigSkillsAndMigratedMarker` and `TestAgySetupEnvCopiesRealMigratedMarkerContent` (`internal/adapter/agy_test.go`), asserting `agy-home/.gemini/config/skills` is a symlink to the real `~/.gemini/config/skills` (created if missing), `agy-home/.gemini/config/.migrated` exists as a plain file (never a symlink), and the real `~/.gemini/antigravity-cli/skills` is never touched. Red.
- [x] Implement in `setupEnv`; update the stale `TestAgyIsolatedHomeCarriesSkillsAndHooks` to plant at and assert the new `config/skills` path. Green. Commit.

#### Unit PA.3: Install repair + doctor warn
- [x] Write the failing tests `TestWriteAgyRepairsALegacySkillsChainIntoRunLaunch`, `TestWriteAgyLeavesAHealthySkillsRootAlone` and `TestCheckAgyWarnsWhenSkillsRootIsInsideARunLaunchSession` (`internal/install/agy_test.go`), with a fake chain in a temp home (`<home>/run/launch/ses_x/agy-home/.gemini/config/skills`, with content) linked from `antigravity-cli/skills`. Red.
- [x] Implement `legacyAgySkillsChain`, `repairAgySkillsRoot` (called from `WriteAgy`, before `WriteSkills`) and `agySkillsRootLegacyCheck` (added to `CheckAgy`). Green. Commit.

#### Unit PA.4: Sealed re-probe + link mode + probe doc
- [x] Build a scratch `HOME` under the scratchpad with no symlink to any real directory; copy (not link) only the auth/onboarding files agy needs; create `$HOME/.gemini/config/.migrated`; symlink the probe skill at `$HOME/.gemini/config/skills/zz-swarm-symlink-probe` to a dir outside `$HOME`; run agy headless and ask for the probe codeword; before and after, diff `ls -la ~/.gemini/antigravity-cli/skills ~/.gemini/config ~/.gemini/antigravity-cli` on the real home — any difference stops the unit and reports BLOCKED. (Zero drift observed across the whole re-probe.)
- [x] Run the same prompt against a real (non-symlink) copy of the probe at the same scratch location as a control.
- [x] Set `skillLinkMode[KindAgy]` from the result (agy 1.2.11: Symlink); update `TestSkillLinkModePerKind` and its comment. Append a dated section (and update the Verdicts row) to `docs/plans/2026-09-24-skill-symlink-probe.md` with the exact command, prompt and output lines. Commit.

---

### PM: muse spawn isolation
**Workflow:** `tdd-reviewed` (solo — user-requested fix, no separate spec story) · **Units:** 4

**Context:** user report, PR #20 — swarm-spawned muse agents got the operator's full
`HOME` (all of `~/.claude`, `~/.codex`, `~/.agents` personal skills/rules, plus every
operator MCP server credential in `~/.config/muse/settings.json`), unlike every other
adapter. Phase 1 probes, controller ruling and live-TUI evidence:
`docs/plans/2026-09-25-muse-isolation-probe.md`. Spec: `A8. muse spawn isolation`.

**Files:**
- `internal/adapter/muse.go` and `internal/adapter/muse_test.go`

**Interfaces (produces):**
- `Muse.setupEnv(s Spec) (map[string]string, error)` now returns `HOME`,
  `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME` (was
  `XDG_CONFIG_HOME` only).

**Acceptance:**
- A spawned muse's `HOME` is a fresh per-launch directory that symlinks in every real `~`
  entry except `.claude`, `.codex`, `.cursor`, `.agents`, `.gemini`, `.config`, `.muse`.
- `settings.json` is the real file cloned then mutated: `mcpServers` replaced with
  swarm-only, `context.foreign_personal_skills`/`context.foreign_personal_rules` set to
  `false`; every other real key (trust hashes, `tui` flags, `model`) preserved.
- Only swarm-managed skills (`install.LinkSkills`/`install.SkillsHome`) are linked into
  the isolated `XDG_CONFIG_HOME/muse/skills`, not the whole real dir.
- `XDG_DATA_HOME`/`XDG_STATE_HOME`/`XDG_CACHE_HOME` are pinned to the real paths
  (`m.d.UserHome/.local/share` etc.), not left to fall through the isolated `HOME`.
- `Resume` uses the same `setupEnv`, so the isolation is identical on relaunch.
- Live-TUI probe (PM.1) confirms no blocking first-run/foreign-context dialog under this
  env; `StartupDialogs()` unchanged.

**Verify:**
- `go test ./internal/adapter/... ./internal/install/...`
- `go build ./... && go vet ./...`
- `go test ./... -count=1`

#### Unit PM.1: Live TUI probe
- [x] Reproduce the proposed env by hand in a scratch tmux session (denylist-isolated
      `HOME`, isolated `XDG_CONFIG_HOME` with cloned-then-mutated `settings.json` and
      swarm-only skills, `XDG_DATA_HOME`/state/cache pinned to the real paths); launch
      muse interactively exactly as `Muse.argv` would.
- [x] Capture the pane at 5s / 15s / 30s; confirm no blocking dialog. Record commands, env
      and captures in `docs/plans/2026-09-25-muse-isolation-probe.md`.
- [x] Commit the probe doc: `docs(plan): live TUI probe for muse spawn isolation`.

#### Unit PM.2: `HOME` denylist isolation + pinned data/state/cache
- [x] Write the failing tests in `internal/adapter/muse_test.go` (temp `UserHome`,
      `launchDir`): `TestMuseSetupEnvIsolatesHOMEExceptOtherAgentsPersonalRoots` (fake
      `UserHome` has `.claude`, `.codex`, `.agents`, `.cursor`, `.gemini` plus an ordinary
      dotfile e.g. `.gitconfig`; assert the returned `HOME`'s dir has no `.claude` etc.
      entries but does have a `.gitconfig` symlink resolving into the fake `UserHome`);
      `TestMuseSetupEnvPinsDataStateCacheToRealHome` (assert `XDG_DATA_HOME` ==
      `<fake UserHome>/.local/share`, same for state/cache); `TestMuseResumeUsesSameIsolation`
      (Resume's env map equals Launch's shape). Record red.
- [x] Implement: in `setupEnv`, isolate `HOME` to a fresh per-launch dir, symlinking real
      `~` entries except the denylist; add `XDG_DATA_HOME`/`XDG_STATE_HOME`/`XDG_CACHE_HOME`
      to the returned map, pinned to `m.d.UserHome`-relative real paths.
- [x] Run green. Commit: `feat(adapter): isolate muse's HOME, pin its real data/state/cache dirs`.

#### Unit PM.3: settings clone-then-mutate
- [x] Write the failing test `TestMuseSetupEnvDropsOperatorMCPServersAndForeignContext`
      (fake real `settings.json` has an extra operator MCP server and an arbitrary unknown
      top-level key; assert the isolated `settings.json` has exactly one `mcpServers` key
      — `swarm` — keeps the arbitrary key, and has `context.foreign_personal_skills` and
      `context.foreign_personal_rules` both `false`). Record red.
- [x] Implement: after unmarshalling the real `settings.json` (unchanged), replace
      `mcpServers` with the swarm-only map (unchanged from today) and set
      `settings["context"] = map[string]any{"foreign_personal_skills": false,
      "foreign_personal_rules": false}` (merging into any existing `context` map rather
      than clobbering unrelated keys, if the real file has one).
- [x] Run green. Commit: `feat(adapter): drop operator MCP servers and foreign personal context from muse spawns`.

#### Unit PM.4: swarm-managed skills only
- [x] Write the failing test `TestMuseSetupEnvLinksOnlySwarmManagedSkills` (fake
      `UserHome/.config/muse/skills` has a real personal skill dir alongside nothing else;
      a separate fake swarm skills home has `swarm`/`swarm-orchestrator`; assert the
      isolated `skills/` dir contains symlinks only for the swarm-managed names, resolving
      into the swarm skills home, not the fake personal one). Record red.
- [x] Implement: replace the whole-dir `symlinkIfExists(realSkillsDir, museDir/skills)`
      call with `install.LinkSkills(filepath.Join(museDir, "skills"),
      skillsHome, install.SkillLinkMode(install.KindMuse))`.
- [x] Run green. Commit: `feat(adapter): link only swarm-managed skills into muse spawns`.

---

## Integration (epic level, run by the orchestrator)

- **Merge order:** P1, P5, P6, P2, P3, P4, P7, P8, P9, P10, P11, P12.
- **Verify:** `make test`, then `make e2e`.
- **Final review:** `reviewer` over the whole branch against the spec (`superpowers:requesting-code-review`).
- **Before accepting:**
  - Run `ponytail-debt` and list any `ponytail:` shortcuts introduced.
  - Do the manual smoke from spec Verification 6–7. Per agent kind, check that skills (including vendored and ponytail skills) are discovered. Then take one small feature spike → research tasks → design task → a plan that triggers a batching warning → materialize → engine delivery → accepted epic.

## Work breakdown

This is the plan in the swarm-tree format it introduces; it validates once P12 lands. Unit steps are abbreviated, and the package sections above are authoritative.

```swarm-tree
{
  "root": {"type": "epic", "title": "Self-contained tasks, workflow engine and role skills",
    "brief": "See docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md", "acceptance": ["All spec Verification steps pass"],
    "workflow": {"integration": {"merge_order": ["p1","p5","p6","p2","p3","p4","p7","p8","p9","p10","p11","p12"],
      "verify": ["make test", "make e2e"], "final_review": ["reviewer"]}}},
  "children": [
    {"ref": "s-a", "type": "story", "title": "Skills and roles", "brief": "Spec Part A", "acceptance": ["Spec A1-A6 met"],
      "children": [
        {"ref": "p1", "type": "task", "title": "Skill distribution", "brief": "Plan P1", "acceptance": ["Plan P1 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "Embed, registry, sync", "steps": ["Write registry/sync/mirror tests; record red (unit 1)", "Embed all:skills, Skills, SyncSkills, SkillNames; Makefile mirror; daemon sync", "Record green; commit"]},
            {"title": "Per-kind links and uninstall", "steps": ["Empirical symlink check per CLI", "Write link/skip/uninstall tests; record red (unit 2)", "Implement LinkSkills/WriteSkills; record green; commit"]},
            {"title": "Claude per-spawn links", "steps": ["Write TestClaudeProjectConfigLinksSkills; record red (unit 3)", "Link in writeProjectSwarmConfig; record green; commit"]},
            {"title": "Doctor", "steps": ["Write doctor tests; record red (unit 4)", "CheckSkills per kind, python3 warn, README line; record green; commit"]}],
          "verify": ["make skills-sync", "go test ./internal/install/... ./internal/adapter/... ./internal/migrate/...", "go vet ./..."],
          "workflow": {"template": "tdd-reviewed"}},
        {"ref": "p2", "type": "task", "title": "Vendored skills", "brief": "Plan P2", "acceptance": ["Plan P2 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "Provenance test and vendor README", "steps": ["Write TestVendoredSkillsHaveLicenseAndProvenance; run; fails", "Write vendor README and README license line; commit"]},
            {"title": "web-design-guidelines and building-components", "steps": ["Vendor with local rules.md and licenses", "Commit"]},
            {"title": "ui-ux-pro-max", "steps": ["Vendor trimmed, relative script paths", "Run search.py", "Commit"]},
            {"title": "Mobile skills", "steps": ["Vendor expo x2 (feedback stripped), RN, iOS, Android", "Commit"]},
            {"title": "ponytail", "steps": ["Vendor ponytail, ponytail-review, ponytail-debt; fixed full level", "Provenance test green", "Commit"]}],
          "verify": ["make skills-sync", "go test ./internal/install/...", "python3 skills/vendor/ui-ux-pro-max/scripts/search.py dashboard --domain style"],
          "workflow": {"steps": [{"id": "vendor", "run": "mechanical", "gates": ["commit", "verify"]},
            {"id": "review", "review": ["reviewer"], "loop": {"fix": "vendor", "max_rounds": 2}}]}},
        {"ref": "p3", "type": "task", "title": "Builder and reviewer skills", "brief": "Plan P3", "acceptance": ["Plan P3 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "Reference-checking tests", "steps": ["Write TestRoleSkillsReferenceTheirSkills and TestSuperpowersReferencesAreKnown; record red (unit 1)"]},
            {"title": "Core swarm rewrite", "steps": ["Rewrite swarm skill; its test rows green (unit 2)", "Commit"]},
            {"title": "swarm-coder", "steps": ["Write skill incl. ponytail precedence and unit execution; record green (unit 3)", "Commit"]},
            {"title": "swarm-reviewer and swarm-ui-reviewer", "steps": ["Write both; record green (unit 4)", "Commit"]},
            {"title": "swarm-designer and adopt swarm-batching", "steps": ["Write designer; check batching draft vs spec; record green (unit 5)", "Commit"]}],
          "verify": ["make skills-sync", "go test ./internal/install/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "p4", "type": "task", "title": "Support-role skills and kickoff", "brief": "Plan P4", "acceptance": ["Plan P4 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "swarm-debugger and swarm-mechanical", "steps": ["Test rows red (unit 1)", "Write skills; green; commit"]},
            {"title": "swarm-researcher", "steps": ["Test row red (unit 2)", "Write skill; green; commit"]},
            {"title": "swarm-advisor and advisor prompt", "steps": ["Advisor tests red (unit 3)", "Write skill and prompt; green; commit"]},
            {"title": "Kickoff role-skill table", "steps": ["Kickoff tests red (unit 4)", "RoleSkills table, signatures, spike/workflows stubs; green; commit"]}],
          "verify": ["make skills-sync", "go test ./internal/install/... ./internal/advisor/... ./internal/runtime/...", "go vet ./..."],
          "workflow": {"template": "tdd-reviewed"}}
      ]},
    {"ref": "s-b", "type": "story", "title": "Workflow engine and multi-agent tasks", "brief": "Spec Part B", "acceptance": ["Spec B1-B8 met"],
      "workflow": {"after_tasks": {"id": "story-review", "review": ["reviewer"]}},
      "children": [
        {"ref": "p5", "type": "task", "title": "Schema migrations", "brief": "Plan P5", "acceptance": ["Plan P5 acceptance"], "repos": ["agent-swarm"],
          "solo": "table-rebuilding migrations",
          "units": [
            {"title": "Migration 0010", "steps": ["Write TestMigration0010PreservesRowsAndWidensChecks; record red (unit 1)", "Write migration; green; commit"]},
            {"title": "Migration 0011", "steps": ["Write schema and one-live-workflow tests; record red (unit 2)", "Write migration; green; commit"]}],
          "verify": ["go test ./internal/db/... -count=1"], "workflow": {"template": "tdd-reviewed", "max_rounds": 4}},
        {"ref": "p6", "type": "task", "title": "Workflow model", "brief": "Plan P6", "acceptance": ["Plan P6 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "Types and templates", "steps": ["TestTemplatesResolve red (unit 1)", "Implement; green; commit"]},
            {"title": "Validation", "steps": ["TestValidateErrors red (unit 2)", "Implement; green; commit"]},
            {"title": "Resolve, RunRole, Render", "steps": ["Tests incl. golden red (unit 3)", "Implement; green; commit"]},
            {"title": "Next planner", "steps": ["TestNext table red (unit 4)", "Implement; green; commit"]}],
          "verify": ["go test ./internal/workflow/... -count=1", "go vet ./internal/workflow/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "p7", "type": "task", "title": "Roles, guards and item fields", "brief": "Plan P7", "acceptance": ["Plan P7 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "Designer role plumbing", "steps": ["Role tests red (unit 1)", "Implement; green; commit"]},
            {"title": "Block the Workflow tool", "steps": ["Hook tests red (unit 2)", "Implement; green; commit"]},
            {"title": "Item fields in the store", "steps": ["Store tests red (unit 3)", "Implement; green; commit"]},
            {"title": "MCP and HTTP wire", "steps": ["Wire tests red (unit 4)", "Implement; green; commit"]}],
          "verify": ["go test ./internal/...", "go vet ./...", "cd web && pnpm test"], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "p8", "type": "task", "title": "Checkpoint semantics", "brief": "Plan P8", "acceptance": ["Plan P8 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "Verdicts and findings", "steps": ["Verdict tests red (unit 1)", "Implement; green; commit"]},
            {"title": "Close siblings by role and step", "steps": ["Sibling tests red (unit 2)", "Implement; green; commit"]},
            {"title": "Per-agent completion and dependency wake-ups", "steps": ["Tests red (unit 3)", "Implement; green; commit"]},
            {"title": "tdd and verify gates", "steps": ["Gate tests incl. per-unit red (unit 4)", "Implement; green; commit"]},
            {"title": "Commit and artifact gates", "steps": ["Gate tests red (unit 5)", "Implement; green; commit"]}],
          "verify": ["go test ./internal/runtime/... ./internal/items/... ./internal/mcpserver/... -count=1", "go vet ./..."],
          "workflow": {"template": "tdd-reviewed"}},
        {"ref": "p9", "type": "task", "title": "Workflow engine", "brief": "Plan P9", "acceptance": ["Plan P9 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "Slots and briefs", "steps": ["Tests red (unit 1)", "Implement; green; commit"]},
            {"title": "Start and spawning steps", "steps": ["Tests incl. idempotency red (unit 2)", "Implement; green; commit"]},
            {"title": "Rounds, success, escalation, relays", "steps": ["Tests red (unit 3)", "Implement; green; commit"]},
            {"title": "Triggers and recovery", "steps": ["Tests red (unit 4)", "Implement; green; commit"]},
            {"title": "Resume, cancel, Done gating", "steps": ["Tests red (unit 5)", "Implement; green; commit"]}],
          "verify": ["go test ./internal/runtime/... ./internal/items/... ./internal/hook/... -count=1", "go vet ./..."],
          "workflow": {"template": "tdd-reviewed", "max_rounds": 4}},
        {"ref": "p10", "type": "task", "title": "Orchestrator surface, story and integration gates, e2e", "brief": "Plan P10", "acceptance": ["Plan P10 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "swarm_workflow", "steps": ["Tool tests red (unit 1)", "Implement; green; commit"]},
            {"title": "Spawn, checkpoint and read changes", "steps": ["Tests red (unit 2)", "Implement; green; commit"]},
            {"title": "Story and integration gates", "steps": ["Tests red (unit 3)", "Implement; green; commit"]},
            {"title": "e2e", "steps": ["Scenarios red via make e2e (unit 4)", "Harness helpers, fix bugs test-first; green; commit"]}],
          "verify": ["go test ./internal/mcpserver/... ./internal/runtime/... ./internal/items/...", "make e2e"],
          "workflow": {"template": "tdd-reviewed"}}
      ]},
    {"ref": "s-c", "type": "story", "title": "Planning, spikes and surfaces", "brief": "Spec Part C, B9", "acceptance": ["Spec C1-C5 and B9 met"],
      "children": [
        {"ref": "p11", "type": "task", "title": "Board and menubar", "brief": "Plan P11", "acceptance": ["Plan P11 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "Designer and chore labels", "steps": ["Web and Swift label tests red (unit 1)", "Implement; green; commit"]},
            {"title": "HTTP payload fields", "steps": ["Go payload tests red (unit 2)", "Implement; green; commit"]},
            {"title": "WorkflowSection", "steps": ["Component tests red (unit 3)", "Implement; green; commit"]},
            {"title": "Kanban crew and plan warnings", "steps": ["Tests red (unit 4)", "Implement; green; commit"]},
            {"title": "Menubar step suffix", "steps": ["Swift test red (unit 5)", "Implement; green; screenshots; commit"]}],
          "verify": ["go test ./internal/httpapi/...", "cd web && pnpm typecheck && pnpm test", "cd apps/menubar && swift test"],
          "workflow": {"template": "ui-tdd-reviewed"}},
        {"ref": "p12", "type": "task", "title": "Planning emits role-assigned packages", "brief": "Plan P12", "acceptance": ["Plan P12 acceptance"], "repos": ["agent-swarm"],
          "units": [
            {"title": "Tree fields and materialize", "steps": ["Tests red (unit 1)", "Implement; green; commit"]},
            {"title": "Plan lint", "steps": ["Lint tests red (unit 2)", "Implement lintTree; green; commit"]},
            {"title": "swarm-workflows", "steps": ["Example-validation test red (unit 3)", "Write skill; green; commit"]},
            {"title": "swarm-spike", "steps": ["Test row red (unit 4)", "Write skill; green; commit"]},
            {"title": "swarm-orchestrator and README", "steps": ["Test red (unit 5)", "Rewrite skill and README; green; commit"]}],
          "verify": ["make skills-sync", "go test ./internal/runtime/... ./internal/install/... ./internal/mcpserver/...", "cd web && pnpm test"],
          "workflow": {"template": "tdd-reviewed"}}
      ]}
  ],
  "deps": [
    {"item": "p2", "blocked_by": "p1"},
    {"item": "p3", "blocked_by": "p1"},
    {"item": "p4", "blocked_by": "p3"},
    {"item": "p7", "blocked_by": "p5"},
    {"item": "p7", "blocked_by": "p6"},
    {"item": "p8", "blocked_by": "p7"},
    {"item": "p9", "blocked_by": "p8"},
    {"item": "p10", "blocked_by": "p9"},
    {"item": "p11", "blocked_by": "p10"},
    {"item": "p12", "blocked_by": "p4"},
    {"item": "p12", "blocked_by": "p10"}
  ]
}
```
