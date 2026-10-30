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

// stretchIdleGap is how long a turn may stay ended before the next tool call
// starts a new working stretch. A Stop followed seconds later by an inbox wake
// keeps the nudge clock; a longer idle starts it over, so hours of silence
// before that wake are not reminded.
const stretchIdleGap = 5 * time.Minute

// busyState is one session's in-memory busy evidence (no schema column, like
// the rest of the bookkeeping block: a restart forgets it and the alerts fall
// back to their plain timers).
type busyState struct {
	tools       int       // PreToolUse calls with no PostToolUse yet
	toolSince   time.Time // when tools went from 0 to 1
	turnSince   time.Time // first ToolStarted of the current working stretch; zero before any tool call
	turnEndedAt time.Time // earliest TurnEnded since the last tool call; zero while a turn is open
	idleAt      time.Time // earliest time tools dropped to 0 since the last ToolStarted; zero while a call is open
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
	// A short gap after the turn went idle is still this stretch. A long one,
	// or the first tool call, starts the clock here.
	if b.turnSince.IsZero() || b.stretchIdle(s.Now()) {
		b.turnSince = s.Now()
	}
	b.turnEndedAt = time.Time{}
	b.idleAt = time.Time{}
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
		if b.tools == 0 {
			b.noteIdle(s.Now())
		}
	}
}

// TurnEnded clears the open tool calls: the turn is over, so a PostToolUse that
// never came (a cancelled tool) must not read busy forever. It records when the
// turn ended, once: a wake fires UserPromptSubmit and SessionStart through here
// again (hook/handler.go), and moving the mark up to that wake would hide the
// idle gap. The next ToolStarted keeps the working stretch when the gap since
// the earliest idle mark is shorter than stretchIdleGap, and starts a new one
// when it is not. A turn cut off with no Stop (a usage limit; Claude registers
// no StopFailure hook) gets its first TurnEnded only at that wake. The idle
// mark from the last tool drop stays where it is, so the gap is the hours
// since the tool, not the seconds since the wake.
func (s *Store) TurnEnded(sessionID string) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if b := s.busy[sessionID]; b != nil {
		if b.tools > 0 {
			b.noteIdle(s.Now())
		}
		b.tools = 0
		if b.turnEndedAt.IsZero() {
			b.turnEndedAt = s.Now()
		}
	}
}

// noteIdle keeps the earliest moment tools sat at 0 since the last tool call.
func (b *busyState) noteIdle(now time.Time) {
	if b.idleAt.IsZero() {
		b.idleAt = now
	}
}

// stretchIdle reports whether the turn has ended and stayed idle for
// stretchIdleGap. A turn that has not ended is still this stretch: a think
// with tools at 0, however long, does not start a new clock. Once the turn
// has ended, the idle started at the earlier of the turn end and the moment
// tools last dropped to 0. A wake that records the end late therefore does
// not hide a gap that began when the last tool finished.
func (b *busyState) stretchIdle(now time.Time) bool {
	if b.turnEndedAt.IsZero() {
		return false
	}
	mark := b.turnEndedAt
	if !b.idleAt.IsZero() && b.idleAt.Before(mark) {
		mark = b.idleAt
	}
	return now.Sub(mark) >= stretchIdleGap
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

// leaderState scans whether the grok leader is working (outside the lock), then
// under bookkeepingMu applies that working/idle transition and returns the
// session's busyState. The caller releases the lock with the returned function.
// A session with no state and an idle leader stays absent. Both BusySince and
// workingSince go through this, so the scan and the transition cannot drift.
func (s *Store) leaderState(sessionID string) (*busyState, func()) {
	leader := false
	for _, ad := range s.Adapters {
		if lb, ok := ad.(interface{ LeaderBusy(sessionID string) bool }); ok && lb.LeaderBusy(sessionID) {
			leader = true
			break
		}
	}
	s.bookkeepingMu.Lock()
	b := s.busy[sessionID]
	if b == nil {
		if !leader {
			return nil, s.bookkeepingMu.Unlock
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
	return b, s.bookkeepingMu.Unlock
}

// BusySince reports whether the session is busy, from an open tool call or the
// grok leader's "working" push, and since when (the earliest evidence). With
// neither, the last pane sample's busy footer counts, since the first sample that saw it.
func (s *Store) BusySince(sessionID string) (time.Time, bool) {
	b, unlock := s.leaderState(sessionID)
	defer unlock()
	if b == nil {
		return time.Time{}, false
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

// workingSince reports when the current working stretch started: the first tool
// call of this stretch for kinds with tool hooks, else the leader or pane
// fallback (both already mean "busy stretch since"). A ToolFinished does not
// restart it, so sequential short calls in one turn keep one clock, and a
// think with tools at 0 keeps it too: the stretch ends only after the turn
// has ended and stretchIdleGap has passed since the earliest idle mark.
// A stretch already idle that long does not anchor the window: leader or pane
// evidence on the wake is the new stretch, so a busy pane before the first
// tool call is not nudged for the hours before it. A UserPromptSubmit or
// SessionStart also ends the turn (hook/handler.go) and does not move a mark
// that is already set.
func (s *Store) workingSince(sessionID string) (time.Time, bool) {
	b, unlock := s.leaderState(sessionID)
	defer unlock()
	if b == nil {
		return time.Time{}, false
	}
	if !b.turnSince.IsZero() && !b.stretchIdle(s.Now()) {
		return b.turnSince, true
	}
	since := b.leaderSince
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
