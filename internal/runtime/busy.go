package runtime

import "time"

// busyCeiling is how long a worker may read busy before the stuck alerts come
// back, so a tool that hangs forever is still reported.
const busyCeiling = 60 * time.Minute

// busyState is one session's in-memory busy evidence (no schema column, like
// the rest of the bookkeeping block: a restart forgets it and the alerts fall
// back to their plain timers).
type busyState struct {
	tools       int       // PreToolUse calls with no PostToolUse yet
	toolSince   time.Time // when tools went from 0 to 1
	leaderSince time.Time // first time the grok leader said "working"; zero when idle
}

// ToolStarted records a PreToolUse: the worker is inside a tool call.
func (s *Store) ToolStarted(sessionID string) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.busy == nil {
		s.busy = map[string]*busyState{}
	}
	b := s.busy[sessionID]
	if b == nil {
		b = &busyState{}
		s.busy[sessionID] = b
	}
	if b.tools == 0 {
		b.toolSince = s.Now()
	}
	b.tools++
}

// ToolFinished records a PostToolUse.
func (s *Store) ToolFinished(sessionID string) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if b := s.busy[sessionID]; b != nil && b.tools > 0 {
		b.tools--
	}
}

// TurnEnded clears the open tool calls: the turn is over, so a PostToolUse that
// never came (a cancelled tool) must not read busy forever.
func (s *Store) TurnEnded(sessionID string) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if b := s.busy[sessionID]; b != nil {
		b.tools = 0
	}
}

// BusySince reports whether the session is busy, from an open tool call or the
// grok leader's "working" push, and since when (the earliest evidence).
func (s *Store) BusySince(sessionID string) (time.Time, bool) {
	leader := false
	for _, ad := range s.Adapters {
		if lb, ok := ad.(interface{ LeaderBusy(sessionID string) bool }); ok && lb.LeaderBusy(sessionID) {
			leader = true
			break
		}
	}
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	b := s.busy[sessionID]
	if b == nil {
		if !leader {
			return time.Time{}, false
		}
		if s.busy == nil {
			s.busy = map[string]*busyState{}
		}
		b = &busyState{}
		s.busy[sessionID] = b
	}
	if leader && b.leaderSince.IsZero() {
		b.leaderSince = s.Now()
	} else if !leader {
		b.leaderSince = time.Time{}
	}
	var since time.Time
	if b.tools > 0 {
		since = b.toolSince
	}
	if !b.leaderSince.IsZero() && (since.IsZero() || b.leaderSince.Before(since)) {
		since = b.leaderSince
	}
	return since, !since.IsZero()
}

// busyWithinCeiling is the alert gate: busy, and not for so long that a hang is
// the likelier reading.
func (s *Store) busyWithinCeiling(sessionID string) bool {
	since, ok := s.BusySince(sessionID)
	return ok && s.Now().Sub(since) < busyCeiling
}
