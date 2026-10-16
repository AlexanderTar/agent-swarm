package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// What the leader pushes to every registered client, as recorded in the probes
// (TASK-1126 section 4, the TASK-1129 probe 6 log):
//
//   - _x.ai/sessions/changed {upserted:[{sessionId, activity:"working"|"idle", ...}]}
//     whenever a session starts or ends a turn;
//   - a retry_state update {sessionUpdate:"retry_state", type:"exhausted"} when
//     the allowance is spent (the quota probe saw it in the stream; which of the
//     leader's notification methods carries it was not recorded, so every
//     notification is checked for the update).

// leaderState is the push-derived state of every watched leader, by Swarm
// session id. The zero value is ready.
type leaderState struct {
	mu          sync.Mutex
	busy        map[string]map[string]bool // swarm session -> provider session -> working
	watchers    map[string]*leaderWatcher
	onExhausted func(text string)
}

type leaderWatcher struct{ cancel context.CancelFunc }

// OnExhausted sets the sink told of every exhausted retry_state, with the
// update's JSON text (usage.GrokExhaustion.Note reads the reset time from it).
func (g *Grok) OnExhausted(f func(text string)) {
	g.leader.mu.Lock()
	g.leader.onExhausted = f
	g.leader.mu.Unlock()
}

// LeaderBusy reports whether the leader says a turn is running in this Swarm
// session. False also means unknown: no watcher yet, or the leader is gone, and
// the pane is then the only evidence.
func (g *Grok) LeaderBusy(sessionID string) bool {
	g.leader.mu.Lock()
	defer g.leader.mu.Unlock()
	for _, working := range g.leader.busy[sessionID] {
		if working {
			return true
		}
	}
	return false
}

// stopWatchersExcept ends every watcher whose session is not live and forgets
// its state.
func (s *leaderState) stopWatchersExcept(live map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, w := range s.watchers {
		if !live[id] {
			w.cancel()
			delete(s.watchers, id)
			delete(s.busy, id)
		}
	}
}

// watchLeader starts the session's watcher unless one runs. A watcher that
// loses its connection removes itself, and the next reconcile tick starts a new
// one, so there is no retry loop of its own.
func (g *Grok) watchLeader(sessionID, sock string) {
	s := &g.leader
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.watchers[sessionID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &leaderWatcher{cancel: cancel}
	if s.watchers == nil {
		s.watchers = map[string]*leaderWatcher{}
	}
	s.watchers[sessionID] = w
	go func() {
		defer cancel()
		defer func() {
			s.mu.Lock()
			if s.watchers[sessionID] == w {
				delete(s.watchers, sessionID)
				delete(s.busy, sessionID)
			}
			s.mu.Unlock()
		}()
		dctx, dcancel := context.WithTimeout(ctx, 5*time.Second)
		c, err := dialLeader(dctx, sock)
		dcancel()
		if err != nil {
			if errors.Is(err, ErrUnsupportedLeader) {
				g.d.Log("grok: leader for %s refused, not watching it: %v", sessionID, err)
			}
			return
		}
		defer c.Close()
		_ = c.Watch(ctx, func(method string, params json.RawMessage) { g.leaderNote(sessionID, w, method, params) })
	}()
}

// leaderNote applies one push. A note from a watcher that has been replaced or
// stopped is dropped, so a stale connection cannot resurrect cleared state.
func (g *Grok) leaderNote(sessionID string, w *leaderWatcher, method string, params json.RawMessage) {
	activity, exhausted := parseLeaderNote(method, params)
	s := &g.leader
	s.mu.Lock()
	if s.watchers[sessionID] != w {
		s.mu.Unlock()
		return
	}
	if len(activity) > 0 && s.busy == nil {
		s.busy = map[string]map[string]bool{}
	}
	for provider, working := range activity {
		if s.busy[sessionID] == nil {
			s.busy[sessionID] = map[string]bool{}
		}
		s.busy[sessionID][provider] = working
	}
	sink := s.onExhausted
	s.mu.Unlock()
	if exhausted != "" && sink != nil {
		sink(exhausted)
	}
}

// parseLeaderNote reads the two pushes Swarm uses: each upserted session's
// activity (provider session id -> working), and the text of an exhausted
// retry_state update ("" for anything else).
func parseLeaderNote(method string, params json.RawMessage) (activity map[string]bool, exhausted string) {
	if method == "_x.ai/sessions/changed" {
		var p struct {
			Upserted []struct {
				SessionID string `json:"sessionId"`
				Activity  string `json:"activity"`
			} `json:"upserted"`
		}
		if json.Unmarshal(params, &p) != nil {
			return nil, ""
		}
		for _, u := range p.Upserted {
			if u.SessionID == "" || u.Activity == "" {
				continue
			}
			if activity == nil {
				activity = map[string]bool{}
			}
			activity[u.SessionID] = u.Activity == "working"
		}
		return activity, ""
	}
	var p struct {
		Update json.RawMessage `json:"update"`
	}
	var u struct {
		SessionUpdate string `json:"sessionUpdate"`
		Type          string `json:"type"`
	}
	if json.Unmarshal(params, &p) == nil && json.Unmarshal(p.Update, &u) == nil &&
		u.SessionUpdate == "retry_state" && u.Type == "exhausted" {
		return nil, string(p.Update)
	}
	return nil, ""
}
