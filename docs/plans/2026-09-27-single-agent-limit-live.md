# One Agent Limit, Applied Live: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove every agent limit except `max_concurrent_agents`, and make that limit apply live. The reconciler pauses the newest eligible agents when there are too many, and resumes paused agents before it starts new queued spawns.

**Architecture:**

- The subagent budget (hook block, workflow gate, `SubagentSlots`) and the per-root check in `Admit` are deleted. The retired settings rows are removed in `Settings.Put`, with no migration.
- A new `Store.EnforceCapacity` runs each reconcile tick. It hands off excess agents through the existing replacement coordinator (`RequestReplacement`, `ModeHandoff`, request key `capacity:<session id>`).
- The coordinator's `queued` phase plus `admitOperation` already resume an agent when a slot frees. `ResumeOperations` moves ahead of `DrainQueue` and orders by `created_at`.
- A manual `Resume` that finds the pool full records a `resume:<session id>` handoff instead of exceeding the limit.
- The request-key prefix becomes `reason` on the replacement wire, which the menubar and board render.

**Tech Stack:** Go (daemon, SQLite), Swift/SwiftUI (menubar, XCTest), TypeScript/React (web, vitest).

**Spec:** `docs/specs/2026-09-27-single-agent-limit-live.md`. Read it first; decisions 1-12 there are locked.

## Global Constraints

- Work only in `/Users/alexandertar/GitHub/agent-swarm-cap-apply` on branch
  `feat/cap-change-applies`. Do not touch the primary checkout, `~/.swarm`, the
  live daemon, launchd, `tmux -L swarm`, `~/.claude*` or `~/.codex`.
- Never run `git add -A`. Stage explicit paths. Never `--amend`.
- Every commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never delete a test to make it pass. Port it. Only these four tests may be
  deleted (the behaviour is intentionally removed):
  - `TestSubagentSlotsMatchesHookCount`
  - `TestNoAckChildren`
  - `TestPreToolUseSurfacesNoAckChildrenInBudgetBlockReason`
  - `TestPreToolUseSurfacesNoAckChildAfterResumeWithZeroNewCheckpoints`
- Do not edit these files, which other branches own:
  - `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift` and the other
    new-orchestrator-dialog files;
  - `internal/runtime/wake.go`;
  - `cmd/swarm/daemon.go` (`checkQuotaResets`);
  - `internal/adapter/codex.go`.
- No new DB migration file (spec decision 2).
- Copy is verbatim:
  - `Paused: over the agent limit`
  - `Queued: waiting for a free slot`
  - `{n} agents will pause and resume when a slot frees.`
  - notification `agent.capacity_paused`: level `info`, title `Agent paused`,
    body `{name} paused to fit the agent limit. It resumes when a slot frees.`,
    category `swarm.info`
- Request-key prefixes are exactly `capacity:` and `resume:`, followed by the
  agent's latest session id. The operation note is always `""`.
- After editing `skills/*`, run `make skills-sync` and commit the synced
  `internal/install/skills` files.
- Quote grep globs under zsh (`--include='*.go'`).

## File structure

| File | Responsibility |
|---|---|
| `internal/runtime/capacity.go` (new) | `OperationReason`, `EnforceCapacity`, `admitsNow`, and the three capacity SQL constants |
| `internal/runtime/capacity_test.go` (new) | Every capacity scenario in the spec's Verification section, plus the manual-resume scenarios |
| `internal/runtime/limits.go` | `Admit` (one pool) and `slotHoldersSQL`; `SubagentSlots` and `NoAckChildren` deleted |
| `internal/runtime/replacement.go` | `ResumeOperations` order; `stopPredecessor` and `startSuccessor` reason handling |
| `internal/runtime/pause.go` | `Resume` queues when full |
| `internal/runtime/reconcile.go` | Tick order |
| `internal/runtime/workflow.go` | `fillWaitingRuns` with no budget; `olderWaitingRunElsewhere` deleted |
| `internal/hook/handler.go` | Budget block deleted |
| `internal/settings/settings.go` | Two fields removed; `retiredKeys` cleanup in `Put` |
| `internal/httpapi/handoff.go` | `Reason` on the wire |
| `internal/notifyrules/notifyrules.go` | New rule |
| Menubar `Wire.swift`, `SettingsModel.swift`, `Copy.swift`, `AgentActions.swift` | Wire, model, copy and row status |
| Web `types.ts`, `copy.ts`, `logic/agentActions.ts`, `mock/fixtures.ts` | Types, labels, display states |

Two build batches:

- **Batch 1 (daemon):** Tasks 1-6. Gate: `go test -race ./...`.
- **Batch 2 (skills + clients):** Tasks 7-9. Gate: web and menubar suites,
  then the removal grep.

---

## Batch 1: daemon

### Task 1: Delete the subagent budget (hook, workflow gate, SubagentSlots, NoAckChildren)

**Files:**
- Modify: `internal/hook/handler.go:659-697` (delete the `swarm_spawn` budget block)
- Modify: `internal/runtime/limits.go` (delete `NoAckChildren` at `:74-114` and `SubagentSlots` at `:395-415`; fix the `NotAZombieSlot` comment at `:63-68`)
- Modify: `internal/runtime/workflow.go:828-878` (`fillWaitingRuns`), `:1536-1557` (delete `olderWaitingRunElsewhere`), and the budget comments at `:4`, `:504-508`, `:536-540`, `:660-663`, `:1511-1519`, `:1562-1566`
- Test: `internal/hook/handler_test.go`, `internal/runtime/workflow_test.go`, `internal/runtime/limits_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `fillWaitingRuns(ctx, workflowID string) error`, which now spawns every waiting run. No `SubagentSlots`, `NoAckChildren` or `olderWaitingRunElsewhere` symbol remains.

- [ ] **Step 1: Port the hook test to the new contract (failing).**

In `internal/hook/handler_test.go`:

1. Rename `TestPreToolUseBlocksSubagentsWhenBudgetExceeded` to
   `TestPreToolUseNeverBudgetBlocksSwarmSpawn`.
2. Delete the `wantReason` variable and each case's `verifyDeny` field and
   func.
3. Replace the block from `// Insert 3 active children` to the end of the
   `t.Run` body with the code below.
4. Change the child-insert loop bound from 3 to 5 (above the old default
   budget of 3).

```go
			// Five active children: far past the removed default budget of 3.
			for i := 1; i <= 5; i++ {
				_, err := h.DB.ExecContext(ctx, `INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, parent_agent_id, brief, state, created_at)
					VALUES (?, ?, ?, 'model', 'coder', 'itm_1', 'itm_1', 'agt_1', '', 'active', 1)`,
					fmt.Sprintf("%s_child_%d", tc.kind, i), fmt.Sprintf("child-%s-%d", tc.kind, i), string(tc.kind))
				if err != nil {
					t.Fatal(err)
				}
			}
			// There is one limit (max_concurrent_agents), enforced by Admit and
			// the reconciler, never by the hook: every spawn tool stays allowed.
			for _, tool := range tc.tools {
				out, err := h.Handle(ctx, tc.kind, "PreToolUse", ses, tool.stdin)
				if err != nil {
					t.Fatalf("%s: %v", tool.name, err)
				}
				if len(out) != 0 {
					t.Fatalf("%s: swarm_spawn must never be budget-blocked, got %s", tool.name, out)
				}
			}
		})
```

Keep the struct field `tools` and the `cases` table (drop only `verifyDeny`).
Delete these two tests, whose behaviour is removed:

- `TestPreToolUseSurfacesNoAckChildrenInBudgetBlockReason` (`:558-631`
  including its doc comment)
- `TestPreToolUseSurfacesNoAckChildAfterResumeWithZeroNewCheckpoints`
  (`:625-688` including its doc comment)

- [ ] **Step 2: Port the workflow tests (failing).**

In `internal/runtime/workflow_test.go`:

1. Delete `setMaxSubagents` (`:773-785`).
2. `TestReplayAdvanceAttachesReviewWorktree`: delete the line
   `setMaxSubagents(t, s, 1) // keep reviewer waiting behind the builder slot`.
3. `TestSlotReleaseSpawnsWaitingRun`: replace `setMaxSubagents(t, s, 1)` with
   `setLimits(t, s, 2, 8) // the orchestrator plus one worker` (Task 2 drops the
   second argument). Replace the "want waiting/empty" assertion block with the
   code below, and at the end keep the "after the slot freed" state/agent read
   but assert the agent's state.

```go
	var state, agentID string
	if err := s.DB.QueryRowContext(ctx, `SELECT state, COALESCE(agent_id, '')
		FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build'`, st2.ID).Scan(&state, &agentID); err != nil {
		t.Fatal(err)
	}
	// No subagent budget: the run spawns at once and its agent waits in the
	// global queue, because the orchestrator and TASK-1's coder hold both slots.
	if state != "active" || agentID == "" {
		t.Fatalf("TASK-2 build run = state %s agent %q, want active with an agent", state, agentID)
	}
	var agentState string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM agents WHERE id = ?`, agentID).Scan(&agentState); err != nil {
		t.Fatal(err)
	}
	if agentState != "queued" {
		t.Fatalf("TASK-2 builder = %s, want queued behind the global limit", agentState)
	}
```

```go
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM agents WHERE id = ?`, agentID).Scan(&agentState); err != nil {
		t.Fatal(err)
	}
	if agentState != "active" {
		t.Fatalf("TASK-2 builder after the slot freed = %s, want active (drained)", agentState)
	}
```

4. `TestFIFOAcrossWorkflows`: rewrite the doc comment to "the oldest queued
   workflow agent takes a freed slot first", and change the body as follows.
   - `setMaxSubagents(t, s, 1)` → `setLimits(t, s, 2, 8)`.
   - Replace `assertWaiting` with `assertAgentState`.
   - Replace the two direct `advance` calls with `s.DrainQueue(ctx)`.

```go
	assertAgentState := func(workflowID, label, want string) {
		t.Helper()
		var st string
		if err := s.DB.QueryRowContext(ctx, `SELECT a.state FROM workflow_runs r JOIN agents a ON a.id = r.agent_id
			WHERE r.workflow_id = ? AND r.step_id = 'build'`, workflowID).Scan(&st); err != nil {
			t.Fatal(err)
		}
		if st != want {
			t.Fatalf("%s builder = %s, want %s", label, st, want)
		}
	}
	assertAgentState(st2.ID, "TASK-2", "queued")
	assertAgentState(st3.ID, "TASK-3", "queued")

	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, occupier.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainQueue(ctx); err != nil {
		t.Fatal(err)
	}
	assertAgentState(st2.ID, "TASK-2 (oldest)", "active")
	assertAgentState(st3.ID, "TASK-3", "queued")
```

   Keep the `tm` variable only if it is still used; drop `startedBefore`.
5. `TestCancelledWorkflowIgnoresLaterSlotRelease`:
   - `setMaxSubagents(t, s, 1)` → `setLimits(t, s, 2, 8)`.
   - The "TASK-2 build run = waiting" check becomes: the run is `active` and
     its agent is `queued`, using the same `SELECT a.state … JOIN agents`
     query as above.
   - After `advanceWaitingForOwner`, also call `s.DrainQueue(ctx)`.
   - The final assertion reads TASK-2's agent state and requires `active`.
   - Keep the TASK-1 cancelled/cancelled assertions and
     `len(tm.started) == startedBefore+1` unchanged.

In `internal/runtime/limits_test.go`, delete these two tests (behaviour
removed):

- `TestNoAckChildren` (`:539-601`, doc comment included)
- `TestSubagentSlotsMatchesHookCount` (`:603-652`, doc comment included)

- [ ] **Step 3: Run the tests and watch them fail.**

Run: `go test ./internal/hook/ -run 'TestPreToolUseNeverBudgetBlocksSwarmSpawn' -v && go test ./internal/runtime/ -run 'TestSlotReleaseSpawnsWaitingRun|TestFIFOAcrossWorkflows|TestCancelledWorkflowIgnoresLaterSlotRelease|TestReplayAdvanceAttachesReviewWorktree' -v`

Expected: the hook test fails with "must never be budget-blocked" (the block
still fires at 3). The workflow tests fail on "want queued" (runs still wait
on the budget).

- [ ] **Step 4: Minimal implementation.**

`internal/hook/handler.go`: delete from `// For Swarm's own swarm_spawn tool: enforce max_concurrent_subagents` through the closing `}` of `if isSpawn && … {` (the whole block, including the `isSpawn` variable). If that leaves `fmt` or `strings` unused, `go build` will say so; remove only the import it names.

`internal/runtime/workflow.go` `fillWaitingRuns` becomes:

```go
// fillWaitingRuns spawns every one of workflowID's waiting runs, oldest
// first. There is no per-owner budget: a spawned agent the global
// max_concurrent_agents pool can't fit yet waits in the global queue
// (spec 2026-09-27-single-agent-limit-live). spawnRunAgent claims its row
// (state waiting -> active) right after Spawn returns an agent, before any
// further side effect, so a concurrent fill can't double-spawn the same row
// and a later Share failure can't strand a live agent with no row pointing
// at it. A Spawn error itself marks the row 'failed' instead of leaving it
// 'waiting' for the stall scan to retry forever; see spawnRunAgent.
func (s *Store) fillWaitingRuns(ctx context.Context, workflowID string) error {
	wf, ok, err := s.workflowRowByID(ctx, workflowID)
	if err != nil || !ok {
		return err
	}
	it, err := s.itemByIDForEngine(ctx, wf.ItemID)
	if err != nil {
		return err
	}
	for {
		run, ok, err := s.oldestWaitingRun(ctx, workflowID)
		if err != nil || !ok {
			return err
		}
		spawned, err := s.spawnRunAgent(ctx, wf, it, run)
		if err != nil {
			return err
		}
		if !spawned {
			return nil // a concurrent call claimed the row; its advance owns it
		}
	}
}
```

Delete `olderWaitingRunElsewhere` and its doc comment. Reword the budget
comments listed under **Files** so that none still says "budget" in the sense
of subagent slots:

- `:4`: "idempotent spawning, FIFO waiting".
- `:506-507`: "try to fill any run still waiting (a spawn that errored and was
  put back, or one this call's own Spawn action just inserted)".
- `:538`: "including a Spawn that only inserted a 'waiting' row".
- `:662`: "is fillWaitingRuns' job".
- `:1513-1519`: "any child of the owner finishing re-triggers advance, which
  retries a 'waiting' row a failed spawn left behind; oldest first".
- `:1565`: drop "or waiting on budget for".

Leave the `:1139`, `:1322` and `:1416` comments alone: they refer to retry
budgets.

`internal/runtime/limits.go`: delete `NoAckChildren` and `SubagentSlots`
(with their doc comments). In the `NotAZombieSlot` comment, replace the
paragraph starting "Exported so every count" with:

```go
// Exported so every count of "agents currently occupying a concurrency slot"
// applies the same exclusion: Admit and EnforceCapacity (capacity.go). Both
// compose it into a query whose outer table is `agents` -- required, since
// the SQL fragment references `agents.id`/`agents.item_id` unqualified.
```

If `ackTimeout` or `db` become unused in `limits.go`, remove only what
`go build` names. `ackTimeout` is still used by `reconcile.go`; do not delete
its definition.

- [ ] **Step 5: Run the tests and verify they pass.**

Run: `go build ./... && go test ./internal/hook/ ./internal/runtime/ -run 'TestPreToolUse|TestSlotReleaseSpawnsWaitingRun|TestFIFOAcrossWorkflows|TestCancelledWorkflowIgnoresLaterSlotRelease|TestReplayAdvanceAttachesReviewWorktree|Workflow' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/hook/handler.go internal/hook/handler_test.go internal/runtime/limits.go internal/runtime/limits_test.go internal/runtime/workflow.go internal/runtime/workflow_test.go
git commit -m "feat(runtime,hook): remove the subagent budget; workflow runs queue in the one global pool

Deleted tests (behaviour intentionally removed): TestSubagentSlotsMatchesHookCount,
TestNoAckChildren, TestPreToolUseSurfacesNoAckChildrenInBudgetBlockReason,
TestPreToolUseSurfacesNoAckChildAfterResumeWithZeroNewCheckpoints.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Remove the per-root limit and both retired settings

**Files:**
- Modify: `internal/settings/settings.go:44-56` (fields), `:86-97` (defaults), `:174-200` (`Put`), `:338-345` (validation)
- Modify: `internal/runtime/limits.go:116-150` (`Admit`)
- Test: `internal/settings/settings_test.go`, `internal/httpapi/config_test.go`, `internal/runtime/limits_test.go`, `internal/runtime/reconcile_test.go:1826`, `internal/advisor/queue_test.go:35`, `scripts/e2e/harness_test.go:81-89`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `settings.Settings` without `MaxAgentsPerRoot` or `MaxConcurrentSubagents`.
  - `var retiredKeys []string` (settings package, unexported).
  - `const slotHoldersSQL string` (runtime).
  - `Admit(ctx, tx, role Role, rootItemID string) (bool, error)`, with the
    signature unchanged and one pool only.
  - The test helper becomes `setLimits(t *testing.T, s *Store, agents int)`.

- [ ] **Step 1: Write the failing tests.**

`internal/settings/settings_test.go`: add this test.

```go
// Spec 2026-09-27-single-agent-limit-live decision 2: no migration; Put
// deletes the rows no field reads any more.
func TestPutDeletesRetiredLimitKeys(t *testing.T) {
	s := newStore(t, kinds.Claude)
	for _, k := range []string{"max_orchestrators", "max_agents", "max_agents_per_root", "max_concurrent_subagents"} {
		if _, err := s.DB.Exec(`INSERT INTO settings (key, value_json, updated_at) VALUES (?, '1', 1)`, k); err != nil {
			t.Fatal(err)
		}
	}
	cur, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IN
		('max_orchestrators', 'max_agents', 'max_agents_per_root', 'max_concurrent_subagents')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("retired rows left = %d, want 0", n)
	}
}
```

Edit the existing tests so they compile against the new struct (these are
ports, not deletions):

- `TestDefaults`: drop `d.MaxAgentsPerRoot != 4 ||`.
- `TestGetPutRoundTrip`: drop `partial.MaxAgentsPerRoot != 4 ||`.
- `TestPutValidation`:
  - drop the `MaxAgentsPerRoot = 17` case;
  - the boundary line becomes `c.MaxConcurrentAgents, c.PauseDeadlineSec = 32, 600`.

`internal/httpapi/config_test.go` `TestSettingsRoutes`: replace
`bad(func(c *settings.Settings) { c.MaxAgentsPerRoot = 0 }, "Maximum concurrent agents per item must be between 1 and 16.")`
with a check that a stale client body still saves:

```go
	// A pre-2026-09-27 client still sends the removed keys: accepted, dropped.
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	raw["max_agents_per_root"], raw["max_concurrent_subagents"] = 0, 0
	if status, body := e.api("PUT", "/api/settings", raw); status != 200 {
		t.Fatalf("PUT with removed keys = %d %s", status, body)
	}
```

Add `"encoding/json"` to the imports if it is missing.

`internal/runtime/limits_test.go`:

1. `setLimits` becomes single-argument:

```go
// setLimits sets max_concurrent_agents, the one admission limit (spec
// 2026-09-27-single-agent-limit-live).
func setLimits(t *testing.T, s *Store, agents int) {
	t.Helper()
	now := s.now().UnixMilli()
	if _, err := s.DB.ExecContext(context.Background(), `INSERT INTO settings (key, value_json, updated_at)
		VALUES ('max_concurrent_agents', ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		fmt.Sprintf("%d", agents), now); err != nil {
		t.Fatal(err)
	}
	s.Events.Notify()
}
```

2. Then drop the second argument at every call site in the package:

```bash
perl -pi -e 's/setLimits\(t, s, (\d+), \d+\)/setLimits(t, s, $1)/g' internal/runtime/*_test.go
```

   Afterwards, `grep -n "setLimits(t, s, [0-9]*, " internal/runtime/*_test.go`
   must be empty. This also covers the calls Task 1 wrote as
   `setLimits(t, s, 2, 8)`.

3. Port `TestAdmitEnforcesThePerRootLimit` to the new contract. Rename it to
   `TestAdmitHasNoPerRootLimit`:

```go
// Spec 2026-09-27-single-agent-limit-live: there is one limit. A stale
// max_agents_per_root row (even 1) no longer queues a second worker in a root.
func TestAdmitHasNoPerRootLimit(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_at)
		VALUES ('max_agents_per_root', '1', 1)`); err != nil {
		t.Fatal(err)
	}
	seedEpicWithTwoTasks(t, s)
	if _, queued, _ := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}}); queued {
		t.Fatal("the first worker fits")
	}
	_, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("the second worker in the same root must start: only max_concurrent_agents (8) applies")
	}
}
```

4. Rename `TestLoweringALimitQueuesTheNextSpawnAndLeavesRunningAgentsAlone`
   to `TestLoweringALimitQueuesTheNextSpawn`.
   - Replace its doc comment with: "Admit alone never stops a running agent;
     it only queues the next spawn. Pausing agents over a lowered limit is
     EnforceCapacity's job (capacity_test.go)."
   - Change the failure text `"a lowered limit must not stop a running agent"`
     to `"Admit must not stop a running agent"`.

`internal/runtime/reconcile_test.go:1826`: drop the
`{"max_agents_per_root", "10"}` pair from the literal. Keep
`max_concurrent_agents`.

`internal/advisor/queue_test.go:35`: comment text → `not count against max_concurrent_agents (4), which is Task 13's`.

`scripts/e2e/harness_test.go`:

- Delete the `{"max_agents_per_root", "50"},` row.
- Comment `(4 concurrent agents, 4 per root)` → `(4 concurrent agents)`.
- `raise them generously` → `raise it generously`.

- [ ] **Step 2: Run the tests and watch them fail.**

Run: `go test ./internal/settings/ ./internal/runtime/ -run 'TestPutDeletesRetiredLimitKeys|TestAdmitHasNoPerRootLimit' -count=1`

Expected:

- `TestPutDeletesRetiredLimitKeys` fails: "retired rows left = 4".
- `TestAdmitHasNoPerRootLimit` fails: "must start".
- If `settings_test.go` does not compile yet because it still references the
  fields, that is also the expected red. Do Step 3 next.

- [ ] **Step 3: Minimal implementation.**

`internal/settings/settings.go`:

- Delete the `MaxAgentsPerRoot` and `MaxConcurrentSubagents` fields and their
  two default lines.
- Delete the two validation cases.
- Add, next to `NoAdvisor`:

```go
// retiredKeys are settings rows no field reads any more (2026-09-24
// unify-agent-limits and 2026-09-27 single-agent-limit-live). Put deletes
// them in its own transaction; no migration, so a rolled-back daemon still
// opens the DB (db.ErrTooNew).
var retiredKeys = []string{"max_orchestrators", "max_agents", "max_agents_per_root", "max_concurrent_subagents"}
```

In `Put`'s `s.DB.Tx` func, before `s.Events.Append`:

```go
		for _, k := range retiredKeys {
			if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, k); err != nil {
				return err
			}
		}
```

`internal/runtime/limits.go`: replace `Admit`'s doc comment and body with:

```go
// slotHoldersSQL counts every agent occupying a slot in the one
// max_concurrent_agents pool (spec 2026-09-27-single-agent-limit-live).
const slotHoldersSQL = `SELECT COUNT(*) FROM agents WHERE state = 'active' AND ` + NotAZombieSlot

// Admit reports whether a new agent may start now (A2, I20): one global
// max_concurrent_agents pool, every role including orchestrators. role and
// rootItemID are unused since the per-root limit went (kept so every call
// site stays put).
func (s *Store) Admit(ctx context.Context, tx *sql.Tx, role Role, rootItemID string) (bool, error) {
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return false, err
	}
	var holders int
	if err := tx.QueryRowContext(ctx, slotHoldersSQL).Scan(&holders); err != nil {
		return false, err
	}
	return holders < cfg.MaxConcurrentAgents, nil
}
```

- [ ] **Step 4: Run the tests and verify they pass.**

Run: `go build ./... && go vet ./... && go test ./internal/settings/ ./internal/httpapi/ ./internal/runtime/ ./internal/advisor/ -count=1`

Expected: PASS. Then run
`grep -rn "MaxAgentsPerRoot\|MaxConcurrentSubagents\|max_agents_per_root\|max_concurrent_subagents" --include='*.go' .`.
The only match should be the `retiredKeys` literal and the two test inserts
(`TestPutDeletesRetiredLimitKeys`, `TestAdmitHasNoPerRootLimit`,
`TestSettingsRoutes`).

- [ ] **Step 5: Commit.**

```bash
git add internal/settings/settings.go internal/settings/settings_test.go internal/httpapi/config_test.go internal/runtime/limits.go internal/runtime/*_test.go internal/advisor/queue_test.go scripts/e2e/harness_test.go
git commit -m "feat(settings,runtime): one agent limit; drop max_agents_per_root and max_concurrent_subagents

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

(`internal/runtime/*_test.go` is a shell glob of files you edited with perl.
Check `git status --short` first, and stage only files that show as modified
by this task.)

---

### Task 3: The capacity marker: `OperationReason`, the notification rule, and `reason` on the wire

**Files:**
- Create: `internal/runtime/capacity.go`
- Create: `internal/runtime/capacity_test.go`
- Modify: `internal/notifyrules/notifyrules.go:22-50`
- Modify: `internal/httpapi/handoff.go:17-29`
- Test: `internal/notify/notify_test.go:30-75`, `internal/httpapi/handoff_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `const capacityKeyPrefix = "capacity:"` and `const resumeKeyPrefix = "resume:"`.
  - `func OperationReason(requestKey string) string` (runtime, exported).
  - `replacementWire.Reason string` (`json:"reason,omitempty"`).
  - Rule `agent.capacity_paused`.

- [ ] **Step 1: Write the failing tests.**

`internal/runtime/capacity_test.go`:

```go
package runtime

import "testing"

func TestOperationReason(t *testing.T) {
	for key, want := range map[string]string{
		"capacity:ses_1": "capacity",
		"resume:ses_1":   "resume",
		"h1":             "",
		"":               "",
		"capacityx":      "",
	} {
		if got := OperationReason(key); got != want {
			t.Errorf("OperationReason(%q) = %q, want %q", key, got, want)
		}
	}
}
```

`internal/notify/notify_test.go`: add this row to the `want` table, after
`agent.interrupted`:

```go
		"agent.capacity_paused":   {"info", "Agent paused", "{name} paused to fit the agent limit. It resumes when a slot frees.", "swarm.info"},
```

`internal/httpapi/handoff_test.go`: add this test.

```go
// Spec 2026-09-27-single-agent-limit-live: a capacity-keyed operation
// carries reason "capacity" on the state node; any other key omits it.
func TestStateNodeReplacementCarriesCapacityReason(t *testing.T) {
	s, _ := newRuntimeServer(t)
	if _, err := s.DB.ExecContext(bg, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		SELECT 'op_cap', id, 'handoff', 'queued', 'capacity:ses_x', '', 1, 1, 1 FROM agents WHERE name = 'task-worker'`); err != nil {
		t.Fatal(err)
	}
	rec := s.get(t, "/api/agents/task-worker/replacement")
	var body struct {
		Reason string `json:"reason"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 200 || body.Reason != "capacity" {
		t.Fatalf("replacement = %d %s, want reason capacity", rec.Code, rec.Body)
	}
	if _, err := s.DB.ExecContext(bg, `UPDATE agent_operations SET request_key = 'h1' WHERE id = 'op_cap'`); err != nil {
		t.Fatal(err)
	}
	rec = s.get(t, "/api/agents/task-worker/replacement")
	if strings.Contains(rec.Body.String(), `"reason"`) {
		t.Fatalf("plain handoff must omit reason: %s", rec.Body)
	}
}
```

Add a `"strings"` import if it is missing.

- [ ] **Step 2: Run the tests and watch them fail.**

Run: `go test ./internal/runtime/ -run TestOperationReason -count=1; go test ./internal/notify/ ./internal/httpapi/ -run 'Rules|TestStateNodeReplacementCarriesCapacityReason' -count=1`

Expected:

- runtime: `undefined: OperationReason` (compile failure).
- notify: `missing rule "agent.capacity_paused"`.
- httpapi: "want reason capacity".

- [ ] **Step 3: Minimal implementation.**

`internal/runtime/capacity.go`:

```go
package runtime

import "strings"

// Request-key prefixes for operations the daemon starts to keep within
// max_concurrent_agents (spec 2026-09-27-single-agent-limit-live). The key,
// never the note, is the marker: a note is pasted into the successor's
// prompt.
const (
	capacityKeyPrefix = "capacity:"
	resumeKeyPrefix   = "resume:"
)

// OperationReason names why the daemon started an operation: "capacity"
// (paused to fit the agent limit), "resume" (a manual resume waiting for a
// slot) or "" (anything else).
func OperationReason(requestKey string) string {
	switch {
	case strings.HasPrefix(requestKey, capacityKeyPrefix):
		return "capacity"
	case strings.HasPrefix(requestKey, resumeKeyPrefix):
		return "resume"
	}
	return ""
}
```

`internal/notifyrules/notifyrules.go`: after the `agent.interrupted` row:

```go
	"agent.capacity_paused":   {"info", "Agent paused", "{name} paused to fit the agent limit. It resumes when a slot frees.", "swarm.info"},
```

`internal/httpapi/handoff.go`:

```go
type replacementWire struct {
	OperationID string `json:"operation_id"`
	Agent       string `json:"agent"`
	Mode        string `json:"mode"`
	Phase       string `json:"phase"`
	RequestKey  string `json:"request_key,omitempty"`
	// Reason is "capacity" or "resume" for an operation the agent limit
	// started (runtime.OperationReason); omitted otherwise.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

func replacementOut(name string, op runtime.Operation) replacementWire {
	return replacementWire{OperationID: op.ID, Agent: name, Mode: string(op.Mode),
		Phase: string(op.Phase), RequestKey: op.RequestKey,
		Reason: runtime.OperationReason(op.RequestKey), Error: op.Error}
}
```

- [ ] **Step 4: Run the tests and verify they pass.**

Run: `go test ./internal/runtime/ -run TestOperationReason -count=1 && go test ./internal/notify/ ./internal/httpapi/ -count=1`

Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/runtime/capacity.go internal/runtime/capacity_test.go internal/notifyrules/notifyrules.go internal/notify/notify_test.go internal/httpapi/handoff.go internal/httpapi/handoff_test.go
git commit -m "feat(runtime,httpapi): capacity/resume operation reason and agent.capacity_paused rule

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `EnforceCapacity`, which pauses the newest eligible agents over the limit

**Files:**
- Modify: `internal/runtime/capacity.go`
- Modify: `internal/runtime/replacement.go:626-639` (`stopPredecessor` notification kind)
- Test: `internal/runtime/capacity_test.go`

**Interfaces:**
- Consumes:
  - `OperationReason` and `capacityKeyPrefix` (Task 3).
  - `slotHoldersSQL` (Task 2).
  - `NotAZombieSlot`.
  - `RequestReplacement(ctx, agentID string, mode ReplacementMode, requestKey, note string) (Operation, error)`.
  - `liveChildNames(ctx, agentID string) []string`.
  - `setLimits(t, s, agents int)` (Task 2).
- Produces: `func (s *Store) EnforceCapacity(ctx context.Context) error`.

- [ ] **Step 1: Write the failing tests.**

Append to `internal/runtime/capacity_test.go`. Merge the import block to
`context`, `strings`, `testing` and `time`.

```go
// spawnRunning spawns a parentless worker on itemKey (or a child when
// parentID is set) and marks its session running. The clock steps a second
// first so created_at strictly orders agents.
func spawnRunning(t *testing.T, s *Store, tm *fakeTmux, itemKey, parentID string) Agent {
	t.Helper()
	ctx := context.Background()
	tm.clk.Advance(time.Second)
	a, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: itemKey, Role: RoleCoder, Kind: Fake, Model: "fake-1",
		ParentAgentID: parentID, Brief: BriefInput{Objective: "work"}})
	if err != nil || queued {
		t.Fatalf("spawn %s: queued=%v err=%v", itemKey, queued, err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'running' WHERE agent_id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	return a
}

// capacityOps maps agent id -> phase for every capacity-keyed operation.
func capacityOps(t *testing.T, s *Store) map[string]string {
	t.Helper()
	rows, err := s.DB.Query(`SELECT agent_id, phase FROM agent_operations WHERE request_key LIKE 'capacity:%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, phase string
		if err := rows.Scan(&id, &phase); err != nil {
			t.Fatal(err)
		}
		out[id] = phase
	}
	return out
}

func latestState(t *testing.T, s *Store, agentID string) SessionState {
	t.Helper()
	ses, err := s.LatestSession(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	return ses.State
}

func countKind(s *Store, kind string) int {
	n := 0
	for _, k := range s.Notify.(*fakeNotifier).kinds() {
		if k == kind {
			n++
		}
	}
	return n
}

// Spec scenarios 1 and 2: lowering the limit by N pauses exactly the N
// newest agents, each through a capacity-keyed handoff, and later ticks are
// no-ops.
func TestEnforceCapacityPausesTheNewestAgentsOverTheLimit(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithThreeTasks(t, s)
	a := spawnRunning(t, s, tm, "TASK-1", "")
	b := spawnRunning(t, s, tm, "TASK-2", "")
	c := spawnRunning(t, s, tm, "TASK-3", "")
	setLimits(t, s, 1)

	if err := s.EnforceCapacity(ctx); err != nil {
		t.Fatal(err)
	}
	ops := capacityOps(t, s)
	if len(ops) != 2 || ops[b.ID] != "queued" || ops[c.ID] != "queued" {
		t.Fatalf("capacity ops = %v, want b and c queued", ops)
	}
	if st := latestState(t, s, a.ID); st != Running {
		t.Fatalf("oldest agent = %s, want running", st)
	}
	for _, id := range []string{b.ID, c.ID} {
		if st := latestState(t, s, id); st != Interrupted {
			t.Fatalf("paused agent %s = %s, want interrupted", id, st)
		}
	}
	if n := countKind(s, "agent.capacity_paused"); n != 2 {
		t.Fatalf("agent.capacity_paused raised %d times, want 2", n)
	}
	if n := countKind(s, "agent.interrupted"); n != 0 {
		t.Fatalf("agent.interrupted raised %d times, want 0 for a capacity pause", n)
	}
	var note string
	if err := s.DB.QueryRow(`SELECT COALESCE(note, '') FROM agent_operations WHERE agent_id = ?`, b.ID).Scan(&note); err != nil || note != "" {
		t.Fatalf("capacity op note = %q (err %v), want empty", note, err)
	}

	for i := 0; i < 2; i++ {
		if err := s.EnforceCapacity(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var total int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations`).Scan(&total); err != nil || total != 2 {
		t.Fatalf("operations after repeat ticks = %d (err %v), want 2", total, err)
	}
}

// Spec scenario 3: a holder still saving its handoff counts toward the
// reduction, so a second tick does not pause an extra agent.
func TestEnforceCapacityCountsInFlightPauses(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithThreeTasks(t, s)
	spawnRunning(t, s, tm, "TASK-1", "")
	spawnRunning(t, s, tm, "TASK-2", "")
	c := spawnRunning(t, s, tm, "TASK-3", "")
	cSes, err := s.LatestSession(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// c's pane is alive, so its handoff parks in preserving (still a holder).
	panes(tm, Pane{Session: cSes.TmuxName, Command: "swarm-fake-agent"})
	tm.env[cSes.TmuxName] = map[string]string{"SWARM_SESSION": cSes.ID}
	setLimits(t, s, 2)

	for i := 0; i < 3; i++ {
		if err := s.EnforceCapacity(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ops := capacityOps(t, s)
	if len(ops) != 1 || ops[c.ID] != "preserving" {
		t.Fatalf("capacity ops = %v, want only c preserving", ops)
	}
}

// Spec scenario 4: each skip rule protects the newest agent P, so the older
// plain worker U is paused instead.
func TestEnforceCapacitySkipRules(t *testing.T) {
	for name, protect := range map[string]func(t *testing.T, s *Store, p Agent, pSes Session){
		"open request": func(t *testing.T, s *Store, p Agent, pSes Session) {
			mustExec(t, s.DB, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id, prompt, state, created_at)
				VALUES ('req_1', 'question', 1, ?, ?, ?, 'Which DB?', 'open', 1)`, p.ID, pSes.ID, p.ItemID)
		},
		"unanswered question": func(t *testing.T, s *Store, p Agent, pSes Session) {
			mustExec(t, s.DB, `INSERT INTO messages (id, seq, kind, origin, from_agent_id, to_agent_id, root_item_id, payload_json, state, created_at)
				VALUES ('msg_q', 9001, 'question', 'agent', ?, ?, ?, '{}', 'acked', 1)`, p.ID, p.ID, p.RootItemID)
		},
		"operation in flight": func(t *testing.T, s *Store, p Agent, pSes Session) {
			mustExec(t, s.DB, `INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
				VALUES ('op_x', ?, 'recover', 'queued', 'k1', ?, ?, 1, 1)`, p.ID, pSes.ID, pSes.Generation)
		},
		"spawning": func(t *testing.T, s *Store, p Agent, pSes Session) {
			mustExec(t, s.DB, `UPDATE sessions SET state = 'spawning' WHERE id = ?`, pSes.ID)
		},
		"cancelled capacity pause of this session": func(t *testing.T, s *Store, p Agent, pSes Session) {
			mustExec(t, s.DB, `INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
				VALUES ('op_c', ?, 'handoff', 'cancelled', ?, ?, ?, 1, 1)`, p.ID, "capacity:"+pSes.ID, pSes.ID, pSes.Generation)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, tm, _ := newStore(t)
			ctx := context.Background()
			setLimits(t, s, 8)
			seedEpicWithTwoTasks(t, s)
			u := spawnRunning(t, s, tm, "TASK-1", "")
			p := spawnRunning(t, s, tm, "TASK-2", "")
			pSes, err := s.LatestSession(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			protect(t, s, p, pSes)
			setLimits(t, s, 1)
			if err := s.EnforceCapacity(ctx); err != nil {
				t.Fatal(err)
			}
			ops := capacityOps(t, s)
			if ops[u.ID] != "queued" {
				t.Fatalf("capacity ops = %v, want the unprotected older worker paused", ops)
			}
			if ph, ok := ops[p.ID]; ok && ph != "cancelled" {
				t.Fatalf("protected agent got capacity op in phase %s", ph)
			}
		})
	}
}

// Spec scenario 4 (orchestrator rule) + "no eligible agent is not an error":
// an orchestrator with a live child is never paused.
func TestEnforceCapacitySkipsOrchestratorWithLiveChildren(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE sessions SET state = 'running' WHERE agent_id = ?`, orch.ID)
	child := spawnRunning(t, s, tm, "TASK-1", orch.ID)
	childSes, err := s.LatestSession(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The child waits on the user, so it is protected too.
	mustExec(t, s.DB, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id, prompt, state, created_at)
		VALUES ('req_1', 'question', 1, ?, ?, ?, 'Which DB?', 'open', 1)`, child.ID, childSes.ID, child.ItemID)
	setLimits(t, s, 1)
	if err := s.EnforceCapacity(ctx); err != nil {
		t.Fatalf("no eligible agent must not be an error: %v", err)
	}
	if ops := capacityOps(t, s); len(ops) != 0 {
		t.Fatalf("capacity ops = %v, want none", ops)
	}
}

// Spec scenario 5: workers go before orchestrators, even an older worker.
func TestEnforceCapacityPausesWorkersBeforeOrchestrators(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithTask(t, s)
	w := spawnRunning(t, s, tm, "TASK-1", "")
	tm.clk.Advance(time.Second)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE sessions SET state = 'running' WHERE agent_id = ?`, orch.ID)
	setLimits(t, s, 1)
	if err := s.EnforceCapacity(ctx); err != nil {
		t.Fatal(err)
	}
	if ops := capacityOps(t, s); len(ops) != 1 || ops[w.ID] == "" {
		t.Fatalf("capacity ops = %v, want only the (older) worker", ops)
	}
}

// Spec scenario 12: a refused replacement is logged and skipped; the next
// candidate is paused and the tick does not fail.
func TestEnforceCapacitySkipsARefusedReplacement(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithThreeTasks(t, s)
	spawnRunning(t, s, tm, "TASK-1", "")
	older := spawnRunning(t, s, tm, "TASK-2", "")
	newest := spawnRunning(t, s, tm, "TASK-3", "")
	mustExec(t, s.DB, `CREATE TRIGGER reject_op BEFORE INSERT ON agent_operations
		WHEN NEW.agent_id = '`+newest.ID+`' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	setLimits(t, s, 2)
	if err := s.EnforceCapacity(ctx); err != nil {
		t.Fatalf("tick failed: %v", err)
	}
	if ops := capacityOps(t, s); len(ops) != 1 || ops[older.ID] != "queued" {
		t.Fatalf("capacity ops = %v, want the next candidate paused", ops)
	}
}
```

`mustExec` lives in `limits_test.go` (`func mustExec(t *testing.T, d *db.DB, query string, args ...any)`).
Check the `requests` insert columns against `internal/db/schema/0004_hitl_requests.sql`.
If a later migration added another NOT NULL column without a default, add it
with a literal value.

- [ ] **Step 2: Run the tests and watch them fail.**

Run: `go test ./internal/runtime/ -run 'TestEnforceCapacity' -count=1`

Expected: compile failure `s.EnforceCapacity undefined`.

- [ ] **Step 3: Minimal implementation.**

Append to `internal/runtime/capacity.go`. Add `context` to the import block.

```go
// inFlightPhasesSQL is nonterminalPhases as a SQL list (keep in sync with
// replacement.go and the agent_operations_one_active index).
const inFlightPhasesSQL = `('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting')`

// pendingCapacitySQL counts slot-holders already being paused for capacity
// (still saving their handoff): they count toward the reduction, so a
// second tick never pauses an extra agent (spec decision 5).
const pendingCapacitySQL = `SELECT COUNT(*) FROM agents WHERE state = 'active' AND ` + NotAZombieSlot + `
	AND EXISTS (SELECT 1 FROM agent_operations o WHERE o.agent_id = agents.id
		AND o.request_key LIKE 'capacity:%' AND o.phase IN ` + inFlightPhasesSQL + `)`

// capacityCandidatesSQL lists slot-holders EnforceCapacity may pause, in
// pause order (spec decision 4): latest session running; no operation in
// flight; this session never capacity-paused before (a cancelled or blocked
// one is not retried); no open request; no unanswered question it asked.
// Workers first, then newest first.
const capacityCandidatesSQL = `SELECT agents.id, ls.id FROM agents
	JOIN sessions ls ON ls.id = (SELECT id FROM sessions WHERE agent_id = agents.id
		ORDER BY generation DESC, attempt DESC LIMIT 1)
	WHERE agents.state = 'active' AND ` + NotAZombieSlot + ` AND ls.state = 'running'
		AND NOT EXISTS (SELECT 1 FROM agent_operations o WHERE o.agent_id = agents.id
			AND (o.phase IN ` + inFlightPhasesSQL + ` OR o.request_key = 'capacity:' || ls.id))
		AND NOT EXISTS (SELECT 1 FROM requests r WHERE r.agent_id = agents.id AND r.state = 'open')
		AND NOT EXISTS (SELECT 1 FROM messages q WHERE q.kind = 'question' AND q.from_agent_id = agents.id
			AND NOT EXISTS (SELECT 1 FROM messages a WHERE a.kind IN ('answer', 'approval_result', 'relay')
				AND (a.reply_to = q.id OR (a.kind = 'answer' AND a.correlation_id = q.id))))
	ORDER BY agents.role = 'orchestrator', agents.created_at DESC, agents.id DESC`

// EnforceCapacity hands off the newest eligible slot-holders while more
// agents hold a slot than max_concurrent_agents allows (spec decisions
// 3-5). Each pause is RequestReplacement(ModeHandoff) keyed
// "capacity:<session id>" with an empty note; the operation parks in
// queued and ResumeOperations restarts the agent once Admit has room.
// Runs every reconcile tick; idempotent.
func (s *Store) EnforceCapacity(ctx context.Context) error {
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	var holders, pending int
	if err := s.DB.QueryRowContext(ctx, slotHoldersSQL).Scan(&holders); err != nil {
		return err
	}
	if err := s.DB.QueryRowContext(ctx, pendingCapacitySQL).Scan(&pending); err != nil {
		return err
	}
	need := holders - pending - cfg.MaxConcurrentAgents
	if need <= 0 {
		return nil
	}
	type candidate struct{ agentID, sessionID string }
	rows, err := s.DB.QueryContext(ctx, capacityCandidatesSQL)
	if err != nil {
		return err
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.agentID, &c.sessionID); err != nil {
			rows.Close()
			return err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range cands {
		if need == 0 {
			break
		}
		if len(s.liveChildNames(ctx, c.agentID)) > 0 {
			continue // an orchestrator coordinating live children keeps running
		}
		if _, err := s.RequestReplacement(ctx, c.agentID, ModeHandoff, capacityKeyPrefix+c.sessionID, ""); err != nil {
			s.logf("capacity: pause %s: %v", c.agentID, err)
			continue
		}
		need--
	}
	return nil
}
```

`internal/runtime/replacement.go` `stopPredecessor`: replace

```go
		state, kind := string(Interrupted), "agent.interrupted"
		if op.Mode == ModePause {
			state, kind = string(Paused), "agent.paused"
		}
```

with

```go
		state, kind := string(Interrupted), "agent.interrupted"
		if op.Mode == ModePause {
			state, kind = string(Paused), "agent.paused"
		} else if OperationReason(op.RequestKey) == "capacity" {
			kind = "agent.capacity_paused"
		}
```

The args already carry `name` and `KEY`.

- [ ] **Step 4: Run the tests and verify they pass.**

Run: `go test ./internal/runtime/ -run 'TestEnforceCapacity|TestOperationReason|Replacement|Handoff' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/runtime/capacity.go internal/runtime/capacity_test.go internal/runtime/replacement.go
git commit -m "feat(runtime): EnforceCapacity pauses the newest eligible agents over the limit

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Tick order so paused agents resume before queued spawns

**Files:**
- Modify: `internal/runtime/reconcile.go:230-265` (ordering block)
- Modify: `internal/runtime/replacement.go:328-333` (`ResumeOperations` query)
- Test: `internal/runtime/capacity_test.go`

**Interfaces:**
- Consumes: `EnforceCapacity` (Task 4), `ResumeOperations(ctx) error`, `DrainQueue(ctx) error`.
- Produces: the Reconcile order `… TickPause → EnforceCapacity → ResumeOperations → DrainQueue …`.

- [ ] **Step 1: Write the failing tests.**

Append to `capacity_test.go`:

```go
// Spec scenario 6: raising the limit resumes a capacity-paused agent before
// a queued spawn takes the slot, in one Reconcile tick.
func TestReconcileResumesCapacityPausedBeforeQueuedSpawns(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithThreeTasks(t, s)
	a := spawnRunning(t, s, tm, "TASK-1", "")
	b := spawnRunning(t, s, tm, "TASK-2", "")
	setLimits(t, s, 1)
	if err := s.EnforceCapacity(ctx); err != nil {
		t.Fatal(err)
	}
	if ops := capacityOps(t, s); ops[b.ID] != "queued" {
		t.Fatalf("setup: capacity ops = %v", ops)
	}
	tm.clk.Advance(time.Second)
	c, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-3", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Brief: BriefInput{Objective: "new"}})
	if err != nil || !queued {
		t.Fatalf("setup: spawn C queued=%v err=%v", queued, err)
	}
	aSes, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: aSes.TmuxName, Command: "swarm-fake-agent"})
	tm.env[aSes.TmuxName] = map[string]string{"SWARM_SESSION": aSes.ID}

	setLimits(t, s, 2)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if ops := capacityOps(t, s); ops[b.ID] != "succeeded" {
		t.Fatalf("capacity ops = %v, want b succeeded (resumed first)", ops)
	}
	got, err := s.Agent(ctx, c.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AgentQueued {
		t.Fatalf("queued spawn C = %s, want still queued", got.State)
	}
}

// Spec scenario 7: among capacity-paused agents, the earliest pause resumes
// first, by created_at, not by updated_at (which every phase swap bumps).
func TestResumeOperationsResumesTheEarliestPauseFirst(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithThreeTasks(t, s)
	spawnRunning(t, s, tm, "TASK-1", "")
	b := spawnRunning(t, s, tm, "TASK-2", "")
	c := spawnRunning(t, s, tm, "TASK-3", "")
	setLimits(t, s, 2)
	if err := s.EnforceCapacity(ctx); err != nil { // pauses c
		t.Fatal(err)
	}
	tm.clk.Advance(time.Minute)
	setLimits(t, s, 1)
	if err := s.EnforceCapacity(ctx); err != nil { // pauses b, later
		t.Fatal(err)
	}
	// c's row was touched last: updated_at order would pick b.
	mustExec(t, s.DB, `UPDATE agent_operations SET updated_at = updated_at + 3600000 WHERE agent_id = ?`, c.ID)
	setLimits(t, s, 2)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	ops := capacityOps(t, s)
	if ops[c.ID] != "succeeded" || ops[b.ID] != "queued" {
		t.Fatalf("capacity ops = %v, want c (earliest pause) resumed, b still queued", ops)
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail.**

Run: `go test ./internal/runtime/ -run 'TestReconcileResumesCapacityPausedBeforeQueuedSpawns|TestResumeOperationsResumesTheEarliestPauseFirst' -count=1`

Expected:

- The first test fails: C is `active` (DrainQueue still runs first) and B is
  `queued`. It can also fail on B not being `succeeded`.
- The second test fails: "want c … resumed" (updated_at order picks B).

- [ ] **Step 3: Minimal implementation.**

`replacement.go` `ResumeOperations`: change the query's `ORDER BY updated_at`
to `ORDER BY created_at, id`. Add one line to the doc comment: "Oldest intent
first, so an agent the limit paused earlier resumes earlier (spec
2026-09-27-single-agent-limit-live decision 6)."

`reconcile.go`:

1. Replace

```go
	if err := s.TickPause(ctx); err != nil {
		return err
	}
	if err := s.DrainQueue(ctx); err != nil {
		return err
	}
```

with

```go
	if err := s.TickPause(ctx); err != nil {
		return err
	}
	// The one agent limit, applied live (spec 2026-09-27-single-agent-limit-
	// live): pause agents over max_concurrent_agents, then resume paused and
	// replacement operations, and only then admit queued spawns, so an agent
	// the limit paused gets a freed slot before a brand-new one.
	if err := s.EnforceCapacity(ctx); err != nil {
		return err
	}
	// Continuity: advance replacement operations from their durable phase.
	// A restart between the intent commit and the successor launch resumes
	// here instead of wedging the agent behind the partial unique index.
	if err := s.ResumeOperations(ctx); err != nil {
		return err
	}
	if err := s.DrainQueue(ctx); err != nil {
		return err
	}
```

2. Delete the old `ResumeOperations` call and its comment after
   `recoverWorkflows` (the three `// Continuity:` comment lines plus the
   `if` block).

- [ ] **Step 4: Run the tests and verify they pass.**

Run: `go test ./internal/runtime/ -count=1`

Expected: PASS for the whole package.

Some existing Reconcile test may now fail because it seeds more running
agents than the default limit of 4, so `EnforceCapacity` hands one off. In
that case raise that test's limit with `setLimits(t, s, 32)` before its
`Reconcile`. Do not change its assertions. Name each such test in the commit
body.

- [ ] **Step 5: Commit.**

```bash
git add internal/runtime/reconcile.go internal/runtime/replacement.go internal/runtime/capacity_test.go
git commit -m "feat(runtime): enforce the limit each tick; resume paused agents before draining new spawns

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Manual Resume respects the limit

**Files:**
- Modify: `internal/runtime/pause.go:874-905` (`Resume`, first half)
- Modify: `internal/runtime/replacement.go:886-940` (`startSuccessor`)
- Modify: `internal/runtime/capacity.go` (add `admitsNow`)
- Test: `internal/runtime/capacity_test.go`

**Interfaces:**
- Consumes: `resumeKeyPrefix`, `OperationReason`, `Admit`, `RequestReplacement`, `PendingOperation`, `IdemTx`.
- Produces: `func (s *Store) admitsNow(ctx context.Context, a Agent) (bool, error)`. `Resume(ctx, name, sessionID, requestID string) (Agent, error)` keeps its signature and now queues when full.

- [ ] **Step 1: Write the failing tests.**

Append to `capacity_test.go`:

```go
// Spec scenario 8: Resume with the pool full records a resume-keyed handoff
// instead of launching; a repeat is a no-op; a freed slot starts the
// successor in resume mode with no agent.retried.
func TestResumeWhenFullQueuesForAFreeSlot(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Paused); err != nil {
		t.Fatal(err)
	}
	setLimits(t, s, 1) // the orchestrator holds the only slot
	sessions := func() int {
		var n int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for i := 0; i < 2; i++ {
		got, err := s.Resume(ctx, w.Name, "", "")
		if err != nil {
			t.Fatalf("resume #%d: %v", i+1, err)
		}
		if got.ID != w.ID {
			t.Fatalf("resume returned %s", got.Name)
		}
	}
	var phase string
	var n int
	if err := s.DB.QueryRow(`SELECT phase, (SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?)
		FROM agent_operations WHERE request_key = ?`, w.ID, "resume:"+wSes.ID).Scan(&phase, &n); err != nil {
		t.Fatal(err)
	}
	if phase != "queued" || n != 1 {
		t.Fatalf("resume op = %s (ops %d), want one queued", phase, n)
	}
	if got := sessions(); got != 1 {
		t.Fatalf("sessions = %d, want 1 (no launch while full)", got)
	}

	setLimits(t, s, 2)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sessions(); got != 2 {
		t.Fatalf("sessions = %d, want 2 after a slot freed", got)
	}
	if !strings.Contains(fa.LastSpec.Kickoff, ResumeAddition) {
		t.Fatalf("successor kickoff is not resume mode:\n%s", fa.LastSpec.Kickoff)
	}
	if n := countKind(s, "agent.retried"); n != 0 {
		t.Fatalf("agent.retried raised %d times for a queued resume, want 0", n)
	}
}

// Spec scenario 9: Resume with room behaves as before: an immediate new
// generation and no operation row.
func TestResumeWithRoomStartsAtOnce(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Paused); err != nil {
		t.Fatal(err)
	}
	setLimits(t, s, 8)
	if _, err := s.Resume(ctx, w.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	var ops int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, w.ID).Scan(&ops); err != nil || ops != 0 {
		t.Fatalf("operations = %d (err %v), want 0", ops, err)
	}
	ses, err := s.LatestSession(ctx, w.ID)
	if err != nil || ses.Generation != wSes.Generation+1 {
		t.Fatalf("latest generation = %d (err %v), want %d", ses.Generation, err, wSes.Generation+1)
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail.**

Run: `go test ./internal/runtime/ -run 'TestResumeWhenFullQueuesForAFreeSlot|TestResumeWithRoomStartsAtOnce' -count=1`

Expected: the first test fails. `Resume` launches at once (sessions = 2 before
the raise), so the check "resume op … no rows" or "want 1" fails. The second
test passes already; it is a regression guard.

- [ ] **Step 3: Minimal implementation.**

`capacity.go`: add `database/sql` to the imports and append:

```go
// admitsNow reports whether Admit would let a start now; used by Resume.
// Best effort: two concurrent resumes can both pass, and EnforceCapacity
// pauses the excess on the next tick.
func (s *Store) admitsNow(ctx context.Context, a Agent) (bool, error) {
	var ok bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		ok, err = s.Admit(ctx, tx, a.Role, a.RootItemID)
		return err
	})
	return ok, err
}
```

`pause.go` `Resume`: replace the block from `// Batch 3: Resume must not launch a competing session` through the `ses.State != Paused && ses.State != Interrupted` check with:

```go
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		return Agent{}, err
	}
	// A repeat of a resume already waiting for a slot returns the agent,
	// not a 409 (spec 2026-09-27-single-agent-limit-live decision 7).
	if op, ok, err := s.PendingOperation(ctx, a.ID); err != nil {
		return Agent{}, err
	} else if ok && op.RequestKey == resumeKeyPrefix+ses.ID {
		return a, nil
	}
	// Batch 3: Resume must not launch a competing session while the
	// replacement coordinator is driving one.
	if err := s.refuseIfOperationInFlight(ctx, a.ID); err != nil {
		return Agent{}, err
	}
	if ses.State != Paused && ses.State != Interrupted {
		return Agent{}, &items.Error{Code: items.CodeConflict, Message: stillStopping}
	}
	// The one agent limit: with the pool full, wait for a slot as a
	// resume-keyed handoff (it parks in queued; ResumeOperations starts it in
	// resume mode once Admit has room) instead of exceeding the limit.
	if room, err := s.admitsNow(ctx, a); err != nil {
		return Agent{}, err
	} else if !room {
		if _, err := s.RequestReplacement(ctx, a.ID, ModeHandoff, resumeKeyPrefix+ses.ID, ""); err != nil {
			return Agent{}, err
		}
		if _, err := IdemTx(ctx, s, sessionID, requestID, "swarm_control", &out, func(*sql.Tx) error {
			out = a
			return nil
		}); err != nil {
			return Agent{}, err
		}
		return a, nil
	}
```

The rest of `Resume` (from `resume := ses.ProviderSessionID != ""` on) is
unchanged.

`replacement.go` `startSuccessor`: replace

```go
	succMode := "handoff"
	if op.Mode == ModeRecover {
		succMode = "recovery"
	}
```

with

```go
	reason := OperationReason(op.RequestKey)
	succMode := "handoff"
	switch {
	case op.Mode == ModeRecover:
		succMode = "recovery"
	case reason == "resume":
		succMode = "resume" // a manual Resume that waited for a slot
	}
```

and wrap the `agent.retried` notify:

```go
		// A capacity pause or a queued resume coming back is expected, not a
		// retry: no agent.retried (spec decision 9).
		if reason == "" {
			if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.retried", AgentName: a.Name,
				ItemKey: key, Args: map[string]string{"name": a.Name, "N": fmt.Sprint(succ.Attempt), "KEY": key}}); err != nil {
				return err
			}
		}
```

- [ ] **Step 4: Run the tests and verify they pass.**

Run: `go test ./internal/runtime/ -count=1`

Expected: PASS.

- [ ] **Step 5: Batch 1 gate.**

Run, in order:

1. `go vet ./...`
2. `gofmt -l internal cmd scripts` (expect empty output)
3. `go test -race ./...`

Expected: all green. Fix only what this batch broke. Run
`superpowers:systematic-debugging` before touching any unexpected failure.

- [ ] **Step 6: Commit.**

```bash
git add internal/runtime/pause.go internal/runtime/replacement.go internal/runtime/capacity.go internal/runtime/capacity_test.go
git commit -m "feat(runtime): manual resume waits for a free slot instead of exceeding the limit

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Batch 2: skills and clients

### Task 7: Skills text

**Files:**
- Modify: `skills/swarm-orchestrator/SKILL.md:28`, `:40`
- Modify: `skills/swarm-spike/SKILL.md:11`
- Generated: `internal/install/skills/**` (via `make skills-sync`)

**Interfaces:** none (text only).

- [ ] **Step 1: Write the failing check.**

Run: `grep -rn "Subagent budget\|budget exceeded\|strict budget\|budget slot\|within budget" skills/ internal/install/skills/`

Expected: matches in `swarm-orchestrator` (`:28`, `:40`) and `swarm-spike`
(`:11`), in both trees. That is the red state.

- [ ] **Step 2: Edit the text.**

In `skills/swarm-orchestrator/SKILL.md:28`, replace

```
Limit your concurrent subagents: you have a strict budget (default 3 active subagents). Do not spawn more until some finish. If you receive a budget exceeded error, do not retry immediately; end your turn to wait for relays from finishing subagents.
```

with

```
There is one limit: the user's max concurrent agents, every role including you. A child spawned past it is queued and starts on its own when a slot frees (swarm_read shows it queued); don't cancel and respawn it. An agent can also be paused to fit a lowered limit; it resumes on its own.
```

In `:40`:

- `nothing to ever release its budget slot on its own` → `nothing to ever release its agent slot on its own`
- `A \`swarm_spawn\` refusal ("Subagent budget exceeded") now names any such child directly in its reason; either way, give it a little longer` → `Give it a little longer`

In `skills/swarm-spike/SKILL.md:11`: `in parallel within budget` → `in parallel`.

- [ ] **Step 3: Sync and verify.**

Run: `make skills-sync && grep -rn "Subagent budget\|budget exceeded\|strict budget\|budget slot\|within budget" skills/ internal/install/skills/`

Expected: no matches. Also run `go test ./internal/install/... -count=1`
(expect PASS; install tests may hash the skills tree).

- [ ] **Step 4: Commit.**

```bash
git status --short skills internal/install/skills
git add skills/swarm-orchestrator/SKILL.md skills/swarm-spike/SKILL.md <each synced file git status lists under internal/install/skills>
git commit -m "docs(skills): one agent limit; spawns past it queue, lowered limits pause

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Menubar: one limit, the new confirm copy, and capacity/resume row status

**Files:**
- Modify: `apps/menubar/Sources/SwarmBarKit/Wire.swift:124-139` (`AgentReplacement`), `:340-470` (`Settings`)
- Modify: `apps/menubar/Sources/SwarmBarKit/SettingsModel.swift:45-54`, `:303-358`
- Modify: `apps/menubar/Sources/SwarmBarKit/Copy.swift:40-44`, `:126`, `:257-259`
- Modify: `apps/menubar/Sources/SwarmBarKit/AgentActions.swift:123-136` (`handoffStatus`), `:151-160` (`actions`)
- Modify fixtures: `apps/menubar/Tests/Fixtures/settings.json:64-66`, `state.json:616`, `state-empty.json:74`, `events/stream.sse:25`
- Test: `apps/menubar/Tests/SwarmBarTests/HandoffTests.swift`, `SettingsModelTests.swift:243-273`, `SettingsRenderTests.swift:22`, `FixtureTests.swift:134`

**Interfaces:**
- Consumes: the wire field `replacement.reason` (`"capacity"` | `"resume"`) from Task 3.
- Produces:
  - `AgentReplacement.reason: String?` and
    `init(operationID:mode:phase:reason:error:)`.
  - `Copy.capacityPaused`, `Copy.resumeQueued`, `Copy.lowerLimit(_:)`.
  - `SettingsModel.Limit` is `.agents` and `.pauseDeadline`.

- [ ] **Step 1: Write the failing tests.**

`HandoffTests.swift`: add these tests.

```swift
    // MARK: - agent limit (spec 2026-09-27-single-agent-limit-live)

    func testReplacementDecodesOptionalReason() throws {
        let with = try SwarmJSON.decode(AgentReplacement.self,
            from: #"{"operation_id":"op_1","mode":"handoff","phase":"queued","reason":"capacity"}"#.data(using: .utf8)!)
        XCTAssertEqual(with.reason, "capacity")
        let without = try SwarmJSON.decode(AgentReplacement.self,
            from: #"{"operation_id":"op_1","mode":"handoff","phase":"queued"}"#.data(using: .utf8)!)
        XCTAssertNil(without.reason)
    }

    func testLimitReasonsLabelTheRowAndOfferOnlyCancel() {
        var a = agent(.interrupted)
        a.replacement = AgentReplacement(operationID: "op_1", mode: "handoff", phase: "queued", reason: "capacity")
        XCTAssertEqual(AgentTree.handoffStatus(a), "Paused: over the agent limit")
        XCTAssertTrue(AgentTree.subtitle(a).hasSuffix(" · Paused: over the agent limit"), AgentTree.subtitle(a))
        XCTAssertEqual(endpoints(a), ["cancel:Cancel:menu"])
        a.replacement?.phase = "preserving"
        XCTAssertEqual(AgentTree.handoffStatus(a), "Paused: over the agent limit")
        a.replacement?.reason = "resume"
        a.replacement?.phase = "queued"
        XCTAssertEqual(AgentTree.handoffStatus(a), "Queued: waiting for a free slot")
        XCTAssertEqual(endpoints(a), ["cancel:Cancel:menu"])
        a.replacement?.phase = "starting"
        XCTAssertEqual(AgentTree.handoffStatus(a), "Starting successor…")
    }
```

Check that `cancel` for a non-orchestrator has placement `.menu`
(`AgentActions.swift:151`). If the `endpoints` string differs, match what
`endpoints(agent(.queued))` returns for its cancel entry.

`SettingsModelTests.testLimitsTab` becomes:

```swift
    func testLimitsTab() async {
        let m = await model()
        XCTAssertEqual([m.value(.agents), m.value(.pauseDeadline)], [4, 120])
        // 3 live agents of any role across the whole tree: auth-epic-orchestrator,
        // login-form-coder and login-review (running/waiting). Excluded: the
        // finished session-coder, the queued billing-spike-orchestrator, the
        // paused crash-debug-orchestrator, the crashed docs-fix-coder, and
        // search-spike-orchestrator (no session in the fixture -> reads as queued).
        XCTAssertEqual(m.overLimit(.agents, 2), 1)
        XCTAssertEqual(m.overLimit(.pauseDeadline, 30), 0)

        await m.setLimit(.agents, 1)
        XCTAssertEqual(m.limitNotice, "2 agents will pause and resume when a slot frees.")
        XCTAssertEqual(saves, 0)
        await m.applyLimit()
        XCTAssertEqual(m.settings.maxConcurrentAgents, 1)
        XCTAssertNil(m.limitNotice)
        await m.applyLimit()
        XCTAssertEqual(saves, 1)

        await m.setLimit(.agents, 99)
        XCTAssertEqual(m.settings.maxConcurrentAgents, 32)
        await m.setLimit(.pauseDeadline, 5)
        XCTAssertEqual(m.settings.pauseDeadlineSec, 30)
        await m.setLimit(.agents, 32)
        XCTAssertEqual(saves, 3)
        XCTAssertEqual(SettingsModel.Limit.allCases.map(\.range), [1...32, 30...600])
    }
```

The other test edits:

- `SettingsRenderTests.swift:22`: `setLimit(.subagents, 1)` →
  `setLimit(.agents, 1)`.
- `FixtureTests.swift:134`:
  `XCTAssertEqual([d.maxConcurrentAgents, d.usagePollSec, d.pauseDeadlineSec], [4, 300, 120])`.

- [ ] **Step 2: Run the tests and watch them fail.**

Run: `cd apps/menubar && swift test --filter 'HandoffTests|SettingsModelTests|FixtureTests|SettingsRenderTests'`

Expected: compile errors, for example `extra argument 'reason'` and
`type 'SettingsModel.Limit' has no member 'subagents'` (from the not yet
edited sources), or the old `lowerLimit` text. That is the red state.

- [ ] **Step 3: Minimal implementation.**

`Wire.swift` `AgentReplacement`:

```swift
public struct AgentReplacement: Codable, Sendable, Equatable {
    public var operationID: String
    public var mode: String
    public var phase: String
    /// "capacity" (paused to fit the agent limit) or "resume" (a manual resume
    /// waiting for a slot); nil for any other replacement. Optional, so a
    /// daemon that predates it decodes unchanged.
    public var reason: String?
    public var error: String?

    enum CodingKeys: String, CodingKey {
        case mode, phase, reason, error
        case operationID = "operation_id"
    }

    public init(operationID: String, mode: String, phase: String, reason: String? = nil, error: String? = nil) {
        self.operationID = operationID; self.mode = mode; self.phase = phase; self.reason = reason; self.error = error
    }
}
```

`Wire.swift` `Settings`: delete `maxConcurrentSubagents` and
`maxAgentsPerRoot` everywhere in the struct:

- the properties;
- the CodingKeys `maxAgentsPerRoot = "max_agents_per_root"` and
  `case maxConcurrentSubagents = "max_concurrent_subagents"`;
- the `.defaults` arguments;
- the `init` parameters and assignments;
- the `init(from:)` lines;
- the `encode(to:)` lines.

In the `init(from:)` comment, `the same tolerance maxConcurrentSubagents/instructions use`
→ `the same tolerance instructions uses`.

`SettingsModel.swift`:

```swift
    public enum Limit: CaseIterable, Sendable {
        case agents, pauseDeadline

        public var range: ClosedRange<Int> {
            switch self {
            case .agents: return 1...32
            case .pauseDeadline: return 30...600
            }
        }
    }
```

Remove the `.subagents` cases from `value`, `store` and `overLimit`. The
`limitNotice` doc comment becomes
`/// "2 agents will pause and resume when a slot frees."`.

`Copy.swift`:

- Delete `maxAgentsPerItem`.
- After `handoffBlocked`, add:

```swift
    public static let capacityPaused = "Paused: over the agent limit"
    public static let resumeQueued = "Queued: waiting for a free slot"
```

- Replace `lowerLimit`:

```swift
    public static func lowerLimit(_ n: Int) -> String {
        "\(n) agents will pause and resume when a slot frees."
    }
```

`AgentActions.swift` `handoffStatus`:

```swift
    public static func handoffStatus(_ a: AgentNode) -> String? {
        guard let op = a.replacement else { return nil }
        if op.phase != "starting" {
            switch op.reason {
            case "capacity": return Copy.capacityPaused
            case "resume": return Copy.resumeQueued
            default: break
            }
        }
        switch op.phase {
        case "requested", "preserving": return Copy.handoffSaving
        case "stopping": return Copy.handoffStopping
        case "ready", "queued": return Copy.handoffQueued
        case "starting": return Copy.handoffStarting
        case "blocked": return Copy.handoffBlocked(op.error ?? "")
        default: return nil
        }
    }
```

In `actions`, right after `let cancel = …`:

```swift
        // The agent limit owns this row until a slot frees (spec 2026-09-27):
        // Resume/Handoff would 409 against the in-flight operation.
        if a.replacement?.reason != nil { return [cancel] }
```

Fixtures: delete the `"max_concurrent_subagents": 3,` and
`"max_agents_per_root": 4,` lines from `settings.json`. Delete
`"max_agents_per_root": 4,` from `state.json` and `state-empty.json`. In
`events/stream.sse` line 25, delete `"max_agents_per_root":4,`. Keep every
JSON document valid (watch trailing commas).

- [ ] **Step 4: Run the tests and verify they pass.**

Run: `cd apps/menubar && swift build && swift test`

Expected: PASS. Then
`grep -rn "maxAgentsPerRoot\|maxConcurrentSubagents\|max_agents_per_root\|max_concurrent_subagents\|maxAgentsPerItem\|\.subagents" apps/menubar`
must be empty.

- [ ] **Step 5: Commit.**

```bash
git add apps/menubar/Sources/SwarmBarKit/Wire.swift apps/menubar/Sources/SwarmBarKit/SettingsModel.swift apps/menubar/Sources/SwarmBarKit/Copy.swift apps/menubar/Sources/SwarmBarKit/AgentActions.swift apps/menubar/Tests/SwarmBarTests/HandoffTests.swift apps/menubar/Tests/SwarmBarTests/SettingsModelTests.swift apps/menubar/Tests/SwarmBarTests/SettingsRenderTests.swift apps/menubar/Tests/SwarmBarTests/FixtureTests.swift apps/menubar/Tests/Fixtures/settings.json apps/menubar/Tests/Fixtures/state.json apps/menubar/Tests/Fixtures/state-empty.json apps/menubar/Tests/Fixtures/events/stream.sse
git commit -m "feat(menubar): one agent limit; lowering it pauses agents; capacity and queued-resume row status

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Board (web): capacity and queued-resume status; drop `max_agents_per_root`

**Files:**
- Modify: `web/src/types.ts:170-190` (`AgentNode`), `:277-281` (`Settings`)
- Modify: `web/src/copy.ts:30-47` (`SESSION_LABEL`)
- Modify: `web/src/logic/agentActions.ts:4-95`
- Modify: `web/src/mock/fixtures.ts:186`
- Test: `web/src/logic/agentActions.test.ts`

**Interfaces:**
- Consumes: the wire field `replacement.reason` (Task 3).
- Produces:
  - `AgentReplacement` (TS).
  - `AgentNode.replacement?`.
  - `DisplayState` gains `"capacity_paused"` and `"resume_queued"`.

- [ ] **Step 1: Write the failing tests.**

Append to `web/src/logic/agentActions.test.ts`:

```ts
describe("agent limit states (spec 2026-09-27-single-agent-limit-live)", () => {
  const op = (reason: "capacity" | "resume", phase = "queued") =>
    ({ operation_id: "op_1", agent: "a", mode: "handoff", phase, reason });

  it("capacity pause: label, tone, Cancel only", () => {
    const a = makeAgent({ session: session("interrupted"), replacement: op("capacity") });
    expect(displayState(a)).toBe("capacity_paused");
    expect(stateLabel(displayState(a))).toBe("Paused: over the agent limit");
    expect(stateTone(displayState(a))).toBe("hollow");
    expect(labels(a)).toEqual(["cancel:Cancel"]);
  });

  it("queued resume: label, tone, Cancel only", () => {
    const a = makeAgent({ session: session("paused"), replacement: op("resume") });
    expect(displayState(a)).toBe("resume_queued");
    expect(stateLabel(displayState(a))).toBe("Queued: waiting for a free slot");
    expect(stateTone(displayState(a))).toBe("grey");
    expect(labels(a)).toEqual(["cancel:Cancel"]);
  });

  it("starting and reasonless replacements fall back to the session state", () => {
    expect(displayState(makeAgent({ session: session("spawning"), replacement: op("capacity", "starting") }))).toBe("spawning");
    expect(displayState(makeAgent({ session: session("running"), replacement: { operation_id: "op_2", agent: "a", mode: "handoff", phase: "stopping" } }))).toBe("running");
  });
});
```

If `C.cancel` is not `"Cancel"`, use the label that the existing
`"queued: Cancel"` case expects.

- [ ] **Step 2: Run the tests and watch them fail.**

Run: `cd web && pnpm exec vitest run src/logic/agentActions.test.ts`

Expected: FAIL. Either a type error (`replacement` is not in `AgentNode`) or
`expected "interrupted" to be "capacity_paused"`.

- [ ] **Step 3: Minimal implementation.**

`types.ts`: add this interface before `AgentNode`.

```ts
// The in-flight replacement operation (GET /api/state node.replacement).
// reason: "capacity" = paused to fit the agent limit; "resume" = a manual
// resume waiting for a free slot (spec 2026-09-27-single-agent-limit-live).
export interface AgentReplacement {
  operation_id: string;
  agent: string;
  mode: string;
  phase: string;
  request_key?: string;
  reason?: "capacity" | "resume";
  error?: string;
}
```

Then:

- In `AgentNode`, after `kind_reason`, add
  `replacement?: AgentReplacement;    // omitted when no operation is in flight`.
- In `Settings`, delete `max_agents_per_root: number;`.

`copy.ts` `SESSION_LABEL`: widen the key type to
`Record<SessionState | "queued" | "waiting" | "stale" | "preflight_failed" | "capacity_paused" | "resume_queued", string>`
and add:

```ts
  capacity_paused: "Paused: over the agent limit",
  resume_queued: "Queued: waiting for a free slot",
```

`logic/agentActions.ts`:

```ts
export type DisplayState = SessionState | "queued" | "waiting" | "stale" | "preflight_failed" | "capacity_paused" | "resume_queued";

export function displayState(a: AgentNode): DisplayState {
  // contracts §3.2: no session + a preflight error is "failed at preflight", whatever agent.state says
  const s = a.session;
  if (!s) return a.preflight_error !== null ? "preflight_failed" : "queued";
  const r = a.replacement;
  if (r?.reason && r.phase !== "starting") return r.reason === "capacity" ? "capacity_paused" : "resume_queued";
  if (a.state === "queued") return "queued";
  if (s.state === "running") return s.waiting ? "waiting" : s.stale ? "stale" : "running";
  return s.state;
}
```

`TONE`: add `capacity_paused: "hollow"` and `resume_queued: "grey"`. In the
`agentActions` switch, add:

```ts
    case "capacity_paused":
    case "resume_queued":
      return [cancel];
```

`mock/fixtures.ts`: delete `max_agents_per_root: 4,`.

- [ ] **Step 4: Run the tests and verify they pass.**

Run: `cd web && pnpm exec tsc --noEmit && pnpm test`

Expected: PASS. `grep -rn "max_agents_per_root" web/src` must be empty.

- [ ] **Step 5: Batch 2 gate.**

Run, in order:

1. `go test -race ./...`
2. `cd web && pnpm test`
3. `cd apps/menubar && swift test`
4. The spec's removal grep:

```bash
grep -rn "max_agents_per_root\|max_concurrent_subagents\|MaxAgentsPerRoot\|MaxConcurrentSubagents\|maxAgentsPerRoot\|maxConcurrentSubagents\|SubagentSlots\|NoAckChildren\|Subagent budget" --include='*.go' --include='*.swift' --include='*.ts' --include='*.tsx' --include='*.json' --include='*.sse' --include='*.md' . | grep -v "^./docs/\|node_modules"
```

   Expected: only these lines:
   - `internal/settings/settings.go` `retiredKeys`;
   - `internal/settings/settings_test.go` `TestPutDeletesRetiredLimitKeys`;
   - `internal/runtime/limits_test.go` `TestAdmitHasNoPerRootLimit`;
   - `internal/httpapi/config_test.go` (the stale-body check).

- [ ] **Step 6: Commit.**

```bash
git add web/src/types.ts web/src/copy.ts web/src/logic/agentActions.ts web/src/logic/agentActions.test.ts web/src/mock/fixtures.ts
git commit -m "feat(web): capacity-paused and queued-resume agent status; drop max_agents_per_root

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Spec coverage

| Spec item | Task |
|---|---|
| Decision 1 (one limit, removed everywhere) | 1, 2, 7, 8, 9 |
| Decision 2 (no migration, `Put` cleanup) | 2 |
| Decisions 3-5 (continuous rule, eligibility, idempotency, anti-flap) | 4 |
| Decision 6 (priority, order) | 5 |
| Decision 7 (manual resume queues) | 6 |
| Decision 8 (marker, wire reason) | 3, 8, 9 |
| Decision 9 (notifications) | 3, 4, 6 |
| Decision 10 (workflows) | 1 |
| Decision 11 (hook, NoAckChildren) | 1 |
| Decision 12 (Cancel only) | 8, 9 |
| Verification 1-5, 12 | 4 |
| Verification 6-7 | 5 |
| Verification 8-9 | 6 |
| Verification 10 | 1, 2 |
| Verification 11 | 3, 8, 9 |
