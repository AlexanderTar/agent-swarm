package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
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

// progressDeadlockTimeout is how long a waiting session gets after its most
// recent checkpoint -- if that checkpoint was "progress" -- before the
// daemon assumes it's stuck on an unresolved ask and relays to its parent.
// See docs/specs/2026-09-22-progress-checkpoint-deadlock-nudge.md.
const progressDeadlockTimeout = 5 * time.Minute

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
	LastCheckpointID, LastCheckpointSummary    string
	LastCheckpointKind                         CheckpointKind
	LastCheckpointAt                           *time.Time
	LastCheckpointNext                         []string
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
	finished, err := s.finishedAgentTmuxNames(ctx)
	if err != nil {
		return err
	}
	if err := s.forgetFinishedClaudeTrust(ctx); err != nil {
		s.logf("reconcile: forget finished claude trust: %v", err)
	}
	for _, p := range panes {
		if known[p.Session] {
			continue
		}
		if finished[p.Session] {
			// The agent is done, live or dead, acted on or not: nothing is
			// ever coming back to this pane. Left running, it's exactly how
			// a stuck trust dialog sat unnoticed for days (2026-09-26
			// incident): the reaper deliberately spares live panes of
			// failed/crashed/cancelled *sessions* (P0-crash-1, above), but
			// once the *agent* itself is finished or acknowledged, there is
			// no human left who is going to inspect it.
			if err := s.Tmux.Kill(ctx, p.Session); err != nil {
				return err
			}
			s.logf("reconcile: killed pane %s of finished agent", p.Session)
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
	if err := s.notifyUndeliveredMessages(ctx); err != nil {
		return err
	}
	if err := s.notifyUnansweredQuestions(ctx); err != nil {
		return err
	}
	if err := s.withdrawOrphanedRequests(ctx); err != nil {
		return err
	}
	// P9 (spec B4): crash recovery for a daemon restart between a
	// checkpoint's commit and the advance() call that should have followed
	// it immediately after.
	if err := s.recoverWorkflows(ctx); err != nil {
		return err
	}
	// Continuity: advance replacement operations from their durable phase.
	// A restart between the intent commit and the successor launch resumes
	// here instead of wedging the agent behind the partial unique index.
	if err := s.ResumeOperations(ctx); err != nil {
		return err
	}
	// resumableAgentIDs, not `live`: paused/interrupted sessions are not in
	// liveSessionRows (LiveStates excludes them) but ARE resumable -- a
	// paused codex session's thread store lives inside its (agent-keyed)
	// CODEX_HOME, and `codex resume` re-reads it, so deleting it the tick
	// after pause would turn a resumable session into "no rollout found for
	// thread id ..." on resume, a regression the old (never-cleaned) home
	// never had. Grouped by agent_id (review round 1, blocking): codex's
	// home is keyed on the agent, not the session, so the keep-set must be
	// too -- a session-id keep-set would leave every OTHER generation of a
	// resumable agent (i.e. every generation but its newest) looking dead.
	// Real wall-clock time.Now(), not s.now(): this is compared against real
	// filesystem mtimes (os.ReadDir/os.FileInfo), which are always real time
	// regardless of any injected/logical clock s.Now overrides for the rest
	// of Reconcile's business-time logic.
	snapshotAt := time.Now()
	resumableAgentIDs, err := s.queryIDs(ctx, `SELECT DISTINCT agent_id FROM sessions
		WHERE state NOT IN ('completed', 'failed', 'crashed', 'cancelled')`)
	if err != nil {
		s.logf("reconcile: list resumable agents for codex home reclaim: %v", err)
	} else {
		reclaimCodexHomes(s.Home, resumableAgentIDs, snapshotAt, s.logf)
	}
	// D8 (batch-2 review): run at most once per daemon run. See the
	// codexLaunchHomesReclaim field doc for why this isn't just a
	// per-tick call left to self-limit.
	s.codexLaunchHomesReclaim.Do(func() {
		if err := s.reclaimOldCodexLaunchHomes(ctx); err != nil {
			s.logf("reconcile: reclaim old codex launch homes: %v", err)
		}
	})
	return s.sweepFinishedRoots(ctx)
}

// reclaimCodexHomes removes internal/adapter.CodexHomeDir's short CODEX_HOME
// directories (<home>/cx/<hash>) for agents that are no longer resumable
// (every one of their sessions is completed/failed/crashed/cancelled -- see
// Reconcile's call site for why this is NOT just the live set). codex runs
// with --no-daemon (internal/adapter/codex.go flags()), so there is no
// daemon process to stop here -- only the directory. There is no general
// launch-dir GC in this codebase (run/launch/<session>/ is never cleaned up
// today, for any adapter, except the one-time codex-home migration in
// reclaimOldCodexLaunchHomes below); this sweep is scoped to codex's own
// short-home dirs, which are cheap to name deterministically from a
// resumable agent id and don't require plumbing a new teardown hook through
// every Tmux.Kill call site.
//
// snapshotAt guards IMPORTANT 2 (review round 1): keepAgentIDs is a DB
// snapshot, and a session can be inserted (with its home mkdir'd, per
// startSession/setupEnv's insert-before-mkdir order) between that snapshot
// and this function's own os.ReadDir. Such a directory would look, wrongly,
// like a dead agent with no matching row. Any directory entry whose mtime is
// after snapshotAt is skipped rather than removed: it could not have been
// accounted for by a keep-set queried at or before that instant, so its
// absence from keep is not evidence it's dead -- the next tick, once its row
// has definitely landed, will resolve it correctly either way. Errors
// removing one entry are logged, not returned, so one stuck directory (e.g.
// permissions) never stops the rest of the sweep (review round 1, MINOR 3).
func reclaimCodexHomes(home string, keepAgentIDs []string, snapshotAt time.Time, logf func(string, ...any)) {
	if home == "" { // never resolve to the relative "cx" (review round 1, MINOR 2)
		return
	}
	root := filepath.Join(home, "cx")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		logf("reconcile: list codex homes: %v", err)
		return
	}
	keep := make(map[string]bool, len(keepAgentIDs))
	for _, id := range keepAgentIDs {
		keep[adapter.CodexHomeDirName(id)] = true
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if info, ierr := e.Info(); ierr == nil && info.ModTime().After(snapshotAt) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			logf("reconcile: remove codex home %s: %v", e.Name(), err)
		}
	}
}

// reclaimOldCodexLaunchHomes is a one-time cleanup (review round 1, MINOR 4)
// of the pre-fix (2026-09-26) per-launch codex-home directories
// (<home>/run/launch/<session id>/codex-home), for sessions in a terminal
// state only -- a live/paused session could in principle still be running
// against its old, long CODEX_HOME, and deleting a running agent's home out
// from under it is not a risk worth taking just to reclaim disk. Removes
// only the codex-home subdir, never the whole per-launch dir (other files,
// e.g. codex's own instructions file or claude's per-launch settings, still
// live alongside it), and only ever under s.Home. Self-limiting: once a
// session's codex-home is gone there is nothing left to remove on a later
// tick, so this needs no separate "already ran once" bookkeeping.
func (s *Store) reclaimOldCodexLaunchHomes(ctx context.Context) error {
	if s.Home == "" {
		return nil
	}
	launchRoot := filepath.Join(s.Home, "run", "launch")
	entries, err := os.ReadDir(launchRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	terminalIDs, err := s.queryIDs(ctx, `SELECT id FROM sessions
		WHERE state IN ('completed', 'failed', 'crashed', 'cancelled')`)
	if err != nil {
		return err
	}
	terminal := make(map[string]bool, len(terminalIDs))
	for _, id := range terminalIDs {
		terminal[id] = true
	}
	for _, e := range entries {
		if !terminal[e.Name()] {
			continue
		}
		old := filepath.Join(launchRoot, e.Name(), "codex-home")
		if _, err := os.Stat(old); err != nil {
			continue // never existed for this session (not codex, or already gone)
		}
		if err := os.RemoveAll(old); err != nil {
			s.logf("reconcile: remove old codex-home %s: %v", old, err)
		}
	}
	return nil
}

// withdrawOrphanedRequests closes every open HITL request whose owning agent
// is done: agents.state = 'finished', or its newest session (same order as
// LatestSession) is completed/failed/crashed/cancelled. paused/interrupted
// keep their rows: the answer is delivered as a message on resume. Runs before
// sweepFinishedRoots, which reads open requests in a tree.
func (s *Store) withdrawOrphanedRequests(ctx context.Context) error {
	// Requests owned by an in-flight replacement operation are skipped: the
	// predecessor session may already read dead (failed/crashed between the
	// intent commit and the stopping step) while the request is about to be
	// repointed at the successor. Withdrawing it here would lose the ask
	// mid-handoff; the operation driver owns these rows until it lands.
	ids, err := s.queryIDs(ctx, `SELECT r.id FROM requests r
		JOIN agents a ON a.id = r.agent_id
		WHERE r.state = 'open' AND r.is_hitl = 1
		  AND NOT EXISTS (SELECT 1 FROM agent_operations o WHERE o.agent_id = r.agent_id AND o.phase IN
		    ('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting'))
		  AND (a.state = 'finished'
		    OR (SELECT se.state FROM sessions se WHERE se.agent_id = r.agent_id
		        ORDER BY se.generation DESC, se.attempt DESC LIMIT 1)
		       IN ('completed', 'failed', 'crashed', 'cancelled'))
		ORDER BY r.created_at`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.tx(ctx, func(tx *sql.Tx) error { return s.closeRequestTx(ctx, tx, id) }); err != nil {
			s.logf("reconcile: withdraw orphaned request %s: %v", id, err)
		}
	}
	return nil
}

// terminalCheckpointKind is the most recent completed/failed checkpoint
// against the agent's OWN item for this attempt, if any. A completed/failed
// checkpoint against a child item (an orchestrator's routine board
// bookkeeping) does not count: it says nothing about whether the agent's own
// assignment is done.
func (s *Store) terminalCheckpointKind(ctx context.Context, agentID, itemID string, attempt int) (CheckpointKind, bool, error) {
	var kind string
	err := s.DB.QueryRowContext(ctx, `SELECT kind FROM checkpoints WHERE agent_id = ? AND item_id = ? AND attempt = ?
		AND kind IN ('completed', 'failed') ORDER BY created_at DESC, rowid DESC LIMIT 1`, agentID, itemID, attempt).Scan(&kind)
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
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	// A held relay counts as handled too: without this the 5 s tick would
	// re-notify and re-hold on every pass while the kind stays exhausted.
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppressed_relays
		WHERE agent_id = ? AND event = ? AND last_at >= ?`,
		toAgentID, event, db.Millis(sinceStartedAt)).Scan(&n)
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

// checkProgressDeadlock relays once to the nearest live ancestor when a
// child has gone idle-and-owes-nothing right after a progress checkpoint --
// the daemon-visible signature of a child waiting on an answer it never
// formally asked for (a "next" note aimed at the orchestrator, not a
// swarm_send question or a blocked checkpoint). See docs/specs/
// 2026-09-22-progress-checkpoint-deadlock-nudge.md.
//
// Deviation from the task brief: the brief said to link the relay to the
// checkpoint via Message.ReplyTo. messages.reply_to has
// `REFERENCES messages(id)` (schema/0001_init.sql) -- it was built for a
// message replying to another message (notifyUndeliveredMessages,
// swarm_send's own reply_to), not for pointing at a row in the checkpoints
// table, and a checkpoint id there trips a real FOREIGN KEY constraint
// failed at runtime. correlation_id has no such FK and is already a plain
// pass-through field (inbox.go), so the checkpoint id goes there instead.
// If a later task needs `reply_to == checkpoint id` specifically (e.g. the
// menubar dereferencing it against messages), that requires a messages
// table-recreate migration (as 0006 did for requests) to drop or relax the
// FK -- out of scope for this task.
func (s *Store) checkProgressDeadlock(ctx context.Context, r liveRow) error {
	if r.ParentAgentID == "" || r.LastCheckpointID == "" || r.LastCheckpointKind != Progress {
		return nil
	}
	// r.LastCheckpointAt is always set together with r.LastCheckpointID (both
	// come from the same LEFT JOIN row in liveSessionRows).
	if s.Now().Sub(*r.LastCheckpointAt) < progressDeadlockTimeout {
		return nil
	}
	already, err := s.alreadyRelayedForCheckpoint(ctx, r.LastCheckpointID)
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
			CorrelationID: r.LastCheckpointID, Payload: payload})
		return err
	})
}

// alreadyRelayedForCheckpoint is alreadyRelayedForMessage's counterpart for
// a checkpoint id (see checkProgressDeadlock's deviation note): same
// idempotency guard, keyed on correlation_id instead of reply_to since a
// checkpoint id can't satisfy messages.reply_to's FK.
// ponytail: no index on correlation_id -- add one if this table scan shows
// up under load.
func (s *Store) alreadyRelayedForCheckpoint(ctx context.Context, checkpointID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE kind = 'relay' AND correlation_id = ?`, checkpointID).Scan(&n)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	// Same held-counts-as-handled rule as alreadyRelayed, scoped to this
	// checkpoint's own tree and time: a progress_deadlock held after this
	// checkpoint was written covers it.
	var agentID string
	var createdAt int64
	if err := s.DB.QueryRowContext(ctx, `SELECT agent_id, created_at FROM checkpoints WHERE id = ?`,
		checkpointID).Scan(&agentID, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppressed_relays sr
		JOIN agents a ON a.id = sr.agent_id
		WHERE a.root_item_id = (SELECT root_item_id FROM agents WHERE id = ?)
		AND sr.event = 'progress_deadlock' AND sr.last_at >= ?`, agentID, createdAt).Scan(&n)
	return n > 0, err
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
//
// Spec B5: every distinct parent gets woken, not one arbitrary agent's --
// `agents.item_id` has no uniqueness constraint, so more than one agent can
// be actively assigned to the same blocked item (e.g. a workflow task's
// builder and reviewer both live on it), each under a different ancestor.
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
		agRows, err := tx.QueryContext(ctx, `SELECT id, name, root_item_id
			FROM agents WHERE item_id = ? AND state = 'active'`, d.id)
		if err != nil {
			return err
		}
		type activeAgent struct{ id, name, rootItemID string }
		var agents []activeAgent
		for agRows.Next() {
			var ag activeAgent
			if err := agRows.Scan(&ag.id, &ag.name, &ag.rootItemID); err != nil {
				agRows.Close()
				return err
			}
			agents = append(agents, ag)
		}
		agRows.Close()
		if err := agRows.Err(); err != nil {
			return err
		}

		woken := map[string]bool{} // ancestor id already relayed to, for this dependant item
		for _, ag := range agents {
			// The unguarded relay straight to parent_agent_id used to assume
			// the parent was alive to receive it -- the identical
			// no-liveness-check bug notifyUndeliveredMessages' own parent
			// fallback had (Bug 5): walk up to the nearest live ancestor
			// instead (this also handles "a top-level orchestrator itself:
			// nothing above it to relay to" -- nearestLiveAncestor returns
			// ok=false right away when the agent has no parent at all), and
			// skip the relay entirely if the whole chain above is dead.
			ancestor, ok, err := s.nearestLiveAncestor(ctx, ag.id)
			if err != nil {
				return err
			}
			if !ok || woken[ancestor.ID] {
				continue
			}
			woken[ancestor.ID] = true
			payload, err := json.Marshal(map[string]any{"event": "dependency_added", "agent": ag.name, "item": d.key})
			if err != nil {
				return err
			}
			if _, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: ancestor.ID,
				RootItemID: ag.rootItemID, ItemID: d.id, Payload: payload}); err != nil {
				return err
			}
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

// resolveDead is P9's wrapper around resolveDeadInner: after whatever
// session/agent transition the inner call makes, it triggers the engine's
// two remaining B4 triggers that live in reconcile rather than
// checkpoint.go -- a workflow run that just went terminal with no
// checkpoint of its own (crash/interrupted) advances its own workflow, and
// ANY child finishing frees a budget slot that may let a sibling
// workflow's waiting run start (spec B4 Budget/Triggers). Both are
// best-effort: a failure here is logged, never allowed to break the
// reconcile tick that already committed its own real state change.
func (s *Store) resolveDead(ctx context.Context, r liveRow, p Pane, paneKnown bool) error {
	err := s.resolveDeadInner(ctx, r, p, paneKnown)
	if err != nil {
		return err
	}
	if r.ParentAgentID != "" {
		if aerr := s.advanceWaitingForOwner(context.WithoutCancel(ctx), r.ParentAgentID); aerr != nil {
			s.logf("reconcile: advance waiting runs for %s: %v", r.ParentAgentID, aerr)
		}
	}
	return nil
}

func (s *Store) resolveDeadInner(ctx context.Context, r liveRow, p Pane, paneKnown bool) error {
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
	// Continuity: a session owned by an in-flight replacement operation is
	// the operation driver's to transition (stopping -> interrupted/paused
	// -> successor). Resolving it here as crashed would corrupt the walk
	// with a second, racing transition and a bogus crash relay.
	if owned, err := s.operationOwnsSession(ctx, r.AgentID); err != nil {
		return err
	} else if owned {
		return nil
	}
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
	var exitCode *int
	if paneKnown && p.Dead {
		c := p.DeadStatus
		exitCode = &c
	}
	// A pausing session that was sent interrupt keys (L11) ends interrupted, never
	// crashed, whatever attempt-scoped checkpoint it left behind.
	if r.State.Pausing() && s.getInterrupted(r.SessionID) != nil {
		// Opus review, fix round 2, finding 6: this branch is exclusively a
		// DELIBERATE pause (swarm_control pause) hitting its interrupt
		// deadline -- getInterrupted is only ever set by TickPause's own
		// interrupt() (pause.go). It is not a crash, so a workflow run
		// here must be left exactly as it is (still 'active', waiting): a
		// human's later swarm_control resume is what starts it going again,
		// not the engine. An earlier version of this branch marked the run
		// 'failed' the same as a crash, which made AutoRetry immediately
		// un-pause the agent with a fresh Retry() -- defeating the pause.
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
	kind, hasTerminal, err := s.terminalCheckpointKind(ctx, r.AgentID, r.ItemID, r.Attempt)
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
		var tail string
		if capture, err := s.Tmux.Capture(ctx, r.TmuxName, 10); err == nil {
			tail = strings.TrimSpace(capture)
		}
		ancestor, ancestorOk, _ := s.nearestLiveAncestor(ctx, r.AgentID)

		var run workflowRun
		var hasRun bool
		err := s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'crashed', exit_code = ?,
				ended_at = ? WHERE id = ?`, exitCode, db.Millis(now), r.SessionID); err != nil {
				return err
			}
			if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.crashed", AgentName: r.AgentName,
				ItemKey: r.ItemKey, Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}}); err != nil {
				return err
			}
			// P9 (spec B4): a crashed workflow agent (no terminal checkpoint
			// at all) is the engine's own crash signal too.
			var werr error
			run, hasRun, werr = s.workflowRunFor(ctx, tx, r.AgentID)
			if werr != nil {
				return werr
			}
			if hasRun {
				if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET state = 'failed', ended_at = ? WHERE id = ? AND state IN ('active', 'waiting')`,
					db.Millis(now), run.ID); err != nil {
					return err
				}
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
		if err == nil && hasRun {
			if aerr := s.advance(context.WithoutCancel(ctx), run.WorkflowID); aerr != nil {
				s.logf("reconcile: advance %s: %v", run.WorkflowID, aerr)
			}
		}
		return err
	}
}

// promptStep reports what promptTick decided to do this tick: press the
// matcher's keys, and/or escalate to a Needs-you row.
type promptStep struct{ Send, Escalate bool }

// promptTick advances the retry/escalation state for a visible (session,
// title) prompt and reports what to do this tick. It never presses keys or
// writes to the requests table itself: the caller does that and, once it has
// a request id for an Escalate step, records it via setPromptReqID.
func (s *Store) promptTick(sessionID, title string, hasKeys bool, now time.Time) promptStep {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.promptState == nil {
		s.promptState = map[string]*dialogState{}
	}
	k := sessionID + "|" + title
	st := s.promptState[k]
	if st == nil {
		st = &dialogState{firstSeen: now}
		s.promptState[k] = st
	}
	var out promptStep
	if hasKeys && st.sends < dialogMaxSends && (st.sends == 0 || now.Sub(st.lastSent) >= dialogRetryEvery) {
		st.sends++
		st.lastSent = now
		out.Send = true
	}
	if st.reqID == "" && now.Sub(st.firstSeen) >= dialogEscalateAfter {
		out.Escalate = true
	}
	return out
}

// setPromptReqID records the request id opened for a (session, title) prompt's
// escalation, so promptTick doesn't escalate it again.
func (s *Store) setPromptReqID(sessionID, title, reqID string) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if st := s.promptState[sessionID+"|"+title]; st != nil {
		st.reqID = reqID
	}
}

// openPromptTitles returns the titles this session currently has retry or
// escalation state for, so resolveAlive can close the ones that stopped
// matching this tick.
func (s *Store) openPromptTitles(sessionID string) []string {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	prefix := sessionID + "|"
	var out []string
	for k := range s.promptState {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	return out
}

// clearPromptState drops a (session, title) prompt's retry/escalation state,
// so the next time it's seen it starts fresh (a new escalation window, a new
// send budget).
func (s *Store) clearPromptState(sessionID, title string) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	delete(s.promptState, sessionID+"|"+title)
}

// setWatchStartupActive and hasActiveWatchStartup track which sessions a
// watchStartup goroutine is currently polling, so resolveAlive knows when it
// is safe to defer a Spawning session's dialogs to it versus when (a daemon
// restart dropped the goroutine) reconcile must take them over itself.
func (s *Store) setWatchStartupActive(sessionID string, active bool) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if active {
		if s.activeWatchStartup == nil {
			s.activeWatchStartup = map[string]bool{}
		}
		s.activeWatchStartup[sessionID] = true
		return
	}
	delete(s.activeWatchStartup, sessionID)
}

func (s *Store) hasActiveWatchStartup(sessionID string) bool {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	return s.activeWatchStartup[sessionID]
}

// hasEscalatedPrompt reports whether this session has a dialog escalated to
// an open Needs-you row. An escalated session is blocked on the user, not on
// the agent: it must not also raise a no-ack relay to the parent or be
// flagged stale.
//
// It checks two things, not just one, because reconcile's own in-memory
// promptState (an escalation it drove itself, or a mirror of one) does not
// survive a daemon restart, while the open request row in the DB does: a
// session that was already escalated before a restart -- Spawning under a
// watchStartup goroutine that is now gone, or simply Running -- must still
// suppress no-ack the instant reconcile resumes, not just once its own
// retry/escalation timers run their course again from zero. The DB check is
// scoped to prompt titles this adapter's own dialogs use (the union of
// PromptPatterns and StartupDialogs -- they share titles today for every
// adapter, but nothing enforces that, and a mismatch would reopen this exact
// gap for whichever dialog only StartupDialogs names), not every open
// 'prompt' row, so an unrelated AskPrompt HITL question does not also get
// treated as a blocking dialog here (owesNothing already covers those).
func (s *Store) hasEscalatedPrompt(ctx context.Context, sessionID string, ad adapter.Adapter) (bool, error) {
	s.bookkeepingMu.Lock()
	prefix := sessionID + "|"
	for k, st := range s.promptState {
		if strings.HasPrefix(k, prefix) && st.reqID != "" {
			s.bookkeepingMu.Unlock()
			return true, nil
		}
	}
	s.bookkeepingMu.Unlock()
	if ad == nil {
		return false, nil
	}
	var titles []string
	for _, m := range ad.PromptPatterns() {
		titles = append(titles, m.Title)
	}
	for _, d := range ad.StartupDialogs() {
		titles = append(titles, d.Title)
	}
	if len(titles) == 0 {
		return false, nil
	}
	args := make([]any, 0, len(titles)+1)
	args = append(args, sessionID)
	placeholders := make([]string, 0, len(titles))
	for _, title := range titles {
		args = append(args, title)
		placeholders = append(placeholders, "?")
	}
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE session_id = ? AND kind = 'prompt'
		AND state = 'open' AND prompt IN (`+strings.Join(placeholders, ",")+`)`, args...).Scan(&n)
	return n > 0, err
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

// getLastTitle and setLastTitle guard Store.lastTitle, so resolveAlive only
// calls RenameWindow when sessionTitle's output actually changed.
func (s *Store) getLastTitle(sessionID string) string {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	return s.lastTitle[sessionID]
}

func (s *Store) setLastTitle(sessionID, title string) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.lastTitle == nil {
		s.lastTitle = map[string]string{}
	}
	s.lastTitle[sessionID] = title
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
	matchedTitle := ""
	// A spawning session's dialogs belong to watchStartup, which is already
	// polling this pane: matching here too would double-press keys and open
	// a duplicate row. That only holds while a watchStartup goroutine is
	// actually alive for it -- after a daemon restart nothing re-attaches
	// one, so a Spawning session left behind (pre-escalation and swallowed
	// keys, or escalated and never resolved) would otherwise sit stuck
	// forever with reconcile permanently deferring to a goroutine that no
	// longer exists.
	ownedByWatchStartup := r.State == Spawning && s.hasActiveWatchStartup(r.SessionID)
	if ok {
		capture, err := s.Tmux.Capture(ctx, r.TmuxName, 15)
		if err != nil {
			return err
		}
		idle = ad.Idle(capture)
		if !idle && !ownedByWatchStartup {
			plain := stripANSI(capture)
			now := s.Now()
			for _, m := range ad.PromptPatterns() {
				if m.Match == nil || !m.Match.MatchString(plain) {
					continue
				}
				if m.Require != nil && !m.Require.MatchString(plain) {
					// The dialog is still on screen, just mid-render (the
					// option line hasn't drawn yet): claim the title so the
					// resolve-on-clear loop below leaves its state and any
					// open row alone instead of resolving it "via terminal"
					// and recreating the send/escalation budget from zero.
					matchedTitle = m.Title
					continue
				}
				matchedTitle = m.Title
				hasKeys := m.Action != ""
				step := s.promptTick(r.SessionID, m.Title, hasKeys, now)
				if step.Send {
					if err := s.Tmux.Keys(ctx, r.TmuxName, strings.Split(m.Action, "+")...); err != nil {
						s.logf("reconcile: auto-answer %q for %s: %v", m.Title, r.SessionID, err)
					} else {
						s.logf("reconcile: %s: sent %v for prompt %q", r.SessionID, m.Action, m.Title)
					}
				}
				if step.Escalate {
					req, _, err := s.OpenDialogPrompt(ctx, r.SessionID, m.Title)
					if err != nil {
						s.logf("reconcile: %s: open dialog prompt %q: %v", r.SessionID, m.Title, err)
						s.clearPromptState(r.SessionID, m.Title)
					} else {
						s.setPromptReqID(r.SessionID, m.Title, req.ID)
						s.logf("reconcile: %s: prompt %q still visible after %s, opened %s", r.SessionID, m.Title, dialogEscalateAfter, req.ID)
					}
				}
				break
			}
		}
	}
	if !ownedByWatchStartup {
		for _, title := range s.openPromptTitles(r.SessionID) {
			if title == matchedTitle {
				continue
			}
			if err := s.ResolveDialogPrompt(ctx, r.SessionID, title); err != nil {
				s.logf("reconcile: %s: resolve prompt %q: %v", r.SessionID, title, err)
			}
			s.clearPromptState(r.SessionID, title)
		}
	}
	waiting := idle && owesNothing
	if title := sessionTitle(waiting, r.Role, r.RootItemID, r.AgentName); title != s.getLastTitle(r.SessionID) {
		if err := s.Tmux.RenameWindow(ctx, r.TmuxName, title); err != nil {
			s.logf("reconcile: rename-window %s: %v", r.SessionID, err)
		} else {
			s.setLastTitle(r.SessionID, title)
		}
	}
	if waiting != r.Waiting {
		if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET waiting = ? WHERE id = ?`,
			boolToInt(waiting), r.SessionID); err != nil {
			return err
		}
	}
	escalated := false
	if !waiting { // waiting alone already takes the early return below
		var err error
		escalated, err = s.hasEscalatedPrompt(ctx, r.SessionID, ad)
		if err != nil {
			return err
		}
	}
	if waiting || escalated {
		if err := s.checkProgressDeadlock(ctx, r); err != nil {
			return err
		}
		return nil // M6: a waiting session is never stale; an escalated dialog is blocked on the user, not the agent
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
		already, err := s.alreadyNotifiedStale(ctx, r.AgentID, r.lastActivity())
		if err != nil {
			return err
		}
		if !already {
			if err := s.notify(ctx, nil, NotifyInput{Kind: "agent.stale", AgentName: r.AgentName, ItemKey: r.ItemKey,
				Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}}); err != nil {
				return err
			}
			// When Notifier does not write to the notifications table (such as fakeNotifier in unit tests),
			// record a row so alreadyNotifiedStale suppresses duplicate notifications within the same silence period.
			var recorded int
			_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
				WHERE agent_id = ? AND kind = 'agent.stale' AND created_at >= ?`,
				r.AgentID, db.Millis(r.lastActivity())).Scan(&recorded)
			if recorded == 0 {
				_, _ = s.DB.ExecContext(ctx, `INSERT INTO notifications
					(id, level, kind, title, body, agent_id, item_id, dedup_key, created_at)
					VALUES (?, 'attention', 'agent.stale', 'Agent stale', 'Agent stale', ?, ?, ?, ?)`,
					fmt.Sprintf("ntf-%s-%d", r.SessionID, db.Millis(s.Now())),
					r.AgentID, r.ItemID, "agent.stale:"+r.AgentName, db.Millis(s.Now()))
			}
			return nil
		}
	}
	return nil
}

func (s *Store) alreadyNotifiedStale(ctx context.Context, agentID string, since time.Time) (bool, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
		WHERE agent_id = ? AND kind = 'agent.stale' AND created_at >= ?`,
		agentID, db.Millis(since)).Scan(&count)
	return count > 0, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// liveDescendants counts agentID's queued/active descendants, walking the
// whole subtree (not just direct children). q is either s.DB or a caller's
// own tx, the same seam txQuerier gives agentHasLiveSession above. Shared by
// owesNothing and the worktree reclaim gate (reconcile.go's ReclaimWorktrees)
// -- the spec calls this out as "reused verbatim rather than inlined."
func (s *Store) liveDescendants(ctx context.Context, q txQuerier, agentID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `WITH RECURSIVE d(id) AS (
			SELECT id FROM agents WHERE parent_agent_id = ?
			UNION ALL SELECT a.id FROM agents a JOIN d ON a.parent_agent_id = d.id)
		SELECT COUNT(*) FROM agents WHERE id IN (SELECT id FROM d) AND state IN ('queued', 'active')`,
		agentID).Scan(&n)
	return n, err
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
	liveChildren, err := s.liveDescendants(ctx, s.DB, r.AgentID)
	if err != nil {
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

// swarmOwnedWorkspace reports whether path is a Swarm-owned workspace: under
// <home>/work or <home>/worktrees (D2/D3, dialog-needs-you spec). It is the
// only predicate D2's cleanup and D3's install-time prune use to decide
// which ~/.claude.json entries they may ever touch -- the user's own
// projects, anywhere else, are never matched. filepath.Clean on both sides
// guards against a trailing slash; the prefix check requires a path
// separator right after the root so "<home>/work-extra" (a real but
// unrelated sibling directory) does not collide with "<home>/work".
func swarmOwnedWorkspace(home, path string) bool {
	if home == "" || path == "" {
		return false
	}
	path = filepath.Clean(path)
	for _, root := range []string{"work", "worktrees"} {
		prefix := filepath.Clean(filepath.Join(home, root)) + string(filepath.Separator)
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// forgetFinishedClaudeTrust removes the Claude trust entry (D1) for every
// Swarm-owned workspace belonging to a session of a finished or
// acknowledged agent (D2, dialog-needs-you spec). ForgetFolder is
// idempotent (a no-op once the key is already gone), so this can run every
// tick without re-checking what it already did. Scoped to Claude sessions
// only: every other kind's trust state (agy, cursor's flags, ...) has no
// per-session file this touches.
func (s *Store) forgetFinishedClaudeTrust(ctx context.Context) error {
	ad, ok := s.Adapters[Claude]
	if !ok {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT s2.id, s2.cwd FROM sessions s2
		JOIN agents a ON a.id = s2.agent_id
		WHERE a.kind = 'claude' AND a.state IN ('finished', 'acknowledged') AND s2.cwd != ''`)
	if err != nil {
		return err
	}
	type row struct{ id, cwd string }
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.cwd); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range todo {
		s.bookkeepingMu.Lock()
		done := s.forgottenClaudeTrust[r.id]
		s.bookkeepingMu.Unlock()
		if done || !swarmOwnedWorkspace(s.Home, r.cwd) {
			continue
		}
		if err := ad.ForgetFolder(ctx, r.cwd); err != nil {
			s.logf("reconcile: forget claude trust for %s: %v", r.cwd, err)
			continue
		}
		s.bookkeepingMu.Lock()
		if s.forgottenClaudeTrust == nil {
			s.forgottenClaudeTrust = map[string]bool{}
		}
		s.forgottenClaudeTrust[r.id] = true
		s.bookkeepingMu.Unlock()
	}
	return nil
}

// finishedAgentTmuxNames returns the tmux names of every session whose owning
// agent is finished or acknowledged: nobody is coming back to that pane, live
// or dead. Checked before terminalTmuxNames (session-state based), since a
// finished agent's most recent session may itself still read as terminal.
func (s *Store) finishedAgentTmuxNames(ctx context.Context) (map[string]bool, error) {
	names, err := s.queryIDs(ctx, `SELECT DISTINCT s.tmux_name FROM sessions s
		JOIN agents a ON a.id = s.agent_id WHERE a.state IN ('finished', 'acknowledged')`)
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

// reclaimGrace is how long an owner agent must have been finished before its
// worktrees become reclaimable. Retry (agents.go) gates on *session* state
// (Completed|Failed|Crashed|Interrupted) and flips a finished agent back to
// active, so "finished" is never permanently terminal and this window is
// real, not decorative.
const reclaimGrace = time.Hour

// reclaimGateWhere is the §4.3 eligibility clause, passed to
// Worktree.Candidates. The owner's own unreleased reservation never blocks
// (measured: 91 real rows look like this, from the self-completion asymmetry
// in WriteCheckpoint's sibling-only release); any other agent's does.
const reclaimGateWhere = `w WHERE w.state IN ('active', 'retained')
	AND EXISTS (
		SELECT 1 FROM agents a
		WHERE a.id = w.owner_agent_id
		  AND a.state IN ('finished', 'acknowledged')
		  AND a.finished_at IS NOT NULL
		  AND a.finished_at <= ?)
	AND NOT EXISTS (
		SELECT 1 FROM sessions s
		WHERE s.agent_id = w.owner_agent_id
		  AND s.state IN ('spawning','running','pause_requested','quiescing','stopping'))
	AND NOT EXISTS (
		SELECT 1 FROM worktree_reservations r
		WHERE r.worktree_id = w.id
		  AND r.agent_id <> w.owner_agent_id
		  AND r.released_at IS NULL)
	ORDER BY w.created_at`

// ReclaimWorktrees is the per-agent backstop sweepFinishedRoots cannot be.
// sweepFinishedRoots waits for a whole root item to reach done/cancelled,
// which a multi-week epic never does; this waits only for one worktree's own
// owner to be genuinely finished.
//
// It never returns early on a per-worktree failure: one unreadable repo must
// not strand every other worktree behind it. sweepFinishedRoots' fail-fast
// loop is not inherited. It does check ctx between candidates, so a shutdown
// mid-pass stops promptly with whatever it has already done intact -- each
// worktree's row write is its own transaction, so there is no partial state
// to unwind.
func (s *Store) ReclaimWorktrees(ctx context.Context) error {
	cutoff := db.Millis(s.Now().Add(-reclaimGrace))
	cands, err := s.Worktree.Candidates(ctx, reclaimGateWhere, cutoff)
	if err != nil {
		return err
	}
	var reclaimed, kept, failed int
	for _, wt := range cands {
		if ctx.Err() != nil {
			break
		}
		live, err := s.liveDescendants(ctx, s.DB, wt.OwnerAgentID)
		if err != nil {
			s.logf("worktree: reclaim of %s failed, keeping it: %v", wt.Path, err)
			failed++
			continue
		}
		if live > 0 {
			s.logf("worktree: keeping %s (owner has a live descendant)", wt.Path)
			kept++
			continue
		}
		_, pathErr := os.Lstat(wt.Path)
		pathWasGone := pathErr != nil
		done, err := s.Worktree.ReclaimOne(ctx, wt)
		if err != nil {
			s.logf("worktree: reclaim of %s failed, keeping it: %v", wt.Path, err)
			failed++
			continue
		}
		switch done.State {
		case "removed":
			if pathWasGone {
				s.logf("worktree: %s is gone, closing its row", wt.Path)
			} else {
				s.logf("worktree: reclaimed %s", wt.Path)
			}
			reclaimed++
		default:
			s.logf("worktree: keeping %s (%s)", wt.Path, done.RetainedReason)
			kept++
		}
	}
	s.logf("worktree: reclaim pass: %d reclaimed, %d kept, %d failed", reclaimed, kept, failed)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// ReclaimWorktreesLoop runs ReclaimWorktrees every `every` until ctx is
// cancelled. Same shape as ReconcileLoop, deliberately.
func (s *Store) ReclaimWorktreesLoop(ctx context.Context, every time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.after(every):
		}
		if err := s.ReclaimWorktrees(ctx); err != nil && ctx.Err() == nil {
			s.logf("reclaim: %v", err)
		}
	}
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

// agentHasLiveSession reports whether agentID currently has any session in a
// live state -- the same session.state list wakeCandidates (wake.go) and
// liveSessionRows above already use to decide who gets woken or reconciled,
// so this asks the exact question the rest of the runtime already asks
// rather than inventing a new definition of liveness. q is either s.DB or a
// caller's own tx (txQuerier, from requests.go): Send() (inbox.go) checks
// inside its existing transaction before enqueuing, and
// notifyUndeliveredMessages below checks outside one.
func (s *Store) agentHasLiveSession(ctx context.Context, q txQuerier, agentID string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ? AND state IN
		('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')`, agentID).Scan(&n)
	return n > 0, err
}

// agentCanReceive reports whether a message enqueued to agentID now will
// ever actually be delivered: a live session now, a resumable one (Resume
// starts generation n+1 on the same agent's inbox -- pause.go), or an agent
// still queued for its first session (DrainQueue admits it later --
// limits.go). Only a terminal latest session (or a finished/acknowledged
// agent) is genuinely undeliverable. Both Send() (inbox.go) and
// undeliveredAgentMessages below use this exact question instead of each
// asking a narrower one of their own -- that mismatch (checking only
// LiveStates) is what let a paused/interrupted/queued target read as
// unreachable when it was not.
func (s *Store) agentCanReceive(ctx context.Context, q txQuerier, agentID string) (bool, error) {
	var agentState, sesState string
	if err := q.QueryRowContext(ctx, `SELECT a.state, COALESCE((SELECT state FROM sessions
		WHERE agent_id = a.id ORDER BY generation DESC, attempt DESC LIMIT 1), '')
		FROM agents a WHERE a.id = ?`, agentID).Scan(&agentState, &sesState); err != nil {
		return false, err
	}
	if agentState == string(AgentFinished) || agentState == string(AgentAcknowledged) {
		return false, nil
	}
	if agentState == string(AgentQueued) {
		return true, nil
	}
	st := SessionState(sesState)
	return st.Live() || st == Paused || st == Interrupted, nil
}

// nearestLiveAncestor walks from agentID's parent upward -- the same
// agentByID/LatestSession ancestor walk pauseTarget uses (pause.go ~888), so
// a dead intermediate never hides a live ancestor further up -- and returns
// the first ancestor whose own latest session is live. Unlike pauseTarget,
// which keeps climbing to report the topmost live ancestor (the right point
// to cascade a pause from), this stops at the closest one: a relay just
// needs somebody still around to receive it. ok is false when the walk
// reaches the top with nobody live at all.
func (s *Store) nearestLiveAncestor(ctx context.Context, agentID string) (Agent, bool, error) {
	cur, err := s.agentByID(ctx, agentID)
	if err != nil {
		return Agent{}, false, err
	}
	for cur.ParentAgentID != "" {
		parent, err := s.agentByID(ctx, cur.ParentAgentID)
		if err != nil {
			return Agent{}, false, err
		}
		if pses, err := s.LatestSession(ctx, parent.ID); err == nil && pses.State.Live() {
			return parent, true, nil
		}
		cur = parent
	}
	return Agent{}, false, nil
}

// undeliveredAgentMessage is one message, still pending, whose recipient
// cannot receive it (agentCanReceive above): the race Send()'s own liveness
// check cannot catch synchronously, because the target was alive when Send()
// validated it and only died afterward.
type undeliveredAgentMessage struct {
	MessageID, FromAgentID, FromParentAgentID   string
	ToAgentID, ToAgentName, RootItemID, ItemKey string
}

// undeliveredAgentMessages finds every message matching undeliveredAgentMessage
// above, whose target died at or before cutoff. Daemon-originated relays
// (from_agent_id NULL, e.g. the no_ack/crashed relays this same file already
// enqueues) are excluded: those already have their own, separate liveness
// gaps and are out of scope here (this covers agent-to-agent swarm_send
// traffic only).
//
// Two things the naive version of this query got wrong:
//   - it matched any un-acked message (state IN delivered, acked-pending,
//     etc.), so a message envelopes() had already handed the recipient's
//     Sync (state = 'delivered') fired a false no_recipient even on a fully
//     successful exchange the recipient simply never explicitly acked. Only
//     'pending' -- never even delivered -- belongs here.
//   - it measured the grace period from the message's own created_at, so a
//     message sent 30 minutes before its target died got zero grace while
//     one sent 10 seconds before the same death got the full window. The
//     cutoff below is anchored to the target's actual death (MAX(ended_at)
//     across its sessions), falling back to created_at only when the target
//     has never had a session end at all.
//
// The live-state predicate itself is applied in Go (agentCanReceive) rather
// than as a subquery here: the same ordering-by-generation-then-attempt
// question Send() and agentCanReceive already ask, asked once per candidate
// row instead of re-encoded a second way in SQL.
func (s *Store) undeliveredAgentMessages(ctx context.Context, cutoff time.Time) ([]undeliveredAgentMessage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id, m.from_agent_id, COALESCE(fa.parent_agent_id, ''),
		ta.id, ta.name, m.root_item_id, ti.key
		FROM messages m
		JOIN agents fa ON fa.id = m.from_agent_id
		JOIN agents ta ON ta.id = m.to_agent_id
		JOIN items ti ON ti.id = ta.item_id
		WHERE m.state = 'pending' AND m.from_agent_id IS NOT NULL
		AND COALESCE((SELECT MAX(se.ended_at) FROM sessions se WHERE se.agent_id = m.to_agent_id), m.created_at) <= ?`,
		db.Millis(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []undeliveredAgentMessage
	for rows.Next() {
		var r undeliveredAgentMessage
		if err := rows.Scan(&r.MessageID, &r.FromAgentID, &r.FromParentAgentID,
			&r.ToAgentID, &r.ToAgentName, &r.RootItemID, &r.ItemKey); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	filtered := out[:0]
	for _, r := range out {
		can, err := s.agentCanReceive(ctx, s.DB, r.ToAgentID)
		if err != nil {
			return nil, err
		}
		if !can {
			// Held for recovery, not undelivered: Send accepted it because
			// an operation owns the target, so no_recipient must not fire
			// while that operation is in flight.
			if owned, err := s.operationOwnsSession(ctx, r.ToAgentID); err != nil {
				return nil, err
			} else if owned {
				continue
			}
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

// questionAnswerTimeout is how long a delivered question can sit unanswered
// before the daemon relays a reminder to whoever owes the answer (F8).
const questionAnswerTimeout = 10 * time.Minute

// notifyUnansweredQuestions relays a question_unanswered reminder, once, to
// the agent a question was sent to once it has sat acked (delivered, never
// answered) for longer than questionAnswerTimeout. It covers plain answers
// (kind:"answer"), approval questions (kind:"approval_result") and blocked
// relays already answered through a relay reply — any of those closes the
// loop and this scan skips the question (spec §3's owed-answer scan).
func (s *Store) notifyUnansweredQuestions(ctx context.Context) error {
	cutoff := db.Millis(s.Now().Add(-questionAnswerTimeout))
	rows, err := s.DB.QueryContext(ctx, `SELECT q.id, q.from_agent_id, q.to_agent_id, q.root_item_id,
		COALESCE(q.item_id, ''), q.payload_json
		FROM messages q
		WHERE q.kind = 'question' AND q.origin = 'agent' AND q.state = 'acked' AND q.created_at < ?
		AND NOT EXISTS (SELECT 1 FROM messages a WHERE a.kind IN ('answer', 'approval_result', 'relay')
		                AND (a.reply_to = q.id OR (a.kind = 'answer' AND a.correlation_id = q.id)))`, cutoff)
	if err != nil {
		return err
	}
	type unanswered struct {
		ID, FromAgentID, ToAgentID, RootItemID, ItemID string
		Payload                                        string
	}
	var out []unanswered
	for rows.Next() {
		var u unanswered
		if err := rows.Scan(&u.ID, &u.FromAgentID, &u.ToAgentID, &u.RootItemID, &u.ItemID, &u.Payload); err != nil {
			rows.Close()
			return err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, u := range out {
		already, err := s.alreadyRelayedForMessage(ctx, u.ID)
		if err != nil {
			return err
		}
		if already {
			continue
		}
		var fromName string
		if err := s.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, u.FromAgentID).Scan(&fromName); err != nil {
			return err
		}
		var itemKey string
		if u.ItemID != "" {
			s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, u.ItemID).Scan(&itemKey)
		}
		var body struct {
			Body string `json:"body"`
		}
		json.Unmarshal([]byte(u.Payload), &body)
		payload, err := json.Marshal(map[string]any{"event": "question_unanswered",
			"agent": fromName, "item": itemKey, "question": body.Body})
		if err != nil {
			return err
		}
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			// Re-check under the transaction: the scan and idempotency guard
			// both ran outside it, so an answer could have landed in the gap.
			var state string
			if err := tx.QueryRowContext(ctx, `SELECT state FROM messages WHERE id = ?`, u.ID).Scan(&state); err != nil {
				return err
			}
			if state != "acked" {
				return nil
			}
			var answered int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
				WHERE kind IN ('answer', 'approval_result', 'relay')
				  AND (reply_to = ? OR (kind = 'answer' AND correlation_id = ?))`, u.ID, u.ID).Scan(&answered); err != nil {
				return err
			}
			if answered > 0 {
				return nil
			}
			_, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: u.ToAgentID,
				RootItemID: u.RootItemID, ItemID: u.ItemID, ReplyTo: u.ID, Payload: payload})
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// alreadyRelayedForMessage reports whether a no_recipient relay for this
// specific stuck message was already enqueued -- undeliveredAgentMessages'
// idempotency guard, keyed by the message's own id (a message has no retry
// "attempt" the way a session does, so alreadyRelayed's sinceStartedAt
// window doesn't apply here). Uses the messages.reply_to FK (an indexed
// equality check) rather than an unbounded LIKE scan of payload_json, which
// had no to_agent_id/event/time bound at all against the append-only
// messages table.
func (s *Store) alreadyRelayedForMessage(ctx context.Context, messageID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE kind = 'relay' AND reply_to = ?`, messageID).Scan(&n)
	return n > 0, err
}

// notifyUndeliveredMessages is the race half of Send()'s liveness check
// (inbox.go): a target's session can end after a message was already
// validated live and enqueued to it. Each tick, any such message still
// un-acked after ackTimeout gets relayed back to the FROM agent so it can
// react (re-check the target, respawn it, or pick someone else) -- or, if
// the FROM agent no longer has a live session either, to its parent,
// mirroring how notifyNoAck decides who hears about a stuck child.
func (s *Store) notifyUndeliveredMessages(ctx context.Context) error {
	rows, err := s.undeliveredAgentMessages(ctx, s.Now().Add(-ackTimeout))
	if err != nil {
		return err
	}
	for _, r := range rows {
		already, err := s.alreadyRelayedForMessage(ctx, r.MessageID)
		if err != nil {
			return err
		}
		if already {
			continue
		}
		notifyTo := r.FromAgentID
		live, err := s.agentHasLiveSession(ctx, s.DB, notifyTo)
		if err != nil {
			return err
		}
		if !live {
			// The FROM agent is dead too: escalate to the nearest live
			// ancestor above it rather than blindly relaying to its parent,
			// which may itself be dead (a crashed subtree, PauseAll, etc.) --
			// the same black-hole bug this function exists to fix, one level
			// up. No live ancestor anywhere in the chain means nobody is left
			// to tell, same as before.
			ancestor, ok, err := s.nearestLiveAncestor(ctx, r.FromAgentID)
			if err != nil {
				return err
			}
			if !ok {
				continue // nobody left to tell
			}
			notifyTo = ancestor.ID
		}
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			// Re-check under the transaction, immediately before writing: the
			// select above and the idempotency guard both ran outside this
			// tx, so the target's Sync/ack could have landed in that gap.
			// Without this re-check a race there produces one spurious relay.
			var state string
			if err := tx.QueryRowContext(ctx, `SELECT state FROM messages WHERE id = ?`, r.MessageID).Scan(&state); err != nil {
				return err
			}
			if state != "pending" {
				return nil
			}
			can, err := s.agentCanReceive(ctx, tx, r.ToAgentID)
			if err != nil {
				return err
			}
			if can {
				return nil
			}
			if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.no_recipient", AgentName: r.ToAgentName,
				ItemKey: r.ItemKey, Args: map[string]string{"name": r.ToAgentName, "KEY": r.ItemKey}}); err != nil {
				return err
			}
			payload, err := json.Marshal(map[string]any{"event": "no_recipient",
				"message_id": r.MessageID, "agent": r.ToAgentName, "item": r.ItemKey})
			if err != nil {
				return err
			}
			_, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: notifyTo,
				RootItemID: r.RootItemID, ReplyTo: r.MessageID, Payload: payload})
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
