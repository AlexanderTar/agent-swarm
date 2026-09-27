# Approval simplification — revised implementation plan

**Status:** Design only. Do not implement this revision until the user approves this plan.

**Spec:** `/Users/alexandertar/GitHub/agent-swarm-approval-simplification/docs/specs/2026-09-27-approval-simplification.md`

**Worktree:** `/Users/alexandertar/GitHub/agent-swarm-approval-simplification` on `feat/approval-simplification`.

**Goal:** Remove repository scope gates, review consequential spec sections with summaries up to 1000 characters, and support native-question approvals for all five agent kinds.

**Method:** Use superpowers TDD for each behavior change: add a failing behavior test, run it to RED, implement the smallest change, then run the focused suite to GREEN. Update canonical skills and sync installed copies. Preserve request ownership, revision binding, and audit provenance.

## Task 1 — Repository choice is a hint

**Files:** `internal/mcpserver/orchestrator.go`, `internal/runtime/materialize.go`, `internal/runtime/confirm.go`, `cmd/swarm/daemon.go`, relevant tests in those packages, and `scripts/e2e/reposconfirm_test.go`.

1. Add failing tests: an orchestrator creates/reviews a worktree in a catalog repository absent from the starting hint; a valid local Git path can be registered and used; a non-repository path receives a clear error. Cover a plan whose task uses a registered repository absent from the hint, and verify the created task stores that repository id. Neither path may require `swarm_repos`.
2. Add failing migration tests with one and multiple historical open `confirm_repos` requests, proposed and expansion entries, dropped entries, a preexisting root hint, repeated startup, and unrelated open approvals. Assert every historical confirmation becomes approved once, keeps its effective repository list, emits resolution/relay events with daemon-migration provenance, and never impersonates a user action. Assert new `confirm_repos` asks are refused.
3. Remove the worktree subset check and materialization subset check. Resolve task repositories against the catalog directly. Add a repository registration entry point independent of worktree creation; allow `swarm_worktree` to accept a catalog id or local Git path. Retain validation that a path is a real repository and that a task repository name resolves unambiguously. Keep `swarm_repos` optional.
4. Run focused runtime, MCP, daemon, and end-to-end repository tests to GREEN. Reconcile any old tests whose only expected behavior was the removed scope gate.

## Task 2 — Review the right spec sections

**Files:** `internal/runtime/artifacts.go`, `internal/runtime/requests.go`, `internal/runtime/materialize.go`, corresponding tests, and MCP tool descriptions.

1. Add failing tests proving `Context`, `Background`, `Bibliography`, `References`, `File list`, `Files`, and `Work breakdown` are skipped after heading normalization; `Out of scope`, `Explicitly out of scope`, unknown headings, and headingless content require approval. Check that request creation and materialization agree.
2. Add failing tests for whitespace-only summaries, exactly 1000 and 1001 Unicode characters, exact preservation of a Markdown table or ASCII sketch, and a revised section hash that requires renewed approval.
3. Change the shared classifier and summary validator, then run focused tests to GREEN. Keep the plan approval prerequisite tied to the current required spec sections.

## Task 3 — Native MCP reporting for Cursor and Muse

**Files:** `internal/runtime/native.go`, `internal/runtime/requests.go`, `internal/mcpserver/tools.go`, request audit/relay code, runtime and MCP tests, and native-tool adapter fixtures.

1. Define a request field such as `answer_text` for `swarm_ask kind:"native_answer"`. Use it only for Cursor and Muse when the native question tool returns but no answer hook is available. Require nonempty exact returned text, a daemon-issued ref, and an explicit `decision`. Document the accepted tool-return shapes for both agents; do not infer a decision from chat text alone.
2. Add failing tests for both agent kinds: approve, request changes, typed comment, cancellation without submission, empty answer, mismatched answer/decision, another agent's ref, stale artifact revision, and duplicate/replayed ref. Cover both request refs and child approval-message refs. Assert successful rows and relays carry `agent_reported`, while hook-observed routes retain their current evidence semantics.
3. Implement the narrowly scoped MCP-report route. Reuse request ownership and approval checks; bypass only the requirement for a hook-created answered-question row for Cursor and Muse when `answer_text` is provided. Store the reported text and provenance in the audit trail. A missing, cancelled, or contradictory answer never resolves approval. Keep board/CLI as a fallback.
4. Test the native question contract for Claude, agy, Codex, Cursor, and Muse. Record installed version, tool name, fixture/live source, answer shape, cancellation behavior, and replay behavior. Use a fresh interactive probe when available; report any kind that could only be verified by recorded fixture or simulated return.

## Task 4 — Skills and user-facing copy

**Files:** `skills/swarm/SKILL.md`, `skills/swarm-orchestrator/SKILL.md`, `skills/swarm-spike/SKILL.md`, `internal/install/skills/*`, MCP descriptions, and request `next`/replay copy.

1. State that starting repositories are hints and that agents may register/use any real Git repository without scope approval. Remove instructions to seek `confirm_repos` or update scope before work.
2. Tell agents to request approval only for the seven exempt-heading complement, including both out-of-scope headings; print the exact 1–1000-character summary before each native question. Before plan approval, print the full absolute spec and plan paths from `review_paths`.
3. Give Cursor and Muse explicit native-tool → MCP `native_answer` instructions, including exact returned answer text, no submission on cancellation, and `agent_reported` audit provenance. Preserve the hook-backed instructions for Claude, agy, and Codex.
4. Run `make skills-sync` and compare canonical/installed copies.

## Task 5 — End-to-end verification and review

1. Exercise start with one repository hint → discover/register another Git repository → create its worktree → approve an out-of-scope spec section with a 1000-character visual summary → approve the plan after seeing both full paths → materialize a task in the newly discovered repository.
2. Exercise startup with historical confirmations and verify automatic resolution, no new confirmation dialog, and no change to unrelated approvals. Exercise Cursor/Muse reported native decisions and stale/cancelled cases through the MCP surface.
3. Run `go test ./...`, the native route fixture suite, `make e2e`, `make skills-sync`, and `git diff --check`. Review the final diff against every acceptance item in the spec.
4. Request an Astra review of the updated implementation and apply findings. Record which native results were live, fixture-based, or simulated. Commit the verified implementation on the worktree branch; do not merge or deploy without separate authorization.

## Decisions already fixed by the user

- Repository selection at start is only a hint. No worktree or materialization scope gate remains.
- Both out-of-scope headings require section approval; the other seven named headings do not.
- Section summaries may contain up to 1000 Unicode characters.
- Historical open repository confirmations are automatically approved; new ones are refused.
- Cursor and Muse ask through native tools and forward returned answers through MCP, marked as agent-reported evidence.
