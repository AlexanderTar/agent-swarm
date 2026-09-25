# P3 review r1 (Opus) — fix all (fix round 1)

Important
1. skills/swarm/SKILL.md:9 (rule 3) and :10 (rule 4 RESUME clause): every agent with a parent still swarm_sends a `finding` to it on each `completed`, including workflow steps and every fix round. Spec B4 (lines ~656-658) suppresses relays of `completed` for workflow agents; the orchestrator must get exactly one workflow_succeeded per package. Rule 11 only forbids swarm_send "to hand off to the next step".
   Fix: carve-out in rules 3 and 4: "…unless your assignment has a `## Workflow` section — the daemon reports workflow steps to your orchestrator; only questions, `blocked` and `failed` go to it from you." (or say it once in rule 11 and reference it from rule 3). Add a test row pinning the carve-out.

Minor (fold in)
2. skills/swarm-coder/SKILL.md:18-19,35: TDD detail lost in the move from old swarm rule 11 — restore: record the red run in a `progress` checkpoint when it happens, with `note: "<why it fails>"`; restore the "refactor" step. Line 35 must not invite writing red entries after the fact.
3. skills/swarm-coder/SKILL.md:35 vs :38: use "every rw worktree shared with you" in both places (B5 commit gate checks every rw worktree shared). Say that a single-unit `## Steps` task omits `unit`.
4. internal/install/skills_test.go:186 reviewer row: pin literal contract strings: "`pass` | `changes_requested` | `blocked`" and "critical|major|minor|nit" (match whatever exact form the skill uses — make the skill use one canonical form), plus "for each acceptance criterion". Add the verdict contract string to the swarm-ui-reviewer row. In TestSuperpowersReferencesAreKnown assert at least one reference was found.
5. skills_test.go:204-216 designer row: add "Interaction & motion", "Mobile specifics", "Open questions", "Mermaid".
6. skills/swarm-batching/SKILL.md:31: replace "(once P12 lands … see spec C2)" with the behaviour: "plan registration returns these as `warnings[]`".
7. skills/swarm-designer/SKILL.md:15: `--stack` examples must be valid values of the vendored ui-ux-pro-max script (react, react-native, swiftui, jetpack-compose, html-tailwind, …) — check scripts/search.py for the real list.
8. skills/swarm-designer/SKILL.md:14: add "your artifact replaces brainstorming's spec/plan steps" (brainstorming otherwise ends in user approval → spec → writing-plans).
