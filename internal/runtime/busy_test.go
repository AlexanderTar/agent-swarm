package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/adapter"
	"github.com/AlexanderTar/swarmrun/internal/events"
)

// leaderBusyAdapter is the fake adapter with a grok-style leader busy signal.
type leaderBusyAdapter struct {
	adapter.Adapter
	busy map[string]bool
}

func (l *leaderBusyAdapter) LeaderBusy(sessionID string) bool { return l.busy[sessionID] }

func busySession(t *testing.T, name string) (*Store, *fakeTmux, *testClock, string) {
	t.Helper()
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: name, Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"})
	tm.captures[a.Name] = []string{"working on it…\n"}
	return s, tm, tm.clk, ses.ID
}

// A worker inside a long tool call has not stopped answering: the message
// waits for the next tool boundary, so no "undeliverable" until the ceiling.
func TestOpenToolCallSuppressesUndeliverableUntilTheCeiling(t *testing.T) {
	s, _, at, sid := busySession(t, "LongBuild")
	ctx := context.Background()
	s.ToolStarted(sid)

	at.Advance(10 * time.Minute)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 0 {
		t.Fatalf("busy at 10 min: %d alerts, want 0", n)
	}
	at.Advance(busyCeiling)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("past the busy ceiling: %d alerts, want 1", n)
	}
}

// A finished tool call is not busy: the alert comes back on schedule.
func TestFinishedToolCallDoesNotSuppressUndeliverable(t *testing.T) {
	s, _, at, sid := busySession(t, "ShortTool")
	ctx := context.Background()
	s.ToolStarted(sid)
	s.ToolFinished(sid)
	at.Advance(6 * time.Minute)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("idle worker at 6 min: %d alerts, want 1", n)
	}
}

func TestOpenToolCallSuppressesStaleUntilTheCeiling(t *testing.T) {
	s, _, at, sid := busySession(t, "LongTest")
	ctx := context.Background()
	s.ToolStarted(sid)

	at.Advance(45 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.stale"); n != 0 {
		t.Fatalf("busy at 45 min: %d stale alerts, want 0", n)
	}
	at.Advance(busyCeiling)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.stale"); n != 1 {
		t.Fatalf("past the busy ceiling: %d stale alerts, want 1", n)
	}
}

// The grok leader's "working" push counts as busy for every alert.
func TestLeaderWorkingSuppressesUndeliverableAndStale(t *testing.T) {
	s, _, at, sid := busySession(t, "GrokTurn")
	ctx := context.Background()
	lb := &leaderBusyAdapter{Adapter: s.Adapters[Fake], busy: map[string]bool{sid: true}}
	s.Adapters[Fake] = lb

	at.Advance(40 * time.Minute)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if a, b := notifiedCount(s, "agent.undeliverable"), notifiedCount(s, "agent.stale"); a != 0 || b != 0 {
		t.Fatalf("leader working: undeliverable=%d stale=%d, want 0 and 0", a, b)
	}
	lb.busy[sid] = false
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("leader idle again: %d undeliverable alerts, want 1", n)
	}
}

// Busy reports how long the worker has been busy; a turn end clears a tool
// call whose PostToolUse never came.
func TestBusySinceTracksToolCallsAndTurnEnd(t *testing.T) {
	s, _, at, sid := busySession(t, "BusyClock")
	if _, ok := s.BusySince(sid); ok {
		t.Fatal("fresh session reads busy")
	}
	s.ToolStarted(sid)
	started, _ := s.BusySince(sid)
	at.Advance(3 * time.Minute)
	s.ToolStarted(sid) // a parallel tool does not restart the clock
	since, ok := s.BusySince(sid)
	if !ok || !since.Equal(started) {
		t.Fatalf("BusySince = %v, %v; want %v", since, ok, started)
	}
	s.ToolFinished(sid)
	if _, ok := s.BusySince(sid); !ok {
		t.Fatal("one of two parallel tools finished: still busy")
	}
	s.TurnEnded(sid)
	if _, ok := s.BusySince(sid); ok {
		t.Fatal("turn end must clear the open tool call")
	}
}

// The board shows "busy" only once it would otherwise look stuck: a 2 s tool call
// is plain running. BusyShown is BusyNow past busyShownAfter, and still under the ceiling.
func TestBusyShownOnlyPastTheThreshold(t *testing.T) {
	s, _, at, sid := busySession(t, "ShownClock")
	s.ToolStarted(sid)
	at.Advance(busyShownAfter - time.Second)
	if _, ok := s.BusyShown(sid); ok {
		t.Fatal("a short tool call must not be shown as busy")
	}
	if _, ok := s.BusyNow(sid); !ok {
		t.Fatal("a short tool call is still busy for the alerts")
	}
	at.Advance(time.Second)
	if _, ok := s.BusyShown(sid); !ok {
		t.Fatal("busy past the threshold must be shown")
	}
	at.Advance(busyCeiling)
	if _, ok := s.BusyShown(sid); ok {
		t.Fatal("past the ceiling the worker reads as stuck again, not busy")
	}
}

// The board refetches agents on agent.changed only, so a session crossing into or
// out of the shown busy state must publish one (and nothing in between).
func TestBusyCrossingPublishesAgentChanged(t *testing.T) {
	s, _, at, sid := busySession(t, "ShownEvents")
	ctx := context.Background()
	count := func() int {
		evs, _ := s.Events.After(ctx, 0, 1000)
		n := 0
		for _, e := range evs {
			if e.Type == events.AgentChanged {
				n++
			}
		}
		return n
	}
	reconcile := func() int {
		t.Helper()
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		return count()
	}
	s.ToolStarted(sid)
	base := reconcile()
	at.Advance(busyShownAfter)
	crossed := reconcile()
	if crossed != base+1 {
		t.Fatalf("agent.changed on crossing the threshold: %d -> %d, want +1", base, crossed)
	}
	if again := reconcile(); again != crossed {
		t.Fatalf("a second tick published again: %d -> %d", crossed, again)
	}
	s.ToolFinished(sid)
	if out := reconcile(); out != crossed+1 {
		t.Fatalf("agent.changed on leaving busy: %d -> %d, want +1", crossed, out)
	}
}

func busyLiveRow(t *testing.T, s *Store, sid string) (liveRow, string) {
	t.Helper()
	r := liveRow{SessionID: sid}
	var agentID string
	if err := s.DB.QueryRow(`SELECT a.id, a.name, a.root_item_id FROM agents a JOIN sessions x ON x.agent_id = a.id WHERE x.id = ?`, sid).
		Scan(&agentID, &r.AgentName, &r.RootItemID); err != nil {
		t.Fatal(err)
	}
	return r, agentID
}

// A failed agent.changed publish must not count as published: the next tick tries again.
func TestBusyChangeIsRetriedAfterAFailedPublish(t *testing.T) {
	s, _, at, sid := busySession(t, "RetryPublish")
	r, _ := busyLiveRow(t, s, sid)
	s.ToolStarted(sid)
	at.Advance(busyShownAfter)
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.publishBusyChange(dead, r); err == nil {
		t.Fatal("publish on a cancelled context should fail")
	}
	before, _ := s.Events.After(context.Background(), 0, 1000)
	if err := s.publishBusyChange(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Events.After(context.Background(), 0, 1000)
	if len(after) != len(before)+1 {
		t.Fatalf("retry published %d events, want 1", len(after)-len(before))
	}
}

// "N messages waiting" is on the board, so a message reaching (or leaving) a worker
// that is shown busy refreshes it without waiting for an unrelated agent.changed.
func TestBusyWaitingCountChangePublishesAgentChanged(t *testing.T) {
	s, _, at, sid := busySession(t, "WaitingCount")
	ctx := context.Background()
	r, agentID := busyLiveRow(t, s, sid)
	s.ToolStarted(sid)
	at.Advance(busyShownAfter)
	events := func() int { evs, _ := s.Events.After(ctx, 0, 1000); return len(evs) }
	if err := s.publishBusyChange(ctx, r); err != nil {
		t.Fatal(err)
	}
	base := events()
	mustExec(t, s.DB, `INSERT INTO messages (id, seq, kind, origin, from_agent_id, to_agent_id, root_item_id, payload_json, state, created_at)
		VALUES ('msg_w1', 9101, 'finding', 'agent', ?, ?, ?, '{}', 'pending', 1)`, agentID, agentID, r.RootItemID)
	if err := s.publishBusyChange(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := events(); got != base+1 {
		t.Fatalf("a message reaching a busy worker: %d events, want %d", got, base+1)
	}
	if err := s.publishBusyChange(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := events(); got != base+1 {
		t.Fatalf("an unchanged count published again: %d events", got)
	}
}
