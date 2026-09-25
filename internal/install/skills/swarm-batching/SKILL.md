---
name: swarm-batching
description: How to size and batch work into work packages for Swarm. Use when writing a plan's swarm-tree, creating tasks with swarm_items, folding follow-up work, or deciding whether several pieces of work should be one delegated task. Default is 3-5 units per task.
---

# Batching work into work packages

Follow the `swarm` skill first; this adds to it.

Every delegated task costs a fixed amount no matter how small it is: an agent spawn, a cold context load, a brief, a review, and usually a fix round. Tiny tasks are dominated by that overhead and still need the same loops. Swarm therefore delegates **work packages**: one task that batches **3–5 units** of related work. It gets one build, one review loop and one set of verify commands.

## Vocabulary

- **Step**: one action, 2–5 minutes. Examples: write the failing test, run it, implement, run it again, commit. The "bite-sized" granularity in `superpowers:writing-plans` describes steps. **Steps are never tasks.**
- **Unit**: the smallest piece of work that carries its own test cycle and commit. A superpowers "task" is a unit. A unit has a title and ordered steps, starts with a failing test, and ends committed; it is not a separate agent assignment or review gate.
- **Work package**: a swarm-tree `task` holding 1–5 units, each listed in `units[]`. The same coder agent executes its units sequentially. Each package has one workflow; when review is required, it happens once at the package boundary. The default package holds 3–5 units.

## The batching test

Put units in the same package when **all** of these hold:

1. **One verdict.** A reviewer would accept or reject the units together. If a reviewer could reasonably reject one unit and approve its neighbour, split them.
2. **One slice.** The units build one behaviour or vertical slice, or a dependency chain where B's tests are the first thing that exercise A's output.
3. **Shared context.** The units touch the same package or module, or files that usually change together. One context load serves all of them. Splitting them risks two agents making conflicting implicit decisions.
4. **One "done when".** You can summarise the package in 1–3 sentences with a single completion statement.

Always fold these into the unit whose deliverable needs them, never into their own unit or task: setup, configuration, scaffolding, fixtures, docs and README updates, and generated-file refreshes.

Small edits of the same kind across many files batch well, even beyond 5 units: renames, adding a field to N wire types, vendoring N similar files. Give each file its own unit or checklist line so the reviewer can confirm every one was touched.

## Keep apart (one package, or one unit, of their own)

- **Risky or irreversible work** gets its own package, with extra verification on real data. Examples: destructive or table-rebuilding migrations, data backfills, anything that publishes or pushes, security-sensitive changes.
- **Work that needs a different reviewer or model tier.** UI work goes to `ui_reviewer`. Keep security or infra changes and public API contracts separate, and keep a refactor separate from a behaviour change.
- **Uncertain or exploratory work**, such as research or "decide X" tasks, so its outcome can reshape the plan before more is built on it.
- **Truly independent work that will actually run in parallel.** Separate packages let the orchestrator run them at the same time. If they would run one after another anyway, batch them.
- **Contracts before consumers.** When packages share an interface (types, schema, API), the package defining it goes first. Batch on each side of it, not across it.

A single-unit package is fine when one of these reasons applies. Say which one in the task's `solo` field (for example `"solo": "irreversible migration"`). Without a reason, plan registration returns these as `warnings[]` (registration still succeeds; the warning shows on the board's plan review screen). Read all plan-registration warnings: they also flag split RED/GREEN/review tasks, TDD scripts without a test step, packages over five units, and stories made entirely of single-unit tasks.

## Size bounds

| Measure | Aim | Split above |
|---|---|---|
| Units per package | 3–5 | 5 (same-kind edits: 8) |
| Changed lines per package (no generated files) | 100–400 | ~500 |
| Files per package | 3–8 | ~10 (same-kind edits exempt) |
| Human-equivalent effort | 30–90 min | ~2 h |

Also split when:
- the package has two "done when" statements that could pass or fail independently;
- it mixes a refactor with a behaviour change;
- a review flags the same unit two rounds running, which means that unit needed its own gate. The orchestrator moves it into a follow-up package instead of retrying the whole package again.

## Writing a package in the swarm-tree

```json
{"ref": "t-limits", "type": "task", "title": "Unified agent limit: settings, API and menubar wire",
 "brief": "Replace the two limits with one max_concurrent_agents end to end.",
 "acceptance": ["Admit counts every role in one pool", "GET/PUT /api/settings round-trips the field", "Menubar decodes it"],
 "role_hint": "coder", "repos": ["agent-swarm"],
 "units": [
   {"title": "Settings model", "steps": ["Write TestSettingsDefaultUnifiedLimit; run; record red (unit 1)", "Add MaxConcurrentAgents and validation; run; record green", "Commit"]},
   {"title": "Admit uses one pool", "steps": ["Write TestAdmitCountsAllRoles; record red (unit 2)", "Change Admit; record green", "Commit"]},
   {"title": "API and menubar wire", "steps": ["Write httpapi and Swift decode tests; record red (unit 3)", "Wire the field; record green", "Commit"]}
 ],
 "verify": ["go test ./internal/settings/... ./internal/runtime/... ./internal/httpapi/...", "cd apps/menubar && swift test"],
 "workflow": {"template": "tdd-reviewed"}}
```

## Assign every role in the plan

Every package gets an explicit `workflow`. The workflow names the role for each step, so the orchestrator never has to pick one. Choose by the work in the package:

| Work in the package | Workflow |
|---|---|
| Backend, library or CLI behaviour change | `tdd-reviewed`: coder, then reviewer |
| Any UI code, web or native | `ui-tdd-reviewed`: coder, then reviewer and ui_reviewer |
| Designing a new screen, flow or component before it is built | `design-reviewed`: designer, then ui_reviewer. It blocks the UI package that builds it. |
| Fixing a reproducible bug | `debug`: debugger, then reviewer |
| Renames, config, docs-only changes, generated files, vendoring | `mechanical` (mechanical, no review) — or steps `[{run: mechanical}, {review: [reviewer]}]` when a license or provenance check matters |
| An open question that needs evidence | `research`: researcher |
| Security-sensitive change | Its own package (`solo: "security"`), `tdd-reviewed` with `max_rounds: 4`. The context names the security focus. |

If a package needs two rows (UI plus backend), use the stricter workflow. If the rows need different reviewers for unrelated work, split the package.

## Executing a package (coders, debuggers, mechanical agents)

- Keep all units with the same coder agent in one task assignment. Work unit by unit, in order. Each unit gets its own red → green cycle. Record verification entries with `"unit": <n>`, and make one commit per unit. That lets a reviewer read the history as the unit list.
- Run the package's `verify` commands once all units are done, then write `completed`.
- If a unit turns out not to belong, finish the others. Explain which unit and why in your `completed` summary; don't silently skip it.

## Reviewing a package (reviewers)

- Review the whole diff once, walking it unit by unit, commit by commit.
- A unit with no commit, or a listed file the diff never touches, is a `major` finding ("missing unit").
- Tag each finding with `"unit": <n>`. If one unit has all the problems and the rest are clean, say so in your summary; the orchestrator may split it out.

## Orchestrators

- Apply the same test when you create tasks yourself with `swarm_items`: batch follow-ups into one package, and never create a task for a single fix.
- Findings from an escalated workflow go back into that task (`swarm_workflow resume retry`) or into the next related package that is still Ready. They don't become new micro-tasks.
- Before starting packages in parallel, check they really are independent (`superpowers:dispatching-parallel-agents`). Coupled packages run one after another.

## Sources

This skill's text is original. The rules draw on:
- `superpowers:writing-plans` "Task Right-Sizing" and `superpowers:subagent-driven-development` "Batch small same-shape work" (obra/superpowers, MIT).
- addyosmani/agent-skills `planning-and-task-breakdown` (MIT) for file-count sizing.
- citypaul/.dotfiles `planning` (MIT) for vertical slices and the 1–3 sentence test.
- Google engineering practices "Small CLs".
- SmartBear/Cisco review-size findings (200–400 LOC).
- Anthropic's engineering posts on multi-agent systems and long-running agents.
- METR's task-length measurements.
