package runtime

import (
	"context"
	"time"
)

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
