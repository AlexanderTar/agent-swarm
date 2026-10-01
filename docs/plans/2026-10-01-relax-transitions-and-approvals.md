# Plan: relax Swarm transitions and approvals (CHORE-18)

Spec: `docs/specs/2026-10-01-relax-transitions-and-approvals.md`. Every unit is
strict TDD: write the failing test, run it and record red (with `"unit": n`),
implement, run and record green, commit. One coder per package.

Order (serial, shared files): B first, then A, then C. Each branches off the
integration branch after the previous package merges.

## Package A — Free-form finish options (backend + skill)

Branch `chore-18-finish-options`, migration `0025`. Workflow `tdd-reviewed`.

1. **Integrated stores finish options.** Test in `internal/runtime/finish_question_test.go`:
   integrated with `finish_options` → accept binding has them; invalid shapes
   refused with spec copy. Implement in `checkpoint.go` + `transition.go`
   (`acceptBinding.FinishOptions`); persist the options on the integrated checkpoint
   in new column `checkpoints.finish_options_json` (migration 0025) and copy them into
   the accept binding when `reconcileRoot` opens the request.
2. **Prompt and native answer (finish + approval kinds).** Tests: `finishPrompt` with
   options returns labels + "Request changes"; `swarm_ask kind:approval` with
   `[{label,description}]` options stores them and its native prompt uses them;
   `native_answer decision:"approve", choice` maps to label; wrong choice refused. Implement in `native.go` (`finishDecisions`,
   `decisionLabels`, `finishDecisionFor`) and `mcpserver/tools.go` schema.
3. **Approve with choice + wire.** Tests in `requests_test.go`/httpapi: approve body
   `{merge:"custom", choice, comment}` sets `$.merge`/`$.choice`; approval kinds
   record `$.choice`; `approval_result` carries both; missing/unknown choice refused;
   `RequestWireTx` returns `options` + `option_descriptions` + `choice`. Implement
   `Approve` (explicit `custom` branch beside auto/manual/local), `approveBody`.
4. **Custom finishing.** Tests in `finish_test.go`/`merges_test.go`: prs+kept →
   InReview until PR merged then Done; kept-only → Done; missing repo refused.
   Implement migration `0025_finish_options.sql` (kept kind) and `writeFinishing`.
5. **Skill text.** Update `skills/swarm-orchestrator/SKILL.md` finish section: derive
   1–4 options from `git log --first-parent`, `gh pr list --state merged`, repo merge
   settings and previous items' finishing; pass them on `integrated`; finish exactly
   as the chosen option says, report `prs`/`merged`/`kept`. Run `make skills-sync`.

Verify: `go test ./internal/runtime/... ./internal/items/... ./internal/mcpserver/... ./internal/httpapi/...`, `go vet ./...`.

## Package B — Orchestrator waivers and overrides (backend + skill)

Branch `chore-18-waivers-overrides`, migration `0024`. Workflow `tdd-reviewed`.

1. **Item columns and wire.** Test in `internal/items/store_test.go`: waivers and
   override round-trip. Migration `0024_item_overrides.sql`, `model.go`, `store.go`.
2. **Waive via swarm_items.** Tests: orchestrator adds/removes a waiver; worker
   refused; unknown gate and empty reason refused; outside tree refused; event
   `item.waived`. Implement in `items/store.go` update path and `mcpserver/tools.go`.
3. **Gates honour waivers.** Tests in `runtime/checkpoint_test.go`/`workflow_test.go`:
   waived tdd/verify/commit lets a worker's completed pass; open_questions and
   required_artifact waivers; integrated `waive` and root waivers skip
   integration_verify/final_review; refusal copy gains the waiver hint.
4. **Overrides.** Tests in `items/transition_test.go`: orchestrator with
   `override_reason` forces task Done (workflow cancelled), story Done survives
   `ReconcileTx`, later normal transition clears `override_json`, root override refused,
   reopen Done → Ready. Implement in `transition.go` (`check` override branch,
   `deriveStory` respect) and `swarm_items` `override_reason`.
5. **Disclosure + skill text.** Test: finish native prompt appends the waiver/override
   count. Update `skills/swarm-orchestrator/SKILL.md` and `skills/swarm/SKILL.md`
   (when to waive/override, never instead of asking the user). `make skills-sync`.

Verify: same as A plus `go test ./...`.

## Package C — Web: agent options, comments, waiver/override display

Branch `chore-18-web-options` off the integration branch after A and B merge.
Workflow `ui-tdd-reviewed`.

1. **Types and copy.** `types.ts` (`option_descriptions`, `choice`, `waivers`,
   `override`, merge `"custom"`), `copy.ts` strings from the spec; `types.test.ts`,
   `copy.test.ts`.
2. **Finish buttons from options.** `Review.test.tsx`: request with options renders
   one button per label with description, posts `{merge:"custom", choice, comment}`;
   no options → old buttons. Implement `Review.tsx`, `logic/review.ts`, `api.ts`.
3. **Approval options for section/plan/report.** Same rendering for approval kinds
   with options; optional comment textarea on every approval.
4. **Waiver/override rows.** Item detail shows waiver and override rows; finish
   request shows the tree count banner.

Verify: `cd web && npm test && npm run build`, then `go test ./web/...`.

## Integration

Merge A, B, then C into `chore-18-relax-transitions`; run `go vet ./...`,
`go test ./...`, `cd web && npm test`; final review by `reviewer`.
