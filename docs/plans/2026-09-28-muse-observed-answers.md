# Plan: Muse observed answers

Spec: `docs/specs/2026-09-28-muse-observed-answers.md`. Worktree `../agent-swarm-muse-observed`,
branch `feat/muse-observed-answers`. Strict TDD per task; commit explicit paths.

## Task 1: `Muse.ObservedAnswer`
- Tests in `internal/adapter/muse_test.go`: copy both fixtures into a temp
  `<home>/.local/share/muse/sessions/2026/09/28/<id>/session.jsonl`. Cover the no-note,
  note, unknown ref, unsettled (requested line only), and missing file cases.
- Implement. Commit `feat(muse): read settled request_user_input answers from muse's session log`.
  Include the new fixture, the spec and the plan in this commit.

## Task 2: runtime uses it
- Tests in `internal/runtime/native_report_test.go`: a fake adapter implementing `ObservedAnswer`
  registered for Muse. Cover observed approve without answer_text, mismatch refused, and
  fallback to the reported path when no match is found.
- Implement the `nativeAnswer` Muse branch. Commit `feat(runtime): muse native answers verified from its session log`.

## Task 3: copy and skills
- Update the pinned-copy tests first, then `NativePromptNextStep` and both SKILL.md copies.
- Commit `docs(skills): muse answers are checked against its session log`.

## Final
`gofmt -l .`, `go vet ./...`, `go test ./...`.
