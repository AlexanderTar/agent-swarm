package runtime

import (
	"context"
	"strings"
	"time"
)

// urgentGrace is how long a pending pause/handoff message waits, with the worker
// busy and its hooks silent, before the worker's turn is interrupted.
const urgentGrace = 30 * time.Second

// interruptForUrgent cancels a busy worker's running turn once for a pending
// urgent message (IsUrgent: pause and handoff only), so the wake that follows
// can deliver it as the next prompt instead of waiting out a long tool call.
// It interrupts only when every guard holds:
//   - the message has waited urgentGrace and the session's hooks have been
//     silent that long (a firing hook delivers the notice at the next tool gap);
//   - the worker is busy by the shared busy signal (open tool call or grok
//     leader "working"), not merely quiet;
//   - nobody is being waited on (no open question or permission dialog, no grok card);
//   - the pane runs the agent and is not at its idle prompt, rechecked right
//     before the keys: grok's C-c at idle arms quit and a second one exits it.
//
// The adapter's own InterruptKeys are used (Escape for claude and codex, one C-c
// for grok: the TASK-1126 probe showed C-c mid-turn cancels the turn). One
// interrupt per message, remembered in memory only: after a daemon restart a
// worker that already got it and is now busy running its handoff turn with quiet
// hooks gets one more (accepted risk, see docs/urgent-interrupt.md).
func (s *Store) interruptForUrgent(ctx context.Context, r wakeRow) {
	if r.UrgentID == "" || r.OpenQuestion || s.Now().Sub(r.UrgentAt) < urgentGrace {
		return
	}
	if r.LastSeenAt != nil && s.Now().Sub(*r.LastSeenAt) < urgentGrace {
		return
	}
	if s.urgentAlreadyInterrupted(r.UrgentID) {
		return
	}
	if _, busy := s.BusySince(r.SessionID); !busy {
		return
	}
	ad, ok := s.Adapters[r.Kind]
	if !ok || !matchesAny(ad.ProcessNames(), r.PaneCommand) || isShell(r.PaneCommand) {
		return
	}
	capture, err := s.Tmux.Capture(ctx, r.TmuxName, 15)
	if err != nil {
		return
	}
	if pr, ok := ad.(interface{ PasteReady(capture string) bool }); ok && !pr.PasteReady(capture) {
		return // a card is up: someone is meant to answer it
	}
	if ad.Idle(capture) {
		return
	}
	if err := s.Tmux.Keys(ctx, r.TmuxName, ad.InterruptKeys()...); err != nil {
		s.logf("wake: urgent interrupt for %s: %v", r.AgentName, err)
		return
	}
	s.markUrgentInterrupted(r.UrgentID)
	action := "pause"
	if r.Handoff {
		action = "handoff"
	}
	s.logf("wake: interrupted %s (%s) for a pending %s request after %s", r.AgentName, r.Kind, action, s.Now().Sub(r.UrgentAt).Round(time.Second))
	_ = s.Notify.Raise(ctx, nil, NotifyInput{Kind: "agent.urgent_interrupt", AgentName: r.AgentName, ItemKey: r.ItemKey,
		Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey, "action": action}})
}

func (s *Store) urgentAlreadyInterrupted(msgID string) bool {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	return s.urgentInterrupted[msgID]
}

func (s *Store) markUrgentInterrupted(msgID string) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.urgentInterrupted == nil {
		s.urgentInterrupted = map[string]bool{}
	}
	s.urgentInterrupted[msgID] = true
}

// pruneUrgentInterrupted forgets messages that are no longer pending: once
// delivered they cannot interrupt anyone again.
func (s *Store) pruneUrgentInterrupted(ctx context.Context) {
	s.bookkeepingMu.Lock()
	ids := make([]any, 0, len(s.urgentInterrupted))
	for id := range s.urgentInterrupted {
		ids = append(ids, id)
	}
	s.bookkeepingMu.Unlock()
	if len(ids) == 0 {
		return
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM messages WHERE state = 'pending' AND id IN (?`+
		strings.Repeat(",?", len(ids)-1)+`)`, ids...)
	if err != nil {
		return
	}
	defer rows.Close()
	pending := map[string]bool{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			pending[id] = true
		}
	}
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	for _, id := range ids {
		if !pending[id.(string)] {
			delete(s.urgentInterrupted, id.(string))
		}
	}
}
