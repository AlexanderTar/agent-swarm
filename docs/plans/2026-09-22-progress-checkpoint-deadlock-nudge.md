# Progress-Checkpoint Deadlock Nudge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect a live child session that ended its turn right after a `progress` checkpoint and has sat idle-and-owing-nothing for 5+ minutes with a parent that never resolved it, and relay that fact once to the nearest live ancestor — breaking the "both sides silently waiting" deadlock that let s11-seams sit stuck for over an hour.

**Architecture:** Extend the existing `liveSessionRows`/`resolveAlive` reconcile pass (`internal/runtime/reconcile.go`) — already the home of `notifyNoAck`/`OnDepUnblocked`, the two closest precedents for "relay to whoever's above a stuck agent" — with one more check inside the `waiting` branch that today short-circuits with no further inspection. No new tables, no new background loop; it rides the existing 5s reconcile tick. A companion one-bullet update to `skills/swarm-orchestrator/SKILL.md` (and its embedded copy) tells the orchestrator this relay means "resolve it now," not "ack and keep waiting."

**Tech Stack:** Go, SQLite (via `internal/db`), existing `internal/runtime` package conventions (no new dependencies).

**Spec:** `docs/specs/2026-09-22-progress-checkpoint-deadlock-nudge.md`

## Global Constraints

- Trigger only on `kind = 'progress'` as the agent's most recent checkpoint this attempt — not `accepted`/`blocked`/`handoff`/`completed`/`failed`/`integrated`, and not "no checkpoint at all" (that's `notifyNoAck`'s territory).
- Threshold: **5 minutes** since that checkpoint's `created_at` (user-approved).
- Only fires when `ParentAgentID != ""` — a top-level orchestrator has nobody to relay to.
- Action is relay-only: enqueue one `relay` message with `payload.event == "progress_deadlock"` to `s.nearestLiveAncestor`. No daemon-authored `notify`/push to the human for this event.
- Dedup key is the triggering checkpoint's own id, via the new relay's `ReplyTo` field and a `reply_to = ?` count query (mirrors `alreadyRelayedForMessage`, `reconcile.go:923-928`) — not a payload `LIKE` scan, not a session-scoped guard.
- Relay payload shape: `{"event": "progress_deadlock", "agent": <name>, "item": <item key>, "checkpoint": {"summary": <string>, "next": [<string>, ...]}}` — same field names `WriteCheckpoint`'s own parent-relay already uses (`checkpoint.go:426-439`).
- Do not change `staleAfter`, `ackTimeout`, `maxFullDeliveries`, or any other existing constant.

---

### Task 1: Carry the last checkpoint through `liveSessionRows`

**Files:**
- Modify: `internal/runtime/reconcile.go:48-112` (`liveRow` struct, `liveSessionRows`)
- Test: `internal/runtime/reconcile_test.go` (new test, add near the other `liveSessionRows`/`liveRow`-adjacent tests — e.g. after `TestOrchestratorWithALiveChildIsNeverWaiting`)

**Interfaces:**
- Consumes: `Store.WriteCheckpoint` (existing, `internal/runtime/checkpoint.go:218`), `CheckpointKind` constants (`Progress`, etc., `internal/runtime/model.go:85-91`).
- Produces: `liveRow` gains five new fields other tasks depend on:
  ```go
  LastCheckpointID, LastCheckpointSummary string
  LastCheckpointKind                      CheckpointKind
  LastCheckpointAt                        *time.Time
  LastCheckpointNext                      []string
  ```
  `LastCheckpointID == ""` means the agent has never checkpointed this attempt (mirrors how `CompletedCheckpointAt == nil` already signals "no completed checkpoint yet").

- [ ] **Step 1: Write the failing test**

Add to `internal/runtime/reconcile_test.go`:

```go
func TestLiveSessionRowsIncludesLastCheckpoint(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	res, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress,
		Summary: "found two bugs", Next: []string{"HITL gap is out of scope"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.liveSessionRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got *liveRow
	for i := range rows {
		if rows[i].AgentID == w.ID {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatal("worker session not found in liveSessionRows")
	}
	if got.LastCheckpointID != res.CheckpointID {
		t.Fatalf("LastCheckpointID = %q, want %q", got.LastCheckpointID, res.CheckpointID)
	}
	if got.LastCheckpointKind != Progress {
		t.Fatalf("LastCheckpointKind = %q, want %q", got.LastCheckpointKind, Progress)
	}
	if got.LastCheckpointSummary != "found two bugs" {
		t.Fatalf("LastCheckpointSummary = %q", got.LastCheckpointSummary)
	}
	if len(got.LastCheckpointNext) != 1 || got.LastCheckpointNext[0] != "HITL gap is out of scope" {
		t.Fatalf("LastCheckpointNext = %v", got.LastCheckpointNext)
	}
	if got.LastCheckpointAt == nil {
		t.Fatal("LastCheckpointAt is nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/... -run TestLiveSessionRowsIncludesLastCheckpoint -v`
Expected: FAIL — compile error, `liveRow` has no field `LastCheckpointID` (etc.).

- [ ] **Step 3: Implement**

In `internal/runtime/reconcile.go`, extend the `liveRow` struct (after `CompletedCheckpointAt`):

```go
type liveRow struct {
	SessionID, AgentID, AgentName, TmuxName    string
	ItemID, ItemKey, RootItemID, ParentAgentID string
	Kind                                       AgentKind
	Role                                       Role
	State                                      SessionState
	Attempt                                    int
	Waiting                                    bool
	StartedAt                                  time.Time
	LastSeenAt                                 *time.Time
	CompletedCheckpointAt                      *time.Time
	LastCheckpointID, LastCheckpointSummary    string
	LastCheckpointKind                         CheckpointKind
	LastCheckpointAt                           *time.Time
	LastCheckpointNext                         []string
}
```

Replace `liveSessionRows` with:

```go
func (s *Store) liveSessionRows(ctx context.Context) ([]liveRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.agent_id, a.name, ses.tmux_name,
		a.item_id, i.key, a.root_item_id, COALESCE(a.parent_agent_id, ''), a.kind, a.role,
		ses.state, ses.attempt, ses.waiting, ses.started_at, ses.last_seen_at,
		(SELECT MAX(created_at) FROM checkpoints c
			WHERE c.agent_id = a.id AND c.attempt = ses.attempt AND c.kind = 'completed'
			  AND c.item_id = a.item_id),
		lc.id, lc.kind, lc.created_at, lc.summary, lc.next_json
		FROM sessions ses JOIN agents a ON a.id = ses.agent_id JOIN items i ON i.id = a.item_id
		LEFT JOIN checkpoints lc ON lc.id = (
			SELECT c.id FROM checkpoints c
			WHERE c.agent_id = a.id AND c.attempt = ses.attempt
			ORDER BY c.created_at DESC, c.rowid DESC LIMIT 1)
		WHERE ses.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []liveRow
	for rows.Next() {
		var r liveRow
		var kind, role, state string
		var waiting int
		var started int64
		var lastSeen, completedAt sql.NullInt64
		var lcID, lcKind, lcSummary, lcNext sql.NullString
		var lcAt sql.NullInt64
		if err := rows.Scan(&r.SessionID, &r.AgentID, &r.AgentName, &r.TmuxName,
			&r.ItemID, &r.ItemKey, &r.RootItemID, &r.ParentAgentID, &kind, &role,
			&state, &r.Attempt, &waiting, &started, &lastSeen, &completedAt,
			&lcID, &lcKind, &lcAt, &lcSummary, &lcNext); err != nil {
			return nil, err
		}
		r.Kind, r.Role, r.State = AgentKind(kind), Role(role), SessionState(state)
		r.Waiting = waiting != 0
		r.StartedAt = db.FromMillis(started)
		if lastSeen.Valid {
			t := db.FromMillis(lastSeen.Int64)
			r.LastSeenAt = &t
		}
		if completedAt.Valid {
			t := db.FromMillis(completedAt.Int64)
			r.CompletedCheckpointAt = &t
		}
		if lcID.Valid {
			r.LastCheckpointID = lcID.String
			r.LastCheckpointKind = CheckpointKind(lcKind.String)
			r.LastCheckpointSummary = lcSummary.String
			t := db.FromMillis(lcAt.Int64)
			r.LastCheckpointAt = &t
			if lcNext.Valid {
				json.Unmarshal([]byte(lcNext.String), &r.LastCheckpointNext)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

`encoding/json` is already imported in this file (used by `notifyNoAck` and others) — no import changes needed.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime/... -run TestLiveSessionRowsIncludesLastCheckpoint -v`
Expected: PASS

- [ ] **Step 5: Run the full existing reconcile suite to confirm no regression**

Run: `go test ./internal/runtime/... -run 'TestWaitingIsSetAndClearedAndNeverStale|TestOrchestratorWithALiveChildIsNeverWaiting|TestStaleAfterThirtyMinutesOfSilence|TestNoAck' -v`
Expected: all PASS (the new columns must not change any existing scan behavior).

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/reconcile.go internal/runtime/reconcile_test.go
git commit -m "feat(runtime): carry the agent's last checkpoint through liveSessionRows"
```

---

### Task 2: Detect the deadlock and relay once

**Files:**
- Modify: `internal/runtime/reconcile.go` (`resolveAlive` at `:576-618`; new `checkProgressDeadlock` and `progressDeadlockTimeout`, placed as a sibling to `notifyNoAck`)
- Test: `internal/runtime/reconcile_test.go` (new test, placed near `TestNoAckAfterTwoMinutesWithNoCheckpointNotifiesAndRelaysOnce`)

**Interfaces:**
- Consumes: `liveRow.LastCheckpointID/Kind/At/Summary/Next` (Task 1), `s.alreadyRelayedForMessage(ctx, id string) (bool, error)` (existing, `reconcile.go:923-928`), `s.nearestLiveAncestor(ctx, agentID string) (Agent, bool, error)` (existing, `reconcile.go:825`), `s.enqueue(ctx, tx, Message{...})` (existing, `inbox.go:45`), `s.tx(ctx, fn)` (existing, `model.go:397`).
- Produces: `func (s *Store) checkProgressDeadlock(ctx context.Context, r liveRow) error`, called from `resolveAlive`'s `waiting` branch. No other task calls it directly.

- [ ] **Step 1: Write the failing test**

Add to `internal/runtime/reconcile_test.go`:

```go
func TestProgressDeadlockRelaysAfterFiveMinutesIdle(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	tm.captures[orch.Name] = []string{"working…\n"} // orchestrator stays busy; w has no capture set, so idle by default
	s.Sync(ctx, wSes.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, w.ID)

	res, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress,
		Summary: "wired the seams", Next: []string{"needs a scope decision from the orchestrator"}})
	if err != nil {
		t.Fatal(err)
	}

	at.Advance(4*time.Minute + 59*time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var before int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"progress_deadlock"%'`, orch.ID).Scan(&before)
	if before != 0 {
		t.Fatalf("relay before 5 minutes = %d, want 0", before)
	}

	at.Advance(2 * time.Second) // total 5:01
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var payload, replyTo string
	if err := s.DB.QueryRowContext(ctx, `SELECT payload_json, COALESCE(reply_to, '') FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' AND payload_json LIKE '%"event":"progress_deadlock"%'`,
		orch.ID).Scan(&payload, &replyTo); err != nil {
		t.Fatalf("expected exactly one progress_deadlock relay: %v", err)
	}
	if replyTo != res.CheckpointID {
		t.Fatalf("reply_to = %q, want checkpoint id %q", replyTo, res.CheckpointID)
	}
	if !strings.Contains(payload, `"wired the seams"`) ||
		!strings.Contains(payload, `"needs a scope decision from the orchestrator"`) {
		t.Fatalf("payload missing checkpoint content: %s", payload)
	}
}
```

`strings` is already imported in `reconcile_test.go` (check the file's import block; add it if this is the first use in that file).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/... -run TestProgressDeadlockRelaysAfterFiveMinutesIdle -v`
Expected: FAIL — no `progress_deadlock` relay exists yet (query returns `sql.ErrNoRows`).

- [ ] **Step 3: Implement**

In `internal/runtime/reconcile.go`, change the `waiting` branch in `resolveAlive` (currently `reconcile.go:616-618`):

```go
	if waiting {
		if err := s.checkProgressDeadlock(ctx, r); err != nil {
			return err
		}
		return nil // M6: a waiting session is never stale
	}
```

Add the new constant near `ackTimeout` (`reconcile.go:23`):

```go
// progressDeadlockTimeout is how long a waiting session gets after its most
// recent checkpoint -- if that checkpoint was "progress" -- before the
// daemon assumes it's stuck on an unresolved ask and relays to its parent.
// See docs/specs/2026-09-22-progress-checkpoint-deadlock-nudge.md.
const progressDeadlockTimeout = 5 * time.Minute
```

Add the new function as a sibling to `notifyNoAck` (after it, e.g. right before `OnDepUnblocked`):

```go
// checkProgressDeadlock relays once to the nearest live ancestor when a
// child has gone idle-and-owes-nothing right after a progress checkpoint --
// the daemon-visible signature of a child waiting on an answer it never
// formally asked for (a "next" note aimed at the orchestrator, not a
// swarm_send question or a blocked checkpoint). See docs/specs/
// 2026-09-22-progress-checkpoint-deadlock-nudge.md.
func (s *Store) checkProgressDeadlock(ctx context.Context, r liveRow) error {
	if r.ParentAgentID == "" || r.LastCheckpointID == "" || r.LastCheckpointKind != Progress {
		return nil
	}
	// r.LastCheckpointAt is always set together with r.LastCheckpointID (both
	// come from the same LEFT JOIN row in liveSessionRows).
	if s.Now().Sub(*r.LastCheckpointAt) < progressDeadlockTimeout {
		return nil
	}
	already, err := s.alreadyRelayedForMessage(ctx, r.LastCheckpointID)
	if err != nil {
		return err
	}
	if already {
		return nil
	}
	ancestor, ok, err := s.nearestLiveAncestor(ctx, r.AgentID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"event": "progress_deadlock",
		"agent": r.AgentName, "item": r.ItemKey,
		"checkpoint": map[string]any{"summary": r.LastCheckpointSummary, "next": r.LastCheckpointNext}})
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon",
			ToAgentID: ancestor.ID, RootItemID: r.RootItemID, ItemID: r.ItemID,
			ReplyTo: r.LastCheckpointID, Payload: payload})
		return err
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime/... -run TestProgressDeadlockRelaysAfterFiveMinutesIdle -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/reconcile.go internal/runtime/reconcile_test.go
git commit -m "feat(runtime): relay a progress-checkpoint deadlock to the nearest live ancestor"
```

---

### Task 3: Edge-case coverage

**Files:**
- Test only: `internal/runtime/reconcile_test.go` (adds tests; no production code changes — Task 2's implementation already covers every guard clause exercised here).

**Interfaces:**
- Consumes: everything from Tasks 1-2. No new production interfaces.

- [ ] **Step 1: Write the guard-clause tests**

Add to `internal/runtime/reconcile_test.go`:

```go
func TestProgressDeadlockNeverFiresForOtherCheckpointKinds(t *testing.T) {
	cases := []struct {
		name string
		in   CheckpointInput
	}{
		{"accepted", CheckpointInput{Kind: Accepted, Summary: "starting"}},
		{"blocked", CheckpointInput{Kind: BlockedCkp, Summary: "stuck", Blockers: []string{"need a decision"}}},
		{"completed", CheckpointInput{Kind: CompletedCkp, Summary: "done",
			Verification: []Verify{{Cmd: "go test ./...", OK: true}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, tm, at := clockStore(t)
			ctx := context.Background()
			orch, w, wSes := worker(t, s)
			panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"},
				Pane{Session: orch.Name, Command: "swarm-fake-agent"})
			tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
			tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
			tm.captures[orch.Name] = []string{"working…\n"}
			s.Sync(ctx, wSes.ID, nil, 20)
			s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, w.ID)
			if _, err := s.WriteCheckpoint(ctx, wSes.ID, tc.in); err != nil {
				t.Fatal(err)
			}
			at.Advance(6 * time.Minute)
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			var n int
			s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
				AND payload_json LIKE '%"event":"progress_deadlock"%'`, orch.ID).Scan(&n)
			if n != 0 {
				t.Fatalf("%s: progress_deadlock relay count = %d, want 0", tc.name, n)
			}
		})
	}
}

func TestProgressDeadlockNeverFiresWithNoCheckpointYet(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	tm.captures[orch.Name] = []string{"working…\n"}
	s.Sync(ctx, wSes.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, w.ID)

	at.Advance(6 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"progress_deadlock"%'`, orch.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("no checkpoint at all is notifyNoAck's territory, not this one: count = %d", n)
	}
}

func TestProgressDeadlockNeverFiresForTopLevelOrchestrator(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	orchSes, _ := s.LatestSession(ctx, orch.ID)
	s.Sync(ctx, orchSes.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, orchSes.ID, CheckpointInput{Kind: Progress, Summary: "surveying the epic"}); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": orchSes.ID}

	at.Advance(6 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay'
		AND payload_json LIKE '%"event":"progress_deadlock"%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("a top-level orchestrator has nobody to relay to: count = %d", n)
	}
}

func TestProgressDeadlockFiresOncePerCheckpointThenAgainForANewOne(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	tm.captures[orch.Name] = []string{"working…\n"}
	s.Sync(ctx, wSes.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, w.ID)

	ck1, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "first stall"})
	if err != nil {
		t.Fatal(err)
	}
	at.Advance(6 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	// two more ticks on the same still-waiting session: must not re-relay ck1
	at.Advance(5 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(5 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var n1 int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay' AND reply_to = ?`,
		ck1.CheckpointID).Scan(&n1)
	if n1 != 1 {
		t.Fatalf("ck1 relay count = %d, want exactly 1 across repeated ticks", n1)
	}

	// a fresh progress checkpoint that also stalls gets its own relay
	ck2, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "second stall"})
	if err != nil {
		t.Fatal(err)
	}
	at.Advance(6 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var n2 int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay' AND reply_to = ?`,
		ck2.CheckpointID).Scan(&n2)
	if n2 != 1 {
		t.Fatalf("ck2 relay count = %d, want exactly 1", n2)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they pass**

Run: `go test ./internal/runtime/... -run TestProgressDeadlock -v`
Expected: all PASS (Task 2's implementation already satisfies every case here — these tests characterize and lock in that behavior, they don't drive new production code).

- [ ] **Step 3: Commit**

```bash
git add internal/runtime/reconcile_test.go
git commit -m "test(runtime): cover progress-checkpoint deadlock guard clauses"
```

---

### Task 4: Skill update and final verification

**Files:**
- Modify: `skills/swarm-orchestrator/SKILL.md:20-21` (insert new bullet between the existing `dependency_added` and `swarm_send` bullets)
- Modify: `internal/install/skills/swarm-orchestrator/SKILL.md` — identical edit (verified hand-duplicated copy, not generated; `go:embed` in `internal/install/skills.go:12`)

**Interfaces:** None — documentation only.

- [ ] **Step 1: Edit both SKILL.md copies identically**

In `skills/swarm-orchestrator/SKILL.md`, insert this new line immediately after the existing `dependency_added` bullet (currently line 20) and before the `swarm_send refuses synchronously...` bullet (currently line 21):

```markdown
- A relay with `event: "progress_deadlock"` means a child ended its turn after a `progress` checkpoint and has been idle 5+ minutes with nothing formally open — acking this relay is not enough, and neither is re-reading it and continuing to wait. Read the checkpoint's `summary`/`next` in the payload and resolve it directly with `swarm_send`: answer whatever it's asking, or if it reads as finished, tell it plainly to write its `completed` checkpoint now. Don't wait for a `completed` checkpoint that will never come without this.
```

Apply the exact same insertion to `internal/install/skills/swarm-orchestrator/SKILL.md` at the same location (both files are currently byte-identical — keep them that way).

- [ ] **Step 2: Verify both copies stay identical**

Run: `diff skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md`
Expected: no output (files identical).

- [ ] **Step 3: Commit the skill update**

```bash
git add skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md
git commit -m "docs(skills): tell the orchestrator to resolve a progress_deadlock relay, not just ack it"
```

- [ ] **Step 4: Full verification**

Run, in order:
```bash
go build ./...
go vet ./...
gofmt -l internal/runtime/reconcile.go internal/runtime/reconcile_test.go
go test ./internal/runtime/... -v
go test ./internal/install/...
```
Expected: `go build`/`go vet` clean; `gofmt -l` prints nothing; both test runs green, including every pre-existing `resolveAlive`/`liveSessionRows`/`notifyNoAck`/`Waiting`-named test (no regressions).

No commit for this step unless a verification command surfaces something to fix — if it does, fix it, re-run, then commit the fix separately with its own message before moving on.
