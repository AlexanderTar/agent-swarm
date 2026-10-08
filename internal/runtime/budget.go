package runtime

import (
	"context"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
)

// RecordContextSample stores a session's latest context size. A nil window
// keeps the existing one (a muse launch limit, or an earlier exact reading).
// It runs for every session, whether or not low-token mode is on.
func (s *Store) RecordContextSample(ctx context.Context, sessionID string, tokens int, window *int) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE sessions SET context_tokens = ?, context_window = COALESCE(?, context_window) WHERE id = ?`,
		tokens, window, sessionID)
	return err
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
