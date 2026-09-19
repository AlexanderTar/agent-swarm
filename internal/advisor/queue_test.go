package advisor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// §17.3: a second call from the same session is refused.
func TestAdviceBusy(t *testing.T) {
	s, seed := newAdvisorService(t)
	ctx := context.Background()
	block := make(chan struct{})
	s.Run = func(context.Context, string, ...string) ([]byte, error) {
		<-block
		return []byte(`{"result":"x"}`), nil
	}
	go s.Ask(ctx, seed.SessionID, "first", nil, time.Second)
	// Wait on the durable state, not on a test-only accessor: the `advice` row is
	// what "busy" means, and an exported Service.running() that exists only so a
	// test can look inside is production API paying for a test's convenience (D65).
	waitUntil(t, func() bool { return adviceState(t, s, seed.SessionID) == "running" })
	_, err := s.Ask(ctx, seed.SessionID, "second", nil, time.Second)
	if err == nil || err.Error() != "advice_busy: wait for your current advice request to finish." {
		t.Fatalf("err = %v", err)
	}
	close(block)
}

// §11.6: at most 2 runs at once across the daemon; more queue. This test also
// carries the "advice never consumes an agent slot" guard (D67) — see the two
// countAgents lines. They are two different properties: the peak assertion pins
// advisor.Service's own MaxConcurrent, the count assertion pins that advice does
// not count against max_agents (8) or max_agents_per_root (4), which are Task 13's
// limiter. Wire Ask through runtime.Store.Admit tomorrow and the peak assertion
// still passes while the second property is broken, so both belong here.
func TestQueueCapsConcurrencyAtTwo(t *testing.T) {
	s, seeds := newAdvisorServiceWithSessions(t, 3)
	ctx := context.Background()
	agentsBefore := countAgents(t, s.DB) // 3, one per seeded session
	var peak, cur int32
	block := make(chan struct{})
	s.Run = func(context.Context, string, ...string) ([]byte, error) {
		n := atomic.AddInt32(&cur, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		<-block
		atomic.AddInt32(&cur, -1)
		return []byte(`{"result":"x"}`), nil
	}
	for _, seed := range seeds {
		go s.Ask(ctx, seed.SessionID, "q", nil, time.Millisecond)
	}
	waitUntil(t, func() bool { return atomic.LoadInt32(&cur) == 2 })
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&peak); got > 2 {
		t.Fatalf("peak concurrency = %d, cap is 2", got)
	}
	close(block)
	waitUntil(t, func() bool { return atomic.LoadInt32(&cur) == 0 })
	// D67: three advice runs, no new agent. "Unchanged", not "zero" — the seeds
	// inserted three agents rows. An Ask that took a slot through
	// runtime.Store.Admit, or wrote its own agents row, fails here.
	if got := countAgents(t, s.DB); got != agentsBefore {
		t.Fatalf("agents went from %d to %d; advice must not consume an agent slot", agentsBefore, got)
	}
}

// (An earlier draft had a standalone TestAdviceDoesNotConsumeAgentSlots here. It
// was removed, not because the property is uncovered — the two countAgents lines
// above cover it — but because a whole test to compare one COUNT(*) either side of
// three calls is a test's worth of ceremony for two lines of assertion, and the
// setup it needed was byte-for-byte the queue test's. The earlier rationale for
// removing it was wrong on one point, and it is worth stating so: "nothing in Ask
// could create an agents row" is false. advisor.Service holds a *db.DB, so an Ask
// that did INSERT INTO agents, or that routed through runtime.Store.Admit to take
// a slot, compiles and fails the assertion. That is what a regression guard is
// for, and it is why the assertion is back above rather than left out.)

func TestListReturnsTheAgentsAdviceNewestFirst(t *testing.T) {
	s, seed := newAdvisorService(t)
	ctx := context.Background()
	at := newClk()
	s.Now = at.Now
	s.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"result":"an answer"}`), nil
	}
	if _, err := s.Ask(ctx, seed.SessionID, "first", nil, time.Second); err != nil {
		t.Fatal(err)
	}
	at.Advance(time.Minute)
	if _, err := s.Ask(ctx, seed.SessionID, "second", nil, time.Second); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(ctx, seed.AgentName)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d advice rows", len(rows))
	}
	if rows[0].Question != "second" {
		t.Fatalf("newest first: got %q", rows[0].Question)
	}
	if rows[0].CreatedAt.Before(rows[1].CreatedAt) {
		t.Fatal("rows come back ordered by created_at descending")
	}
	// another agent's advice never appears in this list
	other := seedOtherAdvisorAgent(t, s)
	rows2, err := s.List(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows2) != 0 {
		t.Fatalf("another agent sees %d rows", len(rows2))
	}
}
