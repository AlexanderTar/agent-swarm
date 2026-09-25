---
name: swarm-workflows
description: The Swarm workflow DSL reference for orchestrators — templates, gates, engine behaviour, authoring guidance, and the role assignment table so a plan never leaves a role to be guessed. Use together with the `swarm` skill when your kickoff names this skill, or when writing a plan's task workflows.
---

# Assign workflows in every plan

A plan assigns every task's role through its `workflow`. The orchestrator starts that workflow with `swarm_workflow start`; the daemon runs the steps, review, fix rounds and completion gates. A task with no workflow is a legacy item, not a valid new plan task. `role_hint` is optional in a tree and, when supplied, must match the resolved workflow's first run role. Never infer a role from a title.

## DSL by item level

A task has `workflow: {"template":"tdd-reviewed"}` or explicit `workflow.steps`. Each workflow step has an `id` and exactly one of `run` (one worker role) or `review` (one or more reviewer roles). A review step may name `of` and `loop: {"fix":"build","max_rounds":3,"on_exhausted":"escalate"}`. `of` and `fix` refer to an earlier run step. `max_rounds` is 1–5, and `retries` is 0–2. The daemon resolves templates before storing the item and uses the first run role for `role_hint`.

Task `steps` are the execution script for one unit. For a package, use `units: [{"title":"...","steps":["..."]}]` instead; never set both. Each unit gets red and green TDD evidence and one commit, executed sequentially by the same coder. `verify` lists package commands. A single unit needs a `solo` reason. Story workflows use only `after_tasks`, for example `{"after_tasks":{"id":"review","review":["reviewer"]}}`. Epic and bug workflows use only `integration`, for example `{"integration":{"verify":["go test ./..."],"final_review":["reviewer"]}}`. A story review and root integration occur after their children finish.

Gates on run steps are `tdd`, `commit`, `verify`, `artifact:design` and `artifact:notes`. `tdd` requires a failing test result before a passing one in the same round, with a unit number for package units. `commit` requires a clean worktree at the checkpoint SHA; `verify` requires passing evidence for each declared command. Artifact gates require the respective design or research file. A reviewer's `changes_requested` verdict retries the named fix step within the round budget; exhaustion escalates to the orchestrator for `swarm_workflow resume` with an explicit decision. `swarm_workflow` also provides `start`, `advance` and `cancel`; repeated `advance` is safe.

## Choose the workflow

| Work in the package | Workflow and roles |
|---|---|
| Backend, library or CLI behavior | `tdd-reviewed`: coder → reviewer |
| Any web or native UI code | `ui-tdd-reviewed`: coder → reviewer + ui_reviewer |
| New screen, flow or component design | `design-reviewed`: designer → ui_reviewer; block the build package on it |
| Reproducible defect | `debug`: debugger → reviewer |
| Rename, config, docs, generated files or vendoring | `mechanical`: mechanical; use explicit review steps for license or provenance checks |
| Open question or evidence gathering | `research`: researcher |
| Security-sensitive change | Own package with `solo: "security"`, `tdd-reviewed` and `max_rounds: 4`; name the security focus in context |

A plan's workflow may be explicit, such as `{"steps":[{"id":"change","run":"mechanical","gates":["commit","verify"]},{"id":"review","review":["reviewer"],"of":"change","loop":{"fix":"change","max_rounds":3,"on_exhausted":"escalate"}}]}`. Use this when a template does not fit. Validate the level and reviewer roles before registration.

## Worked package

This example is a complete plan block. The task has three related units, one coder assignment and one review after all three commits.

```swarm-tree
{"root":{"type":"epic","title":"Reliable uploads","brief":"Keep retries safe","acceptance":["Uploads recover"],"workflow":{"integration":{"verify":["go test ./..."],"final_review":["reviewer"]}}},
 "children":[{"ref":"s-upload","type":"story","title":"Upload reliability","brief":"Finish the upload slice","acceptance":["Retries work"],"workflow":{"after_tasks":{"id":"review","review":["reviewer"]}},"children":[
  {"ref":"t-upload","type":"task","title":"Make uploads retry safely","brief":"Persist attempts and recover interrupted uploads","acceptance":["Attempts are idempotent","Recovery succeeds"],"repos":["agent-swarm"],"workflow":{"template":"tdd-reviewed"},"units":[
   {"title":"Attempt model","steps":["Write a failing attempt persistence test; record red for unit 1","Implement attempt storage; record green","Commit unit 1"]},
   {"title":"Retry behavior","steps":["Write a failing retry test; record red for unit 2","Implement idempotent retry; record green","Commit unit 2"]},
   {"title":"Recovery","steps":["Write a failing recovery test; record red for unit 3","Implement recovery; record green","Commit unit 3"]}],"verify":["go test ./internal/upload/..."]}]}],"deps":[]}
```

Plan registration returns errors for missing or invalid workflows and warnings for split TDD tasks, missing test steps and weak batching. Resolve every warning before requesting approval or explain the intentional exception in `solo`.
