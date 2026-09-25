# Spike approvals via native question tools, every section interactively

Base: `main` @ `a1bfa27` (PR #20 merged; SchemaVersion 13; `swarm_artifact`
returns `warnings`; `Verify` has `unit`; artifact kinds include
design/research; `objSchemaRequired` exists but `swarm_artifact`/`swarm_ask`
don't use it yet).

## Context

Spike approvals today resolve only through `swarm_ask` requests answered in
the web UI (NeedsYou) or the CLI. Two problems, both diagnosed live:

1. The approval page crashed with `TypeError: reading 'slice'`: accept-request
   bindings embedded uppercase git keys (`Repo`/`SHA` from untagged
   `runtime.GitRef`), while the web reads lowercase `g.sha`. Fixed by
   `d160d2d` (tags + migration 0010) and kept through the PR #20 merge.
2. The muse orchestrator struggles calling `swarm_artifact` repeatedly. Root
   cause is the MCP schema, not muse code: `artifactTool`
   (`internal/mcpserver/orchestrator.go:253`) uses plain `objSchema` — no
   required fields, no value hints — so a client guesses `item`/`kind`/`path`,
   fails at runtime, and retries in a loop. Muse's own side is healthy
   (`request_user_input` is its real question tool, confirmed by binary
   strings, `--user-input-auto-resolve`, and the MSP `userInput/request`
   schema).

The user decision: every spike approval must additionally surface through
the orchestrator agent's **native** question tool, and **every spec section**
gets its own interactive prompt — no batching sections into one question.

Follow-up scope, approved into this spec: harden every MCP schema the same
way (required fields + value descriptions via the existing
`objSchemaRequired` helper), and fix the `swarm_send` kind default
(`relay` is daemon-origin only; agent sends must store `finding`).

## Locked decisions

- Per-section native loop: after `swarm_artifact register`, the orchestrator
  presents each entry of `sections[]` as one native question (title +
  section markdown summary + revision). Plan approval stays one prompt;
  debug-report approval stays one prompt. Plan warnings (`warnings[]` in the
  register result) are shown in the plan prompt.
- Native answer drives the daemon request: a native answer alone only
  records `AskQuestion` rows (hook intercept, no block). The orchestrator
  must translate each native answer into the matching `swarm_ask` approval
  outcome (approve / request-changes / withdraw) for the same
  `artifact`+`section`. The daemon's `approval_result` remains the only
  approval that counts.
- Only a top-level (parentless) orchestrator may raise native questions.
  Parented agents stay blocked by the hook relay (`swarm_send` to parent).
- Schema hardening reuses `objSchemaRequired` (already used by
  `swarm_checkpoint`/`swarm_spawn`); no new schema builder.
- Full required-field table (schema metadata only — result shapes and
  handler behavior unchanged, except the relay default below):
  blocker `[reason]`; send `[to, body]`; advise `[question]`;
  instructions `[op]`; kb `[op]`; items `[op]`; worktree `[op]`;
  control `[target, action]`; role_overrides `[op]`; materialize `[spike]`.
  `swarm_read`/`swarm_sync` stay required-empty by design (any filter combo
  is valid) with descriptions added; `swarm_catalog` takes no input;
  `swarm_workflow` already declares `required: [op, item]`.
- `swarm_send` kind: omitted or explicit `"relay"` stores `"finding"`.
  `relay` is daemon-origin only (all 12 non-test producers enqueue with
  `Origin:"daemon"`); the agent hybrid rendered `" from  ()"` garbage in
  summaries. Requiring `kind` was rejected: existing tests and live agents
  omit it and expect success. Enum stays `[question, answer, finding]`.
- Native tool per agent (from `isQuestionTool` + probes + docs; codex/cursor/
  agy live behavior UNVERIFIED, same caveat the hook code carries):

| Agent | Tool | Note |
|---|---|---|
| Claude | AskUserQuestion | 1–4 picks, Other box |
| Codex | request_user_input | Text-only, short picks |
| Cursor | ask_question | Clickable picks |
| Agy | ask_question | Per repo test |
| Muse | request_user_input | Same card as Claude |

- Headless `muse exec` cannot approve: without `--user-input-auto-resolve`
  there is no dialog, with it the prompt self-cancels. Approvals require an
  interactive TUI session. CLI `swarm answer` and the web UI stay as
  fallbacks; this spec adds the native path, it does not remove them.

## DB models

No schema change. `requests` (kind `approve_section`/`approve_plan`/
`approve_report`, `artifact_id`, `section_id`, `section_sha256`,
`artifact_revision`) and `artifact_revisions` (`sections_json`,
`warnings_json`, v13) already carry everything the loop needs.

## Model / API types

Existing (unchanged) register result:

```json
{"artifact_id": "...", "revision": 2, "sections": [{"id": "...", "title": "...", "sha256": "..."}],
 "stale_requests": [], "warnings": ["...plan lint warnings, approve_plan only..."]}
```

New schema shape (apply `objSchemaRequired` + descriptions, same helper):

- `swarm_artifact`: required `["op", "item", "kind", "path"]`; `op` enum
  `[register, revise]`; `kind` enum `[spec, plan, debug_report, note,
  design, research]` with description "artifact kind, not a filename";
  `path` description "existing file under ~/.swarm/specs or ~/.swarm/plans,
  `## `-headed sections; plans end with a ```swarm-tree block";
  `item` description "top-level item KEY (not id) the artifact belongs to".
- `swarm_ask` approval: required `["kind"]`; `kind: "approval"` requires
  `artifact`, and per-section approval requires `section`.

Native prompt content per section (exact copy):

- header: `Spike approval`
- question: `Approve <Kind> section "<SectionTitle>" (rev <N>)?`
  (`<Kind>` = Spec/Plan/Report).
- options (single-select): `Approve`, `Request changes` (free text allowed).
- plan prompt appends: `Warnings:\n- <w1>\n- <w2>` when `warnings` non-empty.

Muse `request_user_input` mapping: one `questions[]` entry per turn with
`id` = `approval:<request_id>`, `selection: {"mode": "single"}`; the answer
`selectedLabel` maps to approve vs request-changes, `freeText`/`note`
becomes the change request body.

## Screens

Interactive TUI only; rendering differs per agent but the content contract
is identical. Muse/Claude card (ASCII):

```
┌ Ask user question ─────────────────┐
│ Spike approval                     │
│ Approve Spec section "Scope"       │
│ (rev 2)?                           │
│   ( ) Approve                      │
│   ( ) Request changes  [text____]  │
└────────────────────────────────────┘
```

Web NeedsYou + `swarm answer` flows are unchanged fallbacks; no web change
in this spec.

## File list

- `internal/mcpserver/orchestrator.go` — `artifactTool` schema →
  `objSchemaRequired` + descriptions above; result shape unchanged.
  Task 5B adds items/worktree/control/role_overrides/materialize required.
- `internal/mcpserver/tools.go` — `askTool` schema → required `kind` +
  approval/section descriptions; `blockerTool`/`sendTool`/`advisorTool`/
  `instructionsTool`/both `swarm_kb` variants → required per the table
  above; `readTool`/`syncTool` → descriptions only; `sendTool` default
  `""`/`"relay"` → `"finding"`.
- `internal/mcpserver/orchestrator_test.go`, `tools_test.go` — schema
  required-field tests + approval happy path (TDD first).
- `skills/swarm-orchestrator/SKILL.md` (+ installed mirror
  `internal/install/skills/swarm-orchestrator/SKILL.md`, via `skills-sync`)
  — spike section: per-section native-question loop before/around each
  `swarm_ask approval`, with the answer-forwarding rule and the
  parentless-only + interactive-TUI-only constraints.
- `scripts/e2e/spike_test.go` — extend the spike flow to assert one
  approval request per section.
- No change: `internal/hook/handler.go` (intercept already records),
  web UI, DB schema.

## Verification

TDD order; run after each step:

1. `go test ./internal/mcpserver/ -run 'TestArtifactTool|TestAskTool'`
   (new failing assertions first: missing `item`/`kind` rejected; then green).
2. `go test ./internal/mcpserver/ ./internal/runtime/ ./internal/hook/`
   full packages green; `gofmt -l` clean.
3. `scripts/e2e/spike_test.go` (or `make e2e` spike subset) green.
4. Manual: run a spike on a scratch item in a muse TUI session; confirm one
   native card per spec section, web UI shows the same requests, and
   approving natively resolves the daemon request (`approval_result`).

## Explicitly out of scope

- Hook block policy (parented agents stay relayed, no auto-approval).
- Removing web UI / CLI approval paths.
- Batching multiple sections into one native question.
- Headless/auto approval modes.
- New DB migrations or wire-shape changes.
