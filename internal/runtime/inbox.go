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

// WakeClassFor: every relay now wakes immediately (2026-09-22: empirical
// checkpoint cadence — median 5 min, worst observed case 8.5 min between
// progress checkpoints — showed the deferred/digest split's anti-spam
// rationale never held in practice, and it was silently losing real
// completion reports like TASK-107's).
func WakeClassFor(kind MessageKind) WakeClass {
	if kind == "relay" {
		return "immediate"
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

// enqueue stores one message, unless it is daemon fan-in to a kind that is
// confirmed out of usage right now: relay/digest/advice from the daemon to an
// exhausted kind are held (one suppressed_relays row per event) instead of
// queued, so a quota-dead parent does not wake up to hundreds of relays
// (2026-09-23 orchestrator-2 incident). Holding returns a zero Message and nil
// error -- no daemon-relay caller reads the returned ID, only agent-origin
// Send does, and it never matches this gate. seq is max(seq)+1 inside this
// transaction, which is safe because the daemon is the only writer (§5).
func (s *Store) enqueue(ctx context.Context, tx *sql.Tx, m Message) (Message, error) {
	if m.Origin == "daemon" && (m.Kind == "relay" || m.Kind == "digest" || m.Kind == "advice") {
		a, err := s.agentByIDTx(ctx, tx, m.ToAgentID)
		if err != nil {
			return m, err
		}
		if s.Usage != nil && s.Usage.Exhausted(ctx, a.Kind) {
			event := string(m.Kind)
			var p struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(m.Payload, &p) == nil && p.Event != "" {
				event = p.Event
			}
			if held, err := s.holdIfExhausted(ctx, tx, m.ToAgentID, event, m.Payload); err != nil {
				return m, err
			} else if held {
				return Message{}, nil
			}
		}
	}
	return s.enqueueRaw(ctx, tx, m)
}

// enqueueRaw is the ungated insert body. foldDigest (which compresses
// pre-existing deferred rows rather than adding load) and the quota-reset
// flush use it directly; everything else goes through enqueue's hold gate.
func (s *Store) enqueueRaw(ctx context.Context, tx *sql.Tx, m Message) (Message, error) {
	if m.ID == "" {
		m.ID = ids.New("msg")
	}
	if m.Priority == 0 && m.Kind != "control" {
		m.Priority = 1
	}
	m.WakeClass = WakeClassFor(m.Kind)
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

// holdIfExhausted reports whether a daemon relay/digest/advice to toAgentID must
// be held instead of enqueued: the target's kind is confirmed exhausted via
// Store.Usage (2026-09-23: queueing hundreds of relays to a quota-dead parent
// burns its whole reset window on wakeup). When holding, it upserts one
// suppressed_relays row per (agent, event) and returns true; the caller skips
// its enqueue. Nil Usage never holds (fail open: tests, SWARM_USAGE unset).
// sample is stored (truncated) on first hold so the quota-reset digest can name it.
func (s *Store) holdIfExhausted(ctx context.Context, tx *sql.Tx, toAgentID, event string, sample json.RawMessage) (bool, error) {
	if s.Usage == nil {
		return false, nil
	}
	a, err := s.agentByIDTx(ctx, tx, toAgentID)
	if err != nil {
		return false, err
	}
	if !s.Usage.Exhausted(ctx, a.Kind) {
		return false, nil
	}
	now := db.Millis(s.Now())
	sampleStr := string(sample)
	if len(sampleStr) > 500 {
		sampleStr = sampleStr[:500]
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO suppressed_relays (agent_id, event, count, first_at, last_at, sample_json)
		VALUES (?, ?, 1, ?, ?, ?)
		ON CONFLICT (agent_id, event) DO UPDATE SET count = count + 1, last_at = excluded.last_at`,
		toAgentID, event, now, now, sampleStr)
	if err != nil {
		return false, err
	}
	return true, nil
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
		COALESCE(advisor_effort, ''), COALESCE(advisor_mode, ''), brief, state, COALESCE(preflight_error, ''), created_at, finished_at,
		COALESCE(role_overrides, '')
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
		COALESCE(advisor_effort, ''), COALESCE(advisor_mode, ''), brief, state, COALESCE(preflight_error, ''), created_at, finished_at,
		COALESCE(role_overrides, '')
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
	Unacked      []UnackedRef
	More         bool
	SessionState SessionState
}

const defaultSyncLimit = 20
const maxFullDeliveries = 3
const maxUnackedRefs = 50

// UnackedRef is a delivered, non-control message that has used its full
// deliveries: the agent sees its id and kind, never the body again, and must ack.
type UnackedRef struct {
	MsgID         string      `json:"msg_id"`
	Seq           int64       `json:"seq"`
	Kind          MessageKind `json:"kind"`
	DeliveryCount int         `json:"delivery_count"`
}

const maxDigest = 1500

// Sync is swarm_sync (§8.1). ack is applied first; un-acked messages are
// returned by priority then seq until delivered three times, after which they
// are listed in Unacked (id and kind only). Deferred relays are folded into one
// digest (I19).
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
		// Before envelopes: a message reaching its third delivery in this very
		// sync must not be listed twice.
		if out.Unacked, err = s.staleUnackedFor(ctx, tx, a.ID); err != nil {
			return err
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

// staleUnackedFor lists delivered non-control messages that have used their
// maxFullDeliveries, oldest first, at most maxUnackedRefs.
func (s *Store) staleUnackedFor(ctx context.Context, tx *sql.Tx, agentID string) ([]UnackedRef, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, seq, kind, delivery_count FROM messages
		WHERE to_agent_id = ? AND state = 'delivered' AND kind <> 'control' AND delivery_count >= ?
		ORDER BY seq LIMIT ?`, agentID, maxFullDeliveries, maxUnackedRefs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnackedRef
	for rows.Next() {
		var u UnackedRef
		var kind string
		if err := rows.Scan(&u.MsgID, &u.Seq, &kind, &u.DeliveryCount); err != nil {
			return nil, err
		}
		u.Kind = MessageKind(kind)
		out = append(out, u)
	}
	return out, rows.Err()
}

// unackedFor returns up to limit un-acked messages for agentID (pending, or
// delivered fewer than maxFullDeliveries times, or control), ordered by
// priority then seq.
func (s *Store) unackedFor(ctx context.Context, tx *sql.Tx, agentID string, limit int) ([]Message, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, seq, kind, wake_class, priority, origin,
		COALESCE(from_agent_id, ''), COALESCE(from_session_id, ''), to_agent_id, root_item_id,
		COALESCE(item_id, ''), COALESCE(correlation_id, ''), COALESCE(reply_to, ''),
		COALESCE(request_id, ''), payload_json, state, delivery_count, created_at
		FROM messages WHERE to_agent_id = ? AND state <> 'acked'
		AND NOT (state = 'delivered' AND kind <> 'control' AND delivery_count >= ?)
		ORDER BY priority, seq LIMIT ?`, agentID, maxFullDeliveries, limit)
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
			// Ungated on purpose: these rows predate any outage and the digest
			// compresses them rather than adding load.
			return s.enqueueRaw(ctx, tx, Message{Kind: "digest", Origin: "daemon",
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
		if m.Kind != "control" && m.DeliveryCount+1 == maxFullDeliveries {
			if err := s.escalateUnacked(ctx, tx, to, m); err != nil {
				return nil, err
			}
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

// escalateUnacked is the orchestrator mirror of notifyNoAck's child-silent
// case (reconcile.go): m has just crossed maxFullDeliveries deliveries to
// `to` without ever being acked. It fires exactly once per message, a
// structural property of where it's called from (see the spec's "Where this
// hooks in") rather than its own dedup table: unackedFor's WHERE clause
// excludes a message from ever being re-selected, and therefore
// re-incremented, once its delivery_count reaches maxFullDeliveries.
func (s *Store) escalateUnacked(ctx context.Context, tx *sql.Tx, to Agent, m Message) error {
	itemKey, _ := s.itemKey(ctx, tx, m.ItemID) // best-effort, matches e.Item's own handling in envelopes()
	if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.message_unacked", AgentName: to.Name,
		ItemKey: itemKey, Args: map[string]string{"name": to.Name, "KEY": itemKey, "kind": string(m.Kind)}}); err != nil {
		return err
	}
	ancestor, ok, err := s.nearestLiveAncestor(ctx, to.ID)
	if err != nil {
		return err
	}
	if !ok {
		return nil // a top-level orchestrator: nothing above it to relay to
	}
	payload, err := json.Marshal(map[string]any{"event": "message_unacked", "agent": to.Name, "message_kind": string(m.Kind)})
	if err != nil {
		return err
	}
	_, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: ancestor.ID,
		RootItemID: to.RootItemID, ItemID: m.ItemID, Payload: payload})
	return err
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
		// A target this message will never reach will never sync/ack: without
		// this check the message just sits in `messages` as pending forever,
		// and the sender has no way to know delivery is impossible (the
		// production incident this guards against — see docs/specs and
		// notifyUndeliveredMessages in reconcile.go for the race where the
		// target dies AFTER this check). A paused or interrupted target still
		// gets it once Resume starts its next generation, and a queued one
		// once DrainQueue admits it — only a target whose latest session is
		// terminal (or that has finished/been acknowledged) is genuinely
		// unreachable, hence agentCanReceive rather than a bare live check.
		can, err := s.agentCanReceive(ctx, tx, target.ID)
		if err != nil {
			return err
		}
		if !can {
			// A target owned by an in-flight replacement or a queued retry
			// is between sessions, not gone: the message waits in its
			// (canonical, agent-keyed) inbox for the successor instead of
			// being refused at the stopping gap.
			if _, ok, herr := s.pendingOperationTx(ctx, tx, target.ID); herr != nil {
				return herr
			} else if !ok {
				return &items.Error{Code: items.CodeBadRequest,
					Message: fmt.Sprintf("%s has no live session; the message was not sent.", target.Name)}
			}
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

// summarizeFor extracts one sanitized, quoted (where it carries free text)
// summary line from a message's kind and payload. Unrecognized kind/shape
// falls back to a JSON preview rather than guessing a field name.
func summarizeFor(kind MessageKind, payload json.RawMessage) string {
	quote := func(s string) string { return `"` + sanitizeOneLine(s) + `"` }
	var raw map[string]any
	json.Unmarshal(payload, &raw)
	str := func(k string) (string, bool) {
		v, ok := raw[k].(string)
		return v, ok
	}
	switch kind {
	case "assignment":
		if b, ok := str("brief"); ok {
			return "New assignment: " + quote(b)
		}
	case "assignment_update":
		// Store.Retry writes {"note": ...}; other callers (e.g. worktree
		// share) use caller-specific fields with no stable text key, which
		// fall through to the raw-JSON preview below.
		if n, ok := str("note"); ok {
			return "Assignment update: " + quote(n)
		}
	case "question", "answer", "finding", "repos_confirmed":
		if b, ok := str("body"); ok {
			return quote(b)
		}
	case "control":
		action, _ := str("action")
		scope, _ := str("scope")
		return sanitizeOneLine(fmt.Sprintf("%s requested (scope=%s)", action, scope))
	case "approval_result":
		decision, _ := str("decision")
		if comment, ok := str("comment"); ok && comment != "" {
			return sanitizeOneLine(decision) + ": " + quote(comment)
		}
		return sanitizeOneLine(decision)
	case "user_answer":
		if t, ok := str("text"); ok {
			return quote(t)
		}
	case "advice":
		switch state, _ := str("state"); state {
		case "answered":
			if a, ok := str("answer"); ok {
				return quote(a)
			}
		case "failed":
			if e, ok := str("error"); ok {
				return "advice failed: " + quote(e)
			}
		default:
			if q, ok := str("question"); ok {
				return quote(q)
			}
		}
	case "relay":
		event, _ := str("event")
		agent, _ := str("agent")
		item, _ := str("item")
		line := sanitizeOneLine(fmt.Sprintf("%s from %s (%s)", event, agent, item))
		if cp, ok := raw["checkpoint"].(map[string]any); ok {
			if summary, ok := cp["summary"].(string); ok && summary != "" {
				line += ": " + quote(summary)
			}
		}
		return line
	case "digest":
		if lines, ok := raw["lines"].([]any); ok && len(lines) > 0 {
			if first, ok := lines[0].(string); ok {
				return sanitizeOneLine(first)
			}
		}
	}
	preview, _ := truncateRunes(string(payload), 120) // rune-safe, see text.go
	return sanitizeOneLine(preview)
}

const maxInboxItems = 8

// pendingInboxItems loads up to limit pending messages for agentID, oldest
// first (priority, seq — the same order swarm_sync delivers them), each
// resolved to a sanitized InboxItem. moreCount is how many pending messages
// exist beyond limit.
func (s *Store) pendingInboxItems(ctx context.Context, agentID string, limit int) ([]InboxItem, int, error) {
	var items []InboxItem
	var total int
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
			WHERE to_agent_id = ? AND state = 'pending'`, agentID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, kind, COALESCE(from_agent_id, ''), payload_json
			FROM messages WHERE to_agent_id = ? AND state = 'pending'
			ORDER BY priority, seq LIMIT ?`, agentID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, kind, fromAgentID string
			var payload []byte
			if err := rows.Scan(&id, &kind, &fromAgentID, &payload); err != nil {
				return err
			}
			// Sender attribution mirrors envelopes() above: a resolvable
			// from_agent_id names the peer, otherwise the daemon. (No
			// origin check here: TestOnlyTheUserPathsWriteUserActionMessages
			// forbids the "user_action" literal outside the allow-listed
			// UI/CLI write paths, and this is a read path.)
			from := "daemon"
			if fromAgentID != "" {
				if a, err := s.agentByIDTx(ctx, tx, fromAgentID); err == nil {
					from = a.Name
				}
			}
			items = append(items, InboxItem{ID: id, Kind: kind, From: from,
				Summary: summarizeFor(MessageKind(kind), json.RawMessage(payload))})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	more := total - len(items)
	if more < 0 {
		more = 0
	}
	return items, more, nil
}

// InboxNotice renders the notice used on every delivery channel: hook
// context, native wake, and tryPaste's raw paste alike (v2 Locked decision 1).
func (s *Store) InboxNotice(ctx context.Context, agentID, name, key string) (string, error) {
	items, more, err := s.pendingInboxItems(ctx, agentID, maxInboxItems)
	if err != nil {
		return "", err
	}
	return Inbox(items, more, name, key), nil
}
