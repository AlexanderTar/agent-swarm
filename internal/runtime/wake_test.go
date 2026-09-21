package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A real tmux failure to list panes must surface, not be treated as "no
// candidate has a pane."
func TestWakeDuePropagatesAPanesError(t *testing.T) {
	s, tm, _ := newStore(t)
	at := tm.clk
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "PanesErr", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	at.Advance(25 * time.Second)
	s.Tmux = &erroringTmux{fakeTmux: tm, panesErr: errors.New("tmux list-panes failed")}
	if err := s.WakeDue(ctx); err == nil {
		t.Fatal("a Panes failure must propagate")
	}
}

// priorityFor mirrors enqueue's own default: control is priority 0, everything
// else is 1 (inbox.go).
func priorityFor(kind MessageKind) int {
	if kind == "control" {
		return 0
	}
	return 1
}

func TestNativeWakeSkipsThePaste(t *testing.T) {
	s, tm, fa := newStore(t)
	fa.WakeOK = true
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Native", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	at.Advance(25 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 0 {
		t.Fatalf("a successful native wake skips the paste: %v", tm.pasted)
	}
	var wakeAt *int64
	s.DB.QueryRowContext(ctx, `SELECT last_wake_at FROM sessions WHERE id = ?`, ses.ID).Scan(&wakeAt)
	if wakeAt == nil {
		t.Fatal("last_wake_at must be recorded")
	}
}

// I11: a native wake that is not followed by a sync falls back to the paste.
func TestNativeWakeWithoutASyncFallsBackToThePaste(t *testing.T) {
	s, tm, fa := newStore(t)
	fa.WakeOK = true
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Fallback", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	at.Advance(25 * time.Second)
	s.WakeDue(ctx)
	at.Advance(31 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 1 || !strings.HasSuffix(tm.pasted[0], "|"+IdleToken) {
		t.Fatalf("pasted = %v, want the idle token", tm.pasted)
	}
}

func TestIdlePasteNeedsAllThreeConditions(t *testing.T) {
	cases := []struct {
		name    string
		command string
		capture string
		paste   bool
	}{
		{"idle agent", "swarm-fake-agent", "─────\n❯ \n─────\n", true},
		{"a shell in the pane", "zsh", "─────\n❯ \n─────\n", false},
		{"busy spinner above the prompt", "swarm-fake-agent",
			"✽ Beboppin'… (48s · ↓ 114 tokens)\n─────\n❯ \n─────\n", false},
		{"typed input", "swarm-fake-agent", "─────\n❯ half typed by a human\n─────\n", false},
	}
	for _, c := range cases {
		s, tm, _ := newStore(t)
		ctx := context.Background()
		at := tm.clk
		_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Paste " + c.name, Intent: "feature",
			Kind: Fake, Model: "fake-1"})
		ses, _ := s.LatestSession(ctx, a.ID)
		tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
		panes(tm, Pane{Session: a.Name, Command: c.command})
		tm.captures[a.Name] = []string{c.capture}
		at.Advance(25 * time.Second)
		if err := s.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
		got := len(tm.pasted) > 0
		if got != c.paste {
			t.Errorf("%s: pasted = %v, want %v", c.name, got, c.paste)
		}
	}
}

// §11.3: 20 s for a normal message, 5 s for a control message.
func TestPasteDelays(t *testing.T) {
	for _, c := range []struct {
		kind  MessageKind
		after time.Duration
		paste bool
	}{
		{"finding", 19 * time.Second, false},
		{"finding", 21 * time.Second, true},
		{"control", 4 * time.Second, false},
		{"control", 6 * time.Second, true},
	} {
		s, tm, _ := newStore(t)
		ctx := context.Background()
		at := tm.clk
		_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Delay", Intent: "feature", Kind: Fake, Model: "fake-1"})
		ses, _ := s.LatestSession(ctx, a.ID)
		s.Sync(ctx, ses.ID, nil, 20)
		s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, a.ID)
		tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
		panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
		enq(t, s, a.ID, a.RootItemID, c.kind, `{"body":"x"}`, priorityFor(c.kind))
		at.Advance(c.after)
		s.WakeDue(ctx)
		if got := len(tm.pasted) > 0; got != c.paste {
			t.Errorf("%s after %v: pasted = %v", c.kind, c.after, got)
		}
	}
}

// §11.3: recent hook/sync activity suppresses the paste even once the message
// is old enough — the agent is visibly still working.
func TestRecentActivitySuppressesThePaste(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Active", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	at.Advance(25 * time.Second)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`,
		int64(at.Now().UnixMilli()), ses.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 0 {
		t.Fatalf("recent activity must suppress the paste: %v", tm.pasted)
	}
}

func TestAtMostOneWakeEveryFiveSeconds(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Rate", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	at.Advance(25 * time.Second)
	s.WakeDue(ctx)
	at.Advance(3 * time.Second)
	s.WakeDue(ctx)
	if len(tm.pasted) != 1 {
		t.Fatalf("pasted %d times within 5 s", len(tm.pasted))
	}
}

// §11.3: once a pending immediate message is five minutes old (11 x 31 s = 341 s
// here) the user is told, regardless of how many paste attempts were made.
func TestUndeliverableAfterFiveMinutesUnsynced(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Undeliverable", Intent: "feature",
		Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"}) // never pasteable
	for i := 0; i < 11; i++ {
		at.Advance(31 * time.Second)
		if err := s.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	n := notified(t, s, "agent.undeliverable")
	if n.AgentName != a.Name {
		t.Fatalf("notification = %+v", n)
	}
	if n.Args["N"] == "" || n.Args["N"] == "0" {
		t.Fatalf("N = %q; §17.5's body says how many messages are waiting", n.Args["N"])
	}
	if len(tm.pasted) != 0 {
		t.Fatalf("nothing should have been pasted into a shell: %v", tm.pasted)
	}
}

// I19: a deferred message never wakes anyone.
func TestDeferredMessagesNeverWake(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Deferred", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.Sync(ctx, ses.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	enq(t, s, a.ID, a.RootItemID, "relay", `{"event":"progress","agent":"w1","item":"TASK-1"}`, 1)
	at.Advance(120 * time.Second)
	s.WakeDue(ctx)
	if len(tm.pasted) != 0 {
		t.Fatalf("a deferred relay must not wake: %v", tm.pasted)
	}
}

// WakeLoop just wraps WakeDue in a ticker and stops on cancel; this only
// exercises that wiring, not the wake order itself (covered above).
func TestWakeLoopStopsOnContextCancel(t *testing.T) {
	s, tm, _ := newStore(t)
	panes(tm)
	// See the matching comment on TestReconcileLoopStopsOnContextCancel: one
	// hand-fed tick lets exactly one WakeDue complete, and the loop is parked
	// back in the select with nothing left to receive by the time we cancel.
	tick := make(chan time.Time, 1)
	tick <- time.Now()
	s.After = func(time.Duration) <-chan time.Time { return tick }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.WakeLoop(ctx, time.Millisecond)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WakeLoop did not stop on cancel")
	}
}

// The claude bridge: PublishWake reaches exactly the one subscribed session.
func TestPublishWakeReachesOnlyItsOwnSession(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	mine, stopMine := s.SubscribeWake("ses_1")
	defer stopMine()
	theirs, stopTheirs := s.SubscribeWake("ses_2")
	defer stopTheirs()
	if err := s.PublishWake(ctx, "ses_1", "[swarm] 1 new message(s)"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-mine:
		if !strings.HasPrefix(got, "[swarm]") {
			t.Fatalf("notice = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the subscribed session got nothing")
	}
	select {
	case got := <-theirs:
		t.Fatalf("another session received %q", got)
	default:
	}
}

func TestWakeOnQuotaReset(t *testing.T) {
	s, tm, _ := newStore(t)
	at := tm.clk
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "RateLimited", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.captures[a.Name] = []string{"─────\n❯ \n─────\n"} // idle

	// Mark session waiting
	s.DB.ExecContext(ctx, `UPDATE sessions SET waiting = 1 WHERE id = ?`, ses.ID)

	cutoff := at.Now().Add(-2 * time.Minute)
	n, err := s.WakeOnQuotaReset(ctx, Fake, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("woken count = %d, want 1", n)
	}
	if len(tm.pasted) == 0 || !strings.HasSuffix(tm.pasted[0], "|"+IdleToken) {
		t.Fatalf("pasted = %v, want idle token", tm.pasted)
	}

	// Debounce: calling again with same cutoff must NOT wake again
	n2, err := s.WakeOnQuotaReset(ctx, Fake, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second call: woken count = %d, want 0 (debounced)", n2)
	}
}

func TestPasteRetryIntervalEnforced(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "RetryGap", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"}) // never pasteable

	// First attempt after delay
	at.Advance(25 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, _ := s.getPasteAttempts(ses.ID)
	if attempts != 1 {
		t.Fatalf("first attempt: expected 1, got %d", attempts)
	}

	// 5 seconds later (within pasteRetry 30s): must be skipped
	at.Advance(5 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, _ = s.getPasteAttempts(ses.ID)
	if attempts != 1 {
		t.Fatalf("within 30s: expected 1 attempt, got %d", attempts)
	}

	// 26 seconds later (total 31s > 30s): second attempt fires
	at.Advance(26 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, _ = s.getPasteAttempts(ses.ID)
	if attempts != 2 {
		t.Fatalf("after 31s: expected 2 attempts, got %d", attempts)
	}
}

func TestUndeliverableNotificationOnlyFiresOncePerBatch(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "DedupUndeliverable", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"}) // never pasteable

	// 15 cycles of 31s: reaches 10 attempts and continues for 5 more
	for i := 0; i < 15; i++ {
		at.Advance(31 * time.Second)
		if err := s.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}

	f := s.Notify.(*fakeNotifier)
	f.mu.Lock()
	defer f.mu.Unlock()
	undeliverableCount := 0
	for _, n := range f.raised {
		if n.Kind == "agent.undeliverable" {
			undeliverableCount++
		}
	}
	if undeliverableCount != 1 {
		t.Fatalf("expected exactly 1 undeliverable notification, got %d", undeliverableCount)
	}
}


// 2026-09-21: wakeCandidates counted state != 'acked', which includes messages
// the agent had already synced, so agents that read but never acked were
// reported as "hasn't picked up N message(s)" (four false alerts that day).
func TestReadButUnackedMessageIsNeverUndeliverable(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "ReadNotAcked", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"}) // never pasteable
	if _, err := s.Sync(ctx, ses.ID, nil, 20); err != nil { // read, never acked
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		at.Advance(31 * time.Second)
		if err := s.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 0 {
		t.Fatalf("undeliverable raised %d times for a message the agent had already read", n)
	}
	if len(tm.pasted) != 0 {
		t.Fatalf("nothing should be pasted for a read message: %v", tm.pasted)
	}
}

// The alert is derived from the database: a pending immediate message older
// than undeliverableAfter on a live session. Not from paste-attempt counts.
func TestUndeliverableFiresAtFiveMinutesOfPendingNotBefore(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "FiveMinutes", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"})

	at.Advance(299 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 0 {
		t.Fatalf("at 299 s: %d alerts, want 0", n)
	}
	at.Advance(2 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("at 301 s: %d alerts, want 1", n)
	}
	for i := 0; i < 15; i++ {
		at.Advance(31 * time.Second)
		if err := s.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("after 15 more ticks: %d alerts, want exactly 1", n)
	}
}

// Losing the in-memory paste counters (a daemon restart) must not change the
// alert: it lives in the database.
func TestUndeliverableSurvivesLosingTheAttemptCounters(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Restart", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"})
	at.Advance(301 * time.Second)
	s.WakeDue(ctx)
	s.bookkeepingMu.Lock()
	s.pasteAttempts, s.lastPasteAttemptAt = nil, nil
	s.bookkeepingMu.Unlock()
	for i := 0; i < 3; i++ {
		at.Advance(31 * time.Second)
		s.WakeDue(ctx)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("%d alerts after a counter reset, want exactly 1", n)
	}
}

// Failed paste attempts on message 1 must not carry into message 2: the second
// batch alerts only once its own oldest pending message is five minutes old.
func TestUndeliverableSecondBatchStartsItsOwnClock(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "TwoBatches", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"})
	for i := 0; i < 9; i++ { // 9 failed paste attempts, still under five minutes of pending
		at.Advance(31 * time.Second)
		s.WakeDue(ctx)
	}
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, a.ID)
	enq(t, s, a.ID, a.RootItemID, "finding", `{"body":"second"}`, 1)
	at.Advance(31 * time.Second)
	s.WakeDue(ctx)
	if n := notifiedCount(s, "agent.undeliverable"); n != 0 {
		t.Fatalf("a fresh message raised %d alerts", n)
	}
	at.Advance(301 * time.Second)
	s.WakeDue(ctx)
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("second batch: %d alerts, want 1", n)
	}
}

// After a successful paste the agent gets pasteRetry (30 s) to respond before
// it is pasted again, unless a newer pending message arrived. Previously only
// the 5 s wakeGap applied, so a mid-turn agent got a paste every ~20 s.
func TestNoRepasteWithinTheCooldownUnlessAMessageIsNewer(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Cooldown", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"}) // default capture is idle

	at.Advance(25 * time.Second)
	s.WakeDue(ctx)
	if len(tm.pasted) != 1 {
		t.Fatalf("first paste: %d", len(tm.pasted))
	}
	for _, step := range []time.Duration{6, 14, 9} { // +6 s, +20 s, +29 s
		at.Advance(step * time.Second)
		s.WakeDue(ctx)
		if len(tm.pasted) != 1 {
			t.Fatalf("re-pasted inside the cooldown: %d pastes", len(tm.pasted))
		}
	}
	at.Advance(2 * time.Second) // +31 s
	s.WakeDue(ctx)
	if len(tm.pasted) != 2 {
		t.Fatalf("after the cooldown: %d pastes, want 2", len(tm.pasted))
	}

	// A message created after the last wake is woken for at the next tick, not
	// after the cooldown. (It must be strictly newer than last_wake_at.)
	at.Advance(time.Second)
	enq(t, s, a.ID, a.RootItemID, "finding", `{"body":"newer"}`, 1)
	at.Advance(6 * time.Second)
	s.WakeDue(ctx)
	if len(tm.pasted) != 3 {
		t.Fatalf("a newer message must be woken at the next tick: %d pastes", len(tm.pasted))
	}
}
