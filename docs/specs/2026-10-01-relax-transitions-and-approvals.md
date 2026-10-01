# Relax Swarm transitions and approvals (CHORE-18)

## Context

Agents regularly can't close work because a deterministic rule isn't met: a gate
refuses `completed` (TDD evidence shape, verify containment, commit HEAD match), the
`integrated` checkpoint refuses (verify not recorded, final review at another sha),
`checkTask`/`deriveStory` won't move an item, or the finish question only offers
three fixed PR/merge choices that don't match how a repository is actually shipped.
The web board also hardcodes approval buttons, so options an agent proposes never
reach the controls.

This chore adds three escape hatches that stay audited, plus option propagation:

1. **Free-form finish options**: the orchestrator proposes how to finish, based on
   repo history and previous items. The daemon asks, records the choice and trusts
   the orchestrator's `finishing` report.
2. **Orchestrator waivers**: an orchestrator waives a named gate on an item in its
   tree, with a reason. Gates still apply to everyone else.
3. **Orchestrator overrides**: an orchestrator moves an item in its tree to a status
   the derived state machine won't reach, with a reason. Reconciliation respects it.
4. **Agent options everywhere**: approval requests carry agent options; the web board
   renders them as buttons, with an optional comment on approve.

Repo: `agent-swarm` only. Integration branch `chore-18-relax-transitions`.
Collision warning: packages A and B both touch `internal/runtime/checkpoint.go`,
`internal/mcpserver/tools.go` and add migrations; A owns migration `0024`, B owns
`0025`.

## Locked decisions

1. Finish options are free-form labels from the orchestrator (user decision). The
   daemon only validates shape, records the chosen label, and trusts `finishing`.
2. Without agent finish options, today's fixed finish prompt is unchanged.
3. "Request changes" is always appended by the daemon; agents never send it.
4. Waivers and overrides are orchestrator-only, scoped to the orchestrator's own
   tree, require a reason (1–300 chars), and are recorded as events and shown on
   the board. Workers can't waive anything.
5. Root Done always still requires the user's finish answer. Overrides can't move a
   root to Done, and nothing can skip the finish question.
6. Agent options on approvals: every agent option is an approve decision carrying
   `choice`; "Request changes" stays the only rejecting decision.
7. Existing flows, wire fields and tests keep working; new fields are optional.

## DB models

Migration `0024_finish_options.sql` (package A):

```sql
-- item_merges gains kind 'kept': the orchestrator reports a repo it finished
-- without a PR or local merge (free-form finish, CHORE-18).
-- Rebuild item_merges with kind IN ('pr','local','kept'); copy rows; recreate
-- index item_merges_open and the UNIQUE (item_id, integrated_checkpoint, repo).
```

Request `binding_json` for `accept_epic`/`accept_fix` gains optional keys (no
column change): `finish_options: [{label, description}]`, `choice: string`.
`$.merge` gains value `"custom"`.

Migration `0025_item_overrides.sql` (package B):

```sql
ALTER TABLE items ADD COLUMN waivers_json  TEXT;  -- [{gate, reason, agent, at}] or NULL
ALTER TABLE items ADD COLUMN override_json TEXT;  -- {status, reason, agent, at} or NULL
```

Approval requests (`approve_section`, `approve_plan`, `approve_report`) reuse the
existing `options_json` column for agent options (`[{label, description}]` JSON);
no migration.

## Model / API types

Go (runtime):

```go
type FinishOption struct {
    Label       string `json:"label"`       // 1–80 runes, unique, not "Request changes"
    Description string `json:"description"` // 0–300 runes
}
// CheckpointInput gains:
FinishOptions []FinishOption `json:"finish_options,omitempty"` // integrated only, 1–4
Waive         []Waiver       `json:"waive,omitempty"`          // integrated only (orchestrator)
Kept          []KeptRepo     `json:"kept,omitempty"`           // finishing only, custom merge only

type KeptRepo struct { Repo, Note string } // note 1–300 runes

type Waiver struct {
    Gate   string `json:"gate"`   // see WaivableGates
    Reason string `json:"reason"` // 1–300 runes
}
var WaivableGates = []string{"tdd", "verify", "commit", "artifact:design",
    "artifact:notes", "integration_verify", "final_review", "open_questions",
    "required_artifact"}

// ApproveInput gains Choice string; AskInput gains Choice string and
// ApprovalOptions []FinishOption (swarm_ask approval).

// items.Item gains:
Waivers  []Waiver  `json:"waivers,omitempty"`
Override *Override `json:"override,omitempty"`
type Override struct { Status Status; Reason, Agent string; At time.Time }
```

MCP:

- `swarm_checkpoint`: `finish_options`, `waive`, `kept` as above.
- `swarm_ask kind:"approval"`: `options` may be `[{label, description}]` (1–4).
- `swarm_ask kind:"native_answer"`: new `choice` (the picked label) with
  `decision:"approve"` for agent-option requests; finish requests with agent
  options use `decision:"approve"` + `choice`.
- `swarm_items op:"update"`: new `waive: [{gate, reason}]` (adds; an entry with
  empty reason removes that gate) and `override_reason` (required to force a status
  the normal check refuses).

HTTP: `POST /api/requests/{id}/approve` body gains `choice` and `comment`;
`merge` accepts `"custom"` when the request has `finish_options`.
Request wire gains `option_descriptions: string[] | null`, `choice: string | null`.
Item wire gains `waivers` and `override`.

Behaviour:

- **Finish** (A): `integrated` with `finish_options` stores them in the accept
  binding. Native prompt options are the labels plus "Request changes";
  descriptions likewise. Approve with `choice` sets `$.merge="custom"`,
  `$.choice`. `approval_result` payload: `{decision, merge:"custom", choice}`.
  `finishing` on a custom approval: every integrated repo must appear in exactly
  one of `prs`, `merged`, `kept`; PRs are looked up but not checked against a
  merge mode; local merges are recorded without ancestor verification; kept repos
  insert `kind='kept', state='merged'`. Done once every row is merged (open PRs keep
  the existing watcher).
- **Waivers** (B): `applyGates` skips a gate waived on the item. `integrated`
  skips `integration_verify`/`final_review` when waived on the root or passed in
  `waive`. The open-question completion block and "completed requires a registered
  artifact" skip when waived. Each waiver appends event `item.waived`.
- **Overrides** (B): with `override_reason`, an orchestrator may set a task or
  story to `ready`, `in_progress`, `in_review` or `done` from any non-cancelled
  status, and reopen `done` → `ready`. A forced Done on a workflow task cancels its
  live workflow. `override_json` is set; `deriveStory`/task reconcile leave an item
  alone while `override_json.status` equals its status; any later non-override
  transition clears it. Event `item.overridden`. Roots: only `in_review` ↔
  `in_progress`; never Done.
- **Finish prompt disclosure**: when any item in the root's tree has waivers or
  overrides, the finish question appends `Waived/overridden: <n> (see board).`.

## Screens

Web Review panel, finish request with agent options:

```
┌ Finish CHORE-18 "Relax Swarm transitions and approvals" ────────┐
│ main ← chore-18-relax-transitions @ 1a2b3c4                     │
│ ⚠ 2 waivers, 1 override in this tree                            │
├─────────────────────────────────────────────────────────────────┤
│ [Squash-merge PR (repo default)]  [Push straight to main]       │
│  Push, open PR, squash when green  Fast-forward main, no PR      │
│ [Keep branch, I'll ship it]                                      │
│ Comment (optional) ______________________________               │
│ [Request changes]                                                │
└─────────────────────────────────────────────────────────────────┘
```

- Buttons: first option primary, rest outline; description under each label in
  muted text. No options → today's buttons.
- Section/plan/report approvals with agent options: same button row in place of
  the single Approve button.
- Item detail: `Waived: verify — <reason>` and `Overridden to Done — <reason>`
  rows in muted warning style. Not shown: waiver/override editing (agent-only).

## All user-facing copy

- `C.approveComment` = "Comment (optional)"
- `C.waiversInTree(n, m)` = "{n} waivers, {m} overrides in this tree"
- `C.waivedRow(gate, reason)` = "Waived: {gate} — {reason}"
- `C.overrideRow(status, reason)` = "Overridden to {status} — {reason}"
- Toast on choice approve: existing `T.toastApproved(key)`.
- Errors (Go):
  - "finish_options needs 1–4 options with unique labels of 1–80 characters."
  - "\"Request changes\" is added by Swarm; don't send it as an option."
  - "Choose one of this request's options: {labels}."
  - "Only an orchestrator can waive gates or override status."
  - "Unknown gate {gate}; waivable gates: {list}."
  - "Give a reason (1–300 characters)."
  - "{key} is outside your tree."
  - "A root reaches Done only through its finish question."
  - "Report every integrated repo once under prs, merged or kept: {missing}."
- Gate refusals append: " An orchestrator can waive this gate with a reason."

## File list

Change: `internal/runtime/{native.go,requests.go,merges.go,checkpoint.go,types.go}`,
`internal/items/{transition.go,model.go,store.go}`, `internal/mcpserver/tools.go`,
`internal/httpapi/{runtime.go,requests.go}`, `internal/db/schema/0024_*.sql`,
`0025_*.sql`, `web/src/{types.ts,copy.ts,api.ts,logic/review.ts,panels/Review.tsx}`,
item detail panel, `skills/swarm-orchestrator/SKILL.md`, `skills/swarm/SKILL.md`
(mirrored by `make skills-sync`). Reuse unchanged: workflow engine, merge watcher.
Delete: nothing.

## Verification

1. `go vet ./... && gofmt -l .` clean.
2. `go test ./internal/items/... ./internal/runtime/... ./internal/mcpserver/... ./internal/httpapi/...`
3. `go test ./...`
4. `cd web && npm test && npm run build`
5. `make skills-sync` then `git diff --exit-code skills` clean.

Scenarios (tests): integrated with options → native prompt shows labels; approve
with a valid/invalid choice; finishing custom with prs+kept → Done after PR merge;
kept-only → Done immediately; no options → old prompt; waiver lets a worker
complete without tdd evidence; waiver by a worker refused; unknown gate refused;
override story Done survives reconcile; override cleared by later transition;
root Done override refused; web renders option buttons and posts `choice`.

## Explicitly out of scope

- Menubar finish controls (it has none today).
- Changing gate semantics for anyone without a waiver.
- Letting workers waive or override.
- Auto-detecting finish options in the daemon; the orchestrator decides.
