# Durable agents, safe pause and handoff (rebased on latest main)

Status: implemented on `feat/agent-continuity-integration` (review fixes
applied 2026-09-26), except the Pause deferral noted in §1 and §3; follow-up
plan `docs/plans/2026-09-26-pause-shares-replacement-operation.md`.
Supersedes `docs/specs/2026-09-24-agent-continuity-and-handoff.md` and its
investigation/plan, whose baselines are stale (main `f40de3f`, unmerged PR #20
`278ded1`, local worktrees `8eaf92f`/`1fe8bd7`). Do not use those baselines.
New baseline: main `a1bfa27` (PR #20 merged: migrations 0010–0013, workflow
DSL/engine, RoleSkills kickoffs, `skills/swarm-batching/SKILL.md`).
User confirmed: orchestrator handoff replaces only its session; children keep
running and their messages wait for the successor.

## 1. Intent and success criteria

An agent is a durable Swarm entity. Provider sessions execute its assignment;
they do not define its identity. Replacing any agent retains its ID, name,
role, assignment, parent, finished/live children, worktrees, artifacts, inbox,
settings and progress. Handoff saves current work and starts a fresh provider
session for the same entity. Pause performs the same preservation but waits
for Resume instead of launching a successor.

Pause deferral (current state): Pause shares Handoff's notice path (control
message, PAUSE/HANDOFF preservation notice, Stop-hook block), its deadline
(pause_deadline_at with TickPause interrupt/kill) and its hook policy
(preservation-mode tool/command rules). Pause does NOT yet create an
`agent_operations` row, assemble a manifest, or pass the ready gate; only
Handoff and recover do. This is safe for now because nothing auto-launches
after Pause (the session parks paused until an explicit Resume or Handoff),
and Resume re-checks worktree HEAD/status and durable state before editing,
so no successor ever inherits an unvalidated save. A later Handoff of a
paused agent runs the full operation, manifest and ready gate.

Also deliver: menubar right-click Handoff action; removal of the generated
`[swarm]` prefix from agent-message delivery; preview header order name,
task, agent display name, human-readable model with effort in parenthesis.
Re-check every F1–F14 finding against merged PR #20; fix only the remainders.

## 2. Rebased evidence (verified on `a1bfa27`)

- Identity bug still present: `internal/runtime/agents.go:StartOrchestrator`
  only refuses when a queued/active orchestrator shares the root item, then
  inserts a new `agents` row; `resolveName` checks all historical names and
  suffixes via `ids.Unique`. Cancel finishes the agent, so the next Start
  mints `...-2`, `-3`, orphaning children, worktrees, requests and mail.
  `Resume`/`Retry` already show the identity-preserving pattern.
- Safe preservation still impossible: `pauseAllowedTools` is
  `swarm_sync`/`swarm_read` (+checkpoint/ask/blocker); `PreToolUse` denies all
  other tools while pausing, so a compliant worker cannot inspect, commit, or
  snapshot scratch after PAUSE.
- Recovery reads still incomplete: `SyncResult` (`inbox.go:230`) carries
  messages/unacked/session-state only — no durable assignment or recovery
  bundle, despite `ResumeKickoff` promising both. `swarm_read` checkpoint
  output still strips next/blockers/git/verification/artifacts/provenance.
- Prefix still load-bearing: `IsDaemonPrompt` accepts the idle token,
  `ShortPreamble`, or `[swarm]` prefix; `Inbox`, `PendingNotice`,
  `ControlNotice`, `CompactionNotice` emit `[swarm]`. Stripping the prefix
  without a replacement classifier turns peer events into apparent user
  prompts.
- Preview still old order: `Copy.paneHeader(name, kind, itemKey)` renders
  name · agent · task; `AgentNode` (Wire.swift:64) carries no effort field;
  `CatalogRules.modelLabel` exists for human-readable models.
- Merged PR #20 already fixed: per-agent completion for workflow tasks
  (`completedCurrent` workflow branch), commit/artifact gates (P8), engine
  retries/rounds/relays (P9), spawn/checkpoint/read + idempotency (P10),
  board/menubar workflow UI (P11), role plans/skills (P12), wake backoff.
  Remainder: legacy-task completion still uses item-wide `MAX(attempt)`
  (`transition.go:368-370`), plus all five requests above.

## 3. Design

Keep `agents.id` canonical; a new session changes session ID, generation,
token and provider session ID only. Preserve creation time, role overrides,
advisor config, model, effort, repo confirmations, item/root/parent links,
brief and artifact lineage. Handoff never silently applies current role
defaults or usage fallback; if the provider/model cannot launch, stay
recoverable with an actionable blocked state.

Lifecycle table: Pause keeps the agent active/resumable with no successor
until Resume/Handoff. Handoff keeps it active/replacing with a fresh provider
session after preservation and old-process death. Stop/crash/cancel with
unfinished assignment stays recoverable — never mint a suffixed agent; Retry
or explicit recovery restarts it in place. User Cancel stops execution,
disables auto-restart, retains recoverable identity. Item/assignment cancel
is terminal. A valid completed checkpoint with gates satisfied finishes the
agent. Provider exit without completion is recoverable, not finished.

`StartOrchestrator` on an unfinished assignment resolves the existing logical
orchestrator (active → return/conflict; recoverable → restart in place),
matching on exact assignment, never `root_item_id` alone. Worker replacement
APIs identify the predecessor by agent ID/name; two coders on one task are
never the same agent. Genuinely new workers keep unique-name behavior.

Historical duplicates (`...-2` … `-7`) are reconciled by an explicit
previewable lineage mapping over exact `(root, item, role, parent)` matches:
sole active (else newest recoverable) entity becomes canonical; earlier rows
link via `agent_lineage(alias, canonical)`. No row deletion, no automatic
rename of the active agent, no merging of independent workers; ambiguous
groups report conflict without mutation. Canonical reads combine
finished/live children and old worktree ownership; original audit IDs kept.

Replacement contract (migration `0014`, after PR #20's `0013`):
`agent_replacements` (id, canonical agent, predecessor/successor sessions,
mode `pause|handoff|recover`, phase
`requested|preserving|stopping|ready|queued|starting|succeeded|blocked|cancelled`,
request key, checkpoint, manifest path/hash, error, timestamps; partial
unique index allows one nonterminal operation per agent) plus
`agent_lineage` and nullable checkpoint recovery metadata. Request identity
is durable per canonical agent + request key: duplicates return the
operation; a different key while active returns 409 + active ID. Persist
intent before side effects; compare session/generation at every transition;
launch identity transactionally and reconcile by `SWARM_SESSION`, never pane
name; revoke predecessor credentials before successor activation; prove the
old process and managed writers are gone before granting write access.
Daemon restart resumes from durable phase. Cancel wins over pending launch.

Preservation (shared by Pause and Handoff; Pause disables auto-launch; the
operation row, manifest and ready gate are Handoff/recover-only for now, see
the Pause deferral in §1):
predecessor stops new work, finishes/interrupts its atomic action, and may
use read/edit/shell/wait/commit under existing permissions — denying only
delegation, new workflow steps, push/deploy, and completed checkpoints.
Per assigned rw worktree: capture status incl. untracked, stage explicit
task-owned paths, signed WIP commit when dirty; never commit ro trees, live
children's work, secrets, or unrelated files; shared rw with another live
writer blocks commit-ready handoff. Snapshot non-git specs/plans/research
under `<swarm home>/handoffs/<agent-id>/<operation-id>/artifacts/` via the
artifact registry. Write `manifest.json` (schema v1: operation/agent/
predecessor/attempt/generation, assignment refs, unit/step + next action,
blockers, worktrees with HEADs, artifacts with hashes, verification results,
command handles, request/message IDs, workflow binding, checkpoint cursor)
atomically, then checkpoint last. Daemon validates ownership, manifest
hash, worktree HEADs and writer absence before ready; any failure yields
explicit blocked state, never a fabricated success.

Successor recovery: always adapter Launch with a new provider session,
same paths and reservations, PR #20 RoleSkills kickoff + recovery
instructions. `swarm_sync` gains additive `assignment` + `recovery`
(operation, manifest path/hash, predecessor session, cursor, workflow
binding) on every generation's first sync, independent of acked inbox rows.
`swarm_read` gains agent-scoped paginated checkpoint history with full
fields/provenance plus readable requests, worktrees (IDs/paths) and artifact
revisions/hashes. Successor replays unacked IDs, resets per-generation
delivery eligibility, keeps acked messages acked, keeps agent/item-owned
requests across sessions, and re-checks disk HEADs and live item/child/
workflow state before editing (children progress during orchestrator
handoff — reconcile, never blindly overwrite).

## 4. Agent prompts (normative templates)

Substitute from daemon state; reuse `Preamble`, `RoleSkills`, checkpoint
format; never add `[swarm]` to new notices.

Pause/Handoff notice: "{PAUSE|HANDOFF} requested for {name} ({item_key}).
Stop taking new work and call swarm_sync. Safely finish or interrupt your
current operation, collect its status, and preserve your work. You may use
tools needed to save files, wait for or stop your own commands, inspect git,
commit task-owned changes, and write the handoff manifest/checkpoint. Do not
delegate, start another workflow step, push, deploy, or report the assignment
completed. If preservation fails, record the blocker and surviving paths; do
not claim a clean handoff. {Handoff: a fresh session for this same agent
starts after preservation and termination. Pause: wait for Resume.}
{Preamble}"

Preservation checklist (core swarm skill): stop new work, save edits,
wait/interrupt owned commands with real exit status; inspect each rw
worktree, stage task-owned paths only, signed commit when dirty (ro trees
and live children's work excluded; failures → blocked with dirty paths);
snapshot specs/plans/scratch with IDs/revisions/hashes plus units, next
action, blockers, tests, resources, questions, workflow binding; manifest
first, then `swarm_checkpoint kind:"handoff"` (summary ≤ 500 chars), then
end the turn. Orchestrator handoff leaves children running and records
their IDs/decisions without fabricating their checkpoints; Pause group
keeps the child-first protocol with a combined snapshot.

Successor kickoff: "You are swarm agent {name} ({role}) for {item_key}:
{title}, continuing in a fresh session after {handoff|interrupted
recovery}. Identity and assignment unchanged. Use {RoleSkills}. Call
swarm_sync first; read assignment and recovery manifest. Read all
checkpoint pages, referenced specs/plans/artifacts, and current
item/worktree/workflow state. Reuse exact worktrees/branches; check
HEAD/status before editing. Continue the unfinished unit and next action;
do not repeat completed work or reset the plan. Checkpoint acceptance
naming the predecessor and next action. On incomplete recovery or
divergence, inspect and report before overwriting. {Preamble}"
Resume adds: reload durable state even if this session remembers work;
durable state wins. Broken-predecessor recovery ships an
incomplete-recovery warning with observed paths/checkpoints — inspect
dirty/untracked files first; never reset/clean or invent test results.

## 5. Menubar and prefix

Right-click Handoff on eligible rows (running/waiting/stale,
paused/interrupted, failed/crashed, cancelled-session with unfinished
assignment); disabled with "Wait for startup" for spawning/queued-without-
session; absent for completed/cancelled assignments. Phases surface as
Saving handoff… / Stopping… / Handoff queued / Starting successor… /
Handoff blocked: reason; repeat clicks disabled; SSE/state drives progress;
reconnect refetches the operation. Row ID, name, expansion, parent,
finished-child grouping and selection stay stable; terminal preview cache
invalidates on generation change.

Preview header: `login-coder · TASK-101 · Claude · Opus 4.6 (High)` —
name, item key, `AgentKind` display label, `CatalogRules.modelLabel`, stored
effort's human label in parenthesis (omit when empty; unknown model falls
back to ID, never a guessed version). Requires optional effort on
`AgentNode` decode (verify Go state wire first; add only if absent) plus
fixture/cache compatibility.

Prefix removal: strip generated `[swarm]` from agent-message
envelopes/notices on hook, native wake, MCP shim and tmux delivery; never
rewrite peer body text or stored history. Classify via transport origin
metadata + existing daemon `Preamble`/`ShortPreamble` (extend
`IsDaemonPrompt` accordingly); keep temporary legacy-prefix recognition for
in-flight sessions; update skill trigger text, embedded copies via
`make skills-sync`, fixtures/goldens, adapter tests, and advisor transcript
filtering. Preserve IDs, sequence, attribution, truncation, ack semantics.

## 6. Report remainders and non-goals

swarm_read gains validated `filter.kind`, field projection, default 50 /
max 200 pagination with stable cursor, per-ref errors with successes, and
explicit unsupported-filter errors (F6). Legacy completion evaluates the
latest relevant builder completion against that builder's own latest
attempt incl. cancelled-A-attempt-2 → completed-B-attempt-1, same-agent
stale, ties and reopened items; parent relays carry final `item_revision`
without relaxing optimistic concurrency (F1/F11). One worker-lifecycle
contract covers destructive-cwd guards, command-handle retention, question
blocking completion, and scratch-resource ownership (F3/F4/F5/F14). F7/F12/
F13 are research-only: reproduce request count/terminal answers, unacked-
on-retry timing, and the provider/mode advisor matrix before any behavior
change; a research task never ships one quietly.

Non-goals: no second workflow engine; no new retention deletion; no live
swarm auto-mutation on deploy (lineage apply needs preview + backup);
no OS-sandbox claims; no silent respawn of completed/cancelled agents;
no re-implementation of merged P1–P12 behavior.
