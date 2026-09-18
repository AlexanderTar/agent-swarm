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
		if !ok || p.Dead {
			if err := s.resolveDead(ctx, r, p, ok); err != nil {
				return err
			}
			continue
		}
		if err := s.resolveAlive(ctx, r, p); err != nil {
			return err
		}
	}
	for _, p := range panes {
		if !known[p.Session] {
			if err := s.notifyUnknownTmux(ctx, p.Session); err != nil {
				return err
			}
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

// resolveDead applies the "pane is dead" half of the §10.6 table. paneKnown is
// false when there was no matching pane at all (as opposed to one this
// generation no longer owns, or one tmux still reports as dead).
func (s *Store) resolveDead(ctx context.Context, r liveRow, p Pane, paneKnown bool) error {
	now := s.Now()
	if r.State == Stopping {
		return s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'paused', ended_at = ? WHERE id = ?`,
				db.Millis(now), r.SessionID); err != nil {
				return err
			}
			return s.notify(ctx, tx, NotifyInput{Kind: "agent.paused", AgentName: r.AgentName, ItemKey: r.ItemKey})
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
				ItemKey: r.ItemKey}); err != nil {
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
	if !hasTerminal {
		// A session that never wrote even its first checkpoint has no confirmed
		// evidence of having actually run: a pane the test/production fixture
		// simply never reported (rather than one that died) must not be flagged
		// as a crash. Once an agent has accepted its assignment, a missing pane
		// with no completed/failed checkpoint really is a crash (§10.6).
		var anyCheckpoints int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints
			WHERE agent_id = ?`, r.AgentID).Scan(&anyCheckpoints); err != nil {
			return err
		}
		if anyCheckpoints == 0 {
			return nil
		}
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
				ItemKey: r.ItemKey}); err != nil {
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
	if s.Now().Sub(r.lastActivity()) >= staleAfter {
		return s.notify(ctx, nil, NotifyInput{Kind: "agent.stale", AgentName: r.AgentName, ItemKey: r.ItemKey})
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

// notifyUnknownTmux is §17.5's tmux.unknown: a tmux session with no matching
// row, never killed.
func (s *Store) notifyUnknownTmux(ctx context.Context, tmuxSession string) error {
	return s.notify(ctx, nil, NotifyInput{Kind: "tmux.unknown", AgentName: tmuxSession,
		Args: map[string]string{"name": tmuxSession}})
}

// sweepFinishedRoots runs the worktree sweep for every top-level item whose
// whole agent tree has finished (§12.2).
func (s *Store) sweepFinishedRoots(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM items WHERE root_id = id AND status IN ('done', 'cancelled')`)
	if err != nil {
		return err
	}
	var rootIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		rootIDs = append(rootIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
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
		var any int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE root_item_id = ?`,
			rootID).Scan(&any); err != nil {
			return err
		}
		if any == 0 {
			continue
		}
		if s.Worktree == nil {
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
