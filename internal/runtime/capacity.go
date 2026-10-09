package runtime

import (
	"context"
	"database/sql"
	"strings"
)

// Request-key prefixes for operations the daemon starts to keep within
// max_concurrent_agents (spec 2026-09-27-single-agent-limit-live). The key,
// never the note, is the marker: a note is pasted into the successor's
// prompt.
const (
	capacityKeyPrefix = "capacity:"
	resumeKeyPrefix   = "resume:"
	retryKeyPrefix    = "retry:"
)

// OperationReason names why the daemon started an operation: "capacity"
// (paused to fit the agent limit), "resume" (a manual resume waiting for a
// slot), "retry" (a manual retry waiting for a slot, or one that arrived
// while its predecessor was still stopping) or "" (anything else).
func OperationReason(requestKey string) string {
	switch {
	case strings.HasPrefix(requestKey, capacityKeyPrefix):
		return "capacity"
	case strings.HasPrefix(requestKey, resumeKeyPrefix):
		return "resume"
	case strings.HasPrefix(requestKey, retryKeyPrefix):
		return "retry"
	}
	return ""
}

// inFlightPhasesSQL is nonterminalPhases as a SQL list (keep in sync with
// replacement.go and the agent_operations_one_active index).
const inFlightPhasesSQL = `('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting')`

// pendingCapacitySQL counts slot-holders already leaving their slot: being
// paused for capacity (still saving their handoff), pausing for any other
// reason, or mid handoff/recover before the stop. They count toward the
// reduction, so a tick never pauses an extra agent (spec decision 5) only
// to restart it once the leaver lets go; any successor comes back through
// Admit.
const pendingCapacitySQL = `SELECT COUNT(*) FROM agents WHERE state = 'active' AND ` + NotAZombieSlot + `
	AND (EXISTS (SELECT 1 FROM agent_operations o WHERE o.agent_id = agents.id
			AND ((o.request_key LIKE 'capacity:%' AND o.phase IN ` + inFlightPhasesSQL + `)
				OR o.phase IN ('requested', 'preserving', 'stopping')))
		OR (SELECT state FROM sessions WHERE agent_id = agents.id
			ORDER BY generation DESC, attempt DESC LIMIT 1) IN ('pause_requested', 'quiescing', 'stopping'))`

// capacityCandidatesSQL lists slot-holders EnforceCapacity may pause, in
// pause order (spec decision 4): latest session running; no operation in
// flight; this session never capacity-paused before (a cancelled or blocked
// one is not retried); no open request; no unanswered question it asked
// (the same answer test as notifyUnansweredQuestions, reconcile.go, minus
// its age cutoff). Workers first, then newest first.
const capacityCandidatesSQL = candidatesHeadSQL + `'capacity:'` + candidatesSafePointSQL + `
	ORDER BY agents.role = 'orchestrator', agents.created_at DESC, agents.id DESC`

// candidatesHeadSQL and candidatesSafePointSQL are the shared pieces of the
// capacity and budget candidate queries; the request-key prefix literal goes
// between them.
const candidatesHeadSQL = `SELECT agents.id, ls.id FROM agents
	JOIN sessions ls ON ls.id = (SELECT id FROM sessions WHERE agent_id = agents.id
		ORDER BY generation DESC, attempt DESC LIMIT 1)
	WHERE agents.state = 'active' AND ` + NotAZombieSlot + ` AND ls.state = 'running'
		AND NOT EXISTS (SELECT 1 FROM agent_operations o WHERE o.agent_id = agents.id
			AND (o.phase IN ` + inFlightPhasesSQL + ` OR o.request_key = `

const candidatesSafePointSQL = ` || ls.id))
		AND NOT EXISTS (SELECT 1 FROM requests r WHERE r.agent_id = agents.id AND r.state = 'open')
		AND NOT EXISTS (SELECT 1 FROM messages q WHERE q.kind = 'question' AND q.origin = 'agent'
			AND q.state = 'acked' AND q.from_agent_id = agents.id
			AND NOT EXISTS (SELECT 1 FROM messages a WHERE a.kind IN ('answer', 'approval_result', 'relay')
				AND (a.reply_to = q.id OR (a.kind = 'answer' AND a.correlation_id = q.id))))`

// EnforceCapacity hands off the newest eligible slot-holders while more
// agents hold a slot than max_concurrent_agents allows (spec decisions
// 3-5). Each pause is RequestReplacement(ModeHandoff) keyed
// "capacity:<session id>" with an empty note; the operation parks in
// queued and ResumeOperations restarts the agent once Admit has room.
// Runs every reconcile tick; idempotent.
func (s *Store) EnforceCapacity(ctx context.Context) error {
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	var holders, pending int
	if err := s.DB.QueryRowContext(ctx, slotHoldersSQL).Scan(&holders); err != nil {
		return err
	}
	if err := s.DB.QueryRowContext(ctx, pendingCapacitySQL).Scan(&pending); err != nil {
		return err
	}
	need := holders - pending - cfg.MaxConcurrentAgents
	if need <= 0 {
		return nil
	}
	type candidate struct{ agentID, sessionID string }
	rows, err := s.DB.QueryContext(ctx, capacityCandidatesSQL)
	if err != nil {
		return err
	}
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
		if need == 0 {
			break
		}
		if len(s.liveChildNames(ctx, c.agentID)) > 0 {
			continue // an orchestrator coordinating live children keeps running
		}
		if _, err := s.RequestReplacement(ctx, c.agentID, ModeHandoff, capacityKeyPrefix+c.sessionID, ""); err != nil {
			s.logf("capacity: pause %s: %v", c.agentID, err)
			continue
		}
		need--
	}
	return nil
}

// admitsNow reports whether Admit would let a start now; used by Resume.
// Best effort: two concurrent resumes can both pass, and EnforceCapacity
// pauses the excess on the next tick.
func (s *Store) admitsNow(ctx context.Context, a Agent) (bool, error) {
	var ok bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		ok, err = s.Admit(ctx, tx, a.Role, a.RootItemID)
		return err
	})
	return ok, err
}
