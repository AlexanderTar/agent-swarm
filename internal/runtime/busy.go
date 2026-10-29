package runtime

import (
	"context"
	"database/sql"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/adapter"
)

// busyCeiling is how long a worker may read busy before the stuck alerts come
// back, so a tool that hangs forever is still reported.
const busyCeiling = 60 * time.Minute

// busyShownAfter is how long a worker stays "running" on the board before it
// reads busy: a 2 s tool call or a fresh grok turn is ordinary work, so the busy
// label (and its flicker) waits until the worker would otherwise look stuck.
const busyShownAfter = 5 * time.Minute

// busyState is one session's in-memory busy evidence (no schema column, like
// the rest of the bookkeeping block: a restart forgets it and the alerts fall
// back to their plain timers).
type busyState struct {
	tools       int       // PreToolUse calls with no PostToolUse yet
	toolSince   time.Time // when tools went from 0 to 1
	leaderSince time.Time // first poll that saw the grok leader "working" (up to one tick after it started); zero when idle
	paneSince   time.Time // first reconcile tick whose pane showed the adapter's busy footer and no dialog; zero otherwise
	shown       bool      // busy_since was last published as shown on the board (agent.changed)
	waiting     int       // pending messages as of that publish, shown with the busy label
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

// ToolFinished records a PostToolUse. Claude's PostToolUseFailure is not a
// registered hook, so a failed tool keeps its call open until the turn ends
// (TurnEnded) or the ceiling; the worst case is a busy label on a turn that is
// in fact still running.
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

// paneBusy reports whether a capture shows the adapter's busy footer with no
// dialog on screen: a prompt or card means the worker waits for a person.
func paneBusy(ad adapter.Adapter, capture string) bool {
	bz := ad.Busy()
	plain := adapter.StripANSI(capture)
	if bz == nil || !bz.MatchString(plain) {
		return false
	}
	for _, m := range ad.PromptPatterns() {
		if m.Match != nil && m.Match.MatchString(plain) && (m.Exclude == nil || !m.Exclude.MatchString(plain)) {
			return false
		}
	}
	if pr, ok := ad.(interface{ PasteReady(capture string) bool }); ok && !pr.PasteReady(capture) {
		return false
	}
	return true
}

// samplePaneBusy records what the pane says: the fallback busy evidence for kinds
// without tool hooks (muse), and a cross-check for the rest. The reconcile tick
// calls it with the capture it already took.
func (s *Store) samplePaneBusy(sessionID string, ad adapter.Adapter, capture string) {
	busy := paneBusy(ad, capture)
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	b := s.busy[sessionID]
	if b == nil {
		if !busy {
			return
		}
		if s.busy == nil {
			s.busy = map[string]*busyState{}
		}
		b = &busyState{}
		s.busy[sessionID] = b
	}
	switch {
	case !busy:
		b.paneSince = time.Time{}
	case b.paneSince.IsZero():
		b.paneSince = s.Now()
	}
}

// busyAfterPaneLook is the wake tick's own look at the pane, taken only when it is
// about to call a worker unreachable (the reconcile sample may be seconds old, or
// not taken yet): it refreshes the pane evidence and reports busy under the ceiling.
func (s *Store) busyAfterPaneLook(ctx context.Context, r wakeRow) bool {
	if ad, ok := s.Adapters[r.Kind]; ok {
		if capture, err := s.Tmux.Capture(ctx, r.TmuxName, 15); err == nil {
			s.samplePaneBusy(r.SessionID, ad, capture)
		}
	}
	return s.busyWithinCeiling(r.SessionID)
}

// BusySince reports whether the session is busy, from an open tool call or the
// grok leader's "working" push, and since when (the earliest evidence). With
// neither, the last pane sample's busy footer counts, since the first sample that saw it.
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
	if since.IsZero() {
		since = b.paneSince
	}
	return since, !since.IsZero()
}

// BusyNow is BusySince under the ceiling: what the alerts and the board treat as
// busy rather than a likely hang.
func (s *Store) BusyNow(sessionID string) (time.Time, bool) {
	since, ok := s.BusySince(sessionID)
	if !ok || s.Now().Sub(since) >= busyCeiling {
		return time.Time{}, false
	}
	return since, true
}

func (s *Store) busyWithinCeiling(sessionID string) bool {
	_, ok := s.BusyNow(sessionID)
	return ok
}

// BusyShown is BusyNow once the worker has been busy past busyShownAfter: what
// the board shows as "busy" instead of plain running.
func (s *Store) BusyShown(sessionID string) (time.Time, bool) {
	since, ok := s.BusyNow(sessionID)
	if !ok || s.Now().Sub(since) < busyShownAfter {
		return time.Time{}, false
	}
	return since, true
}

// publishBusyChange raises agent.changed when the session crosses into or out of
// the shown busy state, or its pending-message count changes while shown, so the
// board (which refetches agents on that event only) keeps the label current
// without waiting for an unrelated refetch. The remembered state moves only
// after the event is stored, so a failed publish is retried on the next tick.
func (s *Store) publishBusyChange(ctx context.Context, r liveRow) error {
	_, shown := s.BusyShown(r.SessionID)
	waiting := 0
	if shown {
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id =
			(SELECT agent_id FROM sessions WHERE id = ?) AND state = 'pending'`, r.SessionID).Scan(&waiting); err != nil {
			return err
		}
	}
	s.bookkeepingMu.Lock()
	b := s.busy[r.SessionID]
	changed := b != nil && (b.shown != shown || b.waiting != waiting)
	s.bookkeepingMu.Unlock()
	if !changed {
		return nil
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error { return s.publishAgentChanged(ctx, tx, r.AgentName, r.RootItemID) }); err != nil {
		return err
	}
	s.bookkeepingMu.Lock()
	b.shown, b.waiting = shown, waiting
	s.bookkeepingMu.Unlock()
	return nil
}
