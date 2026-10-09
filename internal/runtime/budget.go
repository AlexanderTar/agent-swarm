package runtime

import (
	"context"
	"strconv"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
)

// NativeCapTokens is the native compaction cap a low-token orchestrator of this
// kind launches with, or 0 when the kind has none (agy).
func NativeCapTokens(kind string) int {
	switch AgentKind(kind) {
	case Claude, Muse, Cursor:
		return 200000
	case Codex:
		return 150000
	}
	return 0
}

// BackstopTokens is the context size at which the daemon hands a low-token
// orchestrator off: 1.5x its native cap, or 300k for a kind with no cap.
func BackstopTokens(kind string) int {
	if c := NativeCapTokens(kind); c > 0 {
		return c * 3 / 2
	}
	return 300000
}

// RecordContextSample stores a session's latest context size. A nil window
// keeps the existing one (a muse launch limit, or an earlier exact reading).
// It runs for every session, whether or not low-token mode is on.
func (s *Store) RecordContextSample(ctx context.Context, sessionID string, tokens int, window *int) error {
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE sessions SET context_tokens = ?, context_window = COALESCE(?, context_window) WHERE id = ?`,
		tokens, window, sessionID); err != nil {
		return err
	}
	return s.updateContextStrikes(ctx, sessionID, tokens)
}

// budgetKeyPrefix keys a budget handoff "budget:<session id>", so a session
// is handed off at most once.
const budgetKeyPrefix = "budget:"

// budgetStrikes is how many consecutive samples at or over the backstop
// trigger a handoff.
const budgetStrikes = 2

// updateContextStrikes counts consecutive samples at or over the backstop for
// a low-token orchestrator's session; any other session stays at 0.
func (s *Store) updateContextStrikes(ctx context.Context, sessionID string, tokens int) error {
	var agentID string
	var role Role
	if err := s.DB.QueryRowContext(ctx,
		`SELECT a.id, a.role FROM sessions ses JOIN agents a ON a.id = ses.agent_id WHERE ses.id = ?`, sessionID).Scan(&agentID, &role); err != nil {
		return err
	}
	if role != RoleOrchestrator {
		return nil
	}
	a, err := s.agentByID(ctx, agentID)
	if err != nil {
		return err
	}
	on, err := s.LowTokenFor(ctx, a)
	if err != nil {
		return err
	}
	if !on {
		_, err = s.DB.ExecContext(ctx, `UPDATE sessions SET context_strikes = 0 WHERE id = ?`, sessionID)
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`UPDATE sessions SET context_strikes = CASE WHEN ? >= ? THEN context_strikes + 1 ELSE 0 END WHERE id = ?`,
		tokens, BackstopTokens(string(a.Kind)), sessionID)
	return err
}

// budgetCandidatesSQL lists low-token orchestrators whose latest session has
// two strikes, under the capacity safe-point conditions (no in-flight
// operation, no open request or unanswered question, no earlier budget
// handoff for the session). Live children do not exclude one, but an
// operation in flight on a direct child does.
var budgetCandidatesSQL = candidatesHeadSQL + `'` + budgetKeyPrefix + `'` + candidatesSafePointSQL + `
	AND agents.role = 'orchestrator' AND ls.context_strikes >= ` + strconv.Itoa(budgetStrikes) + `
	AND NOT EXISTS (SELECT 1 FROM agents c JOIN agent_operations co ON co.agent_id = c.id
		WHERE c.parent_agent_id = agents.id AND co.phase IN ` + inFlightPhasesSQL + `)
	ORDER BY agents.created_at, agents.id`

// EnforceContextBudget hands off each low-token orchestrator whose context
// has been at or over its backstop for two consecutive samples. The request
// is RequestReplacement(ModeHandoff) keyed "budget:<session id>" with the budget note, so a
// session is handed off once. Runs every reconcile tick; idempotent.
func (s *Store) EnforceContextBudget(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, budgetCandidatesSQL)
	if err != nil {
		return err
	}
	type candidate struct{ agentID, sessionID string }
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.agentID, &c.sessionID); err != nil {
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
		a, err := s.agentByID(ctx, c.agentID)
		if err != nil {
			continue
		}
		if on, err := s.LowTokenFor(ctx, a); err != nil || !on {
			continue
		}
		if _, err := s.RequestReplacement(ctx, c.agentID, ModeHandoff, budgetKeyPrefix+c.sessionID, ""); err != nil {
			s.logf("budget: hand off %s: %v", c.agentID, err)
		}
	}
	return nil
}

// SampleTranscript reads the session's transcript for its kind and records
// the sample, filling the window from ContextWindowTokens when the source has none.
func (s *Store) SampleTranscript(ctx context.Context, sessionID string, kind AgentKind, path string) error {
	smp, ok := adapter.ReadContext(kind, path)
	if !ok {
		return nil
	}
	return s.recordRead(ctx, sessionID, kind, smp)
}

func (s *Store) recordRead(ctx context.Context, sessionID string, kind AgentKind, smp adapter.ContextSample) error {
	if smp.Window == nil {
		var model string
		if err := s.DB.QueryRowContext(ctx,
			`SELECT a.model FROM sessions ses JOIN agents a ON a.id = ses.agent_id WHERE ses.id = ?`, sessionID).Scan(&model); err != nil {
			return err
		}
		if w, ok := adapter.ContextWindowTokens(kind, model); ok {
			smp.Window = &w
		}
	}
	return s.RecordContextSample(ctx, sessionID, smp.Tokens, smp.Window)
}

// sampleMuseContexts records the context size of every live muse session from
// its own session log (no hook carries it). Failures are logged: a bad log
// must not stall the wake loop.
func (s *Store) sampleMuseContexts(ctx context.Context) {
	rd, ok := s.Adapters[Muse].(interface {
		SessionContext(providerSessionID string) (adapter.ContextSample, bool)
	})
	if !ok {
		return
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.provider_session_id FROM sessions ses
		JOIN agents a ON a.id = ses.agent_id
		WHERE a.kind = 'muse' AND ses.state = 'running' AND COALESCE(ses.provider_session_id, '') != ''`)
	if err != nil {
		s.logf("context: list muse sessions: %v", err)
		return
	}
	type live struct{ id, provider string }
	var sessions []live
	for rows.Next() {
		var l live
		if err := rows.Scan(&l.id, &l.provider); err == nil {
			sessions = append(sessions, l)
		}
	}
	rows.Close()
	for _, l := range sessions {
		if smp, ok := rd.SessionContext(l.provider); ok {
			if err := s.recordRead(ctx, l.id, Muse, smp); err != nil {
				s.logf("context: record muse sample for %s: %v", l.id, err)
			}
		}
	}
}
