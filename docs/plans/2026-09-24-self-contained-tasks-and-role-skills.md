# Self-contained Tasks, Workflow Engine and Role Skills — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **This plan is written in the shape it introduces.** Every task is a
> self-contained deliverable: its TDD cycle is *steps inside the task*, and
> its review → fix → re-verify loop is part of the task's acceptance, not a
> separate task. A task is finished only when all its steps are checked, its
> Verify commands pass, it is committed, and its review loop ended in a pass.

**Goal:** Replace micro-task plans and orchestrator-driven review loops with self-contained tasks whose build → review → fix → verify loop is run by the daemon from a declarative workflow; give every agent role its own skill (plus a new `designer` role and vendored UI/design skills); make spikes do deep research and emit per-task workflows.

**Architecture:** Three parts (spec: `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md`). (A) Recursive skill packaging with one on-disk copy under `~/.swarm/skills` symlinked into each agent kind, role skills named in each kickoff, eight vendored skills, a `designer` role, and the native `Workflow` tool blocked. (B) A pure `internal/workflow` package (DSL, templates, validation, `Next` planner) plus an engine in `internal/runtime/workflow.go` that starts, advances, escalates and finishes per-task workflows, with new tables (`workflows`, `workflow_runs`), review verdicts on checkpoints, per-step gates (tdd/commit/verify/artifact), and multi-agent-safe task semantics. (C) swarm-tree nodes carry `workflow`/`steps`/`verify`, plan registration rejects split review tasks and warns on split TDD phases, and the spike skill runs a research → design → plan → critic flow.

**Tech Stack:** Go (stdlib `embed`, `io/fs`, `database/sql` on SQLite, `testing`), React + TypeScript + Vitest + Biome (`web/`), Swift + XCTest (`apps/menubar/`), bash e2e harness (`scripts/e2e`).

**Spec:** `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md`

## Global Constraints

- Everything ships together in one release; no compatibility shims between daemon, web and menubar.
- Legacy behaviour is preserved exactly for tasks whose `workflow_json` is NULL (existing epics, board-created tasks). Every change to checkpoint/transition code must keep today's tests for legacy tasks green.
- Never delete or weaken an existing test to make a new one pass. Tests whose expectations change intentionally are listed in the task that changes them.
- After any edit under `skills/`, run `make skills-sync`; `go test ./internal/install/...` enforces the mirror.
- Each task ends with `go build ./... && go vet ./...` plus its package tests green; web tasks also `cd web && pnpm test && pnpm biome check`; menubar tasks `cd apps/menubar && swift test`.
- Commit at the end of every task (and after each review fix round) with a conventional message (`feat(scope): …`, `fix(scope): …`, `docs(skills): …`), signed, following the repo's existing commit style.
- Hot files: `internal/runtime/checkpoint.go`, `internal/runtime/agents.go`, `internal/items/transition.go`. Pull/rebase before starting a task that touches them.
- Review loop (every task): after the green + commit steps, request review with `superpowers:requesting-code-review` against the task's Acceptance; handle feedback with `superpowers:receiving-code-review`; fix, re-run Verify, commit; repeat until the reviewer passes (max 3 rounds, then escalate to the human).

## Review Focus

Spec inputs most likely to be under-tested — reviewers check these explicitly:

1. Legacy tasks (no workflow) behave byte-for-byte as before across checkpoint, transition and spawn paths.
2. Engine idempotency: a repeated `advance` (daemon restart, duplicate trigger) never double-spawns (`UNIQUE (workflow_id, step_id, round, role)`).
3. `closeCompletedSiblings` no longer tears down a builder when a reviewer completes on the same task, and vice versa.
4. Symlinked skills are actually discovered by each agent CLI (Task 2 empirical check drives the copy fallback).
5. Relay suppression: the orchestrator receives exactly one `workflow_succeeded` per successful task, and still receives `blocked`/`failed`/questions from step agents.

---

## Phase A — Skill platform, role skills, designer role

### Task 1: Recursive skill embed, registry and `~/.swarm/skills` sync

**Files:**
- Modify: `internal/install/skills.go`, `internal/install/skills_test.go`, `Makefile` (`skills-sync`), `cmd/swarm/daemon.go`
- Create: `internal/install/skills/` becomes a full mirror of `skills/`

**Interfaces:**
- Produces: `type Skill struct{ Name, Dir string; Vendored bool }`; `func Skills() ([]Skill, error)`; `func SkillFS() fs.FS`; `func SyncSkills(home string) (changed []string, err error)`; `const ManagedMarker = ".swarm-managed"`.
- Keeps (for callers until Task 2): `SkillNames` now derived from `Skills()`; `SkillBody(name)` reads `<Dir>/SKILL.md`.

**Acceptance:**
- Every file under `skills/` (nested dirs, LICENSE, scripts, data) is embedded and extracted to `~/.swarm/skills/<name>/…`; vendored skills are flattened by name.
- Files removed from the embed are deleted on the next sync; each extracted dir has `.swarm-managed`.
- `make skills-sync` mirrors the tree with deletes; the drift test compares every file.
- The daemon runs `SyncSkills` at startup.

**Verify:** `make skills-sync && go test ./internal/install/... && go build ./... && go vet ./...`

- [ ] **Step 1: Write failing tests** in `internal/install/skills_test.go`:

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
	for _, want := range []string{"swarm", "swarm-orchestrator"} {
		if !names[want] { t.Errorf("missing %s", want) }
	}
}

func TestSyncSkillsWritesNestedFilesAndPrunes(t *testing.T) {
	home := t.TempDir()
	if _, err := SyncSkills(home); err != nil { t.Fatal(err) }
	marker := filepath.Join(home, ".swarm", "skills", "swarm", ManagedMarker)
	if _, err := os.Stat(marker); err != nil { t.Fatalf("marker: %v", err) }
	stale := filepath.Join(home, ".swarm", "skills", "swarm", "stale.md")
	os.WriteFile(stale, []byte("x"), 0o644)
	if _, err := SyncSkills(home); err != nil { t.Fatal(err) }
	if _, err := os.Stat(stale); !os.IsNotExist(err) { t.Fatalf("stale file survived sync") }
}

func TestEmbeddedMirrorMatchesCanonicalTree(t *testing.T) {
	// walk ../../skills and SkillFS(); every file present in both, bytes equal
}
```

- [ ] **Step 2: Run red.** `go test ./internal/install/ -run 'TestSkillsRegistry|TestSyncSkills|TestEmbeddedMirror'` → compile errors (`Skills`, `SyncSkills`, `SkillFS` undefined). Record as red.
- [ ] **Step 3: Implement.** Replace the two-file embed with `//go:embed all:skills` → `var skillsFS embed.FS`; `SkillFS()` returns `fs.Sub(skillsFS, "skills")`. `Skills()` walks the FS: a dir containing `SKILL.md` is a skill; `Vendored = strings.HasPrefix(dir, "vendor/")`. `SyncSkills` writes every file with `WriteIfChanged` into `<home>/.swarm/skills/<name>/<rel>`, preserving the executable bit for files under `scripts/`, writes the marker, then deletes files under each managed dir that are not in the embed. `SkillNames` becomes `func SkillNames() []string` (update its 4 call sites: `WriteSkills`, `adapter/claude.go`, `uninstall.go`, `migrate/integrations.go`).
- [ ] **Step 4: Makefile.** `skills-sync: rsync -a --delete skills/ internal/install/skills/`. Run it.
- [ ] **Step 5: Daemon start.** In `cmd/swarm/daemon.go`, before the reconcile loop starts, call `install.SyncSkills(home)` and log changed count; a failure logs and continues (skills are not fatal to the daemon).
- [ ] **Step 6: Run green**, then the full Verify line. Existing `TestWriteSkills*` count assertions (`skills_test.go:66`) and `internal/migrate/recover_test.go:479-517` now derive their expected count from `len(SkillNames())` — update them to do so (intentional change).
- [ ] **Step 7: Commit** `feat(install): embed and sync the full skills tree`.
- [ ] **Step 8: Review loop** (Global Constraints).

### Task 2: Per-kind skill exposure by symlink (user-owned safe), Claude per-spawn links, doctor

**Files:**
- Modify: `internal/install/skills.go`, `internal/install/{claude,codex,agy,cursor,muse}.go` (Check*), `internal/install/doctor.go`, `internal/install/uninstall.go`, `internal/adapter/claude.go` (`writeProjectSwarmConfig`), tests alongside each.

**Interfaces:**
- Consumes: `Skills()`, `SyncSkills`, `ManagedMarker` (Task 1).
- Produces: `func WriteSkills(c Config, k Kind) (changed []string, skipped []string, err error)`; `func LinkSkills(root, skillsHome string, mode LinkMode) (skipped []string, err error)` with `LinkMode` `Symlink|Copy`; `var skillLinkMode = map[Kind]LinkMode{…}` set from the empirical check below; `func CheckSkills(c Config, k Kind) Check`.

**Acceptance:**
- For every kind, `<skills-root>/<name>` is a symlink to `~/.swarm/skills/<name>` (or a managed copy where the CLI can't follow symlinks).
- An existing non-swarm entry with the same name is left untouched and reported by doctor: `skill <name> for <kind> is user-owned; swarm's copy is not installed there`.
- Claude spawns get symlinks in `<cwd>/.claude/skills/` instead of copied files.
- Uninstall removes only swarm symlinks/managed dirs.
- Doctor has a skills check for every kind and a `python3` warn check.

**Verify:** `go test ./internal/install/... ./internal/adapter/... ./internal/migrate/... && go build ./... && go vet ./...`

- [ ] **Step 1: Empirical symlink check (spike step, record results in the commit message).** For each installed CLI (claude, codex, agy, cursor-agent, muse), create a throwaway skill dir symlinked into its skills root and ask the CLI to list skills (or start a session and ask it to name available skills). Record which follow symlinks. Set `skillLinkMode` accordingly (default `Symlink`; `Copy` for any that don't). If a CLI isn't installed locally, default it to `Copy` and note that in the commit message.
- [ ] **Step 2: Write failing tests:** `TestWriteSkillsSymlinksEveryManagedSkill`, `TestWriteSkillsSkipsUserOwnedSameName` (pre-create a real dir with a user `SKILL.md`; assert untouched and returned in `skipped`), `TestWriteSkillsReplacesStaleSwarmCopy` (an old `.swarm-managed` dir becomes a symlink), `TestUninstallRemovesOnlySwarmSkills`, `TestClaudeProjectConfigLinksSkills` (in `adapter/claude_test.go`: `<cwd>/.claude/skills/swarm` is a symlink into the fake home), `TestDoctorChecksSkillsForEveryKind`, `TestDoctorWarnsWithoutPython3`.
- [ ] **Step 3: Run red** (`go test ./internal/install/ ./internal/adapter/ -run 'Skills|Doctor|ProjectConfig'`) — failures on missing funcs/behaviour. Record red.
- [ ] **Step 4: Implement** `LinkSkills` (swarm-owned = symlink whose target is under `skillsHome`, or a dir with `ManagedMarker`); `WriteSkills` calls `SyncSkills` then `LinkSkills`; `writeProjectSwarmConfig` calls `LinkSkills(<cwd>/.claude/skills, ~/.swarm/skills, skillLinkMode[Claude])`; `CheckSkills` verifies each skill resolves to a readable `SKILL.md`; add `python3` to `doctor.go` base checks as warn.
- [ ] **Step 5: Run green**, then full Verify.
- [ ] **Step 6: Commit** `feat(install): link swarm skills into every agent kind`.
- [ ] **Step 7: Review loop.**

### Task 3: Vendor eight third-party skills with licenses and provenance

**Files:**
- Create: `skills/vendor/{web-design-guidelines,building-components,ui-ux-pro-max,expo-native-ui,expo-design-system,vercel-react-native-skills,mobile-ios-design,mobile-android-design}/**`, each with `LICENSE` and `VENDORED.md`; `skills/vendor/README.md`
- Modify: `internal/install/skills_test.go`, `README.md` (License section line), mirror via `make skills-sync`

**Interfaces:**
- Consumes: Task 1 registry (vendored dirs are discovered automatically).

**Acceptance:**
- Sources, pinned commits, license files and modifications exactly as the spec's A2 table; `VENDORED.md` in the spec's format with the real commit sha.
- web-design-guidelines reads `references/rules.md` (vendored `command.md`) — no runtime WebFetch; ui-ux-pro-max has no `scripts/tests/`, and every `search.py` invocation is relative to the skill dir; expo skills have no "Submitting Feedback" section.
- A test enforces license + VENDORED.md presence and forbids the strings `CLAUDE_PLUGIN_ROOT`, `submit-expo-feedback` and `raw.githubusercontent.com` anywhere under `skills/vendor/`.
- `python3 skills/vendor/ui-ux-pro-max/scripts/search.py "dashboard" --domain style` runs and returns results.

**Verify:** `make skills-sync && go test ./internal/install/... && python3 skills/vendor/ui-ux-pro-max/scripts/search.py "dashboard" --domain style | head -5`

- [ ] **Step 1: Write failing test** `TestVendoredSkillsHaveLicenseAndProvenance` (for each `Vendored` skill: `LICENSE*` and `VENDORED.md` exist; VENDORED.md contains `Source:`, `License:`, `Changes:`; banned strings absent). Run red — fails because no vendored skills exist yet (test asserts the eight expected names are present).
- [ ] **Step 2: Fetch sources** at their current default-branch commits (shallow clones into a scratch dir), copy only the skill folders listed in the spec, plus `vercel-labs/web-interface-guidelines/command.md` → `web-design-guidelines/references/rules.md` and upstream LICENSE files (web-design-guidelines: write MIT text "Copyright (c) Vercel, Inc." and note in VENDORED.md that upstream has no LICENSE file and states MIT in its README).
- [ ] **Step 3: Apply the modifications** listed in the spec; record each in `VENDORED.md`.
- [ ] **Step 4: Write `skills/vendor/README.md`** (table: name, source, license, why we vendor it, which role skills use it) and the README License line.
- [ ] **Step 5: Run green** + Verify line (python check included).
- [ ] **Step 6: Commit** `feat(skills): vendor UI, design and mobile skills with provenance`.
- [ ] **Step 7: Review loop** — reviewer additionally checks each license file against upstream.

### Task 4: `designer` role end-to-end

**Files:**
- Create: `internal/db/schema/0010_designer_and_artifact_kinds.sql`
- Modify: `internal/kinds/kinds.go`, `internal/runtime/{types.go,titles.go,agents.go (OverridableRoles)}`, `internal/settings/settings.go`, `web/src/{types.ts,copy.ts,components/AgentFields.tsx,mock/fixtures.ts,copy.test.ts}`, `apps/menubar/Sources/SwarmBarKit/{Wire.swift,Copy.swift,SettingsModel.swift}` + tests.

**Interfaces:**
- Produces: `kinds.RoleDesigner = "designer"`; artifact kinds `design`, `research` allowed by the DB.

**Acceptance:**
- `designer` is a valid agent role everywhere (DB CHECK, settings validation, overrides, catalog roles, emoji 🎨, labels "Designer" in web and menubar, Settings defaults row).
- Default settings: `designer: {agent: claude, model: opus}`.
- `artifacts.kind` accepts `design` and `research`.
- `chore` added to web `ItemType` and its label (bug found in research).

**Verify:** `go test ./internal/kinds/... ./internal/settings/... ./internal/runtime/... ./internal/db/... && cd web && pnpm test && pnpm biome check && cd ../apps/menubar && swift test`

- [ ] **Step 1: Write failing tests:** Go `TestSettingsDefaultsIncludeDesigner`, `TestSpawnDesignerRoleAccepted` (runtime, fake kind; insert succeeds), `TestMigration0010AllowsDesignerAndDesignArtifacts` (db); web `copy.test.ts` asserts `ROLE_LABEL.designer === "Designer"` and `ITEM_TYPE_LABEL.chore`; Swift `testRoleDecodesDesigner`, `testDefaultsOrderIncludesDesigner`.
- [ ] **Step 2: Run red** in each toolchain; record.
- [ ] **Step 3: Implement** the Go side and migration (copy `0008`'s table-rebuild pattern for `agents`; rebuild `artifacts` with the widened kind CHECK, preserving rows and indexes; `PRAGMA foreign_keys` handling identical to `0008`).
- [ ] **Step 4: Implement** web and menubar mirrors.
- [ ] **Step 5: Run green**, full Verify.
- [ ] **Step 6: Commit** `feat: add designer agent role`.
- [ ] **Step 7: Review loop.**

### Task 5: Block the native `Workflow` tool

**Files:** Modify `internal/hook/handler.go`, `internal/hook/handler_test.go`.

**Acceptance:** PreToolUse for tool names `Workflow`/`workflow` in a swarm session is blocked with `[swarm] The Workflow tool is disabled in Swarm sessions. Use swarm_spawn or swarm_workflow.`; outside swarm sessions nothing changes.

**Verify:** `go test ./internal/hook/... && go vet ./...`

- [ ] **Step 1: Failing test** `TestPreToolUseBlocksWorkflowTool` (mirror the existing `Agent` block test) and `TestWorkflowToolAllowedOutsideSwarm`.
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement** — add the names to the blocked set with the new reason string.
- [ ] **Step 4: Run green**; commit `feat(hook): block the native Workflow tool in swarm sessions`.
- [ ] **Step 5: Review loop.**

### Task 6: Role skills — core protocol, coder, reviewer, ui-reviewer, designer

**Files:**
- Modify: `skills/swarm/SKILL.md`
- Create: `skills/swarm-coder/SKILL.md`, `skills/swarm-reviewer/SKILL.md`, `skills/swarm-ui-reviewer/SKILL.md`, `skills/swarm-designer/SKILL.md`
- Modify: `internal/install/skills_test.go` (content assertions)

**Acceptance (content per spec A4):**
- `swarm`: protocol only; TDD rule moved out; new "one step of a workflow" rule; `Workflow` listed among disabled tools; rule 9a points to `swarm-advisor`.
- `swarm-coder` references `superpowers:test-driven-development`, `superpowers:verification-before-completion`, `superpowers:receiving-code-review`; states "commit your own work" and the `completed` git contract (`dirty:false`, HEAD sha); explains red/green verification entries and `## Steps`/`## Verify`/`## Workflow`.
- `swarm-reviewer` references `superpowers:requesting-code-review`; defines verdict + findings contract and severity rule; "never edit files".
- `swarm-ui-reviewer` references `swarm-reviewer` + `web-design-guidelines`, `building-components`, `ui-ux-pro-max` (pro-rules checklist), `mobile-ios-design`, `mobile-android-design`, `expo-native-ui`, `expo-design-system`, `vercel-react-native-skills`.
- `swarm-designer` references `superpowers:brainstorming`, `ui-ux-pro-max`, `building-components`, mobile skills; defines the design artifact path and sections; "no product code".
- A test asserts each skill mentions the skill names above (so renames upstream get caught) and that every `superpowers:<x>` reference is in a known list of superpowers v6 skill names.

**Verify:** `make skills-sync && go test ./internal/install/...`

- [ ] **Step 1: Failing test** `TestRoleSkillsReferenceTheirSkills` (table: skill → required substrings) and `TestSuperpowersReferencesAreKnown` (regex `superpowers:([a-z-]+)` over all non-vendored skills; allowed set = the 15 v6.4.1 skill names). Run red.
- [ ] **Step 2: Write the skills** per spec A4 (≤ ~200 lines each; frontmatter `name`/`description`; opening line "Follow the `swarm` skill first; this adds to it.").
- [ ] **Step 3: Run green**; commit `docs(skills): core protocol and coder/reviewer/ui-reviewer/designer role skills`.
- [ ] **Step 4: Review loop** — reviewer reads each skill as the target agent would: is every instruction actionable with the tools that role has?

### Task 7: Role skills — debugger, mechanical, researcher, advisor (+ advisor prompt)

**Files:**
- Create: `skills/swarm-{debugger,mechanical,researcher,advisor}/SKILL.md`
- Modify: `internal/advisor/*` (system prompt text), `internal/install/skills_test.go`

**Acceptance (spec A4):**
- `swarm-debugger` → `superpowers:systematic-debugging`, `superpowers:test-driven-development` (regression test first), advisor before root cause.
- `swarm-mechanical` is short (≤ 60 lines): exact change, no refactors, verify, commit, `blocked` if judgement needed.
- `swarm-researcher` → `superpowers:brainstorming` on its own sub-question (no user dialogue), the deep-research loop and notes format, notes path, never invent.
- `swarm-advisor` is original text with the mermaid diagram and the CC BY-NC inspiration credit; the simulated advisor's system prompt in `internal/advisor` states the same decision rules (one decision, decisive answer, evidence over authority, read-only).

**Verify:** `make skills-sync && go test ./internal/install/... ./internal/advisor/...`

- [ ] **Step 1: Failing tests:** extend `TestRoleSkillsReferenceTheirSkills`; `TestAdvisorSkillHasMermaidAndCredit`; `TestAdvisorSystemPromptDecisionRules` (asserts key phrases in the prompt builder's output). Run red.
- [ ] **Step 2: Write the skills and update the advisor prompt.**
- [ ] **Step 3: Run green**; commit `docs(skills): debugger, mechanical, researcher and advisor skills`.
- [ ] **Step 4: Review loop.**

### Task 8: Kickoff names the role skill; mandate for every role

**Files:** Modify `internal/runtime/text.go`, `internal/runtime/text_test.go`, callers of `Kickoff`/`ResumeKickoff` in `internal/runtime/agents.go`.

**Interfaces:**
- Produces: `func Kickoff(name string, role Role, itemType items.Type, key, title string) string`, same extra param on `ResumeKickoff`; `func RoleSkills(role Role, itemType items.Type) []string`.

**Acceptance:**
- Kickoff lists exactly the spec A3 table's skills per role; a spike orchestrator gets `swarm-spike` instead of `swarm-orchestrator`.
- Every role gets the mandate sentence.
- A test asserts every name returned by `RoleSkills` for every role exists in `install.Skills()` (catches a role skill that was never written). Until Task 20 lands, `swarm-spike` and `swarm-workflows` must exist — create them in this task as stubs with valid frontmatter and a one-line body "Filled in by Task 20" so the test can pass; Task 20 replaces them.

**Verify:** `go test ./internal/runtime/ -run 'Kickoff|RoleSkills' && go test ./internal/install/... && go build ./... && go vet ./...`

- [ ] **Step 1: Failing tests** `TestKickoffNamesRoleSkill` (table over roles + spike), `TestRoleSkillsExist`, `TestKickoffMandateForEveryRole`. Existing `text_test.go` assertions on the old kickoff string are updated to the new format (intentional).
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement** the table and signature change; update call sites (they already have the item).
- [ ] **Step 4: Run green**; commit `feat(runtime): kickoff names each role's skill`.
- [ ] **Step 5: Review loop.**

---

## Phase B — Multi-agent tasks and the workflow engine

### Task 9: `internal/workflow` — DSL types, templates, validation, resolve, render

**Files:** Create `internal/workflow/{spec.go,templates.go,validate.go,resolve.go,render.go}` and `*_test.go`.

**Interfaces:**
- Produces (exactly as spec B2): `Gate`, `Loop`, `Step`, `Integration`, `Spec`; `type Level int` (`LevelTask`, `LevelStory`, `LevelRoot`); `Validate(Level, Spec) error`; `Resolve(Spec, tddExempt bool) (Spec, error)`; `DefaultFor(roleHint string) Spec`; `Render(Spec, stepID string, round int) string`; `Templates map[string]Spec`; `func (s Spec) Step(id string) (Step, bool)`; `func (s Spec) MaxRoundsFor(stepID string) int`.

**Acceptance:**
- The six templates resolve to the spec's table.
- Every validation rule in spec B2 has a test with the spec's exact error copy.
- `Resolve` expands a template, applies `max_rounds` override to every loop, defaults `retries` to 1, removes `tdd` gates when `tddExempt`, and fills `of` with the nearest preceding run step.
- `Render` output matches the spec B6 example for `tdd-reviewed`/`build`/round 2 (golden test).

**Verify:** `go test ./internal/workflow/... && go vet ./internal/workflow/...`

- [ ] **Step 1: Failing tests.** `TestTemplatesResolve` (table: template → expected step list), `TestValidateErrors` (table: spec → exact error string, one row per rule), `TestResolveDropsTDDWhenExempt`, `TestResolveMaxRoundsOverride`, `TestDefaultForRoleHints`, `TestRenderBuildStepGolden` (golden file `testdata/render_build_r2.txt`).
- [ ] **Step 2: Run red** (package doesn't exist).
- [ ] **Step 3: Implement.** Templates:

```go
var Templates = map[string]Spec{
	"tdd-reviewed":    reviewed("build", "coder", []Gate{GateTDD, GateCommit, GateVerify}, []string{"reviewer"}, 3),
	"ui-tdd-reviewed": reviewed("build", "coder", []Gate{GateTDD, GateCommit, GateVerify}, []string{"reviewer", "ui_reviewer"}, 3),
	"design-reviewed": reviewed("design", "designer", []Gate{GateArtifactDesign}, []string{"ui_reviewer"}, 2),
	"debug":           reviewed("fix", "debugger", []Gate{GateTDD, GateCommit, GateVerify}, []string{"reviewer"}, 3),
	"mechanical":      {Steps: []Step{{ID: "change", Run: "mechanical", Gates: []Gate{GateCommit, GateVerify}}}},
	"research":        {Steps: []Step{{ID: "research", Run: "researcher", Gates: []Gate{GateArtifactNotes}}}},
}

func reviewed(id, role string, gates []Gate, reviewers []string, rounds int) Spec {
	return Spec{Steps: []Step{
		{ID: id, Run: role, Gates: gates},
		{ID: "review", Review: reviewers, Of: id, Loop: &Loop{Fix: id, MaxRounds: rounds, OnExhausted: "escalate"}},
	}}
}
```

- [ ] **Step 4: Run green**; `go vet`; commit `feat(workflow): declarative task workflow DSL`.
- [ ] **Step 5: Review loop.**

### Task 10: `internal/workflow` — the `Next` planner

**Files:** Create `internal/workflow/next.go`, `internal/workflow/next_test.go`.

**Interfaces:**
- Produces:

```go
type RunState string // "waiting","active","completed","failed","cancelled"
type Verdict string  // "","pass","changes_requested","blocked"

type Run struct {
	StepID      string
	Round       int
	Role        string
	State       RunState
	Verdict     Verdict
	Findings    []Finding
	SHA         string
	AutoRetries int
}

type Finding struct{ Severity, File string; Line int; Summary string }

type ActionKind string // "spawn","retry_fix","auto_retry","wait","succeed","escalate"

type Action struct {
	Kind     ActionKind
	StepID   string
	Roles    []string   // spawn: roles to spawn (review steps: all reviewers)
	Round    int
	Findings []Finding  // retry_fix: merged findings of the round
	Run      *Run       // auto_retry: which run
	SHA      string     // succeed / spawn review: sha under review
	Reason   string     // escalate
}

func Next(s Spec, runs []Run, round, extraRounds int) Action
```

**Acceptance:** `Next` is total and deterministic over the cases in spec B4's table, including: first spawn; wait while active/waiting; review spawn with the builder's sha after build completes; all-pass → succeed with that sha; any `changes_requested` and `round < max+extra` → `retry_fix` with merged findings (stable order: reviewer role order, then file, line); exhausted → escalate `"review rounds exhausted (3/3)"`; any `blocked` → escalate `"reviewer blocked: <summary>"`; failed run with `AutoRetries < retries` → `auto_retry`, else escalate `"<role> <state> twice"`; single-step templates succeed after the run completes; a completed run step without sha when the step has the commit gate is impossible by construction (gate) but `Next` escalates defensively with `"build completed without a sha"`.

**Verify:** `go test ./internal/workflow/... -run Next -count=1 && go vet ./internal/workflow/...`

- [ ] **Step 1: Failing table test** `TestNext` with ≥ 14 rows covering every case above (runs built with a small helper `r(step, round, role, state, verdict)`).
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement** `Next`: find the current round's runs per step in spec order; the first step whose runs are missing → spawn; any non-terminal → wait; evaluate review aggregation; handle failures first (auto-retry/escalate) before verdict logic.
- [ ] **Step 4: Run green**; commit `feat(workflow): pure next-action planner`.
- [ ] **Step 5: Review loop** — reviewer tries to construct a runs table where `Next` loops or double-spawns.

### Task 11: Schema + items carry workflow, steps and verify

**Files:**
- Create: `internal/db/schema/0011_workflows.sql` (spec B1 + `workflows.extra_rounds`)
- Modify: `internal/items/{model.go,store.go}`, `internal/items/store_test.go`, `internal/mcpserver/orchestrator.go` (`swarm_items` fields), `internal/mcpserver/tools.go` (`swarm_read` item output), `internal/httpapi/items.go` (wire output), `web/src/types.ts` (Item fields)

**Interfaces:**
- Produces: `items.Item.Workflow *workflow.Spec`, `Steps []string`, `Verify []string`; same on `CreateInput` and `Patch`; wire JSON `workflow`, `steps`, `verify`.

**Acceptance:**
- Migration applies on a copy of a real DB fixture (`testdata`) and on a fresh DB.
- `CreateTx`/`UpdateTx` validate (`workflow.Validate` at the item's level) and store the resolved spec; only orchestrator/daemon actors may set them (same rule/copy style as `tdd_exempt`).
- `swarm_items create/update` accept the fields; `swarm_read` and `GET /api/items/:key` return them.

**Verify:** `go test ./internal/db/... ./internal/items/... ./internal/mcpserver/... ./internal/httpapi/... && go build ./... && go vet ./... && cd web && pnpm test`

- [ ] **Step 1: Failing tests** `TestMigration0011`, `TestCreateTaskStoresResolvedWorkflow`, `TestCreateRejectsInvalidWorkflow`, `TestUserCannotSetWorkflow`, `TestSwarmItemsAcceptsWorkflowStepsVerify`, `TestSwarmReadReturnsWorkflowFields`.
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement** migration, model, store (JSON columns; `scanItem` updated), MCP schema + handler, HTTP wire, web types.
- [ ] **Step 4: Run green**; commit `feat(items): store workflow, steps and verify on items`.
- [ ] **Step 5: Review loop.**

### Task 12: Multi-agent-safe tasks: verdicts, sibling close by role+step, per-agent completion, dep wake-ups

**Files:** Modify `internal/runtime/{checkpoint.go,model.go,reconcile.go}`, `internal/items/transition.go`, `internal/mcpserver/tools.go` (`swarm_checkpoint` schema), tests: `internal/runtime/checkpoint_test.go`, `internal/items/transition_test.go`, `internal/runtime/reconcile_test.go`.

**Interfaces:**
- Produces: `CheckpointInput.Verdict string`, `CheckpointInput.Findings []workflow.Finding`; `Checkpoint.Verdict`, `.Findings`; helper `func (s *Store) workflowRunFor(ctx, tx, agentID string) (*workflowRunRow, error)` (nil for legacy agents) — the row type lives in `workflow.go` created here as a stub with just the query.

**Acceptance:**
- `verdict` rules per spec B5 with exact copy; stored in the new columns.
- `closeCompletedSiblings` closes only same-role (and, for workflow agents, same-step) siblings. **Intentional test change:** any existing test asserting a coder's `completed` closes a reviewer on the same item is updated to the new rule; legacy same-role duplicate closing is still covered.
- `completedCurrent` is per agent (spec B5); a test reproduces today's bug (agent A completed at attempt 1, agent B retried to attempt 2 and not completed → Done was refused) and shows it fixed.
- `OnDepUnblocked` wakes all distinct parents of active agents on the item.

**Verify:** `go test ./internal/runtime/... ./internal/items/... ./internal/mcpserver/... && go build ./... && go vet ./...`

- [ ] **Step 1: Failing tests** `TestReviewerCompletedDoesNotCloseBuilder`, `TestBuilderCompletedDoesNotCloseReviewer`, `TestSameRoleSiblingStillClosed`, `TestVerdictRequiredForWorkflowReviewer`, `TestVerdictRefusedForCoder`, `TestPassVerdictRefusesMajorFindings`, `TestCompletedCurrentIsPerAgent`, `TestDepUnblockedWakesAllParents`.
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement.** Sibling query adds `AND a2.role = ? AND COALESCE((SELECT step_id FROM workflow_runs WHERE agent_id = a2.id ORDER BY round DESC LIMIT 1), '') = ?` with the caller's role and step. `completedCurrent`: select the latest `completed` checkpoint on the item from a gated-role (or designer/researcher) agent and compare its attempt to that agent's own `MAX(attempt)`.
- [ ] **Step 4: Run green** + full Verify; commit `fix(runtime): make tasks safe for multiple agents`.
- [ ] **Step 5: Review loop** — Review Focus items 1 and 3.

### Task 13: Step gates — tdd, commit, verify, artifact

**Files:** Modify `internal/runtime/checkpoint.go`, `internal/runtime/artifacts.go` (`registerArtifactAsDaemon`), tests in `checkpoint_test.go`, `artifacts_test.go`.

**Interfaces:**
- Produces: `func tddOK(entries []Verify) string`, `func verifyDeclaredOK(declared []string, entries []Verify) string`, `func (s *Store) commitOK(ctx, agentID string, git []GitRef) (sha string, msg string)`, `func (s *Store) artifactGate(ctx, tx, it items.Item, rootKey string, gate workflow.Gate, paths []string) (string, error)`, `func (s *Store) registerArtifactAsDaemon(ctx, tx, itemID, kind, path string) error`.

**Acceptance:**
- For agents with a workflow run, the step's gates replace `verifyOK`; legacy agents unchanged (existing gate tests untouched and green).
- Each gate enforces the spec B5 rule with the exact copy; `tdd` skipped for `tdd_exempt`; the commit gate stores the sha on the run row; artifact gates register `design`/`research` artifacts on the task.

**Verify:** `go test ./internal/runtime/... -run 'Gate|TDD|Verify|Commit|Artifact' && go test ./internal/runtime/... && go vet ./...`

- [ ] **Step 1: Failing tests** `TestTDDGateNeedsRedBeforeGreen` (green only → refused; red then green across two checkpoints of one attempt → ok; red in attempt 1 and green in attempt 2 → refused), `TestTDDGateSkippedWhenExempt`, `TestVerifyGateMatchesDeclaredCommands` (containment + whitespace normalisation), `TestCommitGateRefusesDirtyWorktree`, `TestCommitGateRefusesShaMismatch`, `TestCommitGateStoresSha`, `TestDesignArtifactGateRegistersArtifact`, `TestLegacyCoderKeepsVerifyOK`. Use a real temp git repo for commit tests (existing helpers in `runtime/helpers_test.go`).
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement** the gate functions and wire them into the `CompletedCkp` block behind `workflowRunFor != nil`.
- [ ] **Step 4: Run green**; commit `feat(runtime): enforce per-step workflow gates`.
- [ ] **Step 5: Review loop.**

### Task 14: Engine core — start, advance, fix rounds, succeed, escalate, budget

**Files:** Create/extend `internal/runtime/workflow.go`, `internal/runtime/workflow_test.go`; modify `internal/runtime/{limits.go,agents.go,text.go,checkpoint.go (relay suppression)}`, `internal/hook/handler.go` (use `SubagentSlots`).

**Interfaces:**
- Produces:

```go
type StartWorkflowInput struct {
	ItemKey   string
	Worktrees []WorkflowWorktree // {WorktreeID, Mode}
	Context   []string
	SessionID, RequestID string
}
type WorkflowState struct {
	ID, State, Escalation string
	Round, MaxRounds       int
	Runs                   []WorkflowRunView
}
func (s *Store) StartWorkflow(ctx context.Context, orch Agent, in StartWorkflowInput) (WorkflowState, error)
func (s *Store) WorkflowFor(ctx context.Context, itemKey string) (*WorkflowState, error)
func (s *Store) advance(ctx context.Context, workflowID string) error
func (s *Store) SubagentSlots(ctx context.Context, parentID string) (used, max int, err error)
func BriefForStep(it items.Item, spec workflow.Spec, stepID string, round int, ctxLines []string) BriefInput
```

**Acceptance:**
- Start validates per spec B4 with exact copy and returns the state.
- `advance` applies every `Next` action as spec B4 describes: spawns run/review agents with daemon-rendered briefs (spec B6, including `## Steps` and `## Workflow` sections added to `RenderBrief`), shares rw worktrees to builders and creates/shares/removes review worktrees, retries builders with rendered findings, moves the task InReview ↔ InProgress and to Done (daemon actor), and sends `workflow_succeeded` / `workflow_escalated` relays plus the `workflow.escalated` notification.
- Idempotent: calling `advance` twice in a row spawns nothing extra (Review Focus 2).
- Budget: runs beyond `max_concurrent_subagents` are inserted `waiting` and spawned FIFO when slots free; the hook uses `SubagentSlots` (hook tests unchanged and green).
- `WriteCheckpoint` suppresses `accepted`/`progress`/`completed` relays for workflow agents (Review Focus 5).

**Verify:** `go test ./internal/runtime/... ./internal/hook/... -count=1 && go build ./... && go vet ./...`

- [ ] **Step 1: Failing tests** (runtime with the fake adapter and a temp git repo): `TestStartWorkflowValidates` (no workflow / running exists / no rw worktree / deps open), `TestWorkflowHappyPath` (build completed with gates → reviewer spawned on review worktree at sha → pass → task Done, one `workflow_succeeded` relay, review worktree removed), `TestWorkflowFixRound` (changes_requested → builder retried with findings note in its `assignment_update`, task back to InProgress, round 2), `TestWorkflowEscalatesWhenRoundsExhausted`, `TestWorkflowParallelReviewers` (ui template spawns reviewer + ui_reviewer together; mixed verdicts → fix round), `TestWorkflowAutoRetryOnCrash`, `TestAdvanceIsIdempotent`, `TestWorkflowWaitsForBudgetFIFO`, `TestWorkflowRelaysSuppressed`, `TestRenderBriefStepsAndWorkflowSections`.
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement** `SubagentSlots` (move the hook's query into `limits.go`, hook calls it), `BriefForStep` + `RenderBrief` sections, `StartWorkflow`, `advance` (per-workflow mutex keyed by id; each action's DB writes in one tx, side effects after commit, run rows inserted with `INSERT … ON CONFLICT DO NOTHING` on the unique key and the spawn skipped if no row was inserted).
- [ ] **Step 4: Run green** + full Verify; commit `feat(runtime): daemon workflow engine`.
- [ ] **Step 5: Review loop** — Review Focus 2 and 5; the reviewer adds one adversarial test if they can break idempotency.

### Task 15: Engine triggers, recovery, Done gating, resume and cancel

**Files:** Modify `internal/runtime/{workflow.go,checkpoint.go,reconcile.go}`, `internal/items/transition.go`; tests.

**Interfaces:**
- Produces: `func (s *Store) ResumeWorkflow(ctx, orch Agent, itemKey, decision, note, sessionID, requestID string) (WorkflowState, error)`, `func (s *Store) CancelWorkflow(ctx, orch Agent, itemKey string) (WorkflowState, error)`, `func (s *Store) recoverWorkflows(ctx) error` (called from `Reconcile`).

**Acceptance:**
- `advance` is triggered after commit by: completed/failed checkpoints of workflow agents, session death of workflow agents in `reconcile.go`, a child of the owner leaving a budget slot, start/resume.
- Recovery scan: running workflows whose latest run is terminal and `updated_at` is > 30 s old are advanced; test simulates "commit happened, advance never ran".
- `checkTask`: Done on a workflow task only by daemon after success (or `accept`); orchestrator `swarm_items update status:"done"` refused with spec copy.
- `resume` retry/accept/fail and `cancel` behave per spec B7 (extra round recorded in `extra_rounds`; accept is recorded; fail/cancel → task Ready, active run agents cancelled).

**Verify:** `go test ./internal/runtime/... ./internal/items/... -count=1 && go vet ./...`

- [ ] **Step 1: Failing tests** `TestCheckpointTriggersAdvance`, `TestCrashTriggersAdvance`, `TestSlotReleaseSpawnsWaitingRun`, `TestRecoverStalledWorkflow`, `TestOrchestratorCannotMarkWorkflowTaskDone`, `TestResumeRetryGrantsExtraRound`, `TestResumeAccept`, `TestResumeFail`, `TestCancelWorkflow`, `TestResumeRefusedWhenNotEscalated`.
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run green**; commit `feat(runtime): workflow triggers, recovery, resume and cancel`.
- [ ] **Step 5: Review loop.**

### Task 16: MCP surface — `swarm_workflow`, spawn validation, checkpoint verdicts, read crew

**Files:** Create `internal/mcpserver/workflow.go` + test; modify `internal/mcpserver/{orchestrator.go,tools.go,server.go}` + tests.

**Acceptance (spec B7):**
- `swarm_workflow` ops start/status/resume/cancel for orchestrators only, with `required` in its schema and idempotency via `request_id` (same two-phase pattern as `swarm_control`).
- `swarm_spawn`: role enum validated with spec copy; refused for build/design/research roles on workflow tasks; `worktrees` honoured (shared + in brief header); `advisor`/`cwd` removed from the schema.
- `swarm_checkpoint` exposes `verdict` + `findings`; `swarm_read` returns `workflow_state` and `crew` for tasks.

**Verify:** `go test ./internal/mcpserver/... && go build ./... && go vet ./...`

- [ ] **Step 1: Failing tests** `TestSwarmWorkflowToolsOnlyForOrchestrators`, `TestSwarmWorkflowStartStatusResumeCancel`, `TestSwarmWorkflowIdempotentStart`, `TestSwarmSpawnRejectsUnknownRole`, `TestSwarmSpawnRefusesWorkflowTask`, `TestSwarmSpawnSharesWorktrees`, `TestSwarmCheckpointVerdictSchema`, `TestSwarmReadCrew`.
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run green**; commit `feat(mcp): swarm_workflow and multi-agent task surface`.
- [ ] **Step 5: Review loop.**

### Task 17: Story `after_tasks` review and root `integration` gate

**Files:** Modify `internal/runtime/{workflow.go,checkpoint.go}`, `internal/items/transition.go` (`deriveStory`), tests.

**Acceptance (spec B8):**
- When all tasks of a story with `after_tasks` are Done, the owner gets `story_ready_for_review`; `swarm_workflow start` on the story with an `ro` worktree runs the single review step; `deriveStory` yields Done only after that workflow succeeded; `changes_requested` escalates.
- `integrated` checkpoints on a root with `integration` require each `integration.verify` recorded `ok:true` and, if `final_review` is set, a reviewer checkpoint on the root with `verdict: pass` whose git sha equals the integrated sha; exact copy per spec.

**Verify:** `go test ./internal/runtime/... ./internal/items/... && go vet ./...`

- [ ] **Step 1: Failing tests** `TestStoryReadyForReviewRelay`, `TestStoryDoneWaitsForAfterTasksReview`, `TestStoryReviewChangesEscalates`, `TestIntegratedNeedsIntegrationVerify`, `TestIntegratedNeedsFinalReviewPass`, `TestStoryWithoutAfterTasksUnchanged`.
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run green**; commit `feat(runtime): story reviews and integration gate`.
- [ ] **Step 5: Review loop.**

### Task 18: Board and menubar — workflow section, crew, round badge, step suffix

**Files:** Create `web/src/components/WorkflowSection.tsx` + test; modify `web/src/panels/Details.tsx`, the kanban task card component under `web/src/views/`, `web/src/{types.ts,copy.ts,api.ts}`, `web/src/mock/fixtures.ts`; `internal/httpapi` (include `workflow_state`, `crew` in item detail and `step` in agents payload); `apps/menubar/Sources/SwarmBarKit/{Wire.swift,Copy.swift}` + tests.

**Acceptance (spec Screens + copy):**
- Details shows the Workflow section for workflow tasks exactly as specified (state chip, round, steps with runs, verdict chips, expandable findings, escalation banner); hidden for legacy tasks.
- Kanban task cards show crew emoji (max 3 + "+n") and "R{n}" when round > 1.
- Plan review screen lists `warnings` under "Plan warnings" (data arrives in Task 19; render when present).
- Menubar agent rows show "Role · step r{n}" when `step` is present.
- Verify visually with the `run` skill / Playwright screenshot of the mock board (light + dark).

**Verify:** `go test ./internal/httpapi/... && cd web && pnpm test && pnpm biome check && cd ../apps/menubar && swift test`

- [ ] **Step 1: Failing tests** `WorkflowSection.test.tsx` (renders runs/verdicts/findings/escalation; hidden without workflow), kanban card test (crew + round badge), `Details.test.tsx` addition, Swift `testAgentRowStepSuffix`, Go `TestItemDetailIncludesWorkflowState`.
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run green**; screenshot check; commit `feat(web,menubar): show task workflows and crews`.
- [ ] **Step 5: Review loop** — `ui_reviewer`-style pass using `web-design-guidelines` on the changed components.

### Task 19: e2e — workflow scenario and stale TDD scenario

**Files:** Create `scripts/e2e/workflow_test.go`; modify `scripts/e2e/tdd_test.go`, `scripts/e2e/harness_test.go` (helpers for `swarm_workflow` and reviewer checkpoints).

**Acceptance (spec Verification 5):** scenario passes end-to-end with fake agents: start → builder red/green + clean commit → reviewer `changes_requested` → builder receives findings → completes → reviewer `pass` → task Done, exactly one `workflow_succeeded` relay, review worktree removed; escalation after max rounds; `resume accept`. `tdd_test.go` expects the new TDD gate copy on a workflow task and still checks legacy `verifyOK` copy on a legacy task.

**Verify:** `make e2e`

- [ ] **Step 1: Write the scenarios** (they fail: harness lacks helpers / behaviour).
- [ ] **Step 2: Run red** (`make e2e`), record.
- [ ] **Step 3: Implement harness helpers**; fix any real bug found (in the owning package, with a unit test first).
- [ ] **Step 4: Run green**; commit `test(e2e): workflow engine scenarios`.
- [ ] **Step 5: Review loop.**

---

## Phase C — Planning and spikes

### Task 20: swarm-tree workflow/steps/verify, materialize, plan validation

**Files:** Modify `internal/runtime/{artifacts.go,materialize.go}`, `internal/mcpserver/orchestrator.go` (`swarm_artifact` result `warnings`), `web/src` plan review (warnings wiring from Task 18), tests `artifacts_test.go`, `materialize_test.go`.

**Interfaces:**
- Produces: `TreeNode.Workflow *workflow.Spec`, `.Steps []string`, `.Verify []string`; `RegisterArtifactResult.Warnings []string`; `func lintTree(t Tree) (errs []error, warnings []string)`.

**Acceptance (spec C1–C2):**
- Trees with the new fields parse (still `DisallowUnknownFields`), validate per level, and materialize copies resolved workflow (defaulting from `role_hint` for tasks), steps and verify to items; root/story workflows copied too.
- Errors and warnings exactly per spec C2 copy; warnings returned by `swarm_artifact` and shown on the board.
- Old-format trees (no workflow fields) still register and materialize; their tasks get `DefaultFor(role_hint)` — except reviewer-role tasks, which are now an error (intentional change; update any fixture in `runtime/testdata` that contains one).

**Verify:** `go test ./internal/runtime/... -run 'Tree|Artifact|Materialize' && go test ./internal/runtime/... && cd web && pnpm test`

- [ ] **Step 1: Failing tests** `TestParseTreeWithWorkflowFields`, `TestTreeRejectsReviewTasks`, `TestTreeRequiresStepsAndVerifyForTDDTasks`, `TestTreeWarnsOnSplitTDDTitles` (table of titles from the spec regex, plus negatives such as "Add retry to failing uploads"), `TestMaterializeCopiesWorkflowStepsVerify`, `TestMaterializeDefaultsWorkflowFromRoleHint`, `TestSwarmArtifactReturnsWarnings`.
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run green**; commit `feat(runtime): workflows in swarm-tree plans`.
- [ ] **Step 5: Review loop.**

### Task 21: swarm-workflows, swarm-spike, swarm-orchestrator skills

**Files:** Replace stubs `skills/swarm-workflows/SKILL.md`, `skills/swarm-spike/SKILL.md`; rewrite `skills/swarm-orchestrator/SKILL.md`; extend `internal/install/skills_test.go`.

**Acceptance (spec A4 + C3):**
- `swarm-workflows`: DSL reference (fields, templates table, gates, levels, engine behaviour, escalation handling) and authoring guidance (pipeline-by-default deps, adversarial verify via multiple reviewer roles, loop-until-pass bounds, completeness critic, no silent caps); includes a complete worked task node example that the test parses with `ParseTree` + `workflow.Validate` (so the doc can't drift from the code).
- `swarm-spike`: the seven-step method of spec C3, referencing `superpowers:brainstorming`, `superpowers:writing-plans`, `superpowers:systematic-debugging`; research via `research` template tasks and notes synthesis; design tasks via `design-reviewed`; the completeness critic; self-contained task rules with a "bad → good" example (split RED/GREEN/review tasks → one task with steps).
- `swarm-orchestrator`: delivery only — `swarm_workflow start` for workflow tasks, escalation handling, budget, story `after_tasks`, root `integration`; references `superpowers:dispatching-parallel-agents`, `superpowers:subagent-driven-development`, `superpowers:finishing-a-development-branch`, `swarm-workflows`; removes the old "spawn a reviewer after completed / retry with note / mark done" instructions for workflow tasks (kept, shortened, for legacy tasks).

**Verify:** `make skills-sync && go test ./internal/install/... ./internal/runtime/...`

- [ ] **Step 1: Failing tests** `TestWorkflowsSkillExampleValidates`, extend `TestRoleSkillsReferenceTheirSkills` for the three skills, `TestOrchestratorSkillNoLongerHandRollsReviews` (asserts the old "After a worker's `completed`, spawn a `reviewer`" sentence is gone).
- [ ] **Step 2: Run red.**
- [ ] **Step 3: Write the skills.**
- [ ] **Step 4: Run green**; commit `docs(skills): workflow, spike and orchestrator skills`.
- [ ] **Step 5: Review loop** — reviewer dry-runs the spike skill against a toy request and checks the produced tree would pass `lintTree`.

### Task 22: README and final end-to-end smoke

**Files:** Modify `README.md`.

**Acceptance:**
- README "How work flows", "How agents talk to Swarm" (tools incl. `swarm_workflow`), roles (designer), skills (role skills + vendored list + licenses line), and board sections reflect the release.
- Manual smoke (spec Verification 6–7) done and summarised in the commit message: per-kind skill discovery; a small feature spike → research tasks → design task → plan with a warning → materialize → delivery via the engine to an accepted epic.

**Verify:** `make test && make e2e`

- [ ] **Step 1: Update README.**
- [ ] **Step 2: Run the manual smoke**; fix any defect found in its owning package (test first) before continuing.
- [ ] **Step 3: `make test && make e2e` green**; commit `docs: self-contained tasks, workflow engine and role skills`.
- [ ] **Step 4: Review loop** — final whole-branch review (`superpowers:requesting-code-review` over the full diff against the spec).

---

## Work breakdown

The same plan expressed in the swarm-tree format this work introduces (it
validates once Task 20 lands). Steps are abbreviated; the task sections
above are authoritative.

```swarm-tree
{
  "root": {"type": "epic", "title": "Self-contained tasks, workflow engine and role skills",
    "brief": "See docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md", "acceptance": ["All spec Verification steps pass"],
    "workflow": {"integration": {"merge_order": ["a-embed","a-link","a-vendor","a-designer","a-hook","a-skills1","a-skills2","a-kickoff","b-dsl","b-next","b-items","b-multi","b-gates","b-engine","b-triggers","b-mcp","b-story","b-ui","b-e2e","c-tree","c-skills","c-readme"],
      "verify": ["make test", "make e2e"], "final_review": ["reviewer"]}}},
  "children": [
    {"ref": "s-a", "type": "story", "title": "Skill platform, role skills, designer role", "brief": "Spec Part A", "acceptance": ["Spec A1-A6 met"],
      "workflow": {"after_tasks": {"id": "story-review", "review": ["reviewer"]}},
      "children": [
        {"ref": "a-embed", "type": "task", "title": "Recursive skill embed, registry and ~/.swarm/skills sync", "brief": "Plan Task 1", "acceptance": ["Plan Task 1 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write registry/sync/mirror tests; run; record red", "Embed all:skills, Skills(), SyncSkills, SkillNames()", "Makefile rsync mirror; daemon start sync", "Run green; update count tests", "Commit"],
          "verify": ["make skills-sync", "go test ./internal/install/...", "go vet ./..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "a-link", "type": "task", "title": "Per-kind skill symlinks, Claude per-spawn links, doctor", "brief": "Plan Task 2", "acceptance": ["Plan Task 2 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Empirically check symlink discovery per CLI; set link modes", "Write link/skip/uninstall/doctor tests; record red", "Implement LinkSkills, WriteSkills, CheckSkills, python3 check", "Run green", "Commit"],
          "verify": ["go test ./internal/install/... ./internal/adapter/... ./internal/migrate/...", "go vet ./..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "a-vendor", "type": "task", "title": "Vendor eight UI/design/mobile skills", "brief": "Plan Task 3", "acceptance": ["Plan Task 3 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write license/provenance test; record red", "Copy pinned sources and licenses", "Apply and record modifications", "Write vendor README", "Run green incl. search.py", "Commit"],
          "verify": ["make skills-sync", "go test ./internal/install/...", "python3 skills/vendor/ui-ux-pro-max/scripts/search.py dashboard --domain style"], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "a-designer", "type": "task", "title": "Designer role end-to-end", "brief": "Plan Task 4", "acceptance": ["Plan Task 4 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write Go/web/Swift tests; record red", "Migration 0010 and Go role plumbing", "Web and menubar mirrors", "Run green", "Commit"],
          "verify": ["go test ./internal/...", "cd web && pnpm test", "cd apps/menubar && swift test"], "workflow": {"template": "ui-tdd-reviewed"}},
        {"ref": "a-hook", "type": "task", "title": "Block the native Workflow tool", "brief": "Plan Task 5", "acceptance": ["Plan Task 5 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write hook tests; record red", "Add blocked names and copy", "Run green", "Commit"],
          "verify": ["go test ./internal/hook/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "a-skills1", "type": "task", "title": "Core, coder, reviewer, ui-reviewer, designer skills", "brief": "Plan Task 6", "acceptance": ["Plan Task 6 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write skill reference tests; record red", "Write the five skills", "Run green", "Commit"],
          "verify": ["make skills-sync", "go test ./internal/install/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "a-skills2", "type": "task", "title": "Debugger, mechanical, researcher, advisor skills", "brief": "Plan Task 7", "acceptance": ["Plan Task 7 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write skill and advisor prompt tests; record red", "Write the four skills; align advisor prompt", "Run green", "Commit"],
          "verify": ["make skills-sync", "go test ./internal/install/... ./internal/advisor/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "a-kickoff", "type": "task", "title": "Kickoff names each role's skill", "brief": "Plan Task 8", "acceptance": ["Plan Task 8 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write kickoff tests; record red", "RoleSkills table, signature change, stub spike/workflows skills", "Run green", "Commit"],
          "verify": ["go test ./internal/runtime/... ./internal/install/..."], "workflow": {"template": "tdd-reviewed"}}
      ]},
    {"ref": "s-b", "type": "story", "title": "Multi-agent tasks and the workflow engine", "brief": "Spec Part B", "acceptance": ["Spec B1-B9 met"],
      "workflow": {"after_tasks": {"id": "story-review", "review": ["reviewer"]}},
      "children": [
        {"ref": "b-dsl", "type": "task", "title": "Workflow DSL: types, templates, validation, resolve, render", "brief": "Plan Task 9", "acceptance": ["Plan Task 9 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write template/validation/render tests; record red", "Implement package", "Run green", "Commit"], "verify": ["go test ./internal/workflow/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "b-next", "type": "task", "title": "Next-action planner", "brief": "Plan Task 10", "acceptance": ["Plan Task 10 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write TestNext table; record red", "Implement Next", "Run green", "Commit"], "verify": ["go test ./internal/workflow/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "b-items", "type": "task", "title": "Schema and items carry workflow, steps, verify", "brief": "Plan Task 11", "acceptance": ["Plan Task 11 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write migration/store/MCP tests; record red", "Migration 0011, model, store, MCP, HTTP, web types", "Run green", "Commit"], "verify": ["go test ./internal/...", "cd web && pnpm test"], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "b-multi", "type": "task", "title": "Multi-agent-safe tasks", "brief": "Plan Task 12", "acceptance": ["Plan Task 12 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write sibling/verdict/completion/wake tests; record red", "Implement", "Run green", "Commit"], "verify": ["go test ./internal/runtime/... ./internal/items/... ./internal/mcpserver/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "b-gates", "type": "task", "title": "Step gates", "brief": "Plan Task 13", "acceptance": ["Plan Task 13 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write gate tests; record red", "Implement gates and daemon artifact registration", "Run green", "Commit"], "verify": ["go test ./internal/runtime/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "b-engine", "type": "task", "title": "Engine core", "brief": "Plan Task 14", "acceptance": ["Plan Task 14 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write engine tests; record red", "SubagentSlots, briefs, StartWorkflow, advance", "Run green", "Commit"], "verify": ["go test ./internal/runtime/... ./internal/hook/..."], "workflow": {"template": "tdd-reviewed", "max_rounds": 4}},
        {"ref": "b-triggers", "type": "task", "title": "Triggers, recovery, Done gating, resume, cancel", "brief": "Plan Task 15", "acceptance": ["Plan Task 15 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write trigger/recovery/resume tests; record red", "Implement", "Run green", "Commit"], "verify": ["go test ./internal/runtime/... ./internal/items/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "b-mcp", "type": "task", "title": "MCP surface", "brief": "Plan Task 16", "acceptance": ["Plan Task 16 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write MCP tests; record red", "Implement swarm_workflow and changes", "Run green", "Commit"], "verify": ["go test ./internal/mcpserver/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "b-story", "type": "task", "title": "Story reviews and integration gate", "brief": "Plan Task 17", "acceptance": ["Plan Task 17 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write story/integration tests; record red", "Implement", "Run green", "Commit"], "verify": ["go test ./internal/runtime/... ./internal/items/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "b-ui", "type": "task", "title": "Board and menubar workflow UI", "brief": "Plan Task 18", "acceptance": ["Plan Task 18 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write component/Swift/Go tests; record red", "Implement UI and wire data", "Run green; screenshot light and dark", "Commit"], "verify": ["cd web && pnpm test && pnpm biome check", "cd apps/menubar && swift test", "go test ./internal/httpapi/..."], "workflow": {"template": "ui-tdd-reviewed"}},
        {"ref": "b-e2e", "type": "task", "title": "e2e workflow scenarios", "brief": "Plan Task 19", "acceptance": ["Plan Task 19 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write scenarios; run make e2e; record red", "Harness helpers; fix real bugs test-first", "Run green", "Commit"], "verify": ["make e2e"], "workflow": {"template": "tdd-reviewed"}}
      ]},
    {"ref": "s-c", "type": "story", "title": "Planning and spikes", "brief": "Spec Part C", "acceptance": ["Spec C1-C3 met"],
      "children": [
        {"ref": "c-tree", "type": "task", "title": "swarm-tree workflow fields, materialize, plan lint", "brief": "Plan Task 20", "acceptance": ["Plan Task 20 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write tree/lint/materialize tests; record red", "Implement", "Run green", "Commit"], "verify": ["go test ./internal/runtime/...", "cd web && pnpm test"], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "c-skills", "type": "task", "title": "swarm-workflows, swarm-spike, swarm-orchestrator skills", "brief": "Plan Task 21", "acceptance": ["Plan Task 21 acceptance"], "role_hint": "coder", "repos": ["agent-swarm"],
          "steps": ["Write skill tests incl. example validation; record red", "Write the skills", "Run green", "Commit"], "verify": ["make skills-sync", "go test ./internal/install/... ./internal/runtime/..."], "workflow": {"template": "tdd-reviewed"}},
        {"ref": "c-readme", "type": "task", "title": "README and final smoke", "brief": "Plan Task 22", "acceptance": ["Plan Task 22 acceptance"], "role_hint": "mechanical", "repos": ["agent-swarm"],
          "steps": ["Update README", "Manual smoke per spec", "make test and make e2e", "Commit"], "verify": ["make test", "make e2e"], "workflow": {"template": "mechanical"}}
      ]}
  ],
  "deps": [
    {"item": "a-link", "blocked_by": "a-embed"},
    {"item": "a-vendor", "blocked_by": "a-embed"},
    {"item": "a-skills1", "blocked_by": "a-embed"},
    {"item": "a-skills2", "blocked_by": "a-embed"},
    {"item": "a-kickoff", "blocked_by": "a-skills1"},
    {"item": "a-kickoff", "blocked_by": "a-skills2"},
    {"item": "b-next", "blocked_by": "b-dsl"},
    {"item": "b-items", "blocked_by": "b-dsl"},
    {"item": "b-items", "blocked_by": "a-designer"},
    {"item": "b-multi", "blocked_by": "b-items"},
    {"item": "b-gates", "blocked_by": "b-multi"},
    {"item": "b-engine", "blocked_by": "b-next"},
    {"item": "b-engine", "blocked_by": "b-gates"},
    {"item": "b-triggers", "blocked_by": "b-engine"},
    {"item": "b-mcp", "blocked_by": "b-triggers"},
    {"item": "b-story", "blocked_by": "b-triggers"},
    {"item": "b-ui", "blocked_by": "b-mcp"},
    {"item": "b-e2e", "blocked_by": "b-mcp"},
    {"item": "b-e2e", "blocked_by": "b-story"},
    {"item": "c-tree", "blocked_by": "b-items"},
    {"item": "c-skills", "blocked_by": "c-tree"},
    {"item": "c-skills", "blocked_by": "a-kickoff"},
    {"item": "c-skills", "blocked_by": "b-mcp"},
    {"item": "c-readme", "blocked_by": "c-skills"},
    {"item": "c-readme", "blocked_by": "b-e2e"},
    {"item": "c-readme", "blocked_by": "b-ui"}
  ]
}
```
