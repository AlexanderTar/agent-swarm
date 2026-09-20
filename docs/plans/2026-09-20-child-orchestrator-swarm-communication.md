# Implementation Plan: Child-to-Orchestrator Communication Protocol via Swarm

**Date:** 2026-09-20  
**Spec:** `docs/specs/2026-09-20-child-orchestrator-swarm-communication.md`  
**Working Directory:** `/Users/alexandertar/GitHub/agent-swarm-comm`  

---

## Task 1: Resume Relay & Immediate Wake

### Objective
Ensure that when a paused agent is resumed via `Store.Resume`, the daemon enqueues an immediate `relay` message with `event: "resumed"` to the nearest live ancestor.

### Files
- `internal/runtime/inbox.go`
- `internal/runtime/pause.go`
- `internal/runtime/pause_test.go`

### Step 1: Write failing test in `internal/runtime/pause_test.go`
Add `TestResumeEmitsRelayToParent`:
```go
func TestResumeEmitsRelayToParent(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	_, orch, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)

	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused', provider_session_id = 'p1' WHERE id = ?`, wSes.ID)

	if _, err := s.Resume(ctx, w.Name, "", ""); err != nil {
		t.Fatal(err)
	}

	var count int
	var payloadStr string
	var wakeClass string
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(payload_json, ''), COALESCE(wake_class, '')
		FROM messages WHERE to_agent_id = ? AND kind = 'relay' AND payload_json LIKE '%"event":"resumed"%'`,
		orch.ID).Scan(&count, &payloadStr, &wakeClass)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 resumed relay, got %d", count)
	}
	if wakeClass != "immediate" {
		t.Fatalf("expected wake_class immediate, got %s", wakeClass)
	}
	if !strings.Contains(payloadStr, w.Name) {
		t.Fatalf("expected payload to contain agent name %s: %s", w.Name, payloadStr)
	}
}
```

### Step 2: Run test and watch fail
```bash
go test -v -run TestResumeEmitsRelayToParent ./internal/runtime/...
```
Expected: FAIL (count = 0).

### Step 3: Implement changes
1. In `internal/runtime/inbox.go`:
   Add `"resumed"` to `ImmediateRelayEvents`:
   ```go
   var ImmediateRelayEvents = []string{"accepted", "completed", "failed", "blocked", "crashed",
       "interrupted", "paused", "resumed", "dependency_added", "spawn_failed", "no_ack", "no_recipient"}
   ```
2. In `internal/runtime/pause.go` (`Store.Resume`):
   Fetch `nearestLiveAncestor` *before* the `IdemTx` transaction block to avoid SQLite pool deadlocks:
   ```go
   ancestor, ancestorOk, _ := s.nearestLiveAncestor(ctx, a.ID)
   if _, err := IdemTx(ctx, s, sessionID, requestID, "swarm_control", &out, func(tx *sql.Tx) error {
       if a.State != AgentActive {
           if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'active' WHERE id = ?`, a.ID); err != nil {
               return err
           }
       }
       if err := s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID); err != nil {
           return err
       }
       a.State = AgentActive
       out = a

       if ancestorOk {
           var itemKey string
           _ = tx.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.ItemID).Scan(&itemKey)
           payload, err := json.Marshal(map[string]any{
               "event": "resumed",
               "agent": a.Name,
               "item":  itemKey,
           })
           if err == nil {
               _, _ = s.enqueue(ctx, tx, Message{
                   Kind:       "relay",
                   Origin:     "daemon",
                   ToAgentID:  ancestor.ID,
                   RootItemID: a.RootItemID,
                   ItemID:     a.ItemID,
                   Payload:    payload,
               })
           }
       }
       return nil
   }); err != nil {
       return Agent{}, err
   }
   ```

### Step 4: Run test and verify pass
```bash
go test -v -run TestResumeEmitsRelayToParent ./internal/runtime/...
```

---

## Task 2: Enriched Pause Relay & Fallback in Reconciler

### Objective
Ensure `relayPaused` includes `item: itemKey` and `summary: latestCheckpointSummary`, routes to `nearestLiveAncestor`, and fires in `resolveDead` if no pause relay was recorded.

### Files
- `internal/runtime/pause.go`
- `internal/runtime/reconcile.go`
- `internal/runtime/pause_test.go`
- `internal/runtime/reconcile_test.go`

### Step 1: Write failing test in `internal/runtime/pause_test.go`
Add `TestRelayPausedIncludesItemAndSummary`:
```go
func TestRelayPausedIncludesItemAndSummary(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	_, orch, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)

	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'pause_requested' WHERE id = ?`, wSes.ID)
	_, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{
		Kind:    Handoff,
		Summary: "Holding for review",
	})
	if err != nil {
		t.Fatal(err)
	}

	var payloadStr string
	err = s.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' AND payload_json LIKE '%"event":"paused"%'`,
		orch.ID).Scan(&payloadStr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payloadStr, `"item":`) || !strings.Contains(payloadStr, "Holding for review") {
		t.Fatalf("expected payload to contain item and summary: %s", payloadStr)
	}
}
```

### Step 2: Run test and watch fail
```bash
go test -v -run TestRelayPausedIncludesItemAndSummary ./internal/runtime/...
```

### Step 3: Implement changes
1. In `internal/runtime/pause.go`:
   Refactor `relayPaused` to use `tx` directly for `itemKey` and `summary` queries:
   ```go
   func (s *Store) relayPaused(ctx context.Context, tx *sql.Tx, agentID string) error {
       ancestor, ok, err := s.nearestLiveAncestor(ctx, agentID)
       if err != nil || !ok {
           return err
       }
       a, err := s.agentByIDTx(ctx, tx, agentID)
       if err != nil {
           return err
       }
       var itemKey, summary string
       _ = tx.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.ItemID).Scan(&itemKey)
       _ = tx.QueryRowContext(ctx, `SELECT summary FROM checkpoints WHERE agent_id = ?
           ORDER BY created_at DESC, rowid DESC LIMIT 1`, a.ID).Scan(&summary)
       payload, err := json.Marshal(map[string]any{
           "event":   "paused",
           "agent":   a.Name,
           "item":    itemKey,
           "summary": summary,
       })
       if err != nil {
           return err
       }
       _, err = s.enqueue(ctx, tx, Message{
           Kind:       "relay",
           Origin:     "daemon",
           ToAgentID:  ancestor.ID,
           RootItemID: a.RootItemID,
           ItemID:     a.ItemID,
           Payload:    payload,
       })
       return err
   }
   ```
2. In `internal/runtime/reconcile.go` (`resolveDead` where `r.State == Stopping`):
   Check `alreadyRelayed` and emit `s.relayPaused` if not already sent:
   ```go
   if r.State == Stopping {
       return s.tx(ctx, func(tx *sql.Tx) error {
           if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'paused', ended_at = ? WHERE id = ?`,
               db.Millis(now), r.SessionID); err != nil {
               return err
           }
           if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.paused", AgentName: r.AgentName, ItemKey: r.ItemKey,
               Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}}); err != nil {
               return err
           }
           already, err := s.alreadyRelayed(ctx, r.ParentAgentID, r.ItemID, "paused", r.StartedAt)
           if err == nil && !already {
               _ = s.relayPaused(ctx, tx, r.AgentID)
           }
           return nil
       })
   }
   ```

### Step 4: Run tests and verify pass
```bash
go test -v -run TestRelayPausedIncludesItemAndSummary ./internal/runtime/...
```

---

## Task 3: Crash Diagnostics in Relays

### Objective
Attach `exit_code` and terminal `tail` to `crashed` relays in `resolveDead`.

### Files
- `internal/runtime/reconcile.go`
- `internal/runtime/reconcile_test.go`

### Step 1: Write failing test in `internal/runtime/reconcile_test.go`
Add `TestCrashedRelayIncludesExitCodeAndTail`:
```go
func TestCrashedRelayIncludesExitCodeAndTail(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	_, orch, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)

	tm.dead[w.Name] = true
	tm.deadStatus[w.Name] = 137 // e.g. SIGKILL / OOM
	tm.capture[w.Name] = "line 1\nline 2\nfatal: out of memory"

	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	var payloadStr string
	err := s.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' AND payload_json LIKE '%"event":"crashed"%'`,
		orch.ID).Scan(&payloadStr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payloadStr, `"exit_code":137`) || !strings.Contains(payloadStr, "fatal: out of memory") {
		t.Fatalf("expected exit_code and tail in crashed relay payload: %s", payloadStr)
	}
}
```

### Step 2: Run test and watch fail
```bash
go test -v -run TestCrashedRelayIncludesExitCodeAndTail ./internal/runtime/...
```

### Step 3: Implement changes in `internal/runtime/reconcile.go`
In `resolveDead`, capture the last 10 lines of the pane and find `nearestLiveAncestor` *before* the `default:` transaction block:
```go
	default: // crashed: no terminal checkpoint in this attempt (§10.6)
		var tail string
		if capture, err := s.Tmux.Capture(ctx, r.TmuxName, 10); err == nil {
			tail = strings.TrimSpace(capture)
		}
		ancestor, ancestorOk, _ := s.nearestLiveAncestor(ctx, r.AgentID)

		return s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'crashed', exit_code = ?,
				ended_at = ? WHERE id = ?`, exitCode, db.Millis(now), r.SessionID); err != nil {
				return err
			}
			if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.crashed", AgentName: r.AgentName,
				ItemKey: r.ItemKey, Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}}); err != nil {
				return err
			}
			if ancestorOk {
				payload, err := json.Marshal(map[string]any{
					"event":     "crashed",
					"agent":     r.AgentName,
					"item":      r.ItemKey,
					"exit_code": exitCode,
					"tail":      tail,
				})
				if err == nil {
					_, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: ancestor.ID,
						RootItemID: r.RootItemID, ItemID: r.ItemID, Payload: payload})
					if err != nil {
						return err
					}
				}
			}
			return nil
		})
```

### Step 4: Run test and verify pass
```bash
go test -v -run TestCrashedRelayIncludesExitCodeAndTail ./internal/runtime/...
```

---

## Task 4: Skill & Instruction Updates

### Objective
Update `skills/swarm/SKILL.md` and `skills/swarm-orchestrator/SKILL.md` to establish hierarchical question escalation, resume confirmation, and orchestrator lifecycle actions (`paused`, `resumed`, `crashed` diagnostics, `cancel`).

### Files
- `skills/swarm/SKILL.md`
- `skills/swarm-orchestrator/SKILL.md`

### Step 1: Update `skills/swarm/SKILL.md`
- In Rule 4: Add resume confirmation instruction:
  "On RESUME: call `swarm_sync` first to review your assignment and last checkpoint. Send `swarm_send` to `to: "parent"` (`kind: "finding"`, body: `"Resumed work on <KEY>; proceeding with <next step>"`) to confirm active status to your orchestrator."
- In Rule 5: Add hierarchical question escalation:
  "Have a question about scope, interface, or design? If you have a parent orchestrator, ask it first with `swarm_send` (`to: "parent"`, `kind: "question"`, `body: "..."`) and end your turn to wait for the answer. Only top-level orchestrators (or questions directly requiring human authorization) ask the user via native question tools or `swarm_ask`."

### Step 2: Update `skills/swarm-orchestrator/SKILL.md`
- Add instructions for answering child questions with `swarm_send(to: <child>, kind: "answer", reply_to: <msg_id>, body: "...")`.
- Document handling `paused`, `resumed`, and `crashed` (with `exit_code` and `tail`).
- Document `swarm_control cancel` (`action: "cancel"`) for aborting subagents.

### Step 3: Synchronize and verify skills
```bash
make skills-sync
```

---

## Task 5: Verification & Installation

### Step 1: Run full test suite
```bash
go test ./...
```

### Step 2: Commit and merge
```bash
git add .
git commit -m "feat(comm): child-orchestrator lifecycle relays, crash diagnostics, and hierarchical questioning"
```

### Step 3: Install daemon and skills
```bash
make install-daemon && bin/swarm install -yes
```
