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

// pauseAllowedTools is the daemon-side allow-list during a pause. The hooks
// deny the rest, but the daemon enforces it whatever the hooks do.
// swarm_artifact stays open so the predecessor can snapshot specs/plans
// into the registry while preserving; swarm_spawn and swarm_workflow stay
// denied (delegation, new workflow steps).
var pauseAllowedTools = []string{"swarm_sync", "swarm_read", "swarm_artifact"}

// PauseAllowed gates one MCP tool call for a session state. swarm_checkpoint and
// swarm_ask are gated further by their own handlers: only handoff, blocked and
// failed checkpoints, and only a withdraw ask.
func PauseAllowed(state SessionState, tool string) error {
	if !state.Pausing() || slices.Contains(pauseAllowedTools, tool) ||
		tool == "swarm_checkpoint" || tool == "swarm_ask" || tool == "swarm_blocker" {
		return nil
	}
	return &items.Error{Code: items.CodeConflict, Message: pausedTool}
}

// subtreeRole is what one session row means to a subtree pause. Subtree-pause
// state lives in three columns of that row -- state, pause_scope, pause_root --
// and every decision in this file that depends on it (may this call upgrade the
// target? may the cascade touch this descendant? is this row awaiting its own
// promotion, or past its handoff deadline? does this tree still hold a live
// pause?) is a question about the COMBINATION, never about one column. Four
// separate fix rounds each closed one bare boolean check here and exposed the
// next, so the combination is classified exactly once, below, and every gate
// asks for a role instead of reading a column.
type subtreeRole int

const (
	roleNone           subtreeRole = iota // not a live session: nothing to pause, whatever pause columns it still carries
	roleIdle                              // live and outside any pause: the only role a cascade may pause
	roleSessionPausing                    // pausing under session scope: upgradeable to a subtree root
	roleMember                            // pausing under an OUTER subtree pause (pause_root = 0): that pause owns it
	roleRootSpawning                      // subtree root whose pane is still starting up
	roleRootPending                       // subtree root still running, awaiting its first promotion
	roleRootActive                        // subtree root pausing, no handoff yet: §10.5 step 4's fallback applies to it
	roleRootStopping                      // subtree root past its handoff, on the ordinary kill timer
	roleInvalid                           // a combination no write site in this repo can produce
)

// subtreeRoleOf classifies one session row. It is pure and total: every
// (state, scope, pause_root) triple gets a role, and the combinations no write
// site can produce are roleInvalid rather than quietly falling into a
// meaningful bucket. TestSubtreeRoleTable walks the whole cross-product.
//
// state is the authority on whether a pause is in flight at all; pause_scope
// and pause_root only qualify a row that is already pausing or already marked
// as a root. That ordering matters for one narrow race: watchStartup checks
// "still spawning?" and only later writes 'running', so a pause landing in
// between can be overwritten, leaving a stale pause_scope on a running row.
// Such a row has no pause in flight -- treating it as roleIdle lets the next
// pause reapply cleanly, where calling it invalid would leave it unpausable.
//
// The reachability claims behind roleInvalid, checked against every writer of
// these three columns (pauseOne, pauseSubtree, promotePendingSubtreePauses,
// onPausingSync, onPausingCheckpoint, writeDaemonPauseCheckpoint,
// SetSessionState, reconcile's five resolvers, and the INSERTs in
// startSession and DrainQueue): pause_root = 1 is only ever written by
// pauseSubtree, in the same statement that writes pause_scope = 'subtree', and
// nothing anywhere clears or rewrites either column -- so a row marked as a
// root whose scope is not 'subtree' cannot exist. It is handled fail-safe:
// never cascaded onto, never promoted, never given a fallback checkpoint, but
// still counted as a live subtree pause, so a queue freeze cannot thaw on a
// row nothing here understands.
func subtreeRoleOf(state SessionState, scope string, pauseRoot bool) subtreeRole {
	if !state.Live() {
		return roleNone
	}
	if pauseRoot && scope != "subtree" {
		return roleInvalid
	}
	if !state.Pausing() {
		if !pauseRoot {
			return roleIdle
		}
		if state == Spawning {
			return roleRootSpawning
		}
		return roleRootPending
	}
	if scope != "subtree" {
		return roleSessionPausing
	}
	if !pauseRoot {
		return roleMember
	}
	if state == Stopping {
		return roleRootStopping
	}
	return roleRootActive
}

// live reports whether the row is a live session at all -- every role but
// roleNone is one, by subtreeRoleOf's own first branch.
func (r subtreeRole) live() bool { return r != roleNone }

// isRoot reports whether this row is the target of its own subtree pause, in
// any phase. Such a row owns its subtree: a second pause on it (at either
// scope) is a no-op, and an outer cascade steps over it.
func (r subtreeRole) isRoot() bool {
	switch r {
	case roleRootSpawning, roleRootPending, roleRootActive, roleRootStopping:
		return true
	}
	return false
}

// inSubtreePause reports whether this row means "a subtree pause is live
// here", which is what DrainQueue's freeze is keyed on. roleInvalid counts:
// an unreachable combination is never acted on, but must not thaw a queue.
func (r subtreeRole) inSubtreePause() bool { return r == roleMember || r == roleInvalid || r.isRoot() }

// subtreePauseCandidates is the SQL prefilter the subtree-pause queries share:
// rows carrying any pause bookkeeping at all. It is a narrowing optimisation,
// never a decision -- it is a provable superset of every role but roleIdle and
// roleNone, which every gate below rejects anyway -- so subtreeRoleOf stays the
// only place a combination is interpreted.
const subtreePauseCandidates = `(ses.pause_scope IS NOT NULL OR ses.pause_root = 1)`

// descendantRole is one descendant's agent, latest session and role.
type descendantRole struct {
	agent Agent
	ses   Session
	role  subtreeRole
}

// descendantRoles returns every descendant of agentID, deepest first (§10.5:
// children pause before their orchestrator), each with its subtree role. A
// descendant with no session row at all -- a queued spawn that never started
// -- gets roleNone: nothing for the cascade to pause. It is still something
// to WAIT for (Fix R-1: see queued() and hasWaitingDescendant below), which
// is exactly what role.live() alone cannot see, since it never had a session
// to be live in. (A preflight failure writes no session either, but only
// StartSpike's own root-level agent row takes that path with no parent set,
// so it can never actually surface as anyone's descendant here.)
func (s *Store) descendantRoles(ctx context.Context, agentID string) ([]descendantRole, error) {
	ds, err := s.descendantAgents(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]descendantRole, 0, len(ds))
	for _, d := range ds {
		ses, err := s.LatestSession(ctx, d.ID)
		if err != nil {
			out = append(out, descendantRole{agent: d, role: roleNone})
			continue
		}
		out = append(out, descendantRole{agent: d, ses: ses,
			role: subtreeRoleOf(ses.State, ses.PauseScope, ses.PauseRoot)})
	}
	return out, nil
}

// liveDescendants counts the descendants that are live sessions.
func liveDescendants(ds []descendantRole) int {
	n := 0
	for _, d := range ds {
		if d.role.live() {
			n++
		}
	}
	return n
}

// queued reports whether this descendant is a spawn that never started: no
// session row at all, agent still in state 'queued'. It is always roleNone
// (never live), but it is not nothing: DrainQueue will admit it the moment a
// slot frees up, so a pause that ignores it can be defeated by exactly that
// admission racing the pause it was supposedly subject to (Fix R-1).
func (d descendantRole) queued() bool {
	return d.role == roleNone && d.agent.State == AgentQueued
}

// hasWaitingDescendant reports whether ds has anything a subtree-scope pause
// still needs to account for: a live descendant (something cascadeSubtreePause
// can act on, and promotePendingSubtreePauses must wait to go non-live), or a
// queued spawn that hasn't started yet -- invisible to liveDescendants, since
// it has no session row, but exactly why DrainQueue's freeze
// (rootHasLiveSubtreePause, keyed on pause_scope='subtree') exists: without
// subtree scope here, admission never even asks whether a pause is in the way.
func hasWaitingDescendant(ds []descendantRole) bool {
	for _, d := range ds {
		if d.role.live() || d.queued() {
			return true
		}
	}
	return false
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
	PauseDeadlineAt              *time.Time
	HandoffAt                    time.Time
	InterruptedAt                *time.Time
	Attempt                      int
}

// pausingSessions loads every session in a pausing state (§10.5).
func (s *Store) pausingSessions(ctx context.Context) ([]pausingRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.agent_id, ses.tmux_name, a.kind, ses.state,
		ses.pause_deadline_at, ses.attempt,
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
			&deadline, &r.Attempt, &handoff); err != nil {
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
	if kind != Handoff && (!ses.State.Pausing() || !slices.Contains(pauseAllowedKinds, kind)) {
		return nil
	}
	if ses.State == Stopping {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, ses.ID); err != nil {
		return err
	}
	return s.relayPaused(ctx, tx, ses.AgentID)
}

// relayPaused tells agentID's nearest live ancestor, if it has one, that it is paused.
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
	ses, _, err := s.pause(ctx, name, scope)
	return ses, err
}

// pause is Pause plus the count PauseAll reports: how many sessions this call
// newly brought into a pause -- each descendant its cascade actually paused,
// plus the target itself when the target was not already in one.
//
// Every branch below is chosen by the target's subtreeRole, not by any single
// column:
//   - roleIdle is the only role a fresh pause applies to, at either scope.
//   - roleSessionPausing is the one upgradeable role (§10.5's queue freeze needs
//     pause_scope = 'subtree' on a tree whose pause is already in flight; two
//     ordinary calls, Pause(orch,"session") then PauseAll, used to leave every
//     descendant running with the freeze off) -- and only when it has a live
//     descendant to cascade to. An upgrade is a new pause operation, so it
//     re-stamps the deadline: reusing the part-spent one let step 4's fallback
//     fire on the very next tick and cut the agent's own handoff window short.
//   - every root role is a no-op at either scope. It already owns this subtree;
//     re-stamping would extend its deadline on repeat calls, and a "session"
//     pause would overwrite pause_scope, dropping it out of its own promotion
//     and out of the tree's queue freeze.
//   - roleMember is a no-op: the outer pause owns it. Upgrading a cascaded
//     descendant to pause_root = 1 is what Ruling A exists to prevent -- one
//     tick later it fabricates its own "orchestrator did not respond"
//     checkpoint.
//   - roleInvalid is a no-op: nothing here understands the row well enough to
//     write to it.
func (s *Store) pause(ctx context.Context, name, scope string) (Session, int, error) {
	a, err := s.Agent(ctx, name)
	if err != nil {
		return Session{}, 0, err
	}
	// Batch 3: the replacement coordinator owns session transitions while
	// an operation is in flight; a direct Pause would race its driver.
	if err := s.refuseIfOperationInFlight(ctx, a.ID); err != nil {
		return Session{}, 0, err
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		return Session{}, 0, err
	}
	role := subtreeRoleOf(ses.State, ses.PauseScope, ses.PauseRoot)
	if !role.live() {
		return Session{}, 0, &items.Error{Code: items.CodeConflict, Message: notRunning}
	}
	deadline := s.Now().Add(time.Duration(s.pauseDeadlineSec(ctx)) * time.Second)
	if scope != "subtree" {
		if role != roleIdle {
			return ses, 0, nil
		}
		ses, err = s.pauseOne(ctx, a, ses, deadline, "session")
		if err != nil {
			return Session{}, 0, err
		}
		return ses, 1, nil
	}
	if role != roleIdle && role != roleSessionPausing && !role.isRoot() {
		return ses, 0, nil
	}
	ds, err := s.descendantRoles(ctx, a.ID)
	if err != nil {
		return Session{}, 0, err
	}
	if role.isRoot() {
		// Already this subtree's root: its own row is left exactly as it is
		// (re-stamping would extend its deadline on every repeat call), but the
		// cascade still runs, because the set of descendants can have grown
		// since. Admit is limit-based only -- DrainQueue is the sole caller of
		// rootHasLiveSubtreePause -- so a direct swarm_spawn under a root that
		// is still 'running' (nothing gates spawning on a pause that has not
		// reached the root's own session yet) starts a child immediately.
		// Without this, a repeated pause-all left that child running and the
		// root's promotion waiting on it.
		cascaded, err := s.cascadeSubtreePause(ctx, deadline, ds)
		if err != nil {
			return Session{}, 0, err
		}
		return ses, cascaded, nil
	}
	if role == roleSessionPausing && !hasWaitingDescendant(ds) {
		return ses, 0, nil
	}
	ses, cascaded, err := s.pauseSubtree(ctx, a, ses, deadline, ds)
	if err != nil {
		return Session{}, 0, err
	}
	if role == roleIdle {
		cascaded++ // the target itself is newly in a pause; an upgraded one already was
	}
	return ses, cascaded, nil
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

// pauseSubtree pauses every idle descendant deepest-first, then marks the
// orchestrator's own session as this pause's root: it stays in whatever state
// it is in until advanceSubtreePauses sees every descendant finish (§10.5).
// It returns how many descendants it actually paused. The target's own row
// gets pause_scope, pause_root and a fresh deadline -- an upgrade from session
// scope is a new pause operation, and the caller has already refused one with
// nothing to cascade to.
func (s *Store) pauseSubtree(ctx context.Context, orch Agent, orchSes Session, deadline time.Time, ds []descendantRole) (Session, int, error) {
	cascaded, err := s.cascadeSubtreePause(ctx, deadline, ds)
	if err != nil {
		return Session{}, 0, err
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
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET pause_scope = 'subtree',
			pause_deadline_at = ?, pause_root = 1 WHERE id = ?`, db.Millis(deadline), orchSes.ID); err != nil {
			return err
		}
		return s.publishAgentChanged(ctx, tx, orch.Name, orch.RootItemID)
	})
	if err != nil {
		return Session{}, 0, err
	}
	orchSes.PauseScope, orchSes.PauseRoot, orchSes.PauseDeadlineAt = "subtree", true, &deadline
	return orchSes, cascaded, nil
}

// cascadeSubtreePause pauses every idle descendant, deepest first, and returns
// how many it transitioned.
//
// roleIdle is the only descendant a cascade may touch. An ended one has nothing
// to pause. One already pausing -- under an outer pause (roleMember), under its
// own earlier session pause, or at any point in its own subtree pause -- keeps
// its deadline and its progress: pauseOne has no idempotency guard of its own,
// so a repeated pause-all would otherwise reset a 'stopping' descendant whose
// handoff is already written back to 'pause_requested'. And a descendant that is
// itself a subtree-pause ROOT manages its own subtree: pauseOne'ing it would
// move it off 'running', dropping it out of promotePendingSubtreePauses for good
// and permanently losing the combined handoff payload §10.5 step 3 promises it.
// The pause waits for such a descendant exactly as it waits for any live one.
func (s *Store) cascadeSubtreePause(ctx context.Context, deadline time.Time, ds []descendantRole) (int, error) {
	cascaded := 0
	for _, d := range ds {
		if d.role != roleIdle {
			continue
		}
		if _, err := s.pauseOne(ctx, d.agent, d.ses, deadline, "subtree"); err != nil {
			return 0, err
		}
		cascaded++
	}
	return cascaded, nil
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

// pendingSubtreePauses finds every subtree-pause root still awaiting its first
// promotion (roleRootPending). Only a root is eligible: a cascaded descendant
// carries pause_scope = 'subtree' too, and promoting one would send it a
// combined-handoff control message it was never the target of.
func (s *Store) pendingSubtreePauses(ctx context.Context) ([]pendingSubtree, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.agent_id, ses.state,
		COALESCE(ses.pause_scope, ''), ses.pause_root FROM sessions ses
		WHERE `+subtreePauseCandidates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingSubtree
	for rows.Next() {
		var p pendingSubtree
		var state, scope string
		var root int
		if err := rows.Scan(&p.sesID, &p.agentID, &state, &scope, &root); err != nil {
			return nil, err
		}
		if subtreeRoleOf(SessionState(state), scope, root != 0) == roleRootPending {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

func (s *Store) promotePendingSubtreePauses(ctx context.Context) error {
	candidates, err := s.pendingSubtreePauses(ctx)
	if err != nil {
		return err
	}
	for _, p := range candidates {
		descendants, err := s.descendantRoles(ctx, p.agentID)
		if err != nil {
			return err
		}
		allDone := true
		children := make([]map[string]string, 0, len(descendants))
		for _, d := range descendants {
			// Any live descendant blocks the promotion, whatever its role --
			// including one that is itself a subtree-pause root, which the
			// cascade deliberately left to finish its own pause first.
			if d.role.live() {
				allDone = false
				break
			}
			// A queued descendant never ran (Fix R-1 lets it into this pause
			// at all, via hasWaitingDescendant), so it never has a checkpoint
			// or anything else to report -- it must not appear in the
			// combined handoff as if it were a paused child that simply left
			// an empty summary.
			if d.queued() {
				continue
			}
			var itemKey string
			_ = s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, d.agent.ItemID).Scan(&itemKey)
			children = append(children, map[string]string{"agent": d.agent.Name, "item": itemKey,
				"summary": s.latestCheckpointSummary(ctx, d.agent.ID)})
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

// overdueSubtreePauses finds every unresponsive subtree-pause root: a root
// (roleRootActive) that is past its deadline without having handed off yet.
//
// The role, not any one column, is the whole point of this query. A cascaded
// descendant carries pause_scope = 'subtree' as well, and an earlier version
// keyed on that alone fabricated an "orchestrator did not respond" checkpoint
// for a plain interrupted worker -- which, being kind 'handoff', made
// killAfterHandoff (5 s) pre-empt the killAfterInterrupt (10 s) already in
// flight for it. A later version keyed on "does this agent have children",
// which wrongly excluded a childless root (§10.5 step 4 never gates its
// fallback on child count) and wrongly included a mid-tree orchestrator that
// has children but is only a descendant of someone else's pause. roleRootActive
// answers both: root identity comes from pause_root, and the phase excludes
// roleRootPending (not yet promoted, its deadline is not its own yet),
// roleRootSpawning (its pane has not even started) and roleRootStopping
// (already handed off, on the ordinary kill timer -- a 'blocked' or 'failed'
// checkpoint gets a session there with no handoff row for
// writeUnresponsiveOrchestratorCheckpoints' own guard to catch).
func (s *Store) overdueSubtreePauses(ctx context.Context) ([]overdueSubtree, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.agent_id, ses.attempt, ses.state,
		COALESCE(ses.pause_scope, ''), ses.pause_root FROM sessions ses
		WHERE `+subtreePauseCandidates+`
		AND ses.pause_deadline_at IS NOT NULL AND ses.pause_deadline_at < ?`, db.Millis(s.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []overdueSubtree
	for rows.Next() {
		var o overdueSubtree
		var state, scope string
		var root int
		if err := rows.Scan(&o.sesID, &o.agentID, &o.attempt, &state, &scope, &root); err != nil {
			return nil, err
		}
		if subtreeRoleOf(SessionState(state), scope, root != 0) == roleRootActive {
			out = append(out, o)
		}
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
		// A queued child never started (Fix R-1 lets it be part of a subtree
		// pause at all now), so it was never in a position to hand off --
		// listing it as a blocker would blame it for something it never had
		// a chance to do.
		if c.State == AgentQueued {
			continue
		}
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
// Resume is swarm_control's resume action. sessionID/requestID are I11's
// idempotency key, scoped to the calling orchestrator's own MCP session (not
// the resumed agent's); requestID empty means "no idempotency, just run
// once."
func (s *Store) Resume(ctx context.Context, name, sessionID, requestID string) (Agent, error) {
	a, err := s.Agent(ctx, name)
	if err != nil {
		return Agent{}, err
	}
	// A genuine replay must not re-evaluate the state guard below: by the
	// time it replays, the resumed session has moved on (that's the whole
	// point), so it would spuriously fail here. Must not start a second
	// session either (see PeekIdempotent's own doc comment).
	var out Agent
	if hit, err := PeekIdempotent(ctx, s, sessionID, requestID, &out); err != nil {
		return Agent{}, err
	} else if hit {
		return out, nil
	}
	// Batch 3: Resume must not launch a competing session while the
	// replacement coordinator is driving one.
	if err := s.refuseIfOperationInFlight(ctx, a.ID); err != nil {
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
	// A provider resume reattaches (ResumeKickoff); a fresh launch after a
	// pause is a successor generation (SuccessorKickoff in resume mode plus
	// the durable-state-wins addition).
	succMode := ""
	if !resume {
		succMode = "resume"
	}
	newSes, err := s.startSession(ctx, a, ses.Attempt, ses.Generation+1, resume, ses.ProviderSessionID, succMode)
	if err != nil {
		return Agent{}, err
	}
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

		// A real resume (ad.Resume, not a fresh Launch) reattaches a session
		// with nothing telling it to act: unlike claude/codex/cursor/agy, muse's
		// `resume <sid>` CLI takes no prompt/kickoff argument at all (confirmed
		// live 2026-09-23, see Muse.Resume), so it can't get a reminder on argv
		// the way the others do. Queuing this agent a message closes that gap
		// for every kind, not just muse: WakeDue already pastes the generic
		// "call swarm_sync" idle nudge into any pane with a pending immediate
		// message once it goes idle (the same mechanism muse's Wake -- always
		// tmux-paste, see its doc comment -- already relies on for ordinary
		// inbox delivery). Adapters that already carry a kickoff on their
		// resume argv see this message on the swarm_sync call their own
		// kickoff text tells them to make, well before WakeDue's 20s paste
		// delay, so it never produces a redundant paste for them.
		if resume {
			payload, err := json.Marshal(map[string]any{"event": "resumed", "agent": a.Name})
			if err == nil {
				_, _ = s.enqueue(ctx, tx, Message{
					Kind:       "relay",
					Origin:     "daemon",
					ToAgentID:  a.ID,
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
	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), out, newSes, s.Adapters[out.Kind]); err != nil {
			s.logf("resume: watchStartup %s: %v", out.Name, err)
		}
	})
	return out, nil
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
// n is what pause() reports: the sessions this call actually brought into a
// pause. A target that was already pausing adds nothing of itself (it may
// still be upgraded to subtree scope, which transitions nothing), and a
// descendant the cascade correctly steps over adds nothing either -- counting
// every live descendant instead, as an earlier version did, reported work on a
// tree where nothing at all had changed.
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
		ds, err := s.descendantRoles(ctx, a.ID)
		if err != nil {
			s.logf("pause-all: %s: %v", name, err)
			continue
		}
		// Only a target with something under it gets subtree scope: a
		// standalone agent's pause has no subtree to freeze or cascade to.
		// "Something under it" includes a queued spawn with no live session
		// yet (Fix R-1) -- session scope here would leave DrainQueue free to
		// admit it mid-pause, since its freeze is keyed on pause_scope, which
		// a session-scoped pause never sets on anything but the target itself.
		scope := "session"
		if hasWaitingDescendant(ds) {
			scope = "subtree"
		}
		_, k, err := s.pause(ctx, name, scope)
		if err != nil {
			s.logf("pause-all: %s: %v", name, err)
			continue
		}
		n += k
	}
	return n, nil
}

// rootHasLiveSubtreePause reports whether rootItemID has a subtree pause in
// flight: any session in the tree whose role says it is part of one (§10.5),
// from the moment Pause(scope="subtree") is called -- whether or not the root
// has itself moved past 'running' yet -- through to 'paused'/'interrupted'.
// Once every such session has ended the pause is resolved and the queue thaws.
// DrainQueue calls this to freeze a root's queued spawns for exactly as long as
// the pause is live, without touching agents.state (which stays 'queued').
func (s *Store) rootHasLiveSubtreePause(ctx context.Context, rootItemID string) (bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.state, COALESCE(ses.pause_scope, ''), ses.pause_root
		FROM sessions ses JOIN agents a ON a.id = ses.agent_id
		WHERE a.root_item_id = ? AND `+subtreePauseCandidates, rootItemID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var state, scope string
		var root int
		if err := rows.Scan(&state, &scope, &root); err != nil {
			return false, err
		}
		if subtreeRoleOf(SessionState(state), scope, root != 0).inSubtreePause() {
			found = true
		}
	}
	return found, rows.Err()
}
