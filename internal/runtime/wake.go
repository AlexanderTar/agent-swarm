package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

const wakeGap = 5 * time.Second
const pasteDelay = 20 * time.Second
const controlPasteDelay = 5 * time.Second
const pasteRetry = 30 * time.Second
const maxPasteAttempts = 10

// wakeRow is one live session with at least one pending immediate message,
// enough to decide the §11.3 wake order for it.
type wakeRow struct {
	SessionID, AgentID, AgentName, ItemKey, TmuxName, ProviderID string
	Kind                                                         AgentKind
	Pending                                                      int
	HasControl                                                   bool
	OldestMessageAt                                              time.Time
	LastSeenAt, LastWakeAt                                       *time.Time
	LastPasteAttemptAt                                           *time.Time
	StartedAt                                                    time.Time
	PaneCommand                                                  string
	PasteAttempts                                                int
	NativeTried                                                  bool
}

// wakeCandidates loads every live session with at least one pending
// immediate message, joined against the current panes for PaneCommand.
func (s *Store) wakeCandidates(ctx context.Context) ([]wakeRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.agent_id, a.name, i.key, ses.tmux_name,
		COALESCE(ses.provider_session_id, ''), a.kind, ses.last_seen_at, ses.last_wake_at, ses.started_at,
		(SELECT COUNT(*) FROM messages m WHERE m.to_agent_id = a.id AND m.state = 'pending'),
		(SELECT COUNT(*) FROM messages m WHERE m.to_agent_id = a.id AND m.state = 'pending' AND m.kind = 'control'),
		(SELECT MIN(m.created_at) FROM messages m
			WHERE m.to_agent_id = a.id AND m.state = 'pending' AND m.wake_class = 'immediate')
		FROM sessions ses JOIN agents a ON a.id = ses.agent_id JOIN items i ON i.id = a.item_id
		WHERE ses.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wakeRow
	for rows.Next() {
		var r wakeRow
		var kind string
		var lastSeen, lastWake sql.NullInt64
		var startedAt int64
		var hasControlCount int
		var oldest sql.NullInt64
		if err := rows.Scan(&r.SessionID, &r.AgentID, &r.AgentName, &r.ItemKey, &r.TmuxName,
			&r.ProviderID, &kind, &lastSeen, &lastWake, &startedAt, &r.Pending, &hasControlCount, &oldest); err != nil {
			return nil, err
		}
		if !oldest.Valid {
			continue // nothing immediate to wake for
		}
		r.Kind = AgentKind(kind)
		r.HasControl = hasControlCount > 0
		r.OldestMessageAt = db.FromMillis(oldest.Int64)
		r.StartedAt = db.FromMillis(startedAt)
		if lastSeen.Valid {
			t := db.FromMillis(lastSeen.Int64)
			r.LastSeenAt = &t
		}
		if lastWake.Valid {
			t := db.FromMillis(lastWake.Int64)
			r.LastWakeAt = &t
			r.NativeTried = true
		}
		r.PasteAttempts, r.LastPasteAttemptAt = s.getPasteAttempts(r.SessionID)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	panes, err := s.Tmux.Panes(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]string{}
	for _, p := range panes {
		byName[p.Session] = p.Command
	}
	for i := range out {
		out[i].PaneCommand = byName[out[i].TmuxName]
	}
	return out, nil
}

// WakeDue wakes every live session with a pending immediate message, in the
// §11.3 order. The daemon runs it every 5 s.
func (s *Store) WakeDue(ctx context.Context) error {
	rows, err := s.wakeCandidates(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.LastWakeAt != nil && s.Now().Sub(*r.LastWakeAt) < wakeGap {
			continue
		}
		notice := PendingNotice(r.Pending, r.AgentName, r.ItemKey)
		if r.HasControl {
			notice = ControlNotice(r.AgentName, r.ItemKey)
		}
		ad, ok := s.Adapters[r.Kind]
		if !ok {
			continue
		}
		// 1. native wake, once per message batch
		if !r.NativeTried {
			delivered, err := ad.Wake(ctx, adapter.WakeTarget{SessionID: r.SessionID,
				ProviderSessionID: r.ProviderID, TmuxName: r.TmuxName, Notice: notice})
			if err != nil {
				s.logf("wake: native wake for %s: %v", r.AgentName, err)
			}
			if delivered {
				if err := s.markWoken(ctx, r.SessionID, true); err != nil {
					return err
				}
				continue // a sync must follow; if it does not, the next pass pastes
			}
		}
		// 2. hooks need nothing.
		// 3. idle paste, after the delay and only when every condition holds.
		delay := pasteDelay
		if r.HasControl {
			delay = controlPasteDelay
		}
		if s.Now().Sub(r.OldestMessageAt) < delay {
			continue
		}
		if r.LastSeenAt != nil && s.Now().Sub(*r.LastSeenAt) < delay {
			continue
		}
		if r.PasteAttempts > 0 && r.LastPasteAttemptAt != nil && s.Now().Sub(*r.LastPasteAttemptAt) < pasteRetry {
			continue
		}
		if err := s.tryPaste(ctx, ad, r); err != nil {
			return err
		}
	}
	return nil
}

// isShell rejects a bare shell prompt from the idle-paste check (I2).
var shellNames = regexp.MustCompile(`^(sh|bash|zsh|fish|login)$`)

func isShell(cmd string) bool { return shellNames.MatchString(cmd) }

func matchesAny(patterns []*regexp.Regexp, s string) bool {
	for _, p := range patterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

func (s *Store) alreadyNotifiedUndeliverable(ctx context.Context, agentID string, since time.Time) (bool, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
		WHERE agent_id = ? AND kind = 'agent.undeliverable' AND created_at >= ?`,
		agentID, db.Millis(since)).Scan(&count)
	return count > 0, err
}

// tryPaste checks the three §11.3 conditions and pastes the idle token. The
// daemon never logs a full process listing: other tools' bearer tokens show up
// there (P0-4).
func (s *Store) tryPaste(ctx context.Context, ad adapter.Adapter, r wakeRow) error {
	ok := false
	if matchesAny(ad.ProcessNames(), r.PaneCommand) && !isShell(r.PaneCommand) {
		capture, err := s.Tmux.Capture(ctx, r.TmuxName, 15)
		if err != nil {
			return err
		}
		ok = ad.Idle(capture)
	}
	if ok {
		if err := s.Tmux.PasteLine(ctx, r.TmuxName, IdleToken); err != nil {
			return err
		}
		return s.markWoken(ctx, r.SessionID, false)
	}
	attempts := r.PasteAttempts + 1
	if err := s.recordPasteAttempt(ctx, r.SessionID, attempts, s.Now()); err != nil {
		return err
	}
	if attempts >= maxPasteAttempts {
		since := r.OldestMessageAt
		if r.StartedAt.After(since) {
			since = r.StartedAt
		}
		already, err := s.alreadyNotifiedUndeliverable(ctx, r.AgentID, since)
		if err != nil {
			return err
		}
		if !already {
			if err := s.notify(ctx, nil, NotifyInput{Kind: "agent.undeliverable", AgentName: r.AgentName,
				ItemKey: r.ItemKey, Args: map[string]string{"name": r.AgentName, "N": strconv.Itoa(r.Pending)}}); err != nil {
				return err
			}
			var recorded int
			_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
				WHERE agent_id = ? AND kind = 'agent.undeliverable' AND created_at >= ?`,
				r.AgentID, db.Millis(since)).Scan(&recorded)
			if recorded == 0 {
				_, _ = s.DB.ExecContext(ctx, `INSERT INTO notifications
					(id, level, kind, title, body, agent_id, item_id, dedup_key, created_at)
					VALUES (?, 'attention', 'agent.undeliverable', 'Couldn''t deliver messages', 'Couldn''t deliver messages', ?, (SELECT id FROM items WHERE key = ?), ?, ?)`,
					ids.New("ntf"), r.AgentID, r.ItemKey, fmt.Sprintf("agent.undeliverable:%s:", r.AgentName), db.Millis(s.Now()))
			}
		}
	}
	return nil
}

// markWoken records a successful wake (native or pasted) and resets the paste
// backoff, so a later, unrelated message batch starts its own count from zero.
func (s *Store) markWoken(ctx context.Context, sessionID string, native bool) error {
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET last_wake_at = ? WHERE id = ?`,
		db.Millis(s.Now()), sessionID); err != nil {
		return err
	}
	s.recordPasteAttemptMem(sessionID, 0, time.Time{})
	return nil
}

func (s *Store) recordPasteAttempt(ctx context.Context, sessionID string, n int, at time.Time) error {
	s.recordPasteAttemptMem(sessionID, n, at)
	return nil
}

func (s *Store) recordPasteAttemptMem(sessionID string, n int, at time.Time) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.pasteAttempts == nil {
		s.pasteAttempts = map[string]int{}
	}
	if s.lastPasteAttemptAt == nil {
		s.lastPasteAttemptAt = map[string]time.Time{}
	}
	s.pasteAttempts[sessionID] = n
	if n > 0 {
		s.lastPasteAttemptAt[sessionID] = at
	} else {
		delete(s.lastPasteAttemptAt, sessionID)
	}
}

func (s *Store) getPasteAttempts(sessionID string) (int, *time.Time) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	n := s.pasteAttempts[sessionID]
	t, ok := s.lastPasteAttemptAt[sessionID]
	if !ok || t.IsZero() {
		return n, nil
	}
	return n, &t
}

// PublishWake is the adapter.Deps.PublishWake seam: it fans a notice out to
// every channel SubscribeWake handed out for sessionID (Task 34's SSE route
// subscribes here for the claude bridge).
func (s *Store) PublishWake(ctx context.Context, sessionID, notice string) error {
	s.bookkeepingMu.Lock()
	subs := append([]chan string{}, s.wakeSubs[sessionID]...)
	s.bookkeepingMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- notice:
		default:
		}
	}
	return nil
}

// SubscribeWake returns a buffered channel of notices for sessionID and an
// idempotent unsubscribe func.
func (s *Store) SubscribeWake(sessionID string) (<-chan string, func()) {
	ch := make(chan string, 8)
	s.bookkeepingMu.Lock()
	if s.wakeSubs == nil {
		s.wakeSubs = map[string][]chan string{}
	}
	s.wakeSubs[sessionID] = append(s.wakeSubs[sessionID], ch)
	s.bookkeepingMu.Unlock()
	return ch, func() {
		s.bookkeepingMu.Lock()
		defer s.bookkeepingMu.Unlock()
		subs := s.wakeSubs[sessionID]
		for i, c := range subs {
			if c == ch {
				subs = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(subs) == 0 {
			delete(s.wakeSubs, sessionID)
		} else {
			s.wakeSubs[sessionID] = subs
		}
	}
}

// WakeLoop runs WakeDue every `every` until ctx is cancelled.
func (s *Store) WakeLoop(ctx context.Context, every time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.after(every):
		}
		if err := s.WakeDue(ctx); err != nil {
			s.logf("wake: %v", err)
		}
	}
}

// WakeOnQuotaReset wakes all live or waiting sessions belonging to kind that have
// not already been woken for this cutoff cycle (last_wake_at < cutoff).
func (s *Store) WakeOnQuotaReset(ctx context.Context, kind AgentKind, cutoff time.Time) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.tmux_name, ses.state, ses.waiting
		FROM sessions ses JOIN agents a ON a.id = ses.agent_id
		WHERE a.kind = ? AND ses.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')
		AND (ses.last_wake_at IS NULL OR ses.last_wake_at < ?)`,
		string(kind), db.Millis(cutoff))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	ad, ok := s.Adapters[kind]
	woken := 0
	for rows.Next() {
		var sessionID, tmuxName, state string
		var waiting bool
		if err := rows.Scan(&sessionID, &tmuxName, &state, &waiting); err != nil {
			return woken, err
		}
		// Attempt native wake or paste idle token if pane is idle
		if ok {
			delivered, _ := ad.Wake(ctx, adapter.WakeTarget{SessionID: sessionID, TmuxName: tmuxName, Notice: "[swarm] Quota reset window passed. Resuming."})
			if delivered {
				s.markWoken(ctx, sessionID, true)
				woken++
				continue
			}
		}
		// Fallback to idle paste if pane is alive
		capture, err := s.Tmux.Capture(ctx, tmuxName, 15)
		if err == nil && ok && ad.Idle(capture) {
			if err := s.Tmux.PasteLine(ctx, tmuxName, IdleToken); err == nil {
				s.markWoken(ctx, sessionID, false)
				woken++
			}
		}
	}
	return woken, rows.Err()
}

