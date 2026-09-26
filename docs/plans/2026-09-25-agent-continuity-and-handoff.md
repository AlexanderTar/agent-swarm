# Agent continuity and handoff implementation plan (rebased on latest main)

**Goal:** Make agents durable entities with safe Pause/Handoff, plus the
Handoff menu action, prefix removal, and corrected preview header.

**Architecture:** Keep `agents.id` canonical and add a durable replacement
coordinator; preservation and recovery ride on manifests and additive
sync/read fields. No second workflow engine; reuse merged PR #20.

**Tech Stack:** Go daemon (`internal/runtime`, `internal/items`,
`internal/mcpserver`, `internal/hook`, `internal/db`), Swift menubar
(`SwarmBarKit`/`SwarmBarUI`), `skills/swarm-batching/SKILL.md` for batching.

**Spec:** `docs/specs/2026-09-25-agent-continuity-and-handoff.md`

**Skills in force:** superpowers `test-driven-development` (every unit),
`dispatching-parallel-agents` + `using-git-worktrees` (batch isolation),
`requesting-code-review` (one review per batch),
`verification-before-completion` (no claims without fresh evidence).

## Global Constraints

- Baseline is main `a1bfa27`; 2026-09-24 docs are superseded, not evidence.
- Reuse merged PR #20 (migrations 0010–0013, workflow DSL, RoleSkills,
  batching skill); never re-implement P1–P12.
- Orchestrator handoff replaces only its session; children keep running.
- Preview order: name, task, agent label, human model (effort).
- No live-swarm auto-mutation on deploy; lineage apply needs preview+backup.
- No commit of secrets, ro trees, or another live child's work, ever.
- TDD iron law: no production code without a failing test watched failing
  first for the expected reason. Test-after is not done — delete and redo.
- One build→review→fix cycle per batch (4 total), never one per phase.
  A batch merges only after its reviewer signs off and its gate is green.

## TDD protocol (every cycle in every batch)

1. RED: write one minimal behavior test (real code, no mocks unless
   unavoidable), run its package with `-run`, confirm it fails for the
   expected reason (missing feature, not typo). A test that passes
   immediately is testing existing behavior — rewrite it.
2. GREEN: write the minimal production code to pass; run the FULL package
   suite (`go test <pkg>/ -count=1`), not just the new test. Other failures
   are fixed now, never later.
3. REFACTOR only while green; re-run the package suite after.
4. Commit per cycle (red commit then green commit, or one cycle commit);
   never mix cycles from different units in one commit.

## Batch review gate (once per batch)

1. Implementer runs the batch's full gate fresh and pastes evidence.
2. Orchestrator dispatches one reviewer subagent with: DESCRIPTION (what
   the batch built), the spec path + batch scope, BASE_SHA (batch base)
   and HEAD_SHA. Reviewer gets crafted context, never session history.
3. Fix Critical immediately; fix Important before merging; note Minor.
   Push back with code/tests if the reviewer is wrong.
4. Orchestrator verifies the fix diff itself and re-runs the gate fresh —
   agent success reports are never evidence.

## Review Focus

- Handoff of an agent with a shared-rw worktree must block naming the
  exact owner, never commit another live writer's work.
- Duplicate Handoff requests must return the same operation, never two.
- A successor must reconcile newer child progress, never overwrite it.
- Prefix-free daemon notices must still classify as daemon, never user.
- Legacy TASK-242 shape (cancelled A attempt 2, completed B attempt 1) must
  not read as completed for A.

## Steps

### Batch 1 — Foundation: replacement contract + identity recovery

Worktree: sibling of the main checkout, branch `feat/agent-continuity-1`
off `a1bfa27`. Nothing else touches `internal/db/schema`,
`internal/runtime/agents.go` until this merges.

- [ ] RED `TestAgentContinuityMigrationPreservesRows`
  (`internal/db/agent_continuity_test.go`): migrate a fixture with agents,
  children, sessions, checkpoints, requests, reservations, workflow runs;
  expect preserved counts/IDs/links, empty FK check, second active
  operation for one agent refused. Run: `go test ./internal/db/ -run
  Continuity -count=1` → must fail (no migration).
- [ ] GREEN: migration `0014` (`agent_replacements`, `agent_lineage`,
  nullable checkpoint recovery metadata; number re-checked at execution).
  Full: `go test ./internal/db/ -count=1`.
- [ ] RED `TestReplacementRequestReplay`
  (`internal/runtime/replacement_test.go`): same agent+key replays one
  operation; different key while active → conflict + active ID; late
  checkpoint cannot flip cancelled to ready; close/reopen DB keeps state.
- [ ] GREEN: operation insert/read/CAS in new `replacement.go`.
  Full: `go test ./internal/runtime/ -run Replacement -count=1`.
- [ ] RED `TestStartRecoversSameOrchestrator` (`agents_test.go`): cancel
  execution on an unfinished epic with two finished children, then Start
  → same ID/name, child parents intact, new session; active duplicate
  refused; child orchestrators on distinct items stay distinct.
- [ ] GREEN: route existing-assignment recovery through replacement
  intent in `agents.go`/`reconcile.go`; cancel keeps recoverable state and
  disables auto-launch; item cancel stays terminal.
- [ ] RED `TestLegacyLineagePreservesHistory` (`lineage_test.go`): seven
  same-assignment orchestrator rows preview to the active row; double
  apply changes nothing historical; two active candidates/cycles fail
  without writes.
- [ ] GREEN: dry-run/apply lineage helper + maintenance CLI command;
  canonical reads merge old mailbox/ownership; never reparent workers.
- [ ] RED `TestRetryDuringStoppingQueuesIntent` + requests-survival test:
  completed checkpoint with live stopping pane → Send reports stopping,
  explicit Retry queues once and launches after death, child mail accepted
  during handoff gap, item cancel blocks queued retry; answered request
  keeps ID across sessions with audit session intact.
- [ ] GREEN: recipient readiness and retry eligibility in `requests.go`/
  `inbox.go`; orphan sweep keyed on logical termination, not process death.
- [ ] Gate + review R1. Gate: `go test ./internal/db/... ./internal/runtime/
  -count=1`. Then reviewer (DESCRIPTION: replacement contract + identity
  recovery; spec + this batch; BASE/HEAD SHAs). Fix, re-verify, merge.

### Batch 2 — Save/restore contract: recovery reads + preservation

Worktree: new sibling, branch `feat/agent-continuity-2` off Batch 1 merge.
Docs-only research (Batch 4's investigation) may run alongside in the spec
worktree — it touches no code. Owns `recovery.go`, `preservation.go`,
`pause.go`, `checkpoint.go`, `text.go` prompts, `hook/handler.go` pause
policy, skill text.

- [ ] RED `TestSyncRecoverySurvivesAckedAssignment`: acked assignment +
  new generation → first sync still returns full assignment and recovery
  (op, manifest path/hash, predecessor session, cursor, workflow binding);
  no manifest → explicit absent recovery, never fabricated.
- [ ] GREEN: additive `SyncResult.assignment/recovery` built from durable
  state in new `internal/runtime/recovery.go` + `mcpserver/recovery.go`.
- [ ] RED `TestRecoveryHistoryAcrossGenerations`: 200+ checkpoints across
  attempts/generations page exactly once by (created_at, id) cursor with
  full fields/provenance; another agent's history denied.
- [ ] GREEN: agent-scoped paginated checkpoint history, request/worktree/
  artifact discovery (IDs, paths, mode, HEAD; revisions/hashes; missing
  files are explicit errors, never fresh worktrees).
- [ ] RED→GREEN `TestSpawnBriefNestedSchema`: nested BriefInput schema +
  valid example published in swarm/orchestrator skills; `make skills-sync`.
- [ ] RED `TestPreservationAllowsSaveButNotNewWork` (`handler_test.go`,
  pause allow-list): pausing worker can save/inspect/commit under existing
  permissions; delegation, workflow-start, push/deploy, completed refused.
- [ ] GREEN: preservation-mode rules replace the blanket pause denial in
  `hook/handler.go` + `pause.go`; worktree/config guards kept.
- [ ] RED `TestHandoffManifestPreservesScratchArtifacts`: dirty rw tree +
  outside-git spec → signed WIP commit of explicit paths, atomic artifact
  copy with hashes, symlink/path-escape rejection, no
  credentials/transcripts; clean tree needs no commit; shared rw with a
  live writer blocks commit-ready.
- [ ] GREEN: manifest v1 + atomic writers in new `preservation.go`.
- [ ] RED `TestPauseCannotClaimReadyAfterFailedSave` + rune-count test:
  signing/hook/conflict/live-command/missing-artifact/deadline failures →
  blocked, never ready; wrong predecessor/hash rejected; 501-rune summary
  reports actual 501, 500 accepted.
- [ ] GREEN: checkpoint recovery binding in `checkpoint.go`; spec §4
  prompts/checklist installed with golden tests (pause, fresh handoff,
  resumed history, broken predecessor, live-children orchestrator, ro
  reviewer).
- [ ] Gate + review R2. Gate: `go test ./internal/runtime/
  ./internal/mcpserver/ ./internal/hook/ -count=1` + `make skills-sync`
  clean. Then reviewer (DESCRIPTION: save/restore contract + prompts).
  Fix, re-verify, merge.

### Batch 3 — Delivery surface: handoff API, menubar, prefix removal

Worktree: new sibling, branch `feat/agent-continuity-3` off Batch 2 merge.
Owns HTTP/MCP/CLI surface, SSE/state, `apps/menubar`, `IsDaemonPrompt`,
goldens/adapters. Needs Batch 2 (control notice text, recovery fields).

- [ ] RED: `POST /api/agents/{name}/handoff` (`{request_id}` → 202 +
  `ReplacementResult`), `GET replacement` (404 absent),
  `swarm_control handoff` auth + same-root checks, CLI accepted/phase
  output, Pause/Resume/Retry coordinator guards.
- [ ] GREEN: routes + CLI wiring; no "completed" on a 202.
- [ ] RED (Swift): right-click Handoff eligibility matrix, phase labels
  (Saving…/Stopping…/Queued/Starting…/Blocked: reason), stable row
  identity, preview-cache invalidation on generation change.
- [ ] GREEN: menubar action + state plumbing; disconnect disables actions.
- [ ] RED: header `login-coder · TASK-101 · Claude · Opus 4.6 (High)` —
  name, item key, `AgentKind` label, `CatalogRules.modelLabel`, stored
  effort in parenthesis (omitted when empty, ID fallback never guessed).
- [ ] GREEN: verify Go state wire effort first — extend `AgentNode`
  decoding only if absent; fixture/cache compatibility tests.
- [ ] RED: prefix-free `Inbox`/`PendingNotice`/`ControlNotice`/
  `CompactionNotice` still satisfy extended `IsDaemonPrompt` (origin
  metadata + Preamble/ShortPreamble); peer bodies and stored history
  byte-identical; legacy-prefix recognition retained temporarily.
- [ ] GREEN: strip generated `[swarm]` on all delivery channels; update
  skill triggers, embedded copies, goldens, adapters, advisor filtering.
- [ ] RED→GREEN e2e (fake adapter, isolated assignment, never the live
  swarm): dirty/untracked files + outside-git spec + unfinished unit →
  Handoff → signed WIP + bundle → old token dead → one fresh session,
  same ID/name/path → recovery read → work continues; orchestrator
  variant with a child finishing mid-replacement relayed exactly once.
- [ ] Gate + review R3. Gate: `go test ./internal/... -count=1`,
  `swift test` in `apps/menubar`, `scripts/e2e.sh` handoff scenario, all
  fresh. Then reviewer (DESCRIPTION: handoff surface + menubar + prefix).
  Fix, re-verify, merge.

### Batch 4 — Remainders + report research

Worktree: new sibling, branch `feat/agent-continuity-4` off Batch 2 merge
(read paths settled there). Runtime remainders and the F7/F12/F13 evidence
record; the research ships no behavior change.

- [ ] RED `TestLegacyCompletedCurrentIsPerBuilder`
  (`internal/items/transition_test.go`): cancelled-A-attempt-2 →
  completed-B-attempt-1 not completed for A; same-agent stale completion,
  tied timestamps, reopened items. Legacy flow otherwise untouched.
- [ ] GREEN: per-builder comparison in the `Workflow == nil` branch only.
- [ ] RED→GREEN `swarm_read` kinds/projection/pagination (default 50/max
  200, stable cursor, per-ref partial errors, explicit
  unsupported-filter errors); recovery history stays on Batch 2's
  agent-scoped query.
- [ ] RED→GREEN final `item_revision` in parent relays; optimistic
  concurrency kept, `revision: latest` rejected.
- [ ] RED→GREEN lifecycle contract: destructive-cwd guards,
  command-handle retention, question-blocks-completion, scratch ownership
  labels + exact-ID cleanup; unlabelled/shared services and task data
  never touched.
- [ ] Research-only F7/F12/F13 → write
  `docs/investigations/2026-09-25-runtime-report-followup.md` with
  Baseline/Reproduction/Result/Disposition each (request counts/terminal
  answers; fake-clock unacked timing — no timer change until reproduced;
  provider/mode advisor matrix). Any discovered runtime fix becomes its
  own follow-up package, never rides along.
- [ ] Gate + review R4. Gate: `go test ./internal/items/
  ./internal/runtime/ ./internal/mcpserver/ -count=1` + `python3 -c`
  assert naming F7/F12/F13 with all four sections. Then reviewer
  (DESCRIPTION: remainders + research record). Fix, re-verify, merge.

### Integration

- [ ] Merge order: Batch 1 → Batch 2 → Batch 3 + Batch 4 (parallel after
  Batch 2; rebase Batch 4 onto Batch 2 before its review).
- [ ] After all merges: full `go test ./... -count=1` (or repo `test-go`
  gate if time allows, ~minutes), menubar `swift test`, one final
  `scripts/e2e.sh` run — fresh, same session, before any merge-to-main
  claim.
- [ ] Remove the batch worktrees with `git worktree remove` after merge;
  keep the spec worktree until release notes are written.

## Validation Plan

- Per cycle: the exact `-run` RED/GREEN commands above; per batch: the
  gate line, run fresh in full — a green file run is not a green suite.
- Highest risk is Batch 3 e2e: predecessor death + token rejection +
  single successor + child relay exactly-once must all hold in the
  fake-adapter run; rerun `scripts/e2e.sh` on any retry-logic change.
- Migration safety: `0014` against disposable copied fixtures with
  row-count and `PRAGMA foreign_key_check` verification (Batch 1).
- No full provider smoke on live assignments; isolated test assignments
  only. No claims ("passes", "fixed", "done") without the fresh output
  in hand.

## Risks / Open Questions

- Critical path is Batch 1 → 2 → 3; Batch 4 overlaps Batch 3. Four
  reviews total is the floor — do not split batches further to "go
  faster"; a skipped review is a future fix cycle.
- Migration `0014` assumes no newer migration lands first; re-check
  `internal/db/schema/` at execution or renumber.
- Batch 4 must rebase onto Batch 2 (shared read paths); conflicts are
  the orchestrator's integration job, verified by the full gate.
- Batch worktrees are siblings (never nested in tool dirs); stage explicit
  file paths per commit, never `git add -A` in a shared tree.
- Open questions: None. Prior answers (children keep running, preview
  order, menu placement) are locked in the spec.
