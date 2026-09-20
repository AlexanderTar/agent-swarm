# Specification: Child-to-Orchestrator Communication Protocol via Swarm

**Date:** 2026-09-20  
**Status:** Draft  
**Author:** Antigravity  

---

## 1. Context

In `agent-swarm`, orchestrators delegate tasks to child subagents (`RoleCoder`, `RoleDebugger`, `RoleMechanical`, `RoleReviewer`). However, analysis against industry-standard multi-agent communication protocols (A2A, FIPA ACL, Actor supervision trees) reveals critical gaps in bidirectional communication between orchestrators and their children:

1. **Resume Lifecycle Black Hole**: When an agent resumes execution via `Store.Resume`, the daemon enqueues zero relays to the parent orchestrator, and `"resumed"` is absent from `ImmediateRelayEvents`. The orchestrator is left sleeping and unaware that its child is active again.
2. **Pause Context Degradation**: `relayPaused` enqueues only `{"event": "paused", "agent": a.Name}`, omitting the item key and checkpoint summary. Furthermore, the `handoff` checkpoint is categorized as `deferred`, preventing it from waking an idle orchestrator. If an agent transitions to `paused` through timeout or interrupt without a checkpoint, no relay is emitted.
3. **Escalation Inversion (Child Bypasses Orchestrator)**: `skills/swarm/SKILL.md` directs child agents to ask the human user directly via native question tools or `swarm_ask`. The orchestrator holding the high-level plan is completely bypassed, causing noise in the user's HITL inbox.
4. **Crash Diagnostics Void**: The `crashed` relay only carries `{"event": "crashed", "agent": ..., "item": ...}`, omitting the process exit code and terminal output tail, forcing the orchestrator to guess why the child died.
5. **Orchestrator Cancellation & Lifecycle Documentation**: While `Store.Cancel` exists in the daemon, `skills/swarm-orchestrator/SKILL.md` does not document `swarm_control cancel`, leaving orchestrators unaware of how to cleanly abort runaway or obsolete subagents.

This specification closes these gaps across the runtime daemon (`internal/runtime/`), agent skills (`skills/swarm/`, `skills/swarm-orchestrator/`), and MCP server.

---

## 2. Locked Decisions

1. **Daemon-Mediated Relays**: Lifecycle transitions (paused, resumed, crashed, no_ack, dependency_added) are daemon-originated relays (`Kind: "relay"`, `Origin: "daemon"`) delivered to the nearest live ancestor (`nearestLiveAncestor`).
2. **Immediate Wake for Resumed**: `"resumed"` is added to `ImmediateRelayEvents` in `inbox.go` so orchestrators are immediately woken when a child resumes.
3. **Enriched Payloads**:
   - `resumed`: `{"event": "resumed", "agent": "<name>", "item": "<KEY>"}`.
   - `paused`: `{"event": "paused", "agent": "<name>", "item": "<KEY>", "summary": "<summary>"}`.
   - `crashed`: `{"event": "crashed", "agent": "<name>", "item": "<KEY>", "exit_code": <int|null>, "tail": "<string>"}`.
4. **Hierarchical Escalation**: Child agents must query their orchestrator (`to: "parent"`, `kind: "question"`) for requirements, interfaces, and design decisions. Only top-level orchestrators (or questions unresolvable by the orchestrator) escalate to the human user.
5. **No Schema Migration Needed**: All changes use existing tables (`messages`, `sessions`, `agents`, `checkpoints`) and message envelopes.

---

## 3. Runtime & Daemon Changes

### 3.1 `internal/runtime/inbox.go`
Add `"resumed"` to `ImmediateRelayEvents`:
```go
var ImmediateRelayEvents = []string{
	"accepted", "completed", "failed", "blocked", "crashed",
	"interrupted", "paused", "resumed", "dependency_added", "spawn_failed", "no_ack", "no_recipient",
}
```

### 3.2 `internal/runtime/pause.go`
1. **In `Store.Resume`**:
   Fetch `nearestLiveAncestor` *before* entering `IdemTx` to avoid SQLite connection pool deadlocks:
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
2. **In `s.relayPaused`**:
   Use `tx` directly for `itemKey` and `summary` queries to avoid transaction lock starvation:
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

### 3.3 `internal/runtime/reconcile.go`
1. **In `resolveDead` (Stopping -> Paused)**:
   When an agent in `Stopping` state is reconciled as dead:
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
2. **In `resolveDead` (Crashed)**:
   Capture the last 10 lines of the pane and find `nearestLiveAncestor` *before* the transaction block:
   ```go
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
               _, err = s.enqueue(ctx, tx, Message{
                   Kind:       "relay",
                   Origin:     "daemon",
                   ToAgentID:  ancestor.ID,
                   RootItemID: r.RootItemID,
                   ItemID:     r.ItemID,
                   Payload:    payload,
               })
               if err != nil {
                   return err
               }
           }
       }
       return nil
   })
   ```

---

## 4. Skill & Instruction Updates

### 4.1 `skills/swarm/SKILL.md`
- **Rule 4 (Pause & Resume)**:
  - On a PAUSE: Stop work, do not start new tool calls, write `swarm_checkpoint` (`kind: "handoff"`) with `summary`, `next`, `blockers`, and `git`, then end turn.
  - On RESUME: Call `swarm_sync` first. Check the assignment and last checkpoint. Send `swarm_send` to `to: "parent"` (`kind: "finding"`, body: `"Resumed work on <KEY>; proceeding with <next step>"`) to confirm active status to your orchestrator.
- **Rule 5 (Questions & Escalation)**:
  - If you have a parent orchestrator: Send questions about task scope, interfaces, or approach to `to: "parent"` with `swarm_send` (`kind: "question"`). Keep working on independent parts or end turn to wait for the answer.
  - Only top-level orchestrators (or when instructed by the orchestrator) ask the human user directly via native question tools or `swarm_ask`.

### 4.2 `skills/swarm-orchestrator/SKILL.md`
- **Child Questions**: When a child sends a `question` message, respond using `swarm_send(to: <child>, kind: "answer", reply_to: <msg_id>, body: "...")`. If user clarification is needed, ask the user and forward the response.
- **Child Lifecycle Relays**:
  - `event: "paused"`: Child has paused; read handoff checkpoint via `swarm_read`.
  - `event: "resumed"`: Child has resumed work; session is active.
  - `event: "crashed"`: Child crashed; inspect `exit_code` and `tail` in the relay payload before deciding to retry (`swarm_control retry`) or reassign.
  - `swarm_control cancel`: Use `action: "cancel"` to abort a runaway or obsolete child agent.

---

## 5. File List

| File | Action | Purpose |
|------|--------|---------|
| `internal/runtime/inbox.go` | Modify | Add `"resumed"` to `ImmediateRelayEvents`. |
| `internal/runtime/pause.go` | Modify | Emit `resumed` relay in `Store.Resume`; enrich `relayPaused` with `item`, `summary`, and `nearestLiveAncestor`. |
| `internal/runtime/reconcile.go` | Modify | Attach `exit_code` and `tail` to `crashed` relay; ensure `paused` relay in `resolveDead`. |
| `internal/runtime/pause_test.go` | Modify | Add tests for `resumed` relay and enriched `paused` relay. |
| `internal/runtime/reconcile_test.go` | Modify | Add tests for `crashed` relay diagnostics and fallback `paused` relay. |
| `skills/swarm/SKILL.md` | Modify | Add child resume confirmation and hierarchical questioning. |
| `skills/swarm-orchestrator/SKILL.md` | Modify | Document handling of `paused`, `resumed`, `crashed` diagnostics, and `swarm_control cancel`. |

---

## 6. Verification Scenarios

1. **Resume Relay Verification**:
   - Pause a worker session.
   - Call `s.Resume(ctx, worker.Name, "", "")`.
   - Verify parent agent's inbox receives a relay message with `event: "resumed"`, `agent: worker.Name`, and `item: itemKey`.
   - Verify `WakeClass` is `"immediate"`.
2. **Paused Relay Verification**:
   - Write a `handoff` checkpoint on a pausing worker.
   - Verify parent agent receives `event: "paused"` with `item: itemKey` and `summary: "<summary>"`.
3. **Crash Diagnostics Relay Verification**:
   - Simulate a dead worker pane with exit code `1` and terminal text.
   - Run `s.Reconcile(ctx)`.
   - Verify parent agent receives `event: "crashed"` containing `exit_code: 1` and `tail` matching the pane capture.
4. **Hierarchical Questioning & Skill Sync**:
   - Run `make skills-sync` and `bin/swarm install -yes`.
   - Verify deployed skills match the updated rules.
