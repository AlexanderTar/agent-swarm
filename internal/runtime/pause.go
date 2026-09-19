package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// pausedTool is declared in checkpoint.go (Task 15); do not redeclare it here.
const stillStopping = "Still stopping. Try again in a few seconds."
const daemonPauseSummary = "Paused by daemon; orchestrator did not respond."
const notRunning = "This agent isn't running."

const killAfterHandoff = 5 * time.Second
const killAfterInterrupt = 10 * time.Second
const defaultPauseDeadlineSec = 120

// pauseAllowedTools is the daemon-side allow-list during a pause (C3). The hooks
// deny the rest, but the daemon enforces it whatever the hooks do.
var pauseAllowedTools = []string{"swarm_sync", "swarm_read"}

// PauseAllowed gates one MCP tool call for a session state. swarm_checkpoint and
// swarm_ask are gated further by their own handlers: only handoff, blocked and
// failed checkpoints, and only a withdraw ask.
func PauseAllowed(state SessionState, tool string) error {
	if !state.Pausing() || slices.Contains(pauseAllowedTools, tool) ||
		tool == "swarm_checkpoint" || tool == "swarm_ask" {
		return nil
	}
	return &items.Error{Code: items.CodeConflict, Message: pausedTool}
}

// getInterrupted and setInterrupted guard Store.interruptedAt, the in-memory
// record of when a session was last sent interrupt keys.
func (s *Store) getInterrupted(sessionID string) *time.Time {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	t, ok := s.interruptedAt[sessionID]
	if !ok {
		return nil
	}
	return &t
}

func (s *Store) setInterrupted(sessionID string, at time.Time) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.interruptedAt == nil {
		s.interruptedAt = map[string]time.Time{}
	}
	s.interruptedAt[sessionID] = at
}

// pausingRow is one pausing session's bookkeeping, joined from sessions,
// agents and checkpoints. InterruptedAt is kept in memory (D57-style: no
// column exists for it, and none is needed — a restart mid-interrupt is an
// edge case the reconciler's un-acked-message rule already covers for
// delivery, and TickPause simply resends the interrupt keys after a restart).
type pausingRow struct {
	SessionID, AgentID, TmuxName string
	AgentKind                    AgentKind
	State                        SessionState
	PauseScope                   string
	PauseDeadlineAt              *time.Time
	HandoffAt                    time.Time
	InterruptedAt                *time.Time
	Attempt                      int
}

// pausingSessions loads every session in a pausing state (§10.5).
func (s *Store) pausingSessions(ctx context.Context) ([]pausingRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.agent_id, ses.tmux_name, a.kind, ses.state,
		COALESCE(ses.pause_scope, ''), ses.pause_deadline_at, ses.attempt,
		(SELECT MAX(created_at) FROM checkpoints c WHERE c.session_id = ses.id AND c.kind = 'handoff')
		FROM sessions ses JOIN agents a ON a.id = ses.agent_id
		WHERE ses.state IN ('pause_requested', 'quiescing', 'stopping')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pausingRow
	for rows.Next() {
		var r pausingRow
		var kind, state string
		var deadline sql.NullInt64
		var handoff sql.NullInt64
		if err := rows.Scan(&r.SessionID, &r.AgentID, &r.TmuxName, &kind, &state,
			&r.PauseScope, &deadline, &r.Attempt, &handoff); err != nil {
			return nil, err
		}
		r.AgentKind, r.State = AgentKind(kind), SessionState(state)
		if deadline.Valid {
			t := db.FromMillis(deadline.Int64)
			r.PauseDeadlineAt = &t
		}
		if handoff.Valid {
			r.HandoffAt = db.FromMillis(handoff.Int64)
		}
		r.InterruptedAt = s.getInterrupted(r.SessionID)
		out = append(out, r)
	}
	return out, rows.Err()
}

// TickPause advances every pausing session. The reconciler calls it every 5 s.
func (s *Store) TickPause(ctx context.Context) error {
	rows, err := s.pausingSessions(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		switch {
		case r.State == Stopping && !r.HandoffAt.IsZero() && s.Now().Sub(r.HandoffAt) >= killAfterHandoff:
			if err := s.killIfOurs(ctx, r); err != nil {
				return err
			}
		case r.InterruptedAt != nil && s.Now().Sub(*r.InterruptedAt) >= killAfterInterrupt:
			if err := s.killIfOurs(ctx, r); err != nil {
				return err
			}
		case r.InterruptedAt == nil && r.PauseDeadlineAt != nil && s.Now().After(*r.PauseDeadlineAt):
			if err := s.interrupt(ctx, r); err != nil {
				return err
			}
		}
	}
	return s.advanceSubtreePauses(ctx)
}

// killIfOurs kills the pane only when its SWARM_SESSION is this session (§10.5),
// so a newer generation with the same tmux name survives.
func (s *Store) killIfOurs(ctx context.Context, r pausingRow) error {
	got, err := s.Tmux.Env(ctx, r.TmuxName, "SWARM_SESSION")
	if err != nil {
		s.logf("pause: cannot read SWARM_SESSION for %s: %v", r.TmuxName, err)
		return nil
	}
	if got != r.SessionID {
		s.logf("pause: %s now belongs to %s, not killing", r.TmuxName, got)
		return nil
	}
	return s.Tmux.Kill(ctx, r.TmuxName)
}

// interrupt sends the adapter's interrupt keys and records when, so TickPause
// kills the pane 10 s later if it has not already died.
func (s *Store) interrupt(ctx context.Context, r pausingRow) error {
	ad, ok := s.Adapters[r.AgentKind]
	if !ok {
		s.logf("pause: no adapter for %s", r.AgentKind)
		return nil
	}
	if err := s.Tmux.Keys(ctx, r.TmuxName, ad.InterruptKeys()...); err != nil {
		return err
	}
	s.setInterrupted(r.SessionID, s.Now())
	return nil
}

// onPausingSync is Sync's hook (called from inbox.go): the first sync after a
// pause moves pause_requested -> quiescing.
func (s *Store) onPausingSync(ctx context.Context, tx *sql.Tx, ses *Session) error {
	if ses.State != PauseRequested {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'quiescing' WHERE id = ?`, ses.ID); err != nil {
		return err
	}
	ses.State = Quiescing
	return nil
}

// onPausingCheckpoint is WriteCheckpoint's hook (called from checkpoint.go): a
// handoff (or blocked/failed) checkpoint while pausing moves the session to
// stopping and tells the parent this agent is paused (§10.5).
func (s *Store) onPausingCheckpoint(ctx context.Context, tx *sql.Tx, ses Session, kind CheckpointKind) error {
	if !ses.State.Pausing() || ses.State == Stopping || !slices.Contains(pauseAllowedKinds, kind) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, ses.ID); err != nil {
		return err
	}
	return s.relayPaused(ctx, tx, ses.AgentID)
}

// relayPaused tells agentID's parent, if it has one, that it is paused.
func (s *Store) relayPaused(ctx context.Context, tx *sql.Tx, agentID string) error {
	a, err := s.agentByIDTx(ctx, tx, agentID)
	if err != nil {
		return err
	}
	if a.ParentAgentID == "" {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"event": "paused", "agent": a.Name})
	if err != nil {
		return err
	}
	_, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: a.ParentAgentID,
		RootItemID: a.RootItemID, ItemID: a.ItemID, Payload: payload})
	return err
}

// pauseDeadlineSec reads settings.PauseDeadlineSec with a safe default.
func (s *Store) pauseDeadlineSec(ctx context.Context) int {
	cfg, err := s.Settings.Get(ctx)
	if err != nil || cfg.PauseDeadlineSec <= 0 {
		return defaultPauseDeadlineSec
	}
	return cfg.PauseDeadlineSec
}

// Pause is swarm's pause action (§10.5, L11). scope is "session" or "subtree".
func (s *Store) Pause(ctx context.Context, name, scope string) (Session, error) {
	a, err := s.Agent(ctx, name)
	if err != nil {
		return Session{}, err
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		return Session{}, err
	}
	if !ses.State.Live() {
		return Session{}, &items.Error{Code: items.CodeConflict, Message: notRunning}
	}
	// The idempotency guard ("a second pause must not extend the deadline")
	// gates a plain re-pause, but not a scope UPGRADE: Pause(x, "subtree")
	// on an x already Pausing() under scope="session" must still cascade to
	// x's descendants and mark x as this pause's root (pause_scope,
	// pause_root) -- leaving x's own state/deadline untouched -- or the
	// whole cascade silently never runs at all (found in review: two
	// ordinary calls, Pause(orch,"session") then PauseAll, left every
	// descendant running and rootHasLiveSubtreePause false, reopening the
	// queue-freeze guarantee for this one shape). A plain re-pause at the
	// SAME scope, or any pause while not yet Pausing(), is unaffected.
	pausing := ses.State.Pausing()
	if pausing && scope != "subtree" {
		return ses, nil
	}
	deadline := s.Now().Add(time.Duration(s.pauseDeadlineSec(ctx)) * time.Second)
	if scope == "subtree" {
		return s.pauseSubtree(ctx, a, ses, deadline, pausing)
	}
	return s.pauseOne(ctx, a, ses, deadline, "session")
}

// pauseOne sends one session's pause control message and moves it to
// pause_requested.
func (s *Store) pauseOne(ctx context.Context, a Agent, ses Session, deadline time.Time, scope string) (Session, error) {
	payload, err := json.Marshal(map[string]string{"action": "pause",
		"deadline_at": deadline.UTC().Format(time.RFC3339), "scope": scope})
	if err != nil {
		return Session{}, err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'pause_requested',
			pause_scope = ?, pause_deadline_at = ? WHERE id = ?`,
			scope, db.Millis(deadline), ses.ID); err != nil {
			return err
		}
		if _, err := s.enqueue(ctx, tx, Message{Kind: "control", Origin: "daemon",
			ToAgentID: a.ID, RootItemID: a.RootItemID, Payload: payload}); err != nil {
			return err
		}
		return s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID)
	})
	if err != nil {
		return Session{}, err
	}
	ses.State, ses.PauseScope, ses.PauseDeadlineAt = PauseRequested, scope, &deadline
	return ses, nil
}

// pauseSubtree pauses every live descendant deepest-first, then marks the
// orchestrator's own session as awaiting a subtree pause: it stays running
// until advanceSubtreePauses sees every descendant finish (§10.5). upgrade
// is true when orchSes was already Pausing() under a different scope
// (Pause's own idempotency guard let this call through anyway, specifically
// so the cascade below still runs) -- in that case orchSes's own state and
// pause_deadline_at are left exactly as they are; only pause_scope and
// pause_root are stamped, so §10.5's machinery (overdueSubtreePauses,
// rootHasLiveSubtreePause) sees a truthful subtree pause in this tree.
// Two accepted limitations of an upgrade, not bugs: it won't produce a
// *combined* handoff (orch is already mid its own session-scope handoff
// process, or past it), and its fallback deadline isn't re-stamped, so the
// fallback may fire before a fresh full window. Both are fine -- orch still
// correctly ends paused or interrupted, descendants still pause, and the
// queue still freezes.
func (s *Store) pauseSubtree(ctx context.Context, orch Agent, orchSes Session, deadline time.Time, upgrade bool) (Session, error) {
	descendants, err := s.descendantAgents(ctx, orch.ID)
	if err != nil {
		return Session{}, err
	}
	for _, d := range descendants {
		dses, err := s.LatestSession(ctx, d.ID)
		// An already-pausing descendant is left alone: pauseOne has no
		// idempotency guard of its own (Pause's own "a second pause must
		// not extend the deadline" guard only covers the one session Pause
		// is called on directly, never what pauseSubtree cascades onto), so
		// without this check a repeated pause-all -- or one subtree pause
		// landing on a descendant already mid-pause for any reason -- would
		// reset its deadline forward every time and could even overwrite a
		// descendant already 'stopping' with a handoff already written back
		// to 'pause_requested', discarding real progress.
		if err != nil || !dses.State.Live() || dses.State.Pausing() {
			continue
		}
		if _, err := s.pauseOne(ctx, d, dses, deadline, "subtree"); err != nil {
			return Session{}, err
		}
	}
	// pause_root = 1 marks this specific session as the literal target of
	// THIS Pause(scope="subtree") call, not merely "part of a subtree being
	// paused" (pause_scope='subtree' alone means the latter, and every
	// descendant just paused above got that same value from pauseOne). It
	// is the identity overdueSubtreePauses keys off of, so that §10.5 step
	// 4's fallback checkpoint applies to the one agent this pause was
	// actually requested against -- whether or not it currently has any
	// descendants -- and never to a plain descendant merely cascaded onto
	// (a leaf worker, or a nested orchestrator that is itself only a
	// descendant of this call, not its own target).
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if upgrade {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET pause_scope = 'subtree',
				pause_root = 1 WHERE id = ?`, orchSes.ID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET pause_scope = 'subtree',
				pause_deadline_at = ?, pause_root = 1 WHERE id = ?`, db.Millis(deadline), orchSes.ID); err != nil {
				return err
			}
		}
		return s.publishAgentChanged(ctx, tx, orch.Name, orch.RootItemID)
	})
	if err != nil {
		return Session{}, err
	}
	orchSes.PauseScope = "subtree"
	if !upgrade {
		orchSes.PauseDeadlineAt = &deadline
	}
	return orchSes, nil
}

// descendantAgents returns every agent under rootAgentID, deepest first
// (§10.5: children are paused before their orchestrator).
func (s *Store) descendantAgents(ctx context.Context, rootAgentID string) ([]Agent, error) {
	ids, err := s.queryIDs(ctx, `WITH RECURSIVE d(id, depth) AS (
			SELECT id, 1 FROM agents WHERE parent_agent_id = ?
			UNION ALL SELECT a.id, d.depth + 1 FROM agents a JOIN d ON a.parent_agent_id = d.id)
		SELECT id FROM d ORDER BY depth DESC, id`, rootAgentID)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(ids))
	for _, id := range ids {
		a, err := s.agentByID(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// latestCheckpointSummary is the most recent checkpoint summary for agentID,
// or "" if it never wrote one.
func (s *Store) latestCheckpointSummary(ctx context.Context, agentID string) string {
	var summary string
	_ = s.DB.QueryRowContext(ctx, `SELECT summary FROM checkpoints WHERE agent_id = ?
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, agentID).Scan(&summary)
	return summary
}

// advanceSubtreePauses promotes a subtree pause once every descendant is done
// pausing, and writes the daemon's fallback checkpoint for an orchestrator
// that never responds to its own pause (§10.5).
func (s *Store) advanceSubtreePauses(ctx context.Context) error {
	if err := s.promotePendingSubtreePauses(ctx); err != nil {
		return err
	}
	return s.writeUnresponsiveOrchestratorCheckpoints(ctx)
}

type pendingSubtree struct{ sesID, agentID string }

func (s *Store) pendingSubtreePauses(ctx context.Context) ([]pendingSubtree, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.agent_id FROM sessions ses
		WHERE ses.pause_scope = 'subtree' AND ses.state = 'running'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingSubtree
	for rows.Next() {
		var p pendingSubtree
		if err := rows.Scan(&p.sesID, &p.agentID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) promotePendingSubtreePauses(ctx context.Context) error {
	candidates, err := s.pendingSubtreePauses(ctx)
	if err != nil {
		return err
	}
	for _, p := range candidates {
		descendants, err := s.descendantAgents(ctx, p.agentID)
		if err != nil {
			return err
		}
		allDone := true
		children := make([]map[string]string, 0, len(descendants))
		for _, d := range descendants {
			dses, err := s.LatestSession(ctx, d.ID)
			if err == nil && dses.State.Live() {
				allDone = false
				break
			}
			var itemKey string
			_ = s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, d.ItemID).Scan(&itemKey)
			children = append(children, map[string]string{"agent": d.Name, "item": itemKey,
				"summary": s.latestCheckpointSummary(ctx, d.ID)})
		}
		if !allDone {
			continue
		}
		a, err := s.agentByID(ctx, p.agentID)
		if err != nil {
			return err
		}
		// A fresh deadline, not the one Pause(scope="subtree") stamped back
		// when the pause first started: that original deadline was sized for
		// the descendants' own pause window, which can already be spent by
		// the time every one of them finishes (interrupt/kill fallbacks take
		// up to killAfterInterrupt on their own). Reusing it would let this
		// same root's own promotion and overdueSubtreePauses fire on the same
		// tick, leaving it zero real window to write its own handoff (§10.5
		// step 3's actual intent) before step 4's daemon fallback preempts it.
		deadline := s.Now().Add(time.Duration(s.pauseDeadlineSec(ctx)) * time.Second)
		payload, err := json.Marshal(map[string]any{"action": "pause", "scope": "subtree",
			"deadline_at": deadline.UTC().Format(time.RFC3339), "children": children})
		if err != nil {
			return err
		}
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'pause_requested',
				pause_deadline_at = ? WHERE id = ?`, db.Millis(deadline), p.sesID); err != nil {
				return err
			}
			_, err := s.enqueue(ctx, tx, Message{Kind: "control", Origin: "daemon",
				ToAgentID: a.ID, RootItemID: a.RootItemID, Payload: payload})
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

type overdueSubtree struct {
	sesID, agentID string
	attempt        int
}

// overdueSubtreePauses finds an unresponsive subtree-pause ROOT (pause_root =
// 1 -- the literal target of a Pause(scope="subtree") call), not any overdue
// subtree-scoped session. pauseSubtree stamps pause_scope = 'subtree' on
// every descendant too (so rootHasLiveSubtreePause can see the whole subtree
// is pausing), and a plain descendant sits in the same pause_requested/
// quiescing states past its own deadline while TickPause's normal
// per-session interrupt-then-kill loop handles it. An earlier version of
// this guard checked "does this agent have children" instead of pause_root,
// which got two cases wrong: a childless root (Pause(scope="subtree") on an
// orchestrator with nothing under it right now) was wrongly excluded, even
// though §10.5 step 4's fallback checkpoint is unconditional on child count;
// and a mid-tree orchestrator (has children of its own, but is itself only
// a descendant cascaded onto by some ancestor's subtree pause, never the
// direct target of its own Pause call) was wrongly included, racing
// TickPause's interrupt loop the same way the original worker bug did.
// pause_root is set exactly once, on the one session pauseSubtree's own
// caller named, and is never propagated onto anything pauseOne pauses as a
// cascaded descendant -- so it is the one column that survives the
// running -> pause_requested promotion and still answers "is this the
// agent this specific pause was requested against" correctly in all three
// shapes (plain worker, childless root, mid-tree orchestrator).
// Found while writing scenario 9's e2e test (Batch 6c): the existing unit
// test for this path (TestUnresponsiveOrchestratorGetsADaemonWrittenCheckpoint)
// short-circuits the worker straight to 'interrupted' via raw SQL, which
// never exercises TickPause's own interrupt tick racing this query; a
// second review round (a hand-built 3-level probe) found the EXISTS(children)
// guard's own two gaps above.
func (s *Store) overdueSubtreePauses(ctx context.Context) ([]overdueSubtree, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.agent_id, ses.attempt FROM sessions ses
		WHERE ses.pause_scope = 'subtree' AND ses.pause_root = 1
		AND ses.state IN ('pause_requested', 'quiescing')
		AND ses.pause_deadline_at IS NOT NULL AND ses.pause_deadline_at < ?`, db.Millis(s.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []overdueSubtree
	for rows.Next() {
		var o overdueSubtree
		if err := rows.Scan(&o.sesID, &o.agentID, &o.attempt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) writeUnresponsiveOrchestratorCheckpoints(ctx context.Context) error {
	candidates, err := s.overdueSubtreePauses(ctx)
	if err != nil {
		return err
	}
	for _, o := range candidates {
		var alreadyHandedOff int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints
			WHERE session_id = ? AND kind = 'handoff'`, o.sesID).Scan(&alreadyHandedOff); err != nil {
			return err
		}
		if alreadyHandedOff > 0 {
			continue
		}
		if err := s.writeDaemonPauseCheckpoint(ctx, o.sesID, o.agentID, o.attempt); err != nil {
			return err
		}
	}
	return nil
}

// writeDaemonPauseCheckpoint is §10.5's combined checkpoint: an orchestrator
// that never responds to a subtree pause gets one written on its behalf,
// listing every child that never handed off, and its session moves to
// stopping so the normal 5 s kill timer takes over from here.
func (s *Store) writeDaemonPauseCheckpoint(ctx context.Context, sesID, agentID string, attempt int) error {
	a, err := s.agentByID(ctx, agentID)
	if err != nil {
		return err
	}
	children, err := s.descendantAgents(ctx, agentID)
	if err != nil {
		return err
	}
	var blockers []string
	for _, c := range children {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints
			WHERE agent_id = ? AND kind = 'handoff'`, c.ID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			blockers = append(blockers, c.Name)
		}
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		ckpID := ids.New("ckp")
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind,
			attempt, summary, blockers_json, daemon_written, created_at)
			VALUES (?, ?, ?, ?, 'handoff', ?, ?, ?, 1, ?)`,
			ckpID, sesID, agentID, a.ItemID, attempt, daemonPauseSummary, jsonArray(blockers),
			db.Millis(s.Now())); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, sesID); err != nil {
			return err
		}
		return s.relayPaused(ctx, tx, agentID)
	})
}

// Resume is swarm's resume action (C3, §10.5). It is refused unless the
// session is paused or interrupted, and starts generation n+1 with a new
// token.
func (s *Store) Resume(ctx context.Context, name string) (Agent, error) {
	a, err := s.Agent(ctx, name)
	if err != nil {
		return Agent{}, err
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		return Agent{}, err
	}
	if ses.State != Paused && ses.State != Interrupted {
		return Agent{}, &items.Error{Code: items.CodeConflict, Message: stillStopping}
	}
	resume := ses.ProviderSessionID != ""
	newSes, err := s.startSession(ctx, a, ses.Attempt, ses.Generation+1, resume, ses.ProviderSessionID)
	if err != nil {
		return Agent{}, err
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if a.State != AgentActive {
			if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'active' WHERE id = ?`, a.ID); err != nil {
				return err
			}
		}
		return s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID)
	}); err != nil {
		return Agent{}, err
	}
	a.State = AgentActive
	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), a, newSes, s.Adapters[a.Kind]); err != nil {
			s.logf("resume: watchStartup %s: %v", a.Name, err)
		}
	})
	return a, nil
}

// pauseTarget returns the highest still-live ancestor of a, or a itself if
// none of its ancestors are live. "Live" means that ancestor's own latest
// session is live, not merely that the agent row exists: a live agent whose
// immediate parent has already ended is functionally standalone (Ruling B),
// even when some ancestor further up the very same chain is still live.
// This walks the whole chain to the structural root rather than stopping at
// the first dead link, tracking the farthest-up live ancestor seen along
// the way, so a dead intermediate agent never hides a live one above it.
func (s *Store) pauseTarget(ctx context.Context, a Agent) (Agent, error) {
	best, cur := a, a
	for cur.ParentAgentID != "" {
		parent, err := s.agentByID(ctx, cur.ParentAgentID)
		if err != nil {
			return Agent{}, err
		}
		if pses, err := s.LatestSession(ctx, parent.ID); err == nil && pses.State.Live() {
			best = parent
		}
		cur = parent
	}
	return best, nil
}

// PauseAll requests a pause on every live session, grouped by pause target
// (pauseTarget above) so each connected run of live agents is cascaded
// through pauseSubtree exactly once from its own topmost live point, whether
// or not that point happens to be a true root (agents.parent_agent_id IS
// NULL). Gating an earlier version of this function on live ROOTS only
// missed a live descendant whose own root had already ended (paused,
// interrupted, completed, failed, crashed): PauseAll reported
// requested: 0 for that whole (still very much running) branch. Ruling B's
// own text already says a session with no live orchestrator above it is
// "functionally standalone" -- this just recognizes that a descendant can
// become standalone that way too, not only by being a root from the start.
//
// A target already Pausing() under a different scope (typically
// scope="session", from an earlier direct pause) is upgraded to subtree
// scope by Pause itself when it has a live descendant -- see Pause's own
// comment -- rather than short-circuited into a no-op, which used to leave
// the whole cascade never running and rootHasLiveSubtreePause false on a
// tree that very much still had a pause in flight. n only counts sessions
// actually newly transitioned by this call: an upgraded target itself does
// not add to n, only the descendants its cascade actually reaches.
func (s *Store) PauseAll(ctx context.Context) (int, error) {
	live, err := s.queryIDs(ctx, `SELECT a.name FROM agents a JOIN sessions ses ON ses.agent_id = a.id
		WHERE ses.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')
		AND ses.generation = (SELECT MAX(s2.generation) FROM sessions s2 WHERE s2.agent_id = a.id)`)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	var targets []string
	for _, name := range live {
		a, err := s.Agent(ctx, name)
		if err != nil {
			s.logf("pause-all: %s: %v", name, err)
			continue
		}
		target, err := s.pauseTarget(ctx, a)
		if err != nil {
			s.logf("pause-all: %s: %v", name, err)
			continue
		}
		if !seen[target.ID] {
			seen[target.ID] = true
			targets = append(targets, target.Name)
		}
	}
	n := 0
	for _, name := range targets {
		a, err := s.Agent(ctx, name)
		if err != nil {
			s.logf("pause-all: %s: %v", name, err)
			continue
		}
		ses, err := s.LatestSession(ctx, a.ID)
		if err != nil {
			s.logf("pause-all: %s: %v", name, err)
			continue
		}
		// A target already Pausing() (e.g. session-scope-paused directly,
		// then reached again here) is upgraded to subtree scope by Pause
		// itself when it has a live descendant, but was not newly requested
		// -- only the descendants Pause's cascade actually reaches are new.
		alreadyPausing := ses.State.Pausing()
		descendants, err := s.descendantAgents(ctx, a.ID)
		if err != nil {
			s.logf("pause-all: %s: %v", name, err)
			continue
		}
		liveDescendants := 0
		for _, d := range descendants {
			if dses, err := s.LatestSession(ctx, d.ID); err == nil && dses.State.Live() {
				liveDescendants++
			}
		}
		scope := "session"
		if liveDescendants > 0 {
			scope = "subtree"
		}
		if _, err := s.Pause(ctx, name, scope); err != nil {
			s.logf("pause-all: %s: %v", name, err)
			continue
		}
		if alreadyPausing {
			n += liveDescendants
		} else {
			n += 1 + liveDescendants // the target itself, plus every live descendant it cascades onto
		}
	}
	return n, nil
}

// rootHasLiveSubtreePause reports whether rootItemID has a subtree pause in
// flight: the root's own session already carries pause_scope = 'subtree' from
// the moment Pause(scope="subtree") is called (§10.5), whether or not it has
// itself moved past 'running' yet, through to 'paused'/'interrupted'. Once it
// reaches either of those the pause is resolved and the root's queue thaws.
// DrainQueue calls this to freeze a root's queued spawns for exactly as long
// as the pause is live, without touching agents.state (which stays 'queued').
func (s *Store) rootHasLiveSubtreePause(ctx context.Context, rootItemID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions ses JOIN agents a ON a.id = ses.agent_id
		WHERE a.root_item_id = ? AND ses.pause_scope = 'subtree'
		AND ses.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')`,
		rootItemID).Scan(&n)
	return n > 0, err
}
