# Plan: auto-approve spec sections with nothing to review

Spec: `docs/specs/2026-09-28-empty-section-auto-approve.md`. Worktree: `/Users/alexandertar/GitHub/agent-swarm-empty-sections`, branch `feat/empty-section-auto-approve`. Live DB schema version: 19.

Every task: failing test first, run it and confirm red, minimal implementation, run and confirm green, commit that task's diff alone (`git add <exact files>`, never `-A`/`--amend`).

## Task 1 — migration 0020: allow `responded_via = 'auto'`

File: `internal/db/schema/0020_responded_via_auto.sql`, following `0019_summary_2000.sql`'s exact table-rebuild pattern (temp table, copy, drop, rename, recreate the two indexes) — only the `responded_via` CHECK widens from `('menubar','board','cli','terminal')` to `('menubar','board','cli','terminal','auto')`. Test file: `internal/db/schema_0020_responded_via_auto_test.go`.

1. Write `TestRespondedViaAutoMigrationAllowsAutoOnceMigrated`: `openFixtureAtVersion(t, 19)`, insert an `items` row, then insert a `requests` row with `responded_via = 'auto'` — expect it to fail (CHECK rejects `'auto'` pre-migration). `continueMigratingTo(t, raw, 19, 20)`. Insert the same row again — expect success; read back `responded_via` and assert `'auto'`. Then attempt an unrelated bad value (`'bogus'`) — expect it still fails post-migration (CHECK still closed-world).
2. Run `go test ./internal/db/... -run RespondedViaAuto` — confirm it fails (no migration file yet: version stays 19, or the fixture helper errors).
3. Write `0020_responded_via_auto.sql` copying 0019's rebuild verbatim except the CHECK list and table/temp names.
4. Write `TestMigration0020PreservesRequestsAndReferencingRows`, mirroring `TestMigration0019PreservesRequestsAndReferencingRows`: insert a requests row (any existing `responded_via`) plus a referencing `messages` and `notifications` row at version 19, snapshot, migrate 19→20, assert the requests row is byte-identical and `foreign_key_check` reports 0 violations.
5. Run `go test ./internal/db/...` — confirm both pass.
6. Commit: `internal/db/schema/0020_responded_via_auto.sql`, `internal/db/schema_0020_responded_via_auto_test.go`.

## Task 2 — `nothing_to_review` refused on non-spec-section kinds

File: `internal/runtime/requests.go` (`AskInput`, `askApproval`). Test: `internal/runtime/requests_test.go`.

1. Add `NothingToReview string` to `AskInput` (grouped with `SectionID`, doc comment citing locked decision 1).
2. Write `TestAskApprovalNothingToReviewRefusedOnPlan`: register a plan artifact, call `s.Ask(ctx, ses.ID, AskInput{Kind:"approval", Artifact: planArtifactID, Prompt: "...", NothingToReview: "No new endpoints."})`; assert `items.CodeBadRequest` and message `"nothing_to_review is only for spec sections."`. Confirm it fails to compile/red (field doesn't exist / check doesn't exist).
3. In `askApproval`, once `reqKind` is known, refuse when `in.NothingToReview != "" && reqKind != "approve_section"` with that exact copy.
4. Run the test green.
5. Commit: `internal/runtime/requests.go`, `internal/runtime/requests_test.go`.

## Task 3 — length validation on the reason itself

1. Write `TestAskApprovalNothingToReviewLengthValidated` (table test: 2-rune reason refused, 201-rune reason refused, 3-rune and 200-rune accepted through to the length-of-section-body check).
2. Add the 3–200 rune check in `askApproval` right after the existing prompt-length checks, before the transaction (`&items.Error{Code: items.CodeBadRequest, Message: "nothing_to_review must be 3–200 characters."}`).
3. Green. Commit alongside Task 2's files (same two files; separate commit if Task 2 already landed).

## Task 4 — refuse when the section has real content

1. Write `TestAskApprovalNothingToReviewRefusedWhenSectionHasContent`: register a spec whose "DB models" section body is >300 runes after stripping the heading; call `Ask` with `NothingToReview` set; assert refusal `"This section has content to review (%d characters); ask for approval normally."` with the exact rune count of the trimmed body (no heading line), and that no `requests` row was inserted (`SELECT COUNT(*) FROM requests WHERE artifact_id = ?` unchanged).
2. In `askApproval`'s section-lookup block (`if in.SectionID != ""`), when `in.NothingToReview != ""`: look up the matched section's `Start`/`End` (need the section's own `ArtifactSection` value, not just `SHA256`/`Title` — capture the whole struct in the existing loop), query `artifact_revisions.content` for the head revision, slice `content[sec.Start:sec.End]`, drop everything through the first `\n` (the heading line), `strings.TrimSpace` the remainder, and `utf8.RuneCountInString` it. If `> 300`, refuse with the exact copy above.
3. Green. Commit: `internal/runtime/requests.go`, `internal/runtime/requests_test.go`.

## Task 5 — the auto-approve path itself

1. Write `TestAskApprovalNothingToReviewAutoApproves`: register a spec with a "DB models" section body `"None."` (well under 300 runes after the heading strips); call `Ask` with `NothingToReview: "No new tables, columns or migrations."`. Assert: returned `Request.State == "approved"`, `RespondedVia == "auto"`, no error. Read the row directly: `binding_json` has `evidence:"auto_empty"` and `nothing_to_review` equal to the reason. Assert exactly one `messages` row was enqueued to the calling agent with `kind = 'approval_result'` and payload `decision:"approved"`, matching `section_id`/`section_sha256`. Assert **no** `requests` row of `kind = 'question'` exists (no native question issued) and no `request.approve_section` notification was raised (check `s.Notify`'s captured calls / `notifications` table has no such row for this artifact). Assert the returned `Request.Next` (in-process field, mirroring `NativePrompt`) equals the exact copy: `Print this line in chat: Section "DB models": nothing to review (No new tables, columns or migrations.) — auto-approved. Then continue with the next section.`
2. Add `Next string` to the `Request` struct in `internal/runtime/model.go`, next to `NativePrompt`/`ReviewPaths`, with a doc comment matching their "never persisted" note.
3. Add `autoApproveSectionTx` to `internal/runtime/requests.go`: inserts the `requests` row already `state = 'approved'`, `responded_via = 'auto'`, `binding_json = {"evidence":"auto_empty","nothing_to_review":<reason>}`; enqueues the `approval_result` message (`Origin: "daemon"`, same payload shape as `Approve`'s `resolve()` build func: `decision`, `section_id`, `section_sha256`); appends `events.RequestResolved` via `RequestWireTx`; calls `s.Items.ReconcileTx`; reads the row back via `requestTx`; sets `.Next` to the copy above. Wire it into `askApproval`: when `in.NothingToReview != ""`, call this instead of the existing open-row insert + `finishOpen` + `nativePromptFor` + `freezeNativeQuestionTx` sequence.
4. Green. Commit: `internal/runtime/model.go`, `internal/runtime/requests.go`, `internal/runtime/requests_test.go`.

## Task 6 — rollup and stale-revision scenarios

1. Write `TestSpecApprovedWhenAllSectionsIncludingAutoApproved`: a spec with two required sections, one approved normally (`Approve`), one auto-approved (`NothingToReview`); call whatever currently exercises `checkEverySectionApproved` (e.g. attempt `approve_plan` ask, or call `s.checkEverySectionApproved` directly in-package) and assert no `approval_missing` error.
2. Write `TestAutoApprovedSectionRevisedRequiresNormalApproval`: auto-approve a section, then re-register the artifact with that section's body changed (still short, to isolate "must re-ask" from the length refusal), assert `checkEverySectionApproved` now fails (`approval_missing`) because the new revision's `section_sha256` no longer matches the auto-approved row's stored hash — this should pass with **no production code change** (the existing hash-match check already covers it); if it doesn't, that is a real gap to fix in `checkEverySectionApproved`/`staleApprovals`, not a discovered pre-existing bug to paper over.
3. Green (expect both to pass against Task 5's implementation with no new code, per the spec's existing SHA-match mechanism — confirm before writing any fix).
4. Commit only the test file if no production change was needed: `internal/runtime/requests_test.go`.

## Task 7 — `ResolveQuestionReply` bug fix

File: `internal/runtime/requests.go`. Test: `internal/runtime/requests_test.go`.

1. Write `TestResolveQuestionReplyUsesAlreadyBoundRowNotFreshRebind`: build on the `worker`/`spawnSecondChild` helpers (`native_test.go`) — an orchestrator with two children, both `SendApproval`ing the byte-identical body `"may I drop table x?"` (`msgA`, `msgB`). Call `s.askQuestion(ctx, orchSes.ID, AskInput{Prompt: "may I drop table x?", Options: []string{"Approve","Request changes"}, Header: childA.Name + " asks"})` directly (simulating the PreToolUse hook, which has the real header) — this freezes `rowA`'s `binding_json.ref = msgA`. Then call `s.ResolveQuestionReply(ctx, orchSes.ID, "may I drop table x?", "Approve")` (simulating Codex's async reply, no header) and assert the resolved request's `ID == rowA.ID`, `State == "answered"`, `RespondedVia == "terminal"`. Confirm this is currently red: the old code re-binds fresh with `header=""`, which (per `TestBindNativeQuestionHeaderDisambiguatesIdenticalChildBodies`) picks the *newest* candidate (`msgB`) and finds no open question row bound to it, returning the zero `Request`.
2. Rewrite `ResolveQuestionReply`: keep the `sessionID → agentID` lookup and the `refFromPrompt` fast path (look up the agent's open `question` rows with `binding_json.ref` equal to the extracted token — unchanged behaviour, still covers `TestResolveQuestionReplyBindsByRefThenByPrompt`'s ref-token cases). Replace the no-ref-token branch: instead of calling `BindNativeQuestion` (which re-searches and can rebind to the wrong candidate), query the agent's open `kind = 'question'` rows whose `binding_json.ref IS NOT NULL`, newest first, and pick the first whose `NormalizeQuestion(prompt)` equals `NormalizeQuestion(question)`. If none matches, fall through to `ResolveQuestionByPrompt` exactly as today (covers the plain, never-bound "Pick a color" case).
3. Run `go test ./internal/runtime/... -run ResolveQuestionReply` — confirm both the new test and `TestResolveQuestionReplyBindsByRefThenByPrompt` pass.
4. Commit: `internal/runtime/requests.go`, `internal/runtime/requests_test.go`.

## Task 8 — `swarm_ask` schema and mcpserver wiring

Files: `internal/mcpserver/tools.go`, `internal/mcpserver/tools_test.go`.

1. Write `TestAskToolForwardsNothingToReview`: call the `swarm_ask` tool handler directly (same harness pattern as `TestAskRequiresArtifactForApproval`) with `nothing_to_review` set on a short spec section; assert the JSON result has `state:"approved"` and a `next` string containing `auto-approved`. Confirm red (schema/handler don't forward the field).
2. Add `"nothing_to_review":{"type":"string","description":"Spec sections only: a one-line reason there is nothing for the user to review (e.g. \"No DB changes: no tables, columns or migrations.\"). The section body must be at most 300 characters."}` to `askTool`'s schema (exact copy from the spec's "User-facing copy" section), add `NothingToReview string \`json:"nothing_to_review"\`` to the handler's input struct, thread it into `runtime.AskInput{...NothingToReview: in.NothingToReview}`. In `requestOut`, add `if r.Next != "" { out["next"] = r.Next }` alongside the existing `NativePrompt`/`ReviewPaths` branches.
3. Green. Commit: `internal/mcpserver/tools.go`, `internal/mcpserver/tools_test.go`.

## Task 9 — skills copy

Files: `skills/swarm-orchestrator/SKILL.md`, `internal/install/skills/swarm-orchestrator/SKILL.md`, `skills/swarm-spike/SKILL.md`, `internal/install/skills/swarm-spike/SKILL.md`.

No test to write first (prose, not code) — `internal/install/skills_test.go` already asserts `skills/` and `internal/install/skills/` are byte-identical, so that test is this task's regression guard; run it before and after.

1. In both `swarm-orchestrator/SKILL.md` copies, add a new bullet immediately after the "Register the spec with `swarm_artifact register`..." bullet:
   `- A spec section with nothing for the user to review (e.g. "DB models: none") is asked with nothing_to_review: "<one-line reason>" instead of a native question; print the line the result gives you. Never use it to skip a section that has content.`
2. In both `swarm-spike/SKILL.md` copies, add the identical line (same bullet text, byte-for-byte) as a new standalone bullet after numbered step 7, before the "Bad:" example.
3. Run `go test ./internal/install/...` to confirm the sync test still passes.
4. Commit all four files together.

## Task 10 — full verification

1. `gofmt -l .` — empty output.
2. `go vet ./...` — clean.
3. `go test ./...` — clean.
4. Report SHAs of every commit made above.
