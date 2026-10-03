# CHORE-27 plan: fix BUG-13..BUG-25

Spec: `docs/specs/2026-10-03-chore-27-open-bugs.md`. Three work packages, each one
Swarm task with its own worktree branched from `chore-27-open-bugs`, run in
parallel, merged A → B → C into the integration branch. Every unit is strict
TDD: failing test, watch it fail, minimal fix, watch it pass, one commit.

## Package A — checkpoint gates (tdd-reviewed)

1. **Short sha in commit gate (BUG-14/15/19)** — `internal/runtime/checkpoint.go` `commitGate`.
   Test: completed with 7-char prefix of HEAD passes and stores full sha; a
   non-matching sha fails with the full-sha message. Update the existing
   assertion at `checkpoint_test.go` (~line 1940) to the new copy.
2. **required_artifact gate only for orchestrator (BUG-20/21)** — `checkpoint.go` ~1561.
   Test: reviewer `completed` on a bug root with no debug_report is accepted;
   orchestrator still refused.
3. **Child blocked doesn't strand root (BUG-22)** — find where `blocked`
   checkpoints move the item. Tests: reviewer blocked → completed restores the
   root status; `integrated` on a Blocked root opens `accept_fix`.

Verify: `go test ./internal/runtime/... ./internal/items/...`

## Package B — workflow engine & messaging (debug)

1. **Effective max in brief (BUG-17)** — `internal/workflow/render.go` uses
   `loopMaxRounds + extraRounds`. Test renders round 4 of at most 4 with one extra.
2. **Resume retry carries findings + note (BUG-23)** — `internal/runtime/workflow.go`
   resume path. Test: after escalation, `resume retry` with a note gives the
   fix agent a brief containing the findings and the note. Align
   `skills/swarm-orchestrator/SKILL.md` text if behaviour differs; `make skills-sync`.
3. **Full message body retrievable (BUG-16)** — root-cause where a ~1,100-char
   `swarm_send` body is cut (send handler, store, sync payload, or `swarm_read`
   msg refs). Test: send 1,500-char finding, recipient `swarm_sync` returns it whole.

Verify: `go test ./internal/workflow/... ./internal/runtime/... ./internal/mcpserver/...`

## Package C — isolated contract fixes (tdd-reviewed)

1. **AGY PostToolUse output (BUG-13)** — `internal/adapter/agy.go` `HookOutput`
   returns `{}` for PostToolUse context; PreInvocation keeps `injectSteps`.
2. **Chores may propose (BUG-18)** — remove refusal at `internal/items/store.go:472`,
   update store test, update Chores section of `skills/swarm-orchestrator/SKILL.md`, `make skills-sync`.
3. **Claude transcript slug (BUG-24)** — `internal/advisor/transcript.go` `encodeCwd`
   for Claude maps every non-alphanumeric to `-`.
4. **create honours blocked_by (BUG-25)** — `internal/mcpserver/orchestrator.go`
   create branch calls the link path's `AddDepTx` in the same tx.

Verify: `go test ./internal/adapter/... ./internal/items/... ./internal/advisor/... ./internal/mcpserver/... ./internal/install/...`

## Integration

Merge A, B, C; then `make skills-sync && git diff --exit-code internal/install/skills`,
`go vet ./...`, `go test ./...`; final reviewer on the integrated sha.
