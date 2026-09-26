package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
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
const undeliverableAfter = 5 * time.Minute

// wakeFailBase/wakeFailCap shape the unified retry backoff for failed wake
// attempts across both transports (native and tmux paste). Deterministic, no
// jitter, so tests pin exact ticks. Cap equals undeliverableAfter so the
// longest retry spacing coincides with the alert threshold.
const wakeFailBase = 5 * time.Second
const wakeFailCap = 5 * time.Minute

// backoffForFailures returns base * 2^n capped at cap (n = consecutive failures).
func backoffForFailures(n int) time.Duration {
	if n > 30 { // 5s is ~2^32ns; shifts past bit 62 could wrap small-positive
		return wakeFailCap
	}
	d := wakeFailBase << n
	if d <= 0 || d > wakeFailCap {
		return wakeFailCap
	}
	return d
}

// wakeRow is one live session with at least one pending immediate message,
// enough to decide the §11.3 wake order for it.
type wakeRow struct {
	SessionID, AgentID, AgentName, ItemKey, TmuxName, ProviderID string
	Kind                                                         AgentKind
	Model, Effort                                                string
	Pending                                                      int
	HasControl                                                   bool
	Handoff                                                      bool // a handoff operation is in flight: the control notice is HANDOFF, not PAUSE
	OldestMessageAt, NewestPendingAt                             time.Time
	LastSeenAt, LastWakeAt                                       *time.Time
	LastPasteAttemptAt                                           *time.Time
	StartedAt                                                    time.Time
	PaneCommand                                                  string
	PasteAttempts                                                int // only spaces paste retries; the alert is database-derived (undeliverableAfter)
	NativeTried                                                  bool
}

// wakeCandidates loads every live session with at least one pending
// immediate message, joined against the current panes for PaneCommand.
func (s *Store) wakeCandidates(ctx context.Context) ([]wakeRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.agent_id, a.name, i.key, ses.tmux_name,
		COALESCE(ses.provider_session_id, ''), a.kind, a.model, COALESCE(a.effort, ''), ses.last_seen_at, ses.last_wake_at, ses.started_at,
		(SELECT COUNT(*) FROM messages m WHERE m.to_agent_id = a.id AND m.state = 'pending'),
		(SELECT COUNT(*) FROM messages m WHERE m.to_agent_id = a.id AND m.state = 'pending' AND m.kind = 'control'),
		(SELECT MIN(m.created_at) FROM messages m
			WHERE m.to_agent_id = a.id AND m.state = 'pending' AND m.wake_class = 'immediate'),
		(SELECT MAX(m.created_at) FROM messages m
			WHERE m.to_agent_id = a.id AND m.state = 'pending' AND m.wake_class = 'immediate'),
		EXISTS (SELECT 1 FROM agent_operations o WHERE o.agent_id = a.id AND o.mode = 'handoff'
			AND o.phase IN ('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting'))
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
		var oldest, newest sql.NullInt64
		if err := rows.Scan(&r.SessionID, &r.AgentID, &r.AgentName, &r.ItemKey, &r.TmuxName,
			&r.ProviderID, &kind, &r.Model, &r.Effort, &lastSeen, &lastWake, &startedAt, &r.Pending, &hasControlCount, &oldest, &newest, &r.Handoff); err != nil {
			return nil, err
		}
		if !oldest.Valid {
			continue // nothing immediate to wake for
		}
		r.Kind = AgentKind(kind)
		r.HasControl = hasControlCount > 0
		r.OldestMessageAt = db.FromMillis(oldest.Int64)
		r.NewestPendingAt = db.FromMillis(newest.Int64)
		r.StartedAt = db.FromMillis(startedAt)
		if lastSeen.Valid {
			t := db.FromMillis(lastSeen.Int64)
			r.LastSeenAt = &t
		}
		if lastWake.Valid {
			t := db.FromMillis(lastWake.Int64)
			r.LastWakeAt = &t
			// §11.3 step 1 is "native wake, once per message BATCH", so
			// NativeTried must mean "already natively woken FOR THIS batch",
			// not "this session was woken at some point in its life".
			// last_wake_at is set by markWoken after a native wake OR a paste
			// and is never cleared, so deriving NativeTried from its mere
			// presence disabled native wake forever after a session's first
			// wake -- every later batch fell straight through to the paste
			// fallback (the 2026-09-23 orchestrator incident). A pending
			// message newer than the last wake is by definition a batch that
			// wake has not covered yet. This is deliberately the exact mirror
			// of WakeDue's cooldown escape clause below, so the two cannot
			// drift apart.
			r.NativeTried = !r.NewestPendingAt.After(t)
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
// §11.3 order. The daemon runs it every 5 s. Sessions whose kind is confirmed
// out of usage are skipped entirely (no native wake, no paste, no
// undeliverable escalation): pinging a quota-dead session only feeds the
// pileup the quota-reset flush then has to digest.
func (s *Store) WakeDue(ctx context.Context) error {
	rows, err := s.wakeCandidates(ctx)
	if err != nil {
		return err
	}
	exhausted := map[AgentKind]bool{}
	isExhausted := func(k AgentKind) bool {
		e, ok := exhausted[k]
		if !ok {
			e = s.Usage != nil && s.Usage.Exhausted(ctx, k)
			exhausted[k] = e
		}
		return e
	}
	for _, r := range rows {
		if isExhausted(r.Kind) {
			continue
		}
		if s.Now().Sub(r.OldestMessageAt) >= undeliverableAfter {
			if err := s.raiseUndeliverable(ctx, r); err != nil {
				return err
			}
		}
		// After a wake (native or paste) give the agent pasteRetry to respond,
		// unless a pending immediate message is newer than that wake.
		cool := pasteRetry
		if r.HasControl {
			cool = wakeGap
		}
		if r.LastWakeAt != nil && s.Now().Sub(*r.LastWakeAt) < cool && !r.NewestPendingAt.After(*r.LastWakeAt) {
			continue
		}
		notice, err := s.InboxNotice(ctx, r.AgentID, r.AgentName, r.ItemKey)
		if err != nil {
			s.logf("wake: inbox notice for %s: %v", r.AgentName, err)
			notice = PendingNotice(r.Pending, r.AgentName, r.ItemKey) // fallback, never block a wake on a render error
		}
		if r.HasControl {
			notice = PausePreservationNotice(r.AgentName, r.ItemKey)
			if r.Handoff {
				notice = HandoffPreservationNotice(r.AgentName, r.ItemKey)
			}
		}
		ad, ok := s.Adapters[r.Kind]
		if !ok {
			continue
		}
		// 1. native wake, once per message batch. A native error feeds the
		// unified exponential backoff via recordWakeFailure; a
		// delivered=false (no subscriber, or a paste-only kind like muse)
		// deliberately does not, and falls through to the paste gates below
		// — recording every per-tick false would hold paste-only sessions
		// behind permanent backoff.
		if !r.NativeTried {
			delivered, err := ad.Wake(ctx, adapter.WakeTarget{SessionID: r.SessionID, AgentID: r.AgentID,
				ProviderSessionID: r.ProviderID, TmuxName: r.TmuxName, Notice: notice,
				Model: s.resolveLaunchModel(ctx, r.Kind, r.Model, r.Effort)})
			if err != nil {
				s.recordWakeFailure(ctx, r.SessionID, r.PasteAttempts, s.Now())
				s.logf("wake: native wake for %s: %v (fail=%d backoff=%s)", r.AgentName, err, r.PasteAttempts+1, backoffForFailures(r.PasteAttempts+1))
			}
			if delivered {
				if err := s.markWoken(ctx, r.SessionID, true); err != nil {
					s.recordWakeFailure(ctx, r.SessionID, r.PasteAttempts, s.Now())
					s.logf("wake: markWoken for %s: %v (fail=%d backoff=%s)", r.AgentName, err, r.PasteAttempts+1, backoffForFailures(r.PasteAttempts+1))
					continue
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
		if r.PasteAttempts > 0 && r.LastPasteAttemptAt != nil && s.Now().Sub(*r.LastPasteAttemptAt) < backoffForFailures(r.PasteAttempts) {
			continue
		}
		// tryPaste never fails the tick: per-session errors are logged and
		// counted with backoff inside, so one bad pane cannot starve the rest.
		_ = s.tryPaste(ctx, ad, r, notice)
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

// tryPaste checks the three §11.3 conditions and pastes the caller-supplied notice
// (the same rich Inbox notice native wake gets, or PausePreservationNotice for a
// control batch — never the bare IdleToken). The daemon never logs a full process listing: other
// tools' bearer tokens show up there (P0-4).
func (s *Store) tryPaste(ctx context.Context, ad adapter.Adapter, r wakeRow, pasteNotice string) error {
	fail := func(reason string) error {
		s.recordWakeFailure(ctx, r.SessionID, r.PasteAttempts, s.Now())
		s.logf("wake: paste for %s skipped/failed (%s; fail=%d backoff=%s)", r.AgentName, reason, r.PasteAttempts+1, backoffForFailures(r.PasteAttempts+1))
		return nil
	}
	if !(matchesAny(ad.ProcessNames(), r.PaneCommand) && !isShell(r.PaneCommand)) {
		return fail(fmt.Sprintf("pane command %q matches no ProcessNames pattern", r.PaneCommand))
	}
	capture, err := s.Tmux.Capture(ctx, r.TmuxName, 15)
	if err != nil {
		return fail("capture failed")
	}
	if !ad.Idle(capture) {
		return fail("pane not idle")
	}
	if err := s.Tmux.PasteLine(ctx, r.TmuxName, pasteNotice); err != nil {
		return fail("paste failed")
	}
	if err := s.markWoken(ctx, r.SessionID, false); err != nil {
		s.recordWakeFailure(ctx, r.SessionID, r.PasteAttempts, s.Now())
		s.logf("wake: markWoken for %s failed (fail=%d backoff=%s)", r.AgentName, r.PasteAttempts+1, backoffForFailures(r.PasteAttempts+1))
		return nil
	}
	return nil
}

// raiseUndeliverable raises agent.undeliverable once per batch. A notification
// for this agent created at or after the oldest pending message (or the
// session start, whichever is later) means the batch was already reported.
func (s *Store) raiseUndeliverable(ctx context.Context, r wakeRow) error {
	since := r.OldestMessageAt
	if r.StartedAt.After(since) {
		since = r.StartedAt
	}
	already, err := s.alreadyNotifiedUndeliverable(ctx, r.AgentID, since)
	if err != nil || already {
		return err
	}
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
	return nil
}

// markWoken records a successful wake (native or pasted) and resets the
// wake-failure backoff, so a later, unrelated message batch starts its own
// count from zero.
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

// shouldLogQuotaSkip reports whether WakeOnQuotaReset should log a "pane not
// idle" skip for sessionID at this cutoff -- true (and records it) only the
// first time this exact (session, cutoff) pair is seen, so a session stuck
// for the full hour checkQuotaResets keeps retrying logs once, not ~60 times.
func (s *Store) shouldLogQuotaSkip(sessionID string, cutoff time.Time) bool {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.quotaSkipLogged == nil {
		s.quotaSkipLogged = map[string]int64{}
	}
	c := db.Millis(cutoff)
	if s.quotaSkipLogged[sessionID] == c {
		return false
	}
	s.quotaSkipLogged[sessionID] = c
	return true
}

// recordWakeFailure counts one consecutive wake failure (native error or any
// paste skip/failure) toward the unified exponential backoff. snapshot is the
// tick-start count from wakeCandidates: setting snapshot+1 (not incrementing
// the live map) caps a tick with both a native and a paste failure at a single
// step, while still refreshing the timestamp for the backoff gate.
func (s *Store) recordWakeFailure(ctx context.Context, sessionID string, snapshot int, at time.Time) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.pasteAttempts == nil {
		s.pasteAttempts = map[string]int{}
	}
	if s.lastPasteAttemptAt == nil {
		s.lastPasteAttemptAt = map[string]time.Time{}
	}
	if cur := s.pasteAttempts[sessionID]; cur < snapshot+1 {
		s.pasteAttempts[sessionID] = snapshot + 1
	}
	s.lastPasteAttemptAt[sessionID] = at
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
// subscribes here for the claude bridge). It reports whether there was anyone
// to publish TO -- a publish with no subscriber is not a delivered wake, and
// Claude.Wake must not tell WakeDue otherwise.
func (s *Store) PublishWake(ctx context.Context, sessionID, notice string) (bool, error) {
	s.bookkeepingMu.Lock()
	subs := append([]chan string{}, s.wakeSubs[sessionID]...)
	s.bookkeepingMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- notice:
		default:
		}
	}
	return len(subs) > 0, nil
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

// flushSuppressed delivers one digest per agent holding suppressed rows for
// kind, then deletes the rows. The digest precedes the reset wake in the same
// batch, so the parent learns what piled up while it was quota-dead at the
// cost of one message instead of hundreds.
func (s *Store) flushSuppressed(ctx context.Context, kind AgentKind) error {
	type supRow struct {
		agentID, rootItemID, event, sample string
		count                              int
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT sr.agent_id, a.root_item_id, sr.event, sr.count, sr.sample_json
		FROM suppressed_relays sr JOIN agents a ON a.id = sr.agent_id
		WHERE a.kind = ? ORDER BY sr.agent_id, sr.count DESC`, string(kind))
	if err != nil {
		return err
	}
	var all []supRow
	for rows.Next() {
		var r supRow
		if err := rows.Scan(&r.agentID, &r.rootItemID, &r.event, &r.count, &r.sample); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for len(all) > 0 {
		var group []supRow
		id, root := all[0].agentID, all[0].rootItemID
		for len(all) > 0 && all[0].agentID == id {
			group = append(group, all[0])
			all = all[1:]
		}
		lines := make([]string, 0, len(group))
		for _, g := range group {
			sample := g.sample
			if len(sample) > 120 {
				sample = sample[:120]
			}
			lines = append(lines, fmt.Sprintf("%s x%d while %s exhausted: %s", g.event, g.count, kind, sample))
		}
		for {
			body, err := json.Marshal(map[string]any{"lines": lines, "suppressed": true})
			if err != nil {
				return err
			}
			if len(body) <= maxDigest || len(lines) == 0 {
				err := s.tx(ctx, func(tx *sql.Tx) error {
					if _, err := s.enqueueRaw(ctx, tx, Message{Kind: "digest", Origin: "daemon",
						ToAgentID: id, RootItemID: root, Payload: body}); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `DELETE FROM suppressed_relays WHERE agent_id = ?`, id)
					return err
				})
				if err != nil {
					return err
				}
				break
			}
			lines = lines[:len(lines)-1]
		}
	}
	return nil
}

// WakeOnQuotaReset wakes all live or waiting sessions belonging to kind that have
// not already been woken for this cutoff cycle (last_wake_at < cutoff).
func (s *Store) WakeOnQuotaReset(ctx context.Context, kind AgentKind, cutoff time.Time) (int, error) {
	if err := s.flushSuppressed(ctx, kind); err != nil {
		return 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, a.id, a.name, ses.tmux_name, ses.state, ses.waiting, a.model, COALESCE(a.effort, '')
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
		var sessionID, agentID, agentName, tmuxName, state, model, effort string
		var waiting bool
		if err := rows.Scan(&sessionID, &agentID, &agentName, &tmuxName, &state, &waiting, &model, &effort); err != nil {
			return woken, err
		}
		// Epic-approval-lane decision 2: a quota-reset wake re-surfaces what
		// this live session can't already see (fresh=false). The same write-
		// inside-the-read-loop pattern as markWoken below.
		notice := QuotaResetNotice()
		if a, err := s.agentByID(ctx, agentID); err != nil {
			s.logf("wake: quota-reset agent %s: %v", agentName, err)
		} else if n, err := s.resurfaceOpenRequests(ctx, a, sessionID, false); err != nil {
			s.logf("wake: quota-reset resurface for %s: %v", agentName, err)
		} else if n > 0 {
			notice += " " + OpenRequestsReminder(n)
		}
		// Attempt native wake or paste idle token if pane is idle
		if ok {
			delivered, _ := ad.Wake(ctx, adapter.WakeTarget{SessionID: sessionID, AgentID: agentID, TmuxName: tmuxName,
				Notice: notice,
				Model:  s.resolveLaunchModel(ctx, kind, model, effort)})
			if delivered {
				s.markWoken(ctx, sessionID, true)
				woken++
				continue
			}
		}
		// Fallback to idle paste if pane is alive
		capture, err := s.Tmux.Capture(ctx, tmuxName, 15)
		switch {
		case err != nil:
			s.logf("wake: quota-reset capture for %s (session %s): %v", agentName, sessionID, err)
		case ok && ad.Idle(capture):
			if err := s.Tmux.PasteLine(ctx, tmuxName, IdleToken); err == nil {
				s.markWoken(ctx, sessionID, false)
				woken++
			}
		case ok:
			if s.shouldLogQuotaSkip(sessionID, cutoff) {
				s.logf("wake: quota-reset skip for %s (session %s): pane not idle", agentName, sessionID)
			}
		}
	}
	return woken, rows.Err()
}
