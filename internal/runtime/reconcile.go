package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

const staleAfter = 30 * time.Minute
const killCompletedAfter = 60 * time.Second

// ackTimeout is how long a freshly spawned session gets to write its first
// checkpoint of any kind before the daemon raises agent.no_ack to its parent
// (§10.6's ack-timeout). A child stuck before its first swarm_sync, or one
// that crashed at startup in a way that never trips resolveDead because its
// pane is still alive, otherwise leaves its parent waiting on a wake event
// that will never come.
const ackTimeout = 2 * time.Minute

// queryIDs runs a query returning one string column per row. Several callers
// across pause.go and reconcile.go collect a plain id/name list this way; one
// shared helper keeps the row-scan-close boilerplate (and its error checks)
// in one place instead of copied at every call site.
func (s *Store) queryIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// liveRow is one live session, joined with enough of its agent and item to
// resolve it against its tmux pane (§10.6).
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
}

// lastActivity is the most recent evidence of life: a hook call or sync
// (last_seen_at) once there has been one, otherwise when the session started.
// A1's 30-minute silence window has to start somewhere for a session that has
// never synced at all.
func (r liveRow) lastActivity() time.Time {
	if r.LastSeenAt != nil {
		return *r.LastSeenAt
	}
	return r.StartedAt
}

// liveSessionRows loads every session in a live state (§5's LiveStates).
func (s *Store) liveSessionRows(ctx context.Context) ([]liveRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.agent_id, a.name, ses.tmux_name,
		a.item_id, i.key, a.root_item_id, COALESCE(a.parent_agent_id, ''), a.kind, a.role,
		ses.state, ses.attempt, ses.waiting, ses.started_at, ses.last_seen_at,
		(SELECT MAX(created_at) FROM checkpoints c
			WHERE c.agent_id = a.id AND c.attempt = ses.attempt AND c.kind = 'completed')
		FROM sessions ses JOIN agents a ON a.id = ses.agent_id JOIN items i ON i.id = a.item_id
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
		if err := rows.Scan(&r.SessionID, &r.AgentID, &r.AgentName, &r.TmuxName,
			&r.ItemID, &r.ItemKey, &r.RootItemID, &r.ParentAgentID, &kind, &role,
			&state, &r.Attempt, &waiting, &started, &lastSeen, &completedAt); err != nil {
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
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reconcile applies the §10.6 table once. The daemon runs it every 5 s.
func (s *Store) Reconcile(ctx context.Context) error {
	panes, err := s.Tmux.Panes(ctx)
	if err != nil {
		return err
	}
	byName := map[string]Pane{}
	for _, p := range panes {
		byName[p.Session] = p
	}
	live, err := s.liveSessionRows(ctx)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, r := range live {
		known[r.TmuxName] = true
		p, ok := byName[r.TmuxName]
		if ok {
			// a pane whose SWARM_SESSION is a different session belongs to a newer
			// generation; treat ours as gone.
			if env, err := s.Tmux.Env(ctx, r.TmuxName, "SWARM_SESSION"); err == nil && env != r.SessionID {
				ok = false
			}
		}
		if ok && !p.Dead {
			// P0-crash-3: record this as the last tick that actually confirmed
			// the pane, so a later single-tick miss has a recent basis to grace
			// from instead of falling back to StartedAt (see resolveDead).
			s.setLastAlive(r.SessionID, s.Now())
			if err := s.resolveAlive(ctx, r, p); err != nil {
				return err
			}
			continue
		}
		if err := s.resolveDead(ctx, r, p, ok); err != nil {
			return err
		}
	}
	terminalTmux, err := s.terminalTmuxNames(ctx)
	if err != nil {
		return err
	}
	for _, p := range panes {
		if known[p.Session] {
			continue
		}
		if terminalTmux[p.Session] {
			// A failed/crashed/cancelled session's own pane, not some other
			// process squatting on the name: a dead one is just leftover
			// tmux bookkeeping, safe to clean up; a live one is deliberately
			// left running (P0-crash-1's "let a human inspect it"), so it
			// must not be flagged unknown either.
			if p.Dead {
				if err := s.Tmux.Kill(ctx, p.Session); err != nil {
					return err
				}
			}
			continue
		}
		if err := s.notifyUnknownTmux(ctx, p.Session); err != nil {
			return err
		}
	}
	if err := s.TickPause(ctx); err != nil {
		return err
	}
	if err := s.DrainQueue(ctx); err != nil {
		return err
	}
	return s.sweepFinishedRoots(ctx)
}

// terminalCheckpointKind is the most recent completed/failed checkpoint for
// this attempt, if any.
func (s *Store) terminalCheckpointKind(ctx context.Context, agentID string, attempt int) (CheckpointKind, bool, error) {
	var kind string
	err := s.DB.QueryRowContext(ctx, `SELECT kind FROM checkpoints WHERE agent_id = ? AND attempt = ?
		AND kind IN ('completed', 'failed') ORDER BY created_at DESC, rowid DESC LIMIT 1`, agentID, attempt).Scan(&kind)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return CheckpointKind(kind), true, nil
}

// hasAnyCheckpoint reports whether this attempt has written a checkpoint of
// any kind yet — the daemon's only signal that a freshly spawned session is
// alive and talking. Any kind counts, not just "accepted": a session that
// leads with progress or blocked has still proven it is up, and gating on
// "accepted" specifically would raise a false no_ack against a live agent
// that simply did not lead with that one kind.
func (s *Store) hasAnyCheckpoint(ctx context.Context, agentID string, attempt int) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints WHERE agent_id = ? AND attempt = ?`,
		agentID, attempt).Scan(&n)
	return n > 0, err
}

// alreadyRelayed reports whether a relay for this event was already enqueued
// to toAgentID for this item since sinceStartedAt. Messages are never
// deleted, so the same LIKE-on-payload check the crashed/interrupted relay
// tests already use doubles as an idempotency guard here: unlike a state
// transition (crashed, interrupted) that removes the session from
// liveSessionRows once handled, a no-ack session stays live and gets
// reconciled every 5 s until it either checkpoints or actually dies, so
// without this guard the relay would be enqueued on every tick instead of
// once. The sinceStartedAt bound scopes that guard to the current attempt: a
// retry starts a new session (fresh started_at, zero checkpoints), and a
// retried attempt that also hangs must raise its own no_ack rather than
// being silenced by the relay a prior, already-handled attempt left behind.
func (s *Store) alreadyRelayed(ctx context.Context, toAgentID, itemID, event string, sinceStartedAt time.Time) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE to_agent_id = ? AND item_id = ? AND kind = 'relay' AND payload_json LIKE ? AND created_at >= ?`,
		toAgentID, itemID, `%"event":"`+event+`"%`, db.Millis(sinceStartedAt)).Scan(&n)
	return n > 0, err
}

// notifyNoAck is the ack-timeout half of §10.6: a live session that has never
// written a single checkpoint within ackTimeout of starting. It fires once
// per session (see alreadyRelayed), not on every reconcile tick.
func (s *Store) notifyNoAck(ctx context.Context, r liveRow) error {
	already, err := s.alreadyRelayed(ctx, r.ParentAgentID, r.ItemID, "no_ack", r.StartedAt)
	if err != nil {
		return err
	}
	if already {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.no_ack", AgentName: r.AgentName,
			ItemKey: r.ItemKey, Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}}); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"event": "no_ack", "agent": r.AgentName, "item": r.ItemKey})
		if err != nil {
			return err
		}
		_, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: r.ParentAgentID,
			RootItemID: r.RootItemID, ItemID: r.ItemID, Payload: payload})
		return err
	})
}

// OnDepUnblocked is the items.Store.DepUnblocked hook (wired in
// cmd/swarm/daemon.go): doneID just reached Done or Cancelled. Anything still
// sitting `blocked` because it lists doneID in item_deps.blocked_by_id has an
// agent that wrote a `blocked` checkpoint and then ended its turn to wait --
// nothing before this told it, or its orchestrator, that the wait was over
// (live incident 2026-09-20: s1-lane-a sat blocked ~8h after s1-lane-b's
// dependency finished, until a human typed into the orchestrator's pane).
// This mirrors notifyNoAck's relay exactly -- same enqueue call, same
// {event, agent, item} payload, same target (the blocked agent's own
// parent) -- rather than inventing a new channel: the woken orchestrator can
// then swarm_send the still-live child directly to resume it, exactly as
// skills/swarm-orchestrator/SKILL.md already tells it to act on a no_ack
// relay. "dependency_added" was already reserved as an immediate wake in
// ImmediateRelayEvents (inbox.go) since the message-inbox design landed, but
// nothing ever raised it until now.
func (s *Store) OnDepUnblocked(ctx context.Context, tx *sql.Tx, doneID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT i.id, i.key FROM items i
		JOIN item_deps d ON d.item_id = i.id
		WHERE d.blocked_by_id = ? AND i.status = 'blocked'`, doneID)
	if err != nil {
		return err
	}
	type dependant struct{ id, key string }
	var waiting []dependant
	for rows.Next() {
		var d dependant
		if err := rows.Scan(&d.id, &d.key); err != nil {
			rows.Close()
			return err
		}
		waiting = append(waiting, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range waiting {
		var agentName, parentAgentID, rootItemID string
		err := tx.QueryRowContext(ctx, `SELECT name, COALESCE(parent_agent_id, ''), root_item_id
			FROM agents WHERE item_id = ? AND state = 'active'`, d.id).
			Scan(&agentName, &parentAgentID, &rootItemID)
		if err == sql.ErrNoRows {
			continue // nobody currently assigned to the blocked item: nothing to wake
		}
		if err != nil {
			return err
		}
		if parentAgentID == "" {
			continue // a top-level orchestrator itself: nothing above it to relay to
		}
		payload, err := json.Marshal(map[string]any{"event": "dependency_added", "agent": agentName, "item": d.key})
		if err != nil {
			return err
		}
		if _, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: parentAgentID,
			RootItemID: rootItemID, ItemID: d.id, Payload: payload}); err != nil {
			return err
		}
	}
	return nil
}

// resolveDead applies the "pane is dead" half of the §10.6 table. paneKnown is
// false when there was no matching pane at all (as opposed to one this
// generation no longer owns, or one tmux still reports as dead).
// spawnGracePeriod protects a session against a reconcile tick whose Panes()
// snapshot simply hasn't picked up its tmux pane yet -- a real, transient
// listing miss, not the pane actually being gone. P0-crash-2 (2026-09-19): a
// real, live, actively-working orchestrator session was marked 'crashed' 4.3s
// after Start() succeeded -- confirmed live: the tmux pane was still alive
// and its SWARM_SESSION env still matched the "crashed" session's own ID, so
// the pane was never dead; Reconcile's single Panes() snapshot for that tick
// simply didn't contain it.
//
// P0-crash-3 (2026-09-19): the same agent was then marked crashed a second
// time, ~3.5 minutes into a session that had already been confirmed alive on
// earlier reconcile ticks -- long past any "just started" window, yet still
// exit_code NULL (§10.6's own signal that paneKnown was false, not that tmux
// reported the pane dead). A single-tick Panes() miss is not only a
// just-started race; it can recur for an established session too. So the
// grace window is no longer anchored solely to StartedAt: it counts from
// lastAliveAt, the most recent tick that actually confirmed the pane, and
// only falls back to StartedAt when the session has never been confirmed
// alive at all (its first tick, or a genuinely-fast crash before ever being
// seen). Either way the window is comfortably inside the 30s watchStartup
// already allows a startup dialog to resolve, so it cannot mask a session
// that is genuinely stuck -- and it still does NOT apply when tmux reports
// the pane itself as dead (p.Dead): that is real exit signal, not a listing
// race, and must still be handled immediately.
const spawnGracePeriod = 10 * time.Second

func (s *Store) resolveDead(ctx context.Context, r liveRow, p Pane, paneKnown bool) error {
	now := s.Now()
	if !paneKnown {
		basis := r.StartedAt
		switch last := s.getLastAlive(r.SessionID); {
		case last != nil:
			basis = *last
		case now.Sub(r.StartedAt) >= spawnGracePeriod:
			// P0-crash-4 (2026-09-19): a daemon restart (or an external
			// correction back to a live state) wipes this process's
			// lastAliveAt memory of an already-long-running session --
			// confirmed live: the very first reconcile tick after such a
			// reset saw a single !paneKnown miss on a session that had
			// been running fine for minutes, and with StartedAt long past
			// spawnGracePeriod, that one miss immediately fell through to
			// 'crashed' with no further chance to recover (a crashed
			// session leaves liveSessionRows() and is never re-evaluated).
			// Never having confirmed this session alive in THIS process's
			// lifetime is not evidence it just started, so it gets one
			// fresh grace window counted from right now instead of the
			// stale StartedAt. That window is anchored immediately (not
			// just used for this check) so a session that keeps missing
			// doesn't get a new "now" -- and therefore infinite grace --
			// on every subsequent tick too; it still crashes once this one
			// fresh window elapses, same as any other missing pane. A
			// session genuinely stuck since birth is still caught
			// independently by watchStartup's own 30s deadline.
			basis = now
			s.setLastAlive(r.SessionID, now)
		}
		if now.Sub(basis) < spawnGracePeriod {
			return nil // give the next tick(s) a chance to see the pane again
		}
	}
	if r.State == Stopping {
		return s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'paused', ended_at = ? WHERE id = ?`,
				db.Millis(now), r.SessionID); err != nil {
				return err
			}
			return s.notify(ctx, tx, NotifyInput{Kind: "agent.paused", AgentName: r.AgentName, ItemKey: r.ItemKey,
				Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}})
		})
	}
	var exitCode *int
	if paneKnown && p.Dead {
		c := p.DeadStatus
		exitCode = &c
	}
	// A pausing session that was sent interrupt keys (L11) ends interrupted, never
	// crashed, whatever attempt-scoped checkpoint it left behind.
	if r.State.Pausing() && s.getInterrupted(r.SessionID) != nil {
		return s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'interrupted', exit_code = ?,
				ended_at = ? WHERE id = ?`, exitCode, db.Millis(now), r.SessionID); err != nil {
				return err
			}
			if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.interrupted", AgentName: r.AgentName,
				ItemKey: r.ItemKey, Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}}); err != nil {
				return err
			}
			if r.ParentAgentID == "" {
				return nil
			}
			payload, err := json.Marshal(map[string]any{"event": "interrupted", "agent": r.AgentName, "item": r.ItemKey})
			if err != nil {
				return err
			}
			_, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: r.ParentAgentID,
				RootItemID: r.RootItemID, ItemID: r.ItemID, Payload: payload})
			return err
		})
	}
	kind, hasTerminal, err := s.terminalCheckpointKind(ctx, r.AgentID, r.Attempt)
	if err != nil {
		return err
	}
	switch {
	case hasTerminal && kind == CompletedCkp:
		return s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'completed', exit_code = ?,
				ended_at = ? WHERE id = ?`, exitCode, db.Millis(now), r.SessionID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'finished', finished_at = ? WHERE id = ?`,
				db.Millis(now), r.AgentID)
			return err
		})
	case hasTerminal && kind == FailedCkp:
		return s.tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'failed', exit_code = ?,
				ended_at = ? WHERE id = ?`, exitCode, db.Millis(now), r.SessionID)
			return err
		})
	default: // crashed: no terminal checkpoint in this attempt (§10.6)
		return s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'crashed', exit_code = ?,
				ended_at = ? WHERE id = ?`, exitCode, db.Millis(now), r.SessionID); err != nil {
				return err
			}
			if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.crashed", AgentName: r.AgentName,
				ItemKey: r.ItemKey, Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}}); err != nil {
				return err
			}
			if r.ParentAgentID == "" {
				return nil
			}
			payload, err := json.Marshal(map[string]any{"event": "crashed", "agent": r.AgentName, "item": r.ItemKey})
			if err != nil {
				return err
			}
			_, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: r.ParentAgentID,
				RootItemID: r.RootItemID, ItemID: r.ItemID, Payload: payload})
			return err
		})
	}
}

// getLastAlive and setLastAlive guard Store.lastAliveAt (P0-crash-3), the
// in-memory record of the last reconcile tick that saw each session's pane
// present and correctly owned. Same style as pause.go's getInterrupted /
// setInterrupted.
func (s *Store) getLastAlive(sessionID string) *time.Time {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	t, ok := s.lastAliveAt[sessionID]
	if !ok {
		return nil
	}
	return &t
}

func (s *Store) setLastAlive(sessionID string, at time.Time) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.lastAliveAt == nil {
		s.lastAliveAt = map[string]time.Time{}
	}
	s.lastAliveAt[sessionID] = at
}

// resolveAlive applies the "pane is alive" half of the §10.6 table.
func (s *Store) resolveAlive(ctx context.Context, r liveRow, p Pane) error {
	if r.CompletedCheckpointAt != nil && s.Now().Sub(*r.CompletedCheckpointAt) >= killCompletedAfter {
		return s.Tmux.Kill(ctx, r.TmuxName)
	}
	owesNothing, err := s.owesNothing(ctx, r)
	if err != nil {
		return err
	}
	ad, ok := s.Adapters[r.Kind]
	idle := false
	if ok {
		capture, err := s.Tmux.Capture(ctx, r.TmuxName, 15)
		if err != nil {
			return err
		}
		idle = ad.Idle(capture)
	}
	waiting := idle && owesNothing
	if waiting != r.Waiting {
		if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET waiting = ? WHERE id = ?`,
			boolToInt(waiting), r.SessionID); err != nil {
			return err
		}
	}
	if waiting {
		return nil // M6: a waiting session is never stale
	}
	if r.ParentAgentID != "" && s.Now().Sub(r.StartedAt) >= ackTimeout {
		acked, err := s.hasAnyCheckpoint(ctx, r.AgentID, r.Attempt)
		if err != nil {
			return err
		}
		if !acked {
			return s.notifyNoAck(ctx, r)
		}
	}
	if s.Now().Sub(r.lastActivity()) >= staleAfter {
		return s.notify(ctx, nil, NotifyInput{Kind: "agent.stale", AgentName: r.AgentName, ItemKey: r.ItemKey,
			Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}})
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// owesNothing is M6: no un-acked messages, no open request this agent raised,
// and, for an orchestrator, no live children and no open requests in its tree.
func (s *Store) owesNothing(ctx context.Context, r liveRow) (bool, error) {
	var pending int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE to_agent_id = ? AND state != 'acked'`, r.AgentID).Scan(&pending); err != nil {
		return false, err
	}
	if pending > 0 {
		return false, nil
	}
	var openReq int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests
		WHERE agent_id = ? AND state = 'open'`, r.AgentID).Scan(&openReq); err != nil {
		return false, err
	}
	if openReq > 0 {
		return false, nil
	}
	if r.Role != RoleOrchestrator {
		return true, nil
	}
	var liveChildren int
	if err := s.DB.QueryRowContext(ctx, `WITH RECURSIVE d(id) AS (
			SELECT id FROM agents WHERE parent_agent_id = ?
			UNION ALL SELECT a.id FROM agents a JOIN d ON a.parent_agent_id = d.id)
		SELECT COUNT(*) FROM agents WHERE id IN (SELECT id FROM d) AND state IN ('queued', 'active')`,
		r.AgentID).Scan(&liveChildren); err != nil {
		return false, err
	}
	if liveChildren > 0 {
		return false, nil
	}
	var treeOpenReq int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests
		WHERE state = 'open' AND item_id IN (SELECT id FROM items WHERE root_id = ?)`,
		r.RootItemID).Scan(&treeOpenReq); err != nil {
		return false, err
	}
	return treeOpenReq == 0, nil
}

// terminalTmuxNames returns tmux names whose most recent session ended in a
// terminal state, so Reconcile's unknown-tmux pass recognizes their leftover
// pane instead of flagging it forever (§17.5 was only ever meant for a pane
// swarm never spawned).
func (s *Store) terminalTmuxNames(ctx context.Context) (map[string]bool, error) {
	names, err := s.queryIDs(ctx, `SELECT DISTINCT tmux_name FROM sessions
		WHERE state IN ('failed', 'crashed', 'cancelled')`)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out, nil
}

// notifyUnknownTmux is §17.5's tmux.unknown: a tmux session with no matching
// row, never killed.
func (s *Store) notifyUnknownTmux(ctx context.Context, tmuxSession string) error {
	return s.notify(ctx, nil, NotifyInput{Kind: "tmux.unknown", AgentName: tmuxSession,
		Args: map[string]string{"name": tmuxSession}})
}

// sweepFinishedRoots runs the worktree sweep for every top-level item whose
// whole agent tree has finished (§12.2).
func (s *Store) sweepFinishedRoots(ctx context.Context) error {
	rootIDs, err := s.queryIDs(ctx, `SELECT id FROM items WHERE root_id = id AND status IN ('done', 'cancelled')`)
	if err != nil {
		return err
	}
	for _, rootID := range rootIDs {
		var stuck int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents
			WHERE root_item_id = ? AND state NOT IN ('finished', 'acknowledged')`, rootID).Scan(&stuck); err != nil {
			return err
		}
		if stuck > 0 {
			continue
		}
		if _, err := s.Worktree.Sweep(ctx, rootID); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileLoop runs Reconcile every `every` until ctx is cancelled.
func (s *Store) ReconcileLoop(ctx context.Context, every time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.after(every):
		}
		if err := s.Reconcile(ctx); err != nil {
			s.logf("reconcile: %v", err)
		}
	}
}
