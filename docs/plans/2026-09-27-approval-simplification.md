# Approval Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove redundant repository approval, make spec section review concise, and show both artifact paths at the final plan approval.

**Architecture:** Reuse the repository set chosen at start and the existing request ledger. Classify spec sections by stable heading rules, validate approval summaries at request creation, and add registered artifact paths to the daemon's plan prompt. Keep native answer evidence rules unchanged.

**Tech Stack:** Go, SQLite, Markdown skills, hook adapters.

**Spec:** `docs/specs/2026-09-27-approval-simplification.md`.

## Global Constraints

- Work in `/Users/alexandertar/GitHub/agent-swarm-approval-simplification` on `feat/approval-simplification`.
- TDD for each behavior change: observe a meaningful RED, implement the smallest fix, observe GREEN.
- Preserve user-selected repo boundaries and `repos_version` checks; no unobserved native answer grants approval.
- Approval summary length is 1–700 Unicode characters, including Markdown table or ASCII sketch syntax.
- After `skills/*` changes, run `make skills-sync` and verify canonical and installed copies match.
- Stage only task-owned files; do not touch unrelated untracked files in the primary checkout.

## Review Focus

1. An existing spike has suggested repositories but no confirmed set: first repository read or direct materialization adopts exactly those repositories and withdraws obsolete requests once.
2. A registered spec has only informational sections: its required approval set is empty and plan approval can proceed; an unknown heading requires approval.
3. A changed decision section loses approval while unchanged sections retain it.
4. A 700-character Unicode summary passes, 701 fails, and a Markdown table remains unchanged.
5. Cursor and Muse answers are never counted as approved without an observable hook result.

## File map

| Files | Responsibility |
|---|---|
| `internal/runtime/agents.go`, `internal/runtime/confirm.go`, `internal/mcpserver/orchestrator.go` | Adopt selected repositories, expose `swarm_repos`, and preserve scope checks |
| `internal/runtime/artifacts.go`, `internal/runtime/materialize.go`, `internal/runtime/requests.go` | Classify required sections and validate summaries |
| `internal/runtime/native.go`, `internal/mcpserver/tools.go` | Return lossless artifact paths as approval review data and keep prompts bounded |
| `internal/hook/*`, `internal/adapter/*` | Verify native tool event and answer routes by agent kind |
| `skills/swarm-spike/SKILL.md`, `skills/swarm-orchestrator/SKILL.md`, `skills/swarm/SKILL.md`, `internal/install/skills/*` | Match agent instructions to runtime |

## Task 1: Adopt repository selection without a second approval

**Files:** `internal/runtime/agents.go`, `internal/runtime/confirm.go`, `internal/mcpserver/orchestrator.go`; tests in corresponding `_test.go` files.

**Interfaces:** Keep registered repo ids and `repos_version`; expose a narrow store method for adopting legacy suggested repos. Worktree create/review consumes the resulting root set. Scope expansion uses `swarm_repos` with `repos_version`; the daemon validates registered ids and active worktrees atomically.

- [ ] Write tests for newly started spike, legacy spike with concurrent first access or direct materialization, preserving a nonempty confirmed set, withdrawal/events for open confirmation requests, out-of-scope repo, and active reservation on scope change.
- [ ] Run focused tests and record the expected RED failures.
- [ ] Set selected repos at spike creation; adopt legacy selected repos atomically and close obsolete requests; retain unknown/out-of-scope refusal.
- [ ] Run focused tests to GREEN, then the affected package tests.
- [ ] Commit the repository-scope change.

## Task 2: Request only consequential spec approvals with short summaries

**Files:** `internal/runtime/artifacts.go`, `internal/runtime/requests.go`, `internal/runtime/materialize.go`, `internal/mcpserver/tools.go`; focused tests in their existing test files.

**Interfaces:** `RequiredSpecSection(title string) bool` is shared by approval creation and materialization. It exempts only the exact normalized informational headings in the spec. `AskInput.Prompt` is the exact summary for `approval` on a spec and is limited to 700 Unicode characters.

- [ ] Write tests for numbered/mixed-case heading normalization, unknown/headingless headings, informational section skip, whitespace-only summaries, 700/701 Unicode boundaries, table preservation, and revised hashes.
- [ ] Run focused tests and record RED failures.
- [ ] Add the classifier and summary validation; reject approval requests for informational sections; use the classifier at materialization.
- [ ] Run focused tests to GREEN, then affected package tests.
- [ ] Commit the spec approval change.

## Task 3: Show spec and plan paths at plan approval

**Files:** `internal/runtime/native.go`, `internal/runtime/artifacts.go`, `internal/runtime/native_test.go`.

**Interfaces:** `Request.ReviewPaths` (or equivalent wire field) carries the absolute registered spec and plan paths losslessly on initial ask and replay. `nativePromptFor` keeps a short plan question with the revision, warnings, and ref token. Plan approval creation requires the current spec's required sections to be approved; drafting and registering a plan earlier are allowed.

- [ ] Write tests for relative input paths and legacy relative-path revision identity, plan approval refusal before current spec approval, and a long-path/many-warning relay that preserves both paths fully and keeps the prompt ref token.
- [ ] Run the test and record RED.
- [ ] Normalize artifact paths at registration; expose both paths and the exact submitted summary as review data on initial ask and replay; enforce spec approval before plan approval creation.
- [ ] Run focused tests to GREEN, then affected package tests.
- [ ] Commit the path change.

## Task 4: Align skills and exercise every supported agent route

**Files:** canonical `skills/swarm*` files, installed copies, `internal/hook/*`, `internal/adapter/*`, and test fixtures as needed.

**Interfaces:** Existing ref token and `native_answer` contract remains. Claude, agy, and Codex use observed native answers; Cursor and Muse use board/CLI until an observable hook is proven. Update tool descriptions, `next` copy, request-open relays, and repository refusal copy.

- [ ] Record agent versions and named live probe fixtures where possible. Write/extend tests for all five kinds: question submission, approve, request changes, typed comment, cancellation, and replay/stale ref; distinguish live-probe evidence from fixture evidence.
- [ ] Run those tests and record RED for any new behavior assertion.
- [ ] Update skills and any deterministic hook routing supported by actual event evidence; run `make skills-sync`.
- [ ] Run focused tests to GREEN and compare synced skill copies.
- [ ] Commit the skill and agent-route change.

## Task 5: End-to-end verification and review

**Files:** `scripts/e2e/*` only if the existing scenarios do not cover the new path.

- [ ] Exercise start → selected repo worktree → section summaries/approvals → plan approval with paths → materialize, plus stale revision refusal.
- [ ] Run `go test ./...`, `make skills-sync` (check no drift), and relevant `scripts/e2e` tests.
- [ ] Inspect `git diff --check`, branch status, and the full requirement list in the spec.
- [ ] Resolve review findings and rerun the affected verification before reporting completion.
