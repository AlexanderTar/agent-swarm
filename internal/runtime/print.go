package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// Print-then-ask (docs/specs/2026-09-29-approval-print-then-ask.md): an approval with a chat_block
// is two turns. The agent first gets only the block and replies with it; once that turn ends the
// daemon checks the reply and sends the native question in a separate request_ask relay.

const PrintNext = "End your turn now. Your whole reply is chat_block, verbatim: nothing before or after it, not restated, shortened or paraphrased. Swarm sends you the question next."
const ReprintNext = "Your last reply must be chat_block, verbatim. Reply with it and end your turn. Swarm sends you the question next."

const (
	printPhasePrint = "print"
	printPhaseAsk   = "ask"
)

// TurnReply is what the daemon knows about the agent's just-finished turn.
type TurnReply struct {
	Text     string
	Readable bool // false: transcript missing/unparseable -> fail open
	Trusted  bool // kind has no reader (cursor) -> pass
}

// turnReplyReader is implemented by an adapter with no Stop hook (Muse): LastReply returns the
// text of the newest assistant run recorded at or after since.
type turnReplyReader interface {
	LastReply(providerSessionID string, since time.Time) (text string, found, readable bool)
}

func isMsgRef(ref string) bool { return strings.HasPrefix(ref, "msg_") }

// printRef is one approval waiting in the print phase.
type printRef struct {
	id       string
	attempts int
	printAt  int64
}

// printRefs lists the agent's print-phase refs: open requests, and approval questions from a
// child that nobody has answered yet (notifyUnansweredQuestions' NOT EXISTS, plus a request
// already approved through a bound question row).
func (s *Store) printRefs(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, agentID string) ([]printRef, error) {
	var out []printRef
	for _, query := range []string{
		`SELECT id, COALESCE(json_extract(binding_json, '$.print_attempts'), 0), COALESCE(json_extract(binding_json, '$.print_at'), 0)
			FROM requests WHERE agent_id = ? AND state = 'open' AND json_extract(binding_json, '$.print_phase') = 'print'
			ORDER BY created_at, id`,
		`SELECT m.id, COALESCE(json_extract(m.payload_json, '$.print_attempts'), 0), COALESCE(json_extract(m.payload_json, '$.print_at'), 0)
			FROM messages m
			WHERE m.to_agent_id = ? AND m.kind = 'question' AND json_extract(m.payload_json, '$.approval') = 1
			  AND json_extract(m.payload_json, '$.print_phase') = 'print'
			  AND NOT EXISTS (SELECT 1 FROM messages a WHERE a.kind IN ('answer', 'approval_result', 'relay')
			                  AND (a.reply_to = m.id OR (a.kind = 'answer' AND a.correlation_id = m.id)))
			  AND NOT EXISTS (SELECT 1 FROM requests rq WHERE json_extract(rq.binding_json, '$.ref') = m.id
			                  AND rq.state IN ('approved', 'changes_requested'))
			ORDER BY m.seq`,
	} {
		rows, err := q.QueryContext(ctx, query, agentID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r printRef
			if err := rows.Scan(&r.id, &r.attempts, &r.printAt); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// setPrintTx writes the phase keys onto the ref's request binding or its child message payload.
func (s *Store) setPrintTx(ctx context.Context, tx *sql.Tx, ref, phase string, attempts int) error {
	col, table := "binding_json", "requests"
	if isMsgRef(ref) {
		col, table = "payload_json", "messages"
	}
	_, err := tx.ExecContext(ctx, `UPDATE `+table+` SET `+col+` = json_set(COALESCE(`+col+`, '{}'),
		'$.print_phase', ?, '$.print_attempts', ?, '$.print_at', ?) WHERE id = ?`,
		phase, attempts, db.Millis(s.Now()), ref)
	return err
}

// startPrintTx puts ref (a request id or a child approval's msg_ id) in the print phase.
func (s *Store) startPrintTx(ctx context.Context, tx *sql.Tx, ref string) error {
	return s.setPrintTx(ctx, tx, ref, printPhasePrint, 0)
}

// retirePrintRelaysTx acks a request's unacked request_ask/request_print relays, so a resolved or
// resurfaced request never leaves a stale instruction for the agent.
func (s *Store) retirePrintRelaysTx(ctx context.Context, tx *sql.Tx, requestID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE messages SET state = 'acked', acked_at = ?
		WHERE request_id = ? AND kind = 'relay' AND state <> 'acked'
		  AND json_extract(payload_json, '$.event') IN ('request_ask', 'request_print')`,
		db.Millis(s.Now()), requestID)
	return err
}

// printTarget is what a print-phase relay needs about its ref.
type printTarget struct {
	ref, kind, block string
	req              *Request // nil for a child approval
	agent            Agent
	itemKey, rootID  string
	itemID           string
}

func (s *Store) printTargetTx(ctx context.Context, tx *sql.Tx, ref string) (printTarget, error) {
	t := printTarget{ref: ref}
	if isMsgRef(ref) {
		var agentID, child, body string
		if err := tx.QueryRowContext(ctx, `SELECT m.to_agent_id, ag.name, COALESCE(json_extract(m.payload_json, '$.body'), '')
			FROM messages m JOIN agents ag ON ag.id = m.from_agent_id WHERE m.id = ?`, ref).Scan(&agentID, &child, &body); err != nil {
			return t, err
		}
		a, err := s.agentByIDTx(ctx, tx, agentID)
		if err != nil {
			return t, err
		}
		t.agent, t.kind, t.itemID, t.rootID = a, "child_approval", a.ItemID, a.RootItemID
		t.block = ApprovalChatBlock(ChatBlockInput{Child: child, Summary: body})
	} else {
		req, err := s.requestTx(ctx, tx, ref)
		if err != nil {
			return t, err
		}
		a, err := s.agentByIDTx(ctx, tx, req.AgentID)
		if err != nil {
			return t, err
		}
		if t.rootID, err = s.rootItemID(ctx, tx, req.ItemID); err != nil {
			return t, err
		}
		if t.block, err = s.approvalChatBlockTx(ctx, tx, req); err != nil {
			return t, err
		}
		t.req, t.agent, t.kind, t.itemID = &req, a, string(req.Kind), req.ItemID
	}
	var err error
	t.itemKey, err = s.itemKey(ctx, tx, t.itemID)
	return t, err
}

// enqueuePrintRelayTx sends the agent a daemon relay about ref: request_id is the ref in the
// payload; the message row keys a request by request_id and a child approval by correlation_id
// (messages.request_id is an FK to requests).
func (s *Store) enqueuePrintRelayTx(ctx context.Context, tx *sql.Tx, t printTarget, payload map[string]any) error {
	payload["agent"], payload["item"], payload["request_id"], payload["kind"] = t.agent.Name, t.itemKey, t.ref, t.kind
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	m := Message{Kind: "relay", Origin: "daemon", ToAgentID: t.agent.ID, RootItemID: t.rootID, ItemID: t.itemID, Payload: body}
	if t.req != nil {
		m.RequestID = t.ref
	} else {
		m.CorrelationID = t.ref
	}
	_, err = s.enqueueRaw(ctx, tx, m)
	return err
}

// sendAskTx sends the request_ask relay: the frozen native prompt, to be shown now.
func (s *Store) sendAskTx(ctx context.Context, tx *sql.Tx, ref string) error {
	t, err := s.printTargetTx(ctx, tx, ref)
	if err != nil {
		return err
	}
	var np NativePrompt
	decisions := []string{"approve", "request_changes"}
	payload := map[string]any{}
	if t.req != nil {
		if np, err = s.storedNativePromptTx(ctx, tx, *t.req); err != nil {
			return err
		}
		decisions = PromptDecisions(t.req.Kind, np)
		if t.req.Kind == KindApprovePlan {
			paths, _, err := s.planReviewPathsTx(ctx, tx, t.req.ItemID, t.req.ArtifactID)
			if err != nil {
				return err
			}
			payload["review_paths"] = paths
		}
	} else {
		var child, body string
		if err := tx.QueryRowContext(ctx, `SELECT ag.name, COALESCE(json_extract(m.payload_json, '$.body'), '')
			FROM messages m JOIN agents ag ON ag.id = m.from_agent_id WHERE m.id = ?`, ref).Scan(&child, &body); err != nil {
			return err
		}
		np = nativePromptForMsg(child, body, ref)
	}
	payload["event"], payload["question"], payload["native_prompt"] = "request_ask", np.Question, np
	payload["next"] = NativePromptNextStep(ref, decisions)
	return s.enqueuePrintRelayTx(ctx, tx, t, payload)
}

// sendReprintTx sends the request_print relay: the block again, after a reply that lacked it.
func (s *Store) sendReprintTx(ctx context.Context, tx *sql.Tx, ref string) error {
	t, err := s.printTargetTx(ctx, tx, ref)
	if err != nil {
		return err
	}
	return s.enqueuePrintRelayTx(ctx, tx, t, map[string]any{"event": "request_print", "chat_block": t.block,
		"attempt": 2, "next": ReprintNext})
}

// PrintTurnEnded judges every print-phase ref of the session's agent against the reply it just
// finished (decision 4) and enqueues request_ask / request_print relays. sent reports whether
// anything was enqueued.
func (s *Store) PrintTurnEnded(ctx context.Context, sessionID string, r TurnReply) (sent bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		sent = false
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		refs, err := s.printRefs(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			// A relay the agent has not seen yet (pending) means this turn never held its
			// instruction; delivered counts as seen.
			var unseen bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM messages WHERE kind = 'relay' AND state = 'pending'
				AND to_agent_id = ? AND (request_id = ? OR correlation_id = ?))`, a.ID, ref.id, ref.id).Scan(&unseen); err != nil {
				return err
			}
			if unseen {
				continue
			}
			t, err := s.printTargetTx(ctx, tx, ref.id)
			if err != nil {
				return err
			}
			switch {
			case r.Trusted:
				s.logf("print: %s %s has no transcript reader, trusting the reply", ref.id, a.Kind)
			case !r.Readable:
				s.logf("print: %s reply unreadable, asking", ref.id)
			case strings.Contains(NormForMatch(r.Text), NormForMatch(t.block)):
				s.logf("print: %s reply matches chat_block, asking", ref.id)
			case ref.attempts == 0:
				s.logf("print: %s reply is missing chat_block (attempt 1), re-sending the block", ref.id)
				if err := s.setPrintTx(ctx, tx, ref.id, printPhasePrint, 1); err != nil {
					return err
				}
				if err := s.sendReprintTx(ctx, tx, ref.id); err != nil {
					return err
				}
				sent = true
				continue
			default:
				s.logf("print: %s reply is missing chat_block after 2 attempts, asking anyway", ref.id)
			}
			if err := s.setPrintTx(ctx, tx, ref.id, printPhaseAsk, ref.attempts); err != nil {
				return err
			}
			if err := s.sendAskTx(ctx, tx, ref.id); err != nil {
				return err
			}
			sent = true
		}
		return nil
	})
	return sent, err
}

// PrintTurnTick is the no-hook turn-end path: for each live session whose adapter reads its own
// replies (Muse), that has print-phase refs and no pending messages, when the pane is idle and a
// reply newer than the instruction exists, it judges that reply.
// ponytail: a Muse agent that goes idle without writing any text stays in print; the board still
// resolves it. Add a timeout if this is ever seen.
func (s *Store) PrintTurnTick(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.tmux_name, COALESCE(ses.provider_session_id, ''), a.kind, a.id
		FROM sessions ses JOIN agents a ON a.id = ses.agent_id
		WHERE ses.state IN ('spawning', 'running')
		  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.to_agent_id = a.id AND m.state = 'pending')`)
	if err != nil {
		return err
	}
	type cand struct{ id, tmux, provider, kind, agentID string }
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.tmux, &c.provider, &c.kind, &c.agentID); err != nil {
			rows.Close()
			return err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range cands {
		reader, ok := s.Adapters[AgentKind(c.kind)].(turnReplyReader)
		if !ok {
			continue
		}
		refs, err := s.printRefs(ctx, s.DB, c.agentID)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			continue
		}
		var since int64
		for _, r := range refs {
			since = max(since, r.printAt)
		}
		capture, err := s.Tmux.Capture(ctx, c.tmux, 15)
		if err != nil || !s.Adapters[AgentKind(c.kind)].Idle(capture) {
			continue
		}
		text, found, readable := reader.LastReply(c.provider, db.FromMillis(since))
		reply := TurnReply{}
		switch {
		case !readable:
		case found:
			reply = TurnReply{Text: text, Readable: true}
		default:
			continue
		}
		if _, err := s.PrintTurnEnded(ctx, c.id, reply); err != nil {
			return err
		}
	}
	return nil
}
