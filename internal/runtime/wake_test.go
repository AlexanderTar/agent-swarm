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

// §11.3: after ten failed attempts the user is told.
func TestUndeliverableAfterTenRetries(t *testing.T) {
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
