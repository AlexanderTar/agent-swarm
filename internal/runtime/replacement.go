package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// ReplacementMode is the kind of agent replacement (§5 continuity).
// pause stops the predecessor into a resumable session with no successor;
// handoff and recover both stop it and start a successor session on the
// same canonical agent row (handoff carries a handoff note for the next
// generation, recover does not).
type ReplacementMode string

const (
	ModePause   ReplacementMode = "pause"
	ModeHandoff ReplacementMode = "handoff"
	ModeRecover ReplacementMode = "recover"
)

// OperationPhase is one step of a replacement operation's durable walk.
// requested..starting are nonterminal (exactly one per agent, enforced by
// agent_operations_one_active); succeeded, blocked and cancelled are
// terminal history.
type OperationPhase string

const (
	PhaseRequested  OperationPhase = "requested"
	PhasePreserving OperationPhase = "preserving"
	PhaseStopping   OperationPhase = "stopping"
	PhaseReady      OperationPhase = "ready"
	PhaseQueued     OperationPhase = "queued"
	PhaseStarting   OperationPhase = "starting"
	PhaseSucceeded  OperationPhase = "succeeded"
	PhaseBlocked    OperationPhase = "blocked"
	PhaseCancelled  OperationPhase = "cancelled"
)

// nonterminalPhases is the phase set the partial unique index
// agent_operations_one_active covers. Keep the two in sync.
var nonterminalPhases = []OperationPhase{
	PhaseRequested, PhasePreserving, PhaseStopping, PhaseReady, PhaseQueued, PhaseStarting,
}

func isTerminalPhase(p OperationPhase) bool {
	switch p {
	case PhaseSucceeded, PhaseBlocked, PhaseCancelled:
		return true
	}
	return false
}

// Operation is one durable replacement intent for a canonical agent.
type Operation struct {
	ID         string
	AgentID    string
	Mode       ReplacementMode
	Phase      OperationPhase
	RequestKey string
	SessionID  string // predecessor session observed when the intent was recorded
	Generation int
	Note       string
	Error      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// RequestReplacement records a replacement intent for the canonical agent
// and drives it as far as it can go synchronously. The intent row is
// committed before any side effect runs, so a daemon restart resumes from
// the durable phase (ResumeOperations) instead of losing or doubling the
// replacement. Replaying the same request key returns the same operation
// without running anything again; a different key while one is in flight
// is a 409 naming the active operation.
func (s *Store) RequestReplacement(ctx context.Context, agentID string, mode ReplacementMode, requestKey, note string) (Operation, error) {
	switch mode {
	case ModePause, ModeHandoff, ModeRecover:
	default:
		return Operation{}, &items.Error{Code: items.CodeBadRequest,
			Message: "mode must be pause, handoff or recover."}
	}
	a, err := s.agentByID(ctx, agentID)
	if err != nil {
		return Operation{}, err
	}
	ses, err := s.LatestSession(ctx, agentID)
	if err != nil {
		return Operation{}, &items.Error{Code: items.CodeBadRequest,
			Message: "This agent has no session to replace yet."}
	}
	var op Operation
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if requestKey != "" {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_operations
				WHERE agent_id = ? AND request_key = ?`, agentID, requestKey).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return errDuplicateKey
			}
		}
		var activeID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM agent_operations WHERE agent_id = ? AND phase IN
			('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting')`, agentID).Scan(&activeID)
		if err == nil {
			return &items.Error{Code: items.CodeConflict,
				Message: fmt.Sprintf("A replacement is already in progress: %s.", activeID)}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := db.Millis(s.now())
		op = Operation{ID: ids.New("op"), AgentID: agentID, Mode: mode, Phase: PhaseRequested,
			RequestKey: requestKey, SessionID: ses.ID, Generation: ses.Generation, Note: note,
			CreatedAt: s.now(), UpdatedAt: s.now()}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_operations
			(id, agent_id, mode, phase, request_key, session_id, generation, note, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			op.ID, op.AgentID, string(op.Mode), string(op.Phase), op.RequestKey,
			op.SessionID, op.Generation, op.Note, now, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return &items.Error{Code: items.CodeConflict,
					Message: "A replacement is already in progress for this agent."}
			}
			return err
		}
		// An explicit replacement request re-enables auto_restart: the
		// Cancel flag only gates restarts the daemon drives on its own,
		// and ResumeOperations must keep driving this walk if it parks.
		_, err = tx.ExecContext(ctx, `UPDATE agents SET auto_restart = 1 WHERE id = ?`, a.ID)
		return err
	})
	if err != nil {
		if errors.Is(err, errDuplicateKey) {
			return s.operationByKey(ctx, agentID, requestKey)
		}
		return Operation{}, err
	}
	return s.advanceOperation(ctx, op.ID)
}

// errDuplicateKey is the private sentinel for "the (agent, request_key)
// row already exists": the outer tx rolls back and the caller re-reads the
// winning row instead of inserting a second one.
var errDuplicateKey = errors.New("duplicate request key")

// operationByKey re-reads the durable row for a replayed request key.
func (s *Store) operationByKey(ctx context.Context, agentID, requestKey string) (Operation, error) {
	var op Operation
	var mode, phase string
	var created, updated int64
	err := s.DB.QueryRowContext(ctx, `SELECT id, agent_id, mode, phase, request_key,
		COALESCE(session_id, ''), generation, COALESCE(note, ''), COALESCE(error, ''),
		created_at, updated_at FROM agent_operations WHERE agent_id = ? AND request_key = ?`,
		agentID, requestKey).Scan(&op.ID, &op.AgentID, &mode, &phase, &op.RequestKey,
		&op.SessionID, &op.Generation, &op.Note, &op.Error, &created, &updated)
	if err != nil {
		return Operation{}, err
	}
	op.Mode, op.Phase = ReplacementMode(mode), OperationPhase(phase)
	op.CreatedAt, op.UpdatedAt = db.FromMillis(created), db.FromMillis(updated)
	return op, nil
}

// operationOwnsSession reports whether the agent has an in-flight
// replacement operation: while it does, the operation driver owns every
// transition of the agent's sessions and the reconciler must not resolve
// them (crashed, paused, or otherwise) on its own.
func (s *Store) operationOwnsSession(ctx context.Context, agentID string) (bool, error) {
	_, ok, err := s.PendingOperation(ctx, agentID)
	return ok, err
}

// PendingOperation reports the agent's in-flight operation, if any.
func (s *Store) PendingOperation(ctx context.Context, agentID string) (Operation, bool, error) {
	return s.pendingOperationTx(ctx, s.DB, agentID)
}

// pendingOperationTx is PendingOperation inside the caller's transaction
// (Send checks it in inbox.go before enqueueing to a target with no live
// session).
func (s *Store) pendingOperationTx(ctx context.Context, q txQuerier, agentID string) (Operation, bool, error) {
	var op Operation
	var mode, phase string
	var created, updated int64
	err := q.QueryRowContext(ctx, `SELECT id, agent_id, mode, phase, request_key,
		COALESCE(session_id, ''), generation, COALESCE(note, ''), COALESCE(error, ''),
		created_at, updated_at FROM agent_operations WHERE agent_id = ? AND phase IN
		('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting')
		ORDER BY updated_at DESC LIMIT 1`, agentID).Scan(
		&op.ID, &op.AgentID, &mode, &phase, &op.RequestKey,
		&op.SessionID, &op.Generation, &op.Note, &op.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, false, nil
	}
	if err != nil {
		return Operation{}, false, err
	}
	op.Mode, op.Phase = ReplacementMode(mode), OperationPhase(phase)
	op.CreatedAt, op.UpdatedAt = db.FromMillis(created), db.FromMillis(updated)
	return op, true, nil
}

// queueRetryIntent persists a Retry that arrived while the latest session
// is still stopping: a durable queued recover operation carrying the note,
// idempotent per (caller session, request key) like every other control
// mutation. The agent row and sessions are untouched -- no launch happens
// here. The next ResumeOperations executes the intent once the session
// settles into a retryable state, and Cancel wins over it meanwhile.
func (s *Store) queueRetryIntent(ctx context.Context, a Agent, ses Session, note, callerSessionID, requestID string) (Agent, error) {
	var out Agent
	if _, err := IdemTx(ctx, s, callerSessionID, requestID, "swarm_control", &out, func(tx *sql.Tx) error {
		if requestID != "" {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_operations
				WHERE agent_id = ? AND request_key = ?`, a.ID, requestID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				out = a
				return nil
			}
		}
		var activeID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM agent_operations WHERE agent_id = ? AND phase IN
			('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting')`, a.ID).Scan(&activeID)
		if err == nil {
			return &items.Error{Code: items.CodeConflict,
				Message: fmt.Sprintf("A replacement is already in progress: %s.", activeID)}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := db.Millis(s.now())
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_operations
			(id, agent_id, mode, phase, request_key, session_id, generation, note, created_at, updated_at)
			VALUES (?, ?, 'recover', 'queued', ?, ?, ?, ?, ?, ?)`,
			ids.New("op"), a.ID, requestID, ses.ID, ses.Generation, note, now, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return &items.Error{Code: items.CodeConflict,
					Message: "A replacement is already in progress for this agent."}
			}
			return err
		}
		out = a
		return nil
	}); err != nil {
		return Agent{}, err
	}
	return out, nil
}

// CancelOperation marks one operation cancelled. Terminal rows are returned
// unchanged; Cancel (agents.go) uses cancelAgentOperationsTx for the
// whole-agent sweep instead.
func (s *Store) CancelOperation(ctx context.Context, opID string) (Operation, error) {
	op, err := s.getOperation(ctx, opID)
	if err != nil {
		return Operation{}, err
	}
	defer lockAgentOperations(op.AgentID)()
	if op, err = s.getOperation(ctx, opID); err != nil {
		return Operation{}, err
	}
	if isTerminalPhase(op.Phase) {
		return op, nil
	}
	if err := s.setPhase(ctx, opID, op.Phase, PhaseCancelled, "cancelled"); err != nil {
		return Operation{}, err
	}
	op.Phase = PhaseCancelled
	return op, nil
}

// cancelAgentOperationsTx marks every in-flight operation for agentID
// cancelled inside the caller's transaction. Cancel calls it before
// killing anything, so a cancelled replacement can never launch its
// successor afterwards: Cancel wins over pending launch.
func (s *Store) cancelAgentOperationsTx(ctx context.Context, tx *sql.Tx, agentID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE agent_operations SET phase = 'cancelled',
		error = 'cancelled', updated_at = ? WHERE agent_id = ? AND phase IN
		('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting')`,
		db.Millis(s.now()), agentID)
	return err
}

// autoRestart reports the durable auto-restart flag (migration 0014,
// agents.auto_restart; user Cancel flips it to 0). Missing rows and NULL
// read as enabled, matching the migration default.
func (s *Store) autoRestart(ctx context.Context, agentID string) bool {
	var v sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT auto_restart FROM agents WHERE id = ?`, agentID).Scan(&v); err != nil {
		return true
	}
	return !v.Valid || v.Int64 != 0
}

// ResumeOperations advances every in-flight operation from its durable
// phase. The daemon calls it on every reconcile tick (and therefore after
// every restart), so an operation stranded mid-walk by a crash resumes
// instead of wedging the agent behind the partial unique index.
func (s *Store) ResumeOperations(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM agent_operations WHERE phase IN
		('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting') ORDER BY updated_at`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		op, err := s.getOperation(ctx, id)
		if err != nil || isTerminalPhase(op.Phase) {
			continue
		}
		if !s.autoRestart(ctx, op.AgentID) {
			continue // user Cancel disabled auto-restart; Cancel already marked these cancelled
		}
		if _, err := s.advanceOperation(ctx, id); err != nil {
			s.logf("replacement: advance %s: %v", id, err)
		}
	}
	return nil
}

func (s *Store) getOperation(ctx context.Context, opID string) (Operation, error) {
	var op Operation
	var mode, phase string
	var created, updated int64
	err := s.DB.QueryRowContext(ctx, `SELECT id, agent_id, mode, phase, request_key,
		COALESCE(session_id, ''), generation, COALESCE(note, ''), COALESCE(error, ''),
		created_at, updated_at FROM agent_operations WHERE id = ?`, opID).Scan(
		&op.ID, &op.AgentID, &mode, &phase, &op.RequestKey,
		&op.SessionID, &op.Generation, &op.Note, &op.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, &items.Error{Code: items.CodeNotFound, Message: fmt.Sprintf("No operation %s.", opID)}
	}
	if err != nil {
		return Operation{}, err
	}
	op.Mode, op.Phase = ReplacementMode(mode), OperationPhase(phase)
	op.CreatedAt, op.UpdatedAt = db.FromMillis(created), db.FromMillis(updated)
	return op, nil
}

func (s *Store) setPhase(ctx context.Context, opID string, from, to OperationPhase, errMsg string) error {
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		return casPhaseTx(ctx, tx, opID, from, to, errMsg, s.now())
	}); err != nil {
		return err
	}
	// Batch 3: every phase change publishes agent.changed, so SSE consumers
	// refetch state and the replacement field drives handoff progress.
	s.publishOperationProgress(ctx, opID)
	return nil
}

// errPhaseRaced reports that an operation left the phase a writer observed:
// another driver (or Cancel) moved it first. The writer's transaction rolls
// back and the driver re-reads instead of overwriting the newer phase.
var errPhaseRaced = errors.New("operation phase changed under this writer")

// casPhaseTx is the one operation phase write: from -> to only while the row
// is still in from. Run it first in a transaction so a lost race rolls back
// every side effect written beside it.
func casPhaseTx(ctx context.Context, tx *sql.Tx, opID string, from, to OperationPhase, errMsg string, now time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE agent_operations SET phase = ?, error = ?, updated_at = ?
		WHERE id = ? AND phase = ?`, string(to), errMsg, db.Millis(now), opID, string(from))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return errPhaseRaced
	}
	return nil
}

// operationLocks serializes every driver of one agent's operations (the
// request's own walk, reconcile's ResumeOperations, Cancel), like
// workflowLocks does for the engine: the phase CAS stops a stale overwrite,
// the lock stops two drivers from both reaching a side effect such as a
// successor launch.
var operationLocks sync.Map // agent id -> *sync.Mutex

func lockAgentOperations(agentID string) func() {
	v, _ := operationLocks.LoadOrStore(agentID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// publishOperationProgress emits one agent.changed for the operation's
// agent. Best-effort: a missing agent row or unwired events never fails the
// phase transition itself.
func (s *Store) publishOperationProgress(ctx context.Context, opID string) {
	if s.Events == nil {
		return
	}
	op, err := s.getOperation(ctx, opID)
	if err != nil {
		return
	}
	a, err := s.agentByID(ctx, op.AgentID)
	if err != nil {
		return
	}
	var rootKey string
	_ = s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.RootItemID).Scan(&rootKey)
	_, _ = s.Events.Publish(ctx, events.AgentChanged, map[string]string{"name": a.Name, "root_key": rootKey})
}

// advanceOperation walks one operation forward until it parks (predecessor
// still alive, no admission slot) or reaches a terminal phase. Every step
// re-reads the latest session and compares it against the session the
// intent observed: acting on a session the operation did not see would let
// a racing retry or resume double-launch a successor, so a mismatch blocks
// the operation instead.
func (s *Store) advanceOperation(ctx context.Context, opID string) (Operation, error) {
	first, err := s.getOperation(ctx, opID)
	if err != nil {
		return Operation{}, err
	}
	defer lockAgentOperations(first.AgentID)()
	for i := 0; i < len(nonterminalPhases)+2; i++ {
		op, err := s.getOperation(ctx, opID)
		if err != nil {
			return Operation{}, err
		}
		if isTerminalPhase(op.Phase) {
			return op, nil
		}
		a, err := s.agentByID(ctx, op.AgentID)
		if err != nil {
			return Operation{}, err
		}
		latest, err := s.LatestSession(ctx, op.AgentID)
		if err != nil {
			return Operation{}, err
		}
		if latest.ID != op.SessionID {
			_ = s.setPhase(ctx, opID, op.Phase, PhaseBlocked,
				fmt.Sprintf("session %s changed under this operation; refusing to act on %s.", op.SessionID, latest.ID))
			return s.getOperation(ctx, opID)
		}
		var parked bool
		switch op.Phase {
		case PhaseRequested:
			if err := s.beginPreservation(ctx, op, a, latest); err != nil && !errors.Is(err, errPhaseRaced) {
				return Operation{}, err
			}
		case PhasePreserving:
			wait, err := s.waitForPreservation(ctx, op, latest)
			if err != nil {
				return Operation{}, err
			}
			if wait {
				parked = true
				break
			}
			if err := s.stopPredecessor(ctx, op, a, latest); err != nil && !errors.Is(err, errPhaseRaced) {
				return Operation{}, err
			}
		case PhaseStopping:
			parked, err = s.settlePredecessor(ctx, op)
			if err != nil && !errors.Is(err, errPhaseRaced) {
				return Operation{}, err
			}
		case PhaseReady:
			if err := s.setPhase(ctx, opID, PhaseReady, PhaseQueued, ""); err != nil && !errors.Is(err, errPhaseRaced) {
				return Operation{}, err
			}
		case PhaseQueued:
			parked, err = s.admitOperation(ctx, op, a, latest)
			if err != nil && !errors.Is(err, errPhaseRaced) {
				return Operation{}, err
			}
		case PhaseStarting:
			if err := s.startSuccessor(ctx, op, a, latest); err != nil && !errors.Is(err, errPhaseRaced) {
				return Operation{}, err
			}
		}
		if parked {
			return s.getOperation(ctx, opID)
		}
	}
	return s.getOperation(ctx, opID)
}

// beginPreservation is requested->preserving. A handoff of a live session
// that is not already pausing first asks it to preserve its work through the
// pause delivery path (pause_requested, a deadline, and a HANDOFF control
// notice), in the same transaction as the phase swap, so the predecessor is
// never stopped before it had a chance to save. Scope is always "session":
// an orchestrator handoff replaces only its own session and its children keep
// running.
func (s *Store) beginPreservation(ctx context.Context, op Operation, a Agent, latest Session) error {
	prePause := op.Mode == ModeHandoff && latest.State.Live() && !latest.State.Pausing()
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := casPhaseTx(ctx, tx, op.ID, PhaseRequested, PhasePreserving, "", s.now()); err != nil {
			return err
		}
		if !prePause {
			return nil
		}
		deadline := s.Now().Add(time.Duration(s.pauseDeadlineSec(ctx)) * time.Second)
		return s.requestPreservationTx(ctx, tx, a, latest, deadline, "session", "handoff")
	}); err != nil {
		return err
	}
	s.publishOperationProgress(ctx, op.ID)
	return nil
}

// waitForPreservation parks a handoff in preserving while its predecessor
// can still save: the session is pausing (a pause-first flow keeps it live),
// no manifest is recorded yet, and the predecessor pane is still alive to
// write the handoff checkpoint that binds it. A pausing session whose pane
// is already gone proceeds manifest-less (the successor ships the
// broken-predecessor warning instead); every other mode and state keeps the
// immediate stop.
func (s *Store) waitForPreservation(ctx context.Context, op Operation, latest Session) (bool, error) {
	if op.Mode != ModeHandoff || !latest.State.Pausing() {
		return false, nil
	}
	var manifestPath string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(manifest_path, '') FROM agent_operations WHERE id = ?`,
		op.ID).Scan(&manifestPath); err != nil {
		return false, err
	}
	if manifestPath != "" {
		return false, nil
	}
	// The preservation window is bounded: past the pause deadline the walk
	// stops the predecessor itself even if TickPause's kill never landed.
	if latest.PauseDeadlineAt != nil && s.Now().After(*latest.PauseDeadlineAt) {
		return false, nil
	}
	alive, err := s.predecessorAlive(ctx, op.SessionID)
	if err != nil || !alive {
		return false, err
	}
	return true, nil
}

// stopPredecessor is the preserving->stopping side effect: interrupt keys,
// kill the pane, and land the predecessor session in a recoverable state.
// pause parks it as paused (Resume owns it from there); handoff and recover
// mark it interrupted, mirroring resolveDead's own interrupted transition
// including its relay to the parent.
func (s *Store) stopPredecessor(ctx context.Context, op Operation, a Agent, ses Session) error {
	// A session that already ended (crash, cancel, failure) keeps its audit
	// state: there is no pane to interrupt and no stop to report, so only the
	// phase moves on.
	if slices.Contains(endedStates, ses.State) {
		to := PhaseStopping
		if op.Mode == ModePause {
			to = PhaseSucceeded
		}
		return s.setPhase(ctx, op.ID, PhasePreserving, to, "")
	}
	if ad, ok := s.Adapters[a.Kind]; ok && ad != nil {
		_ = s.Tmux.Keys(ctx, ses.TmuxName, ad.InterruptKeys()...)
	}
	_ = s.Tmux.Kill(ctx, ses.TmuxName)
	return s.tx(ctx, func(tx *sql.Tx) error {
		// A pause replacement has no successor: the paused session is the
		// end state, so stopping lands straight on succeeded.
		to := PhaseStopping
		if op.Mode == ModePause {
			to = PhaseSucceeded
		}
		if err := casPhaseTx(ctx, tx, op.ID, PhasePreserving, to, "", s.now()); err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, a.ItemID)
		if err != nil {
			return err
		}
		state, kind := string(Interrupted), "agent.interrupted"
		if op.Mode == ModePause {
			state, kind = string(Paused), "agent.paused"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = ?, ended_at = ? WHERE id = ?`,
			state, db.Millis(s.now()), ses.ID); err != nil {
			return err
		}
		if err := s.notify(ctx, tx, NotifyInput{Kind: kind, AgentName: a.Name,
			ItemKey: key, Args: map[string]string{"name": a.Name, "KEY": key}}); err != nil {
			return err
		}
		if op.Mode == ModeHandoff && a.ParentAgentID != "" {
			// One relay per handoff: the handoff checkpoint already relayed
			// event "handoff"; a predecessor that never saved (deadline, dead
			// pane) gets the same event from here, marked unsaved.
			var saved int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints
				WHERE session_id = ? AND kind = 'handoff'`, ses.ID).Scan(&saved); err != nil {
				return err
			}
			if saved == 0 {
				payload := []byte(fmt.Sprintf(`{"event":"handoff","agent":%q,"item":%q,"saved":false}`, a.Name, key))
				if _, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: a.ParentAgentID,
					RootItemID: a.RootItemID, ItemID: a.ItemID, Payload: payload}); err != nil {
					return err
				}
			}
		}
		if op.Mode == ModeRecover && a.ParentAgentID != "" {
			payload := []byte(fmt.Sprintf(`{"event":"interrupted","agent":%q,"item":%q}`, a.Name, key))
			if _, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: a.ParentAgentID,
				RootItemID: a.RootItemID, ItemID: a.ItemID, Payload: payload}); err != nil {
				return err
			}
		}
		return s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID)
	})
}

// settlePredecessor is the stopping->ready gate: the successor must never
// start while the predecessor pane is still alive, and liveness is decided
// by the pane's SWARM_SESSION env (the session the pane actually runs),
// never by the pane name (which the successor will reuse). When the pane
// is gone the predecessor's credentials are revoked first, so a resurrected
// predecessor token can never act beside its successor.
func (s *Store) settlePredecessor(ctx context.Context, op Operation) (parked bool, err error) {
	alive, err := s.predecessorAlive(ctx, op.SessionID)
	if err != nil {
		return false, err
	}
	if alive {
		return true, nil
	}
	if err := s.revokeAgentTokens(ctx, op.AgentID); err != nil {
		return false, err
	}
	if err := s.setPhase(ctx, op.ID, PhaseStopping, PhaseReady, ""); err != nil {
		return false, err
	}
	return false, nil
}

// predecessorAlive reports whether any live pane still runs predecessorID,
// matching Reconcile's own ownership check: a pane whose SWARM_SESSION is
// a different session belongs to a newer generation and does not count.
func (s *Store) predecessorAlive(ctx context.Context, predecessorID string) (bool, error) {
	panes, err := s.Tmux.Panes(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range panes {
		if p.Dead {
			continue
		}
		if env, err := s.Tmux.Env(ctx, p.Session, "SWARM_SESSION"); err == nil && env == predecessorID {
			return true, nil
		}
	}
	return false, nil
}

// revokeAgentTokens deletes every token file for the agent's sessions, the
// same cleanup startSession runs before minting a new token.
func (s *Store) revokeAgentTokens(ctx context.Context, agentID string) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM sessions WHERE agent_id = ?`, agentID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		_ = os.Remove(filepath.Join(s.Home, "run", "tokens", id))
	}
	return nil
}

// endedStates are the session states an operation finds already over: the
// retryable ones plus a user-cancelled session (Cancel keeps the identity
// recoverable). stopPredecessor leaves them untouched, keeping the audit trail.
var endedStates = append(slices.Clone(retryableStates), Cancelled)

// admitOperation is the queued->starting gate. The successor launches only
// once the observed predecessor session has settled into a retryable state:
// a still-live session means someone else already recovered the agent (the
// session-compare in advanceOperation catches a replaced row; this catches
// the same row come back), and a paused one belongs to Resume, not to a
// duplicate launch. Without an admission slot the operation parks in queued
// and a later tick retries it.
func (s *Store) admitOperation(ctx context.Context, op Operation, a Agent, latest Session) (bool, error) {
	if latest.State.Live() || !slices.Contains(endedStates, latest.State) {
		return true, nil
	}
	parked := true
	err := s.tx(ctx, func(tx *sql.Tx) error {
		admitted, err := s.Admit(ctx, tx, a.Role, a.RootItemID)
		if err != nil {
			return err
		}
		if !admitted {
			return nil
		}
		if err := casPhaseTx(ctx, tx, op.ID, PhaseQueued, PhaseStarting, "", s.now()); err != nil {
			return err
		}
		if err := s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID); err != nil {
			return err
		}
		parked = false
		return nil
	})
	return parked, err
}

// successorKickoff composes the section-4 fresh-session kickoff for a
// successor generation: the SuccessorKickoff template for mode (one of
// "handoff", "recovery" or "resume") plus whichever normative additions
// apply. A resume rides ResumeAddition (durable state wins); a handoff
// orchestrator with live children names them via OrchestratorHandoffAddition
// (absent children means no addition, never an invented list); a recovering
// reviewer gets ReviewerRecoveryAddition; and a successor whose predecessor
// left no usable manifest gets BrokenPredecessorWarning with the observed
// paths and checkpoints instead of a fabricated history.
func (s *Store) successorKickoff(ctx context.Context, a Agent, itemType items.Type, itemKey, itemTitle, mode string) string {
	out := SuccessorKickoff(a.Name, a.Role, itemType, itemKey, itemTitle, mode)
	if mode == "resume" {
		return out + " " + ResumeAddition
	}
	if a.Role == RoleOrchestrator && mode == "handoff" {
		if kids := s.liveChildNames(ctx, a.ID); len(kids) > 0 {
			out += " " + OrchestratorHandoffAddition(kids)
		}
	}
	if isReviewerRole(a.Role) && mode == "recovery" {
		out += " " + ReviewerRecoveryAddition
	}
	if paths, ckpts, ok := s.brokenPredecessorEvidence(ctx, a.ID); ok {
		out += " " + BrokenPredecessorWarning(paths, ckpts)
	}
	return out
}

// liveChildNames returns the names of the agent's direct children whose
// latest session is still live: the handoff leaves them running, so the
// successor must reconcile rather than report them complete.
func (s *Store) liveChildNames(ctx context.Context, agentID string) []string {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name FROM agents WHERE parent_agent_id = ? ORDER BY name`, agentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		ses, err := s.LatestSession(ctx, id)
		if err != nil || !ses.State.Live() {
			continue
		}
		out = append(out, name)
	}
	return out
}

// brokenPredecessorEvidence reports whether the agent's latest operation
// left no usable handoff manifest, with the owned worktree paths and
// checkpoint IDs observed for the warning. ok is false when a manifest was
// recorded (the predecessor saved); otherwise the successor must inspect
// first rather than inherit an invented history.
func (s *Store) brokenPredecessorEvidence(ctx context.Context, agentID string) (paths, ckpts []string, ok bool) {
	var manifestPath string
	err := s.DB.QueryRowContext(ctx, `SELECT manifest_path FROM agent_operations
		WHERE agent_id = ? ORDER BY updated_at DESC, rowid DESC LIMIT 1`, agentID).Scan(&manifestPath)
	if err == nil && manifestPath != "" {
		return nil, nil, false
	}
	wrows, err := s.DB.QueryContext(ctx, `SELECT path FROM worktrees
		WHERE owner_agent_id = ? ORDER BY path`, agentID)
	if err == nil {
		for wrows.Next() {
			var p string
			if err := wrows.Scan(&p); err == nil {
				paths = append(paths, p)
			}
		}
		wrows.Close()
	}
	crows, err := s.DB.QueryContext(ctx, `SELECT id FROM checkpoints
		WHERE agent_id = ? ORDER BY created_at, rowid`, agentID)
	if err == nil {
		for crows.Next() {
			var id string
			if err := crows.Scan(&id); err == nil {
				ckpts = append(ckpts, id)
			}
		}
		crows.Close()
	}
	return paths, ckpts, true
}

// startSuccessor launches the next generation on the same canonical agent
// row: same id, same name, same attempt; only the session id, generation,
// token and provider session change. Open requests follow the agent onto
// the new session, a lineage row links the generations, and the operation
// lands on succeeded.
func (s *Store) startSuccessor(ctx context.Context, op Operation, a Agent, latest Session) error {
	// A queued retry's note (or a handoff note) reaches the successor as an
	// inbox message before it starts, the same delivery an immediate Retry
	// performs.
	if op.Note != "" {
		if err := s.deliverNote(ctx, a.ID, op.Note); err != nil {
			s.logf("replacement: deliver note to %s: %v", a.Name, err)
		}
	}
	// The successor continues the same assignment: its kickoff is the
	// section-4 template for the operation mode (pause never reaches here;
	// it lands succeeded at stop with no successor).
	succMode := "handoff"
	if op.Mode == ModeRecover {
		succMode = "recovery"
	}
	succ, err := s.startSession(ctx, a, latest.Attempt, latest.Generation+1, false, "", succMode)
	if err != nil {
		return s.setPhase(ctx, op.ID, PhaseStarting, PhaseBlocked, err.Error())
	}
	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), a, succ, s.Adapters[a.Kind]); err != nil {
			s.logf("replacement: watchStartup %s: %v", a.Name, err)
		}
	})
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := casPhaseTx(ctx, tx, op.ID, PhaseStarting, PhaseSucceeded, "", s.now()); err != nil {
			return err
		}
		// A recovered finished row (Cancel, then Start) is active again;
		// admitOperation already counted it against the limits.
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'active', finished_at = NULL
			WHERE id = ? AND state <> 'active'`, a.ID); err != nil {
			return err
		}
		if err := s.repointRequestsTx(ctx, tx, a.ID, succ.ID); err != nil {
			return err
		}
		if err := s.EnsureLineageTx(ctx, tx, a); err != nil {
			return err
		}
		if err := s.appendLineageTx(ctx, tx, a, succ.ID, succ.Generation, a.ID); err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, a.ItemID)
		if err != nil {
			return err
		}
		if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.retried", AgentName: a.Name,
			ItemKey: key, Args: map[string]string{"name": a.Name, "N": fmt.Sprint(succ.Attempt), "KEY": key}}); err != nil {
			return err
		}
		return s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID)
	})
}
