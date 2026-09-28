# Plan: Codex sync request_user_input

Spec: `docs/specs/2026-09-28-codex-sync-request-user-input.md`. Worktree:
`../agent-swarm-codex-sync-ask`, branch `feat/codex-sync-request-user-input`.
Strict TDD per task: failing test, watch fail, implement, pass, commit (explicit paths).

## Task 1: Codex answer parsing
- Test (`internal/hook/codex_native_test.go`): replay `testdata/codex/native-question-sync/PreToolUse-approval.json`
  (question with ref `req_01PROBE0000000000000000001`) against an open approval row bound to that ref,
  then `PostToolUse-request-changes-note.json`. Expect row answered with
  `response_text = "Request changes: use German instead"` and hook context naming
  `decision:"request_changes"`. Unit test `codexAnswerText` for: French fixture → "French";
  `["Approve (Recommended)"]` → "Approve"; non-codex JSON string → not ok.
- Implement `codexAnswerText` and call it first in `extractToolResponseText`'s string branch.
- Commit `feat(hook): read codex request_user_input answers from PostToolUse`.

## Task 2: Launch override
- Test (`internal/adapter/codex_test.go`): Launch and Resume argv contain
  `-c`, `features.default_mode_request_user_input=true`.
- Implement in `internal/adapter/codex.go` argv builder.
- Commit `feat(codex): enable synchronous request_user_input in Default mode`.

## Task 3: Copy and skills
- Update tests pinning `NativePromptNextStep` text and skill text first, then copy per spec.
- Keep `skills/` and `internal/install/skills/` copies identical (existing test enforces).
- Commit `docs(skills): codex asks with request_user_input`.

## Task 4: Document overrides and env vars
- Doc comment above each adapter's argv/env builder (claude, codex, agy, cursor, muse) listing
  every flag, `-c`/config override and env var, one line each with the reason.
- Muse: record the 1.4.0 ingress-gate and session.jsonl findings; update `TestMuseWakeProbe` comment.
- Commit `docs(adapter): document every launch override and env var`.

## Final
`gofmt -l .`, `go vet ./...`, `go test ./...`. Report SHAs.
