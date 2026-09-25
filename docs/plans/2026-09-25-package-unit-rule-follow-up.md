# Package unit rule follow-up

P11's handoff recorded the gap: units 11.3–11.5 needed one coder working sequentially, while the orchestrator skill's generic worker-completion instruction could be read as a review after each unit. This change makes the task the coder assignment, each unit a TDD/commit boundary, and the completed package the review boundary. Workflow review remains daemon-run; legacy tasks keep their manual review path. Review findings return to the same coder for a fix attempt.

The focused install-skill contract test was written first. It failed for all three skills with the expected missing contract phrases, then passed after the canonical skill edits and `make skills-sync`. The test covers the distinct dispatch, package, and coder instructions; the existing mirror test checks that installed copies match the canonical tree. No agent pressure test was run because this work was assigned a one-active-agent limit.

Verification: `go test ./internal/install/...`, `go build ./...`, `go vet ./...`, and `git diff --check` passed. The first `go test ./...` run failed `TestBoardServedAtRoot` because this worktree had no built board assets; `make web-build` produced them, and the isolated HTTP test and full `go test ./...` then passed.

P12 rewrites orchestration guidance. Preserve the one-coder-per-task and package-boundary review contract when replacing the old manual reviewer sentence; keep the legacy no-workflow path only where it remains applicable.
