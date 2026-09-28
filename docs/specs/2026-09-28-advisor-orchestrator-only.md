# Spec: the advisor belongs to the orchestrator only

## Context

Today every Swarm agent gets an advisor:
- orchestrators through `StartSpike` / `StartOrchestrator`;
- children through `Spawn` (`internal/runtime/agents.go:~1107`, `resolveAdvisorAfterFallback`);
- relaunches through the Retry / DrainQueue re-resolution (`agents.go:~2071`, `limits.go:~215`).

Claude sessions with a native advisor get `spec.AdvisorModel` (`agents.go:~1451`). Everyone
else calls `swarm_advise` (`internal/mcpserver/tools.go` `advisorTool`). The skills tell every
agent to consult it (`swarm` rule 9a, `swarm-advisor`, `swarm-debugger`), including children
("debuggers and reviewers also consult it before writing `completed`").

The user wants the advisor tied to the orchestrator:
- children are actively prohibited from using it;
- the orchestrator is actively encouraged to use it when reviewing complex worker reports.

This keeps cross-agent advisor traffic low. For example, Claude orchestrator + Claude
advisor stays one tightly coupled pair.

## Locked decisions (user-approved 2026-09-28)

1. Only an agent with role `orchestrator` gets an advisor: `advisor_kind/model/effort/mode`,
   and the native `AdvisorModel` at launch. Every non-orchestrator spawn and relaunch path
   resolves **no** advisor, whatever the settings or the `swarm_spawn` `advisor` field say.
2. `swarm_advise` refuses any caller whose role is not `orchestrator`, with the copy below.
   This also covers children launched before the change.
3. A running Claude child that already has a native advisor keeps it until its next
   relaunch; the CLI can't drop it mid-session. It is not killed or relaunched for this.
4. The `swarm_spawn` `advisor` field is ignored for children. It is removed from the tool
   schema/description if present there.
5. Settings: the single `advisor` role default stays (it configures the orchestrator's
   advisor). No per-child-role advisor UI exists, so there is nothing to hide. The spec
   records that.
6. A missing advisor is a launch fault only for orchestrators.

## DB models

None.

## Model / API types

- `internal/runtime`: a single guard `func advisorAllowed(role Role) bool { return role == RoleOrchestrator }`.
  - Every place that calls `resolveAdvisor` / `resolveAdvisorAfterFallback` for an agent
    whose role fails the guard gets five empty strings instead.
  - `startSession` sets `spec.AdvisorModel` only when `advisorAllowed(a.Role) && a.AdvisorMode == "native"`.
    This is defence in depth for rows written before the change.
- `internal/mcpserver/tools.go` `advisorTool`: after decoding, if `c.Role != runtime.RoleOrchestrator`,
  return the refusal. Check `Caller.Role` population (used by `recovery_test.go`).
- Agent wire and menubar: children show no advisor. Nothing to change beyond the empty fields.

## User-facing copy

- `swarm_advise` refusal (plain error text):
  `The advisor is only available to your orchestrator. Put the decision and your evidence in your checkpoint (blockers/next), or ask your parent with swarm_send kind:"question".`
- `swarm` skill rule 9a, replaced entirely:
  `9a. Only orchestrators have an advisor. If you are not an orchestrator, do not look for or call one (swarm_advise refuses you): put hard decisions, with your evidence, in your checkpoint or ask your parent with swarm_send kind:"question". Orchestrators: follow the swarm-advisor skill; Claude orchestrators with a native advisor call their built-in advisor tool, others call swarm_advise. An orchestrator with neither tool reports it as a launch fault in its first checkpoint.`
- `swarm-advisor` skill: the description changes to
  `…Referenced from the swarm skill's rule 9a; for orchestrators only — child agents have no advisor.`
  Add a first paragraph: `Only orchestrators have an advisor. If you are a child agent, stop here and escalate to your orchestrator instead.`
- `swarm-debugger` skill line 13, replaced:
  `Before you commit to a root cause, put the candidate cause and its evidence in a progress checkpoint; your orchestrator weighs it (with its advisor) before you fix.`
- `swarm-orchestrator` skill, a new bullet under the existing advisor consult points:
  ```
  - Consult your advisor when you review a complex worker report, before you accept it or re-dispatch:
    a completed or failed checkpoint with findings, a review with disagreements or blocking findings,
    a debug root cause, a design or spec from a designer, or any report you would otherwise accept on trust.
    Good briefing: "Coder says TASK-7 is done: red `go test ./auth -run TestExpiry` failed, green passes;
    diff touches token.go:40-88. Reviewer flags a race in refresh(). Accept, or send back?" plus the checkpoint refs.
    Bad briefing: "Is TASK-7 ok?"
  ```
- Every changed skill has two copies (`skills/` and `internal/install/skills/`); both stay
  byte-identical.

## Screens

None changed.

## File list

- `internal/runtime/agents.go`:
  - `Spawn`;
  - `startSession` (the `AdvisorModel` guard);
  - `Retry`'s re-resolution;
  - `advisorAllowed`.
- `internal/runtime/limits.go`: the DrainQueue re-resolution.
- Any other `resolveAdvisor*` caller (grep).
- `internal/mcpserver/tools.go` (`advisorTool`), `internal/mcpserver/orchestrator.go` (spawn `advisor` field).
- Skills: `swarm`, `swarm-advisor`, `swarm-debugger`, `swarm-orchestrator` (both copies each).
- Tests beside each.

## Verification

1. `go test ./...`, `go vet ./...`, `gofmt -l .`
2. Scenarios, each a test:
   - Spawn a coder with an advisor in settings → the agent row has empty `advisor_*`, and the launch spec's `AdvisorModel` is empty.
   - A Claude orchestrator with a native-capable advisor → `AdvisorModel` set (unchanged).
   - A row with an advisor written for a child (the pre-change shape) → relaunch spec has no `AdvisorModel`.
   - `swarm_advise` from a coder → the refusal text, and no advice row is created; from an orchestrator → works as today.
   - `swarm_spawn` with an `advisor` field for a child → ignored (no advisor on the row).
   - Retry and DrainQueue of a child → no advisor.
   - Skill copies are identical (the existing install test).

## Explicitly out of scope

- Changing which model the advisor uses, or the advisor protocol itself.
- Killing or relaunching running children.
- Advisor accounting and cost reporting.
