package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// ImmediateKinds wake the recipient straight away (I19).
var ImmediateKinds = []MessageKind{"assignment", "control", "question", "answer", "finding",
	"approval_result", "user_answer", "repos_confirmed", "assignment_update", "advice"}

// ImmediateRelayEvents are the relay events that wake (I19). progress and handoff
// are deferred and arrive folded into a digest.
var ImmediateRelayEvents = []string{"accepted", "completed", "failed", "blocked", "crashed",
	"interrupted", "paused", "dependency_added", "spawn_failed"}

func WakeClassFor(kind MessageKind, relayEvent string) WakeClass {
	if kind == "relay" {
		if slices.Contains(ImmediateRelayEvents, relayEvent) {
			return "immediate"
		}
		return "deferred"
	}
	if slices.Contains(ImmediateKinds, kind) {
		return "immediate"
	}
	return "deferred"
}

// nullIf turns an empty string into a SQL NULL for a nullable text column.
func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// enqueue stores one message. seq is max(seq)+1 inside this transaction, which is
// safe because the daemon is the only writer (§5).
func (s *Store) enqueue(ctx context.Context, tx *sql.Tx, m Message) (Message, error) {
	if m.ID == "" {
		m.ID = ids.New("msg")
	}
	if m.Priority == 0 && m.Kind != "control" {
		m.Priority = 1
	}
	var relayEvent string
	if m.Kind == "relay" {
		var p struct {
			Event string `json:"event"`
		}
		json.Unmarshal(m.Payload, &p)
		relayEvent = p.Event
	}
	m.WakeClass = WakeClassFor(m.Kind, relayEvent)
	m.State, m.CreatedAt = "pending", s.Now()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM messages`).Scan(&m.Seq); err != nil {
		return m, err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO messages (id, seq, kind, wake_class, priority, origin,
		from_agent_id, from_session_id, to_agent_id, root_item_id, item_id, correlation_id, reply_to,
		request_id, payload_json, state, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'pending',?)`,
		m.ID, m.Seq, m.Kind, m.WakeClass, m.Priority, m.Origin,
		nullIf(m.FromAgentID), nullIf(m.FromSessionID), m.ToAgentID, m.RootItemID, nullIf(m.ItemID),
		nullIf(m.CorrelationID), nullIf(m.ReplyTo), nullIf(m.RequestID), string(m.Payload), db.Millis(m.CreatedAt))
	return m, err
}

// sessionAndAgent loads a session and its agent inside the caller's transaction,
// refusing an unknown session. Every tool call that carries a session id starts
// here (Sync, WriteCheckpoint, Ask, Materialize, …).
func (s *Store) sessionAndAgent(ctx context.Context, tx *sql.Tx, sessionID string) (Session, Agent, error) {
	ses, err := s.sessionTx(ctx, tx, sessionID)
	if err != nil {
		return Session{}, Agent{}, err
	}
	a, err := s.agentByIDTx(ctx, tx, ses.AgentID)
	if err != nil {
		return Session{}, Agent{}, err
	}
	return ses, a, nil
}

func (s *Store) sessionTx(ctx context.Context, tx *sql.Tx, id string) (Session, error) {
	var ses Session
	var st string
	var waiting int
	var pauseRoot int
	var needsCompaction int
	var lastSeen, lastWake, started, ended, pauseDeadline sql.NullInt64
	var exitCode sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT
		id, agent_id, attempt, generation, COALESCE(provider_session_id, ''), token_hash, tmux_name,
		cwd, cwd_kind, state, waiting, COALESCE(pause_scope, ''), pause_root, pause_deadline_at, stop_blocks,
		needs_compaction_notice, last_seen_at, last_wake_at, exit_code, started_at, ended_at
		FROM sessions WHERE id = ?`, id).Scan(
		&ses.ID, &ses.AgentID, &ses.Attempt, &ses.Generation, &ses.ProviderSessionID, &ses.TokenHash,
		&ses.TmuxName, &ses.Cwd, &ses.CwdKind, &st, &waiting, &ses.PauseScope, &pauseRoot, &pauseDeadline,
		&ses.StopBlocks, &needsCompaction, &lastSeen, &lastWake, &exitCode, &started, &ended,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ses, &items.Error{Code: items.CodeNotFound, Message: "Unknown session."}
	}
	if err != nil {
		return ses, err
	}
	ses.State = SessionState(st)
	ses.PauseRoot = pauseRoot != 0
	ses.Waiting = waiting != 0
	ses.NeedsCompactionNotice = needsCompaction != 0
	if pauseDeadline.Valid {
		t := db.FromMillis(pauseDeadline.Int64)
		ses.PauseDeadlineAt = &t
	}
	if lastSeen.Valid {
		t := db.FromMillis(lastSeen.Int64)
		ses.LastSeenAt = &t
	}
	if lastWake.Valid {
		t := db.FromMillis(lastWake.Int64)
		ses.LastWakeAt = &t
	}
	if exitCode.Valid {
		c := int(exitCode.Int64)
		ses.ExitCode = &c
	}
	if started.Valid {
		ses.StartedAt = db.FromMillis(started.Int64)
	}
	if ended.Valid {
		t := db.FromMillis(ended.Int64)
		ses.EndedAt = &t
	}
	return ses, nil
}

func (s *Store) agentByIDTx(ctx context.Context, tx *sql.Tx, id string) (Agent, error) {
	row := tx.QueryRowContext(ctx, `SELECT
		id, name, kind, model, COALESCE(effort, ''), role, item_id, root_item_id,
		COALESCE(parent_agent_id, ''), COALESCE(advisor_kind, ''), COALESCE(advisor_model, ''),
		COALESCE(advisor_effort, ''), COALESCE(advisor_mode, ''), brief, state, COALESCE(preflight_error, ''), created_at, finished_at
		FROM agents WHERE id = ?`, id)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return a, &items.Error{Code: items.CodeNotFound, Message: "Unknown agent."}
	}
	return a, err
}

func (s *Store) agentByNameTx(ctx context.Context, tx *sql.Tx, name string) (Agent, error) {
	row := tx.QueryRowContext(ctx, `SELECT
		id, name, kind, model, COALESCE(effort, ''), role, item_id, root_item_id,
		COALESCE(parent_agent_id, ''), COALESCE(advisor_kind, ''), COALESCE(advisor_model, ''),
		COALESCE(advisor_effort, ''), COALESCE(advisor_mode, ''), brief, state, COALESCE(preflight_error, ''), created_at, finished_at
		FROM agents WHERE name = ?`, name)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return a, &items.Error{Code: items.CodeNotFound, Message: fmt.Sprintf("No agent %s.", name)}
	}
	return a, err
}

// SyncResult is swarm_sync's result (§6.2, §8.1).
type SyncResult struct {
	Messages     []Envelope
	More         bool
	SessionState SessionState
}

const defaultSyncLimit = 20
const maxDigest = 1500

// Sync is swarm_sync (§8.1). ack is applied first; then every un-acked message is
// returned by priority then seq, with delivery_count + 1. Deferred relays are
// folded into one digest (I19).
func (s *Store) Sync(ctx context.Context, sessionID string, ack []string, limit int) (SyncResult, error) {
	if limit <= 0 {
		limit = defaultSyncLimit
	}
	var out SyncResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ses, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if err := s.onPausingSync(ctx, tx, &ses); err != nil {
			return err
		}
		out.SessionState = ses.State
		for _, id := range ack {
			res, err := tx.ExecContext(ctx, `UPDATE messages SET state = 'acked', acked_at = ?
				WHERE id = ? AND to_agent_id = ? AND state <> 'acked'`, db.Millis(s.Now()), id, a.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return &items.Error{Code: items.CodeBadRequest,
					Message: fmt.Sprintf("Unknown message %s.", id)}
			}
		}
		rows, err := s.unackedFor(ctx, tx, a.ID, limit+1)
		if err != nil {
			return err
		}
		out.More = len(rows) > limit
		if out.More {
			rows = rows[:limit]
		}
		deferred, immediate := split(rows)
		if len(deferred) > 0 {
			d, err := s.foldDigest(ctx, tx, a, deferred)
			if err != nil {
				return err
			}
			immediate = append(immediate, d)
		}
		out.Messages, err = s.envelopes(ctx, tx, a, immediate)
		return err
	})
	return out, err
}

// unackedFor returns up to limit un-acked messages (pending or delivered) for
// agentID, ordered by priority then seq.
func (s *Store) unackedFor(ctx context.Context, tx *sql.Tx, agentID string, limit int) ([]Message, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, seq, kind, wake_class, priority, origin,
		COALESCE(from_agent_id, ''), COALESCE(from_session_id, ''), to_agent_id, root_item_id,
		COALESCE(item_id, ''), COALESCE(correlation_id, ''), COALESCE(reply_to, ''),
		COALESCE(request_id, ''), payload_json, state, delivery_count, created_at
		FROM messages WHERE to_agent_id = ? AND state <> 'acked'
		ORDER BY priority, seq LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var kind, wake, payload, state string
		var created int64
		if err := rows.Scan(&m.ID, &m.Seq, &kind, &wake, &m.Priority, &m.Origin,
			&m.FromAgentID, &m.FromSessionID, &m.ToAgentID, &m.RootItemID,
			&m.ItemID, &m.CorrelationID, &m.ReplyTo, &m.RequestID,
			&payload, &state, &m.DeliveryCount, &created); err != nil {
			return nil, err
		}
		m.Kind, m.WakeClass, m.State = MessageKind(kind), WakeClass(wake), state
		m.Payload = json.RawMessage(payload)
		m.CreatedAt = db.FromMillis(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// split separates messages by wake class, preserving order within each group.
func split(rows []Message) (deferred, immediate []Message) {
	for _, m := range rows {
		if m.WakeClass == "deferred" {
			deferred = append(deferred, m)
		} else {
			immediate = append(immediate, m)
		}
	}
	return deferred, immediate
}

// digestPayload is one deferred relay's shape, enough to render one digest line.
type digestPayload struct {
	Event      string `json:"event"`
	Agent      string `json:"agent"`
	Item       string `json:"item"`
	Checkpoint struct {
		Summary string `json:"summary"`
	} `json:"checkpoint"`
}

// foldDigest inserts one digest message whose payload lists "KEY · agent ·
// summary" per deferred row (capped at 1500 bytes) and acks the folded rows in
// the same transaction (I19).
func (s *Store) foldDigest(ctx context.Context, tx *sql.Tx, a Agent, deferred []Message) (Message, error) {
	var lines []string
	for _, m := range deferred {
		var p digestPayload
		json.Unmarshal(m.Payload, &p)
		lines = append(lines, fmt.Sprintf("%s · %s · %s", p.Item, p.Agent, p.Checkpoint.Summary))
	}
	for {
		body, err := json.Marshal(map[string]any{"lines": lines})
		if err != nil {
			return Message{}, err
		}
		if len(body) <= maxDigest || len(lines) == 0 {
			for _, m := range deferred {
				if _, err := tx.ExecContext(ctx, `UPDATE messages SET state = 'acked', acked_at = ?
					WHERE id = ?`, db.Millis(s.Now()), m.ID); err != nil {
					return Message{}, err
				}
			}
			return s.enqueue(ctx, tx, Message{Kind: "digest", Origin: "daemon",
				ToAgentID: a.ID, RootItemID: a.RootItemID, Payload: body})
		}
		lines = lines[:len(lines)-1]
	}
}

// envelopes marks each row delivered (delivery_count + 1) and builds its Envelope.
func (s *Store) envelopes(ctx context.Context, tx *sql.Tx, to Agent, rows []Message) ([]Envelope, error) {
	out := make([]Envelope, 0, len(rows))
	for _, m := range rows {
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET state = 'delivered',
			delivery_count = delivery_count + 1, delivered_at = ? WHERE id = ?`, db.Millis(s.Now()), m.ID); err != nil {
			return nil, err
		}
		e := Envelope{V: 1, MsgID: m.ID, Seq: m.Seq, Kind: m.Kind, Origin: m.Origin,
			To:          Party{Agent: to.ID, Name: to.Name},
			Correlation: m.CorrelationID, ReplyTo: m.ReplyTo, RequestID: m.RequestID, Payload: m.Payload}
		if rootKey, err := s.itemKey(ctx, tx, m.RootItemID); err == nil {
			e.RootItem = rootKey
		}
		if m.ItemID != "" {
			if key, err := s.itemKey(ctx, tx, m.ItemID); err == nil {
				e.Item = key
			}
		}
		if m.FromAgentID != "" {
			if from, err := s.agentByIDTx(ctx, tx, m.FromAgentID); err == nil {
				e.From = &Party{Agent: from.ID, Name: from.Name, Session: m.FromSessionID}
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) itemKey(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var key string
	err := tx.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, id).Scan(&key)
	return key, err
}

// Send is swarm_send (§8.1). "parent" resolves through agents.parent_agent_id; a
// cross-root target or an unknown name is refused. origin is always 'agent'.
// requestID is I11's idempotency key (empty means "no idempotency, just run
// once"): a repeated (session, requestID) pair replays the first message's id
// instead of enqueueing a second message.
func (s *Store) Send(ctx context.Context, sessionID, to string, kind MessageKind, body, correlationID, requestID string) (string, error) {
	if len(body) > 4000 {
		return "", &items.Error{Code: items.CodeBadRequest, Message: "A message body is limited to 4000 characters."}
	}
	var id string
	_, err := IdemTx(ctx, s, sessionID, requestID, "swarm_send", &id, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		var target Agent
		if to == "parent" {
			if a.ParentAgentID == "" {
				return &items.Error{Code: items.CodeBadRequest, Message: "This agent has no parent."}
			}
			target, err = s.agentByIDTx(ctx, tx, a.ParentAgentID)
		} else {
			target, err = s.agentByNameTx(ctx, tx, to)
		}
		if err != nil {
			return err
		}
		if target.RootItemID != a.RootItemID {
			return &items.Error{Code: items.CodeBadRequest,
				Message: "A message can only go to an agent inside the same top-level item."}
		}
		payload, err := json.Marshal(map[string]string{"body": body})
		if err != nil {
			return err
		}
		m, err := s.enqueue(ctx, tx, Message{Kind: kind, Origin: "agent",
			FromAgentID: a.ID, FromSessionID: sessionID, ToAgentID: target.ID,
			RootItemID: a.RootItemID, CorrelationID: correlationID, Payload: payload})
		id = m.ID
		return err
	})
	return id, err
}

// PendingCount is the number of un-acked messages waiting for agentID.
func (s *Store) PendingCount(ctx context.Context, agentID string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE to_agent_id = ? AND state <> 'acked'`, agentID).Scan(&n)
	return n, err
}

// Idempotent runs fn once per (session, request_id) and replays the stored result
// (I11). The caller is the session, never the daemon token: the HTTP helper in
// internal/httpapi covers UI replays and is deliberately separate.
func (s *Store) Idempotent(ctx context.Context, tx *sql.Tx, sessionID, requestID, tool string, fn func() (any, error)) (json.RawMessage, error) {
	if requestID == "" {
		v, err := fn()
		if err != nil {
			return nil, err
		}
		return json.Marshal(v)
	}
	if len(requestID) > 64 {
		return nil, &items.Error{Code: items.CodeBadRequest,
			Message: "request_id must be at most 64 characters."}
	}
	var stored string
	err := tx.QueryRowContext(ctx, `SELECT result_json FROM idempotency WHERE caller = ? AND request_id = ?`,
		sessionID, requestID).Scan(&stored)
	if err == nil {
		return json.RawMessage(stored), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	v, err := fn()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO idempotency (caller, request_id, tool, result_json, created_at)
		VALUES (?, ?, ?, ?, ?)`, sessionID, requestID, tool, string(body), db.Millis(s.Now()))
	return body, err
}
