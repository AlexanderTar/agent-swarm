package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
)

// usageLimitUnnamedReset is how long a kind stays exhausted when its limit line
// names no reset time (or one that does not parse). Short, so a wrong guess
// costs an hour on the fallback rather than a day.
const usageLimitUnnamedReset = time.Hour

// kindExhausted is the one question every usage-gate caller asks: is kind out
// of usage right now. Either the usage poller confirms it (Store.Usage) or a
// session's pane showed the kind's usage-limit line (noteUsageLimit), which
// needs no poller and no meter. The poller alone misses a cap it does not read
// as 100% (BUG-110: muse's weekly cap read 99% while its pane said "Usage limit
// reached"), so the pane is the primary signal.
func (s *Store) kindExhausted(ctx context.Context, kind AgentKind) bool {
	if s.Usage != nil && s.Usage.Exhausted(ctx, kind) {
		return true
	}
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	return s.usageLimits[kind].After(s.Now())
}

// noteUsageLimit marks kind exhausted until resetsAt. In memory like the rest
// of the bookkeeping block: a daemon restart forgets it, and the next session of
// that kind shows its limit line again and is ended and re-admitted the same way.
func (s *Store) noteUsageLimit(kind AgentKind, resetsAt time.Time) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.usageLimits == nil {
		s.usageLimits = map[AgentKind]time.Time{}
	}
	s.usageLimits[kind] = resetsAt
}

// UsageLimitReset is one kind's pane-noted usage limit and when it lifts.
type UsageLimitReset struct {
	Kind     AgentKind
	ResetsAt time.Time
}

// UsageLimitResets lists every pane-noted limit so the daemon's quota-reset loop
// can call WakeOnQuotaReset for it: relays held while the kind read exhausted
// (holdIfExhausted) are flushed only there, and the usage poller never saw a
// pane-only limit's reset.
func (s *Store) UsageLimitResets() []UsageLimitReset {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	out := make([]UsageLimitReset, 0, len(s.usageLimits))
	for kind, at := range s.usageLimits {
		out = append(out, UsageLimitReset{Kind: kind, ResetsAt: at})
	}
	return out
}

// endIfUsageLimited ends a session whose pane shows its kind's usage-limit line
// (adapter.UsageLimiter): the kind is marked exhausted until the line's reset,
// and the session fails with a reason naming the limit and what happens next.
// Failed, not cancelled, is what sends a workflow run to auto-retry, and Retry
// re-resolves the kind through the same fallback a spawn uses. It reports
// whether it ended the session. The caller has already established the
// session has no checkpoint yet; a session that has one may merely print the
// words.
func (s *Store) endIfUsageLimited(ctx context.Context, a Agent, ses Session, ad adapter.Adapter, capture string) (bool, error) {
	ul, ok := ad.(adapter.UsageLimiter)
	if !ok {
		return false, nil
	}
	_, resetsAt, ok := ul.UsageLimit(capture, s.Now())
	if !ok {
		return false, nil
	}
	if resetsAt.IsZero() {
		resetsAt = s.Now().Add(usageLimitUnnamedReset)
	}
	s.noteUsageLimit(a.Kind, resetsAt)
	reason := fmt.Sprintf("%s usage limit reached, resets %s.", a.Kind.Display(), resetsAt.Local().Format("Jan 2 15:04"))
	if fb, _, _, substituted, err := s.resolveUsageFallback(ctx, a.Kind, a.Model, a.Effort); err != nil {
		reason += " " + err.Error()
	} else if substituted {
		reason += " A retry runs on " + fb.Display() + " (the usage fallback agent)."
	}
	s.logf("usage limit: %s: %s", a.Name, reason)
	if err := s.failSessionWith(ctx, a, ses, lastLines(capture, 40), reason); err != nil {
		return true, err
	}
	// A workflow step is re-admitted now (heal to failed, auto-retry through the
	// fallback) rather than at the 30 s stall scan, same as a crash.
	var workflowID string
	err := s.DB.QueryRowContext(ctx, `SELECT workflow_id FROM workflow_runs WHERE agent_id = ?
		ORDER BY round DESC, created_at DESC LIMIT 1`, a.ID).Scan(&workflowID)
	if err == nil {
		if err := s.advance(context.WithoutCancel(ctx), workflowID); err != nil {
			s.logf("usage limit: advance %s: %v", workflowID, err)
		}
	}
	return true, nil
}

// endIfUsageLimitedLive is reconcile's entry: only for a session with no
// checkpoint yet this attempt, so the lookups run only once the pane matches.
func (s *Store) endIfUsageLimitedLive(ctx context.Context, r liveRow, ad adapter.Adapter, capture string) (bool, error) {
	ul, ok := ad.(adapter.UsageLimiter)
	if !ok {
		return false, nil
	}
	if _, _, ok := ul.UsageLimit(capture, s.Now()); !ok {
		return false, nil
	}
	if acked, err := s.hasAnyCheckpoint(ctx, r.AgentID, r.Attempt); err != nil || acked {
		return false, err
	}
	a, err := s.agentByID(ctx, r.AgentID)
	if err != nil {
		return false, err
	}
	ses, err := s.LatestSession(ctx, r.AgentID)
	if err != nil {
		return false, err
	}
	return s.endIfUsageLimited(ctx, a, ses, ad, capture)
}
