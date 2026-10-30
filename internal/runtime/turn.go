package runtime

import (
	"context"
	"database/sql"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/adapter"
	"github.com/AlexanderTar/swarmrun/internal/db"
	"github.com/AlexanderTar/swarmrun/internal/kinds"
)

// HookTurn is one session's hook verdict. Known is false until a hook or a
// daemon paste has opened or closed the turn. LastHook is memory only: a
// restart keeps Open and CloseEvent and zeroes LastHook.
type HookTurn struct {
	Known      bool
	Open       bool
	Since      time.Time
	CloseEvent string
	LastHook   time.Time
}

// turnRec is the in-memory copy. loaded marks a row we have already read,
// including "no hook state yet", so a restart is the only time we hit the DB.
type turnRec struct {
	known      bool
	open       bool
	since      time.Time
	closeEvent string
	lastHook   time.Time
}

// hookEndsTurn matches hook.endsTurn. runtime cannot import hook.
func hookEndsTurn(ev string) bool {
	return ev == "Stop" || ev == "StopCancelled" || ev == "StopFailure"
}

// NoteHookTurn applies one normalized hook event. A blocked Stop keeps the
// turn open. cursor's UserPromptSubmit (beforeSubmitPrompt) is not a turn
// start. grok's shutdown Stop is not a turn end.
func (s *Store) NoteHookTurn(sessionID string, kind kinds.AgentKind, event string, blocked bool, reason string) {
	now := s.now()
	if event == "Stop" && kind == Grok && reason == "shutdown" {
		s.touchLastHook(sessionID, now)
		return
	}
	switch {
	case hookEndsTurn(event) && !blocked:
		s.setTurn(sessionID, false, event, now)
	case event == "PreToolUse" && !blocked:
		s.setTurn(sessionID, true, "", now)
	case event == "UserPromptSubmit" && kind != Cursor:
		s.setTurn(sessionID, true, "", now)
	case event == "SessionStart":
		s.setTurn(sessionID, true, "", now)
	default:
		s.touchLastHook(sessionID, now)
	}
}

// MarkPromptSubmitted opens the turn after a pane delivery that submits a
// prompt, before that prompt's hook lands. It also refreshes LastHook so a
// just-woken session is not treated as a lost Stop while its pane still
// looks idle. A tool call still open on a stale turn is marked idle first:
// the wake's TurnEnded would otherwise see this refresh and treat the hours
// since the call as still working.
func (s *Store) MarkPromptSubmitted(sessionID string) {
	s.noteStaleOpenCall(sessionID)
	s.setTurn(sessionID, true, "", s.now())
}

// noteStaleOpenCall records the nudge idle mark for a tool call still open
// when a paste is about to refresh LastHook. The mark is the call's start.
// A turn a Stop already closed is not open, so a long tool that ends
// normally is left for TurnEnded to mark at the Stop.
func (s *Store) noteStaleOpenCall(sessionID string) {
	now := s.now()
	ht := s.HookTurn(sessionID)
	if !turnOpenStale(ht, now) {
		return
	}
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	b := s.busy[sessionID]
	if b == nil || b.tools == 0 || b.toolSince.IsZero() {
		return
	}
	b.noteIdle(b.toolSince)
}

// hookTurnStale is how long an open turn may go without a hook before an idle
// pane counts as a lost Stop. 15 minutes covers a long tool call; past that,
// the pane is the fallback.
const hookTurnStale = 15 * time.Minute

// SessionIdle is the turn-over verdict. A hook close is idle. A hook open is
// not, unless the last hook is older than hookTurnStale and the pane is idle.
// No hook state falls back to the pane. grok LeaderBusy is never idle.
func (s *Store) SessionIdle(sessionID string, ad adapter.Adapter, capture string) bool {
	if lb, ok := ad.(interface{ LeaderBusy(sessionID string) bool }); ok && lb.LeaderBusy(sessionID) {
		return false
	}
	ht := s.HookTurn(sessionID)
	if !ht.Known {
		return ad.Idle(capture)
	}
	if !ht.Open {
		return true
	}
	stale := ht.LastHook.IsZero() || s.now().Sub(ht.LastHook) > hookTurnStale
	return stale && ad.Idle(capture)
}

// HookTurn reports the session's hook turn, loading it from the session row
// when this process has not seen the session yet. The fields are copied while
// holding bookkeepingMu: setTurn and touchLastHook write them under the same
// lock, so reading the shared record after the lock is released races.
func (s *Store) HookTurn(sessionID string) HookTurn {
	s.bookkeepingMu.Lock()
	rec := s.turns[sessionID]
	if rec == nil {
		s.bookkeepingMu.Unlock()
		s.loadTurn(sessionID)
		s.bookkeepingMu.Lock()
		rec = s.turns[sessionID]
	}
	defer s.bookkeepingMu.Unlock()
	if rec == nil || !rec.known {
		return HookTurn{}
	}
	return HookTurn{
		Known: true, Open: rec.open, Since: rec.since,
		CloseEvent: rec.closeEvent, LastHook: rec.lastHook,
	}
}

func (s *Store) turn(sessionID string) *turnRec {
	s.bookkeepingMu.Lock()
	rec := s.turns[sessionID]
	s.bookkeepingMu.Unlock()
	if rec != nil {
		return rec
	}
	return s.loadTurn(sessionID)
}

func (s *Store) loadTurn(sessionID string) *turnRec {
	rec := &turnRec{}
	if s.DB == nil {
		return rec
	}
	var open sql.NullInt64
	var since int64
	var ev string
	err := s.DB.QueryRowContext(context.Background(),
		`SELECT turn_open, turn_since, turn_close_event FROM sessions WHERE id = ?`, sessionID).
		Scan(&open, &since, &ev)
	if err != nil {
		return rec
	}
	if open.Valid {
		rec.known = true
		rec.open = open.Int64 != 0
		rec.since = db.FromMillis(since)
		rec.closeEvent = ev
	}
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.turns == nil {
		s.turns = map[string]*turnRec{}
	}
	if have := s.turns[sessionID]; have != nil {
		return have
	}
	s.turns[sessionID] = rec
	return rec
}

func (s *Store) touchLastHook(sessionID string, now time.Time) {
	rec := s.turn(sessionID)
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.turns == nil {
		s.turns = map[string]*turnRec{}
	}
	if have := s.turns[sessionID]; have != nil {
		rec = have
	}
	rec.lastHook = now
	s.turns[sessionID] = rec
}

// setTurn records an open or close. A repeat of the current state only
// refreshes LastHook; the row is written on a transition. turnMu is held
// across the memory transition and the row write, so a concurrent close and
// open persist in the same order memory took them: without it the row could
// end closed while memory is open, and a restart would reap a woken session
// mid-turn.
func (s *Store) setTurn(sessionID string, open bool, closeEvent string, now time.Time) {
	if open {
		closeEvent = ""
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	rec := s.turn(sessionID)
	s.bookkeepingMu.Lock()
	if s.turns == nil {
		s.turns = map[string]*turnRec{}
	}
	if have := s.turns[sessionID]; have != nil {
		rec = have
	}
	same := rec.known && rec.open == open && rec.closeEvent == closeEvent
	rec.lastHook = now
	if same {
		s.turns[sessionID] = rec
		s.bookkeepingMu.Unlock()
		return
	}
	rec.known = true
	rec.open = open
	rec.since = now
	rec.closeEvent = closeEvent
	s.turns[sessionID] = rec
	s.bookkeepingMu.Unlock()

	if s.DB == nil {
		return
	}
	if _, err := s.DB.ExecContext(context.Background(),
		`UPDATE sessions SET turn_open = ?, turn_since = ?, turn_close_event = ? WHERE id = ?`,
		boolToInt(open), db.Millis(now), closeEvent, sessionID); err != nil {
		s.logf("turn: persist %s: %v", sessionID, err)
	}
}
