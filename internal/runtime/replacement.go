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
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
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
		_ = a
		return nil
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
	if isTerminalPhase(op.Phase) {
		return op, nil
	}
	if err := s.setPhase(ctx, opID, PhaseCancelled, "cancelled"); err != nil {
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

func (s *Store) setPhase(ctx context.Context, opID string, to OperationPhase, errMsg string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agent_operations SET phase = ?, error = ?, updated_at = ?
			WHERE id = ?`, string(to), errMsg, db.Millis(s.now()), opID)
		return err
	})
}

// advanceOperation walks one operation forward until it parks (predecessor
// still alive, no admission slot) or reaches a terminal phase. Every step
// re-reads the latest session and compares it against the session the
// intent observed: acting on a session the operation did not see would let
// a racing retry or resume double-launch a successor, so a mismatch blocks
// the operation instead.
func (s *Store) advanceOperation(ctx context.Context, opID string) (Operation, error) {
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
			_ = s.setPhase(ctx, opID, PhaseBlocked,
				fmt.Sprintf("session %s changed under this operation; refusing to act on %s.", op.SessionID, latest.ID))
			return s.getOperation(ctx, opID)
		}
		var parked bool
		switch op.Phase {
		case PhaseRequested:
			if err := s.setPhase(ctx, opID, PhasePreserving, ""); err != nil {
				return Operation{}, err
			}
		case PhasePreserving:
			if err := s.stopPredecessor(ctx, op, a, latest); err != nil {
				return Operation{}, err
			}
		case PhaseStopping:
			parked, err = s.settlePredecessor(ctx, op)
			if err != nil {
				return Operation{}, err
			}
		case PhaseReady:
			if err := s.setPhase(ctx, opID, PhaseQueued, ""); err != nil {
				return Operation{}, err
			}
		case PhaseQueued:
			parked, err = s.admitOperation(ctx, op, a, latest)
			if err != nil {
				return Operation{}, err
			}
		case PhaseStarting:
			if err := s.startSuccessor(ctx, op, a, latest); err != nil {
				return Operation{}, err
			}
		}
		if parked {
			return s.getOperation(ctx, opID)
		}
	}
	return s.getOperation(ctx, opID)
}

// stopPredecessor is the preserving->stopping side effect: interrupt keys,
// kill the pane, and land the predecessor session in a recoverable state.
// pause parks it as paused (Resume owns it from there); handoff and recover
// mark it interrupted, mirroring resolveDead's own interrupted transition
// including its relay to the parent.
func (s *Store) stopPredecessor(ctx context.Context, op Operation, a Agent, ses Session) error {
	if ad, ok := s.Adapters[a.Kind]; ok && ad != nil {
		_ = s.Tmux.Keys(ctx, ses.TmuxName, ad.InterruptKeys()...)
	}
	_ = s.Tmux.Kill(ctx, ses.TmuxName)
	return s.tx(ctx, func(tx *sql.Tx) error {
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
		if op.Mode != ModePause && a.ParentAgentID != "" {
			payload := []byte(fmt.Sprintf(`{"event":"interrupted","agent":%q,"item":%q}`, a.Name, key))
			if _, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: a.ParentAgentID,
				RootItemID: a.RootItemID, ItemID: a.ItemID, Payload: payload}); err != nil {
				return err
			}
		}
		phase := PhaseStopping
		if op.Mode == ModePause {
			// A pause replacement has no successor: the paused session is
			// the end state, so stopping lands straight on succeeded.
			_, err = tx.ExecContext(ctx, `UPDATE agent_operations SET phase = 'succeeded', updated_at = ? WHERE id = ?`,
				db.Millis(s.now()), op.ID)
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_operations SET phase = ?, updated_at = ? WHERE id = ?`,
			string(phase), db.Millis(s.now()), op.ID)
		return err
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
	if err := s.setPhase(ctx, op.ID, PhaseReady, ""); err != nil {
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

// admitOperation is the queued->starting gate. The successor launches only
// once the observed predecessor session has settled into a retryable state:
// a still-live session means someone else already recovered the agent (the
// session-compare in advanceOperation catches a replaced row; this catches
// the same row come back), and a paused one belongs to Resume, not to a
// duplicate launch. Without an admission slot the operation parks in queued
// and a later tick retries it.
func (s *Store) admitOperation(ctx context.Context, op Operation, a Agent, latest Session) (bool, error) {
	if latest.State.Live() || !slices.Contains(retryableStates, latest.State) {
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
		if _, err := tx.ExecContext(ctx, `UPDATE agent_operations SET phase = 'starting', updated_at = ? WHERE id = ?`,
			db.Millis(s.now()), op.ID); err != nil {
			return err
		}
		parked = false
		return nil
	})
	return parked, err
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
	succ, err := s.startSession(ctx, a, latest.Attempt, latest.Generation+1, false, "")
	if err != nil {
		_ = s.setPhase(ctx, op.ID, PhaseBlocked, err.Error())
		return nil
	}
	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), a, succ, s.Adapters[a.Kind]); err != nil {
			s.logf("replacement: watchStartup %s: %v", a.Name, err)
		}
	})
	return s.tx(ctx, func(tx *sql.Tx) error {
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
		if err := s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_operations SET phase = 'succeeded', updated_at = ? WHERE id = ?`,
			db.Millis(s.now()), op.ID)
		return err
	})
}
