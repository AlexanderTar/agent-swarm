package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// TestNativeWakeResolvesAgyLaunchModel is the P0 model-passthrough fix
// (docs/specs/2026-09-26-agy-launch-model.md): WakeDue's native-wake step
// must resolve the agent's catalog base model to its agy launch id before
// building the WakeTarget, the same way startSession resolves it for
// launch/resume.
func TestNativeWakeResolvesAgyLaunchModel(t *testing.T) {
	s, tm, fa := newStore(t)
	fa.WakeOK = true
	s.Adapters[Agy] = fa
	seedAgyCatalog(t, s.DB)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE settings SET value_json = '["claude", "fake", "agy"]' WHERE key = 'enabled_agents'`); err != nil {
		t.Fatal(err)
	}
	at := tm.clk
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "AgyWake", Intent: "feature",
		Kind: Agy, Model: "gemini-3.8-flash"})
	if err != nil {
		t.Fatal(err)
	}
	if a.PreflightError != "" {
		t.Fatalf("preflight error: %s", a.PreflightError)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "agy"})
	at.Advance(25 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if fa.LastWakeTarget.Model != "gemini-3.8-flash-high" {
		t.Fatalf("WakeTarget.Model = %q, want the suffixed default-effort launch id", fa.LastWakeTarget.Model)
	}
}

// TestWakeOnQuotaResetResolvesAgyLaunchModel mirrors
// TestNativeWakeResolvesAgyLaunchModel for the quota-reset wake path.
func TestWakeOnQuotaResetResolvesAgyLaunchModel(t *testing.T) {
	s, tm, fa := newStore(t)
	fa.WakeOK = true
	s.Adapters[Agy] = fa
	seedAgyCatalog(t, s.DB)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE settings SET value_json = '["claude", "fake", "agy"]' WHERE key = 'enabled_agents'`); err != nil {
		t.Fatal(err)
	}
	at := tm.clk
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "AgyQuotaReset", Intent: "feature",
		Kind: Agy, Model: "gemini-3.8-flash", Effort: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if a.PreflightError != "" {
		t.Fatalf("preflight error: %s", a.PreflightError)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "agy"})
	tm.captures[a.Name] = []string{"─────\n❯ \n─────\n"} // idle
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET waiting = 1 WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}

	cutoff := at.Now().Add(-2 * time.Minute)
	if _, err := s.WakeOnQuotaReset(ctx, Agy, cutoff); err != nil {
		t.Fatal(err)
	}
	if fa.LastWakeTarget.Model != "gemini-3.8-flash-medium" {
		t.Fatalf("WakeTarget.Model = %q, want the suffixed medium-effort launch id", fa.LastWakeTarget.Model)
	}
}

// 2026-09-22 live incident: s11-seams filed a progress checkpoint reading
// like a finished report (TASK-107) and its orchestrator never got pinged
// -- progress was deferred-wake-class, so it just sat pending until
// somebody happened to sync. Checkpoint cadence data (873 checkpoints /
// 167 sessions, median gap 5 min, worst observed case 8.5 min) showed the
// anti-spam rationale for deferring progress never held in practice, so
// every relay checkpoint now wakes immediately, same as completed/failed/etc.
func TestProgressCheckpointWakesImmediately(t *testing.T) {
	s, tm, fa := newStore(t)
	fa.WakeOK = true
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": orchSes.ID}
	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	// Clear the orchestrator's own kickoff assignment first (already immediate)
	// so the only pending immediate message left is the relay under test.
	s.Sync(ctx, orchSes.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "midway report"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	var wakeAt *int64
	s.DB.QueryRowContext(ctx, `SELECT last_wake_at FROM sessions WHERE id = ?`, orchSes.ID).Scan(&wakeAt)
	if wakeAt == nil {
		t.Fatal("a progress checkpoint's relay must wake its parent immediately, not wait for a sync")
	}
}

// Step 1 of §11.3 is "native wake, once per message BATCH", but NativeTried
// used to be derived from last_wake_at alone -- and last_wake_at is written by
// markWoken after a native wake OR a paste, and never cleared. So the first
// wake of a session's life disabled native wake for the rest of that session:
// every later batch dropped straight to the paste fallback. That is how the
// 2026-09-23 orchestrator incident reached the paste path at all (it had many
// prior turns), and it is why the user saw claude being pasted at instead of
// woken natively. A message newer than the last wake is a new batch and must
// get its own native attempt.
func TestNativeWakeIsRetriedForEachNewMessageBatch(t *testing.T) {
	s, tm, fa := newStore(t)
	fa.WakeOK = true
	ctx := context.Background()
	at := tm.clk
	orch, _, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": orchSes.ID}
	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	// Clear the orchestrator's own kickoff assignment so the only pending
	// immediate messages left are the two findings below.
	s.Sync(ctx, orchSes.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, orch.ID)

	if _, err := s.Send(ctx, wSes.ID, "parent", "finding", "first batch", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.WakeDue(ctx); err != nil { // batch 1: native
		t.Fatal(err)
	}
	if len(tm.pasted) != 0 {
		t.Fatalf("batch 1 should have woken natively, pasted = %v", tm.pasted)
	}
	// Past the paste cooldown, then a genuinely new message arrives.
	at.Advance(31 * time.Second)
	if _, err := s.Send(ctx, wSes.ID, "parent", "finding", "second batch", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 0 {
		t.Fatalf("a message newer than the last wake is a new batch and must get its own native wake, "+
			"not fall through to the paste: pasted = %v", tm.pasted)
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
	if len(tm.pasted) != 1 || strings.HasSuffix(tm.pasted[0], "|"+IdleToken) {
		t.Fatalf("pasted = %v, want the inbox paste notice, not the bare idle token", tm.pasted)
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

// I19: a deferred message never wakes anyone. 2026-09-22: progress/handoff
// relays used to be the deferred case exercised here, but every relay now
// wakes immediately (see TestProgressCheckpointWakesImmediately) -- digest
// is the kind that's actually still deferred, so this test now covers that.
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
	enq(t, s, a.ID, a.RootItemID, "digest", `{"lines":["TASK-1 · w1 · step 1 done"]}`, 1)
	at.Advance(120 * time.Second)
	s.WakeDue(ctx)
	if len(tm.pasted) != 0 {
		t.Fatalf("a deferred digest must not wake: %v", tm.pasted)
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
	want := PendingNotice(1, "a", "TASK-1")
	delivered, err := s.PublishWake(ctx, "ses_1", want)
	if err != nil {
		t.Fatal(err)
	}
	if !delivered {
		t.Fatal("PublishWake must report delivered when a session is subscribed")
	}
	select {
	case got := <-mine:
		if got != want {
			t.Fatalf("notice = %q, want %q (the wake channel must not rewrite the notice)", got, want)
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

// A skip here used to be silent (WakeOnQuotaReset dropped straight to the
// next row with no log line), which hid every "pane not idle" quota-reset
// wake behind a stuck-paste bug (2026-09-25 22:27Z, go-migration-agent-debug)
// until the daemon was already hours into logging nothing useful about it.
func TestWakeOnQuotaResetLogsSkipWhenPaneNotIdle(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Busy", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.captures[a.Name] = []string{"still working, no prompt here\n"} // not idle

	s.DB.ExecContext(ctx, `UPDATE sessions SET waiting = 1 WHERE id = ?`, ses.ID)

	var logs []string
	s.Log = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	cutoff := tm.clk.Now().Add(-2 * time.Minute)
	n, err := s.WakeOnQuotaReset(ctx, Fake, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("woken count = %d, want 0 (pane not idle)", n)
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l, a.Name) && strings.Contains(l, ses.ID) && strings.Contains(l, "not idle") {
			found = true
		}
	}
	if !found {
		t.Fatalf("logs = %v, want a skip log naming %s and %s", logs, a.Name, ses.ID)
	}
}

// A Capture failure inside the quota-reset fallback used to be silent (the
// `err == nil && ok` guard just skipped straight past it), which would hide
// a real tmux problem the same way the non-idle skip hid the stuck pane.
func TestWakeOnQuotaResetLogsCaptureFailure(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "CaptureErr", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.captureErr = errors.New("tmux: no such pane")

	s.DB.ExecContext(ctx, `UPDATE sessions SET waiting = 1 WHERE id = ?`, ses.ID)

	var logs []string
	s.Log = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	cutoff := tm.clk.Now().Add(-2 * time.Minute)
	if _, err := s.WakeOnQuotaReset(ctx, Fake, cutoff); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l, a.Name) && strings.Contains(l, ses.ID) && strings.Contains(l, "tmux: no such pane") {
			found = true
		}
	}
	if !found {
		t.Fatalf("logs = %v, want a capture-failure log naming %s, %s and the error", logs, a.Name, ses.ID)
	}
}

// checkQuotaResets (cmd/swarm/daemon.go) calls WakeOnQuotaReset every minute
// for up to an hour after a cutoff, so an unthrottled skip log would write
// up to ~60 near-identical lines for one stuck session. Only the first skip
// for a given (session, cutoff) pair should log.
func TestWakeOnQuotaResetThrottlesSkipLogToOncePerSessionPerCutoff(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "BusyRepeat", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.captures[a.Name] = []string{"still working, no prompt here\n"} // not idle, repeats

	s.DB.ExecContext(ctx, `UPDATE sessions SET waiting = 1 WHERE id = ?`, ses.ID)

	var logs []string
	s.Log = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	cutoff := tm.clk.Now().Add(-2 * time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := s.WakeOnQuotaReset(ctx, Fake, cutoff); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for _, l := range logs {
		if strings.Contains(l, "not idle") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("skip log fired %d times across 3 calls with the same cutoff, want 1: %v", count, logs)
	}
}

// Review fix: checkQuotaResets (cmd/swarm/daemon.go) calls WakeOnQuotaReset
// once a minute for up to an hour with the SAME cutoff. Previously,
// resurfaceOpenRequests ran on every one of those ticks regardless of
// whether the tick actually woke the session, so a session whose pane never
// goes idle (or whose adapter's native Wake never succeeds) got a fresh
// request_open relay every tick its agent had acked the last one in its
// ordinary swarm_sync -- up to ~59 duplicates for one open request. A relay
// already created for this cutoff must not be recreated while the session
// still hasn't woken.
func TestWakeOnQuotaResetDoesNotDuplicateRelayAcrossTicks(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "BusyHidden", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses := mustSessionID(t, s, a.ID)
	path := writeFile(t, "# Spec\n\n## Data model\n\nrows\n")
	res, err := s.RegisterArtifact(ctx, ses, "register", "SPIKE-1", "spec", path, "")
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := s.Ask(ctx, ses, AskInput{Kind: "approval", Prompt: "Review", ArtifactID: res.ArtifactID, SectionID: res.Sections[0].ID})
	if err != nil {
		t.Fatal(err)
	}

	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.captures[a.Name] = []string{"still working, no prompt here\n"} // never idle -> never woken

	cutoff := tm.clk.Now().Add(-2 * time.Minute)
	if _, err := s.WakeOnQuotaReset(ctx, Fake, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, n := relayFor(t, s, a.ID, hidden.ID); n != 1 {
		t.Fatalf("%d relays after tick 1, want 1", n)
	}
	// The session's own regular swarm_sync acks the relay, same as it would
	// any other tick, well before the pane ever goes idle.
	if _, err := s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE request_id = ?`, hidden.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.WakeOnQuotaReset(ctx, Fake, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, n := relayFor(t, s, a.ID, hidden.ID); n != 1 {
		t.Fatalf("%d relays after tick 2 (same cutoff, pane still busy), want 1 (no duplicate)", n)
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

func TestWakeBackoffIsExponentialWithFiveMinuteCap(t *testing.T) {
	cases := map[int]time.Duration{
		0: 5 * time.Second, 1: 10 * time.Second, 2: 20 * time.Second,
		3: 40 * time.Second, 4: 80 * time.Second, 5: 160 * time.Second,
		6: 5 * time.Minute, 10: 5 * time.Minute, 31: 5 * time.Minute,
		100: 5 * time.Minute,
	}
	for n, want := range cases {
		if got := backoffForFailures(n); got != want {
			t.Errorf("backoffForFailures(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestWakeFailureBackoffGatesAndResets(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "BackoffGate", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"}) // never pasteable: every pass records a failure

	at.Advance(25 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.getPasteAttempts(ses.ID); n != 1 {
		t.Fatalf("first failure: attempts = %d, want 1", n)
	}

	// backoff(1) = 10s: 5s later must skip
	at.Advance(5 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.getPasteAttempts(ses.ID); n != 1 {
		t.Fatalf("within 10s backoff: attempts = %d, want 1", n)
	}

	// 6s more (11s after failure) exceeds backoff(1): second failure fires
	at.Advance(6 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.getPasteAttempts(ses.ID); n != 2 {
		t.Fatalf("after 10s backoff: attempts = %d, want 2", n)
	}

	// backoff(2) = 20s: 19s later must skip
	at.Advance(19 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.getPasteAttempts(ses.ID); n != 2 {
		t.Fatalf("within 20s backoff: attempts = %d, want 2", n)
	}

	// 2s more (21s after) exceeds backoff(2): third failure fires
	at.Advance(2 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.getPasteAttempts(ses.ID); n != 3 {
		t.Fatalf("after 20s backoff: attempts = %d, want 3", n)
	}

	// Success resets: next failure must gate at 10s again, not 40s (backoff(3)).
	// Advance past the 30s success cooldown first, or WakeDue skips before
	// reaching the paste (no failure recorded).
	if err := s.markWoken(ctx, ses.ID, false); err != nil {
		t.Fatal(err)
	}
	at.Advance(35 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.getPasteAttempts(ses.ID); n != 1 {
		t.Fatalf("after reset: attempts = %d, want 1", n)
	}
	at.Advance(5 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.getPasteAttempts(ses.ID); n != 1 {
		t.Fatalf("after reset within 10s: attempts = %d, want 1", n)
	}
}

type captureFailTmux struct {
	*fakeTmux
	failName string
}

func (c *captureFailTmux) Capture(ctx context.Context, name string, lines int) (string, error) {
	if name == c.failName {
		return "", errors.New("capture boom")
	}
	return c.fakeTmux.Capture(ctx, name, lines)
}

// One bad pane must not starve the rest of the tick: A Capture-errors every
// pass while B is idle with pending, so B is still pasted and WakeDue still
// returns nil, with A's failure counted under exponential backoff.
func TestWakeDueIsolatesPerSessionPasteErrors(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "IsoFail", Intent: "feature", Kind: Fake, Model: "fake-1"})
	_, b, _, _ := s.StartSpike(ctx, SpikeInput{Name: "IsoOK", Intent: "feature", Kind: Fake, Model: "fake-1"})
	sesA, _ := s.LatestSession(ctx, a.ID)
	sesB, _ := s.LatestSession(ctx, b.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": sesA.ID}
	tm.env[b.Name] = map[string]string{"SWARM_SESSION": sesB.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"}, Pane{Session: b.Name, Command: "swarm-fake-agent"})
	s.Tmux = &captureFailTmux{fakeTmux: tm, failName: a.Name}

	at.Advance(25 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatalf("per-session paste errors must not fail the tick: %v", err)
	}
	found := false
	for _, p := range tm.pasted {
		if strings.HasPrefix(p, b.Name+"|") {
			found = true
		}
		if strings.HasPrefix(p, a.Name+"|") {
			t.Fatalf("failing session must not be pasted: %q", p)
		}
	}
	if !found {
		t.Fatal("healthy session was not pasted while its peer failed")
	}
	if n, _ := s.getPasteAttempts(sesA.ID); n != 1 {
		t.Fatalf("failing session attempts = %d, want 1", n)
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
	panes(tm, Pane{Session: a.Name, Command: "zsh"})        // never pasteable
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

func TestTryPasteReceivesTheSameRichNoticeAsNativeWake(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "PasteNotice", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.captures[a.Name] = []string{"─────\n❯ \n─────\n"}
	enq(t, s, a.ID, a.RootItemID, "question", `{"body":"does the paste carry the full summary now?"}`, 1)
	at.Advance(25 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 1 {
		t.Fatalf("pasted = %v, want one paste", tm.pasted)
	}
	pasted := tm.pasted[0]
	if strings.HasSuffix(pasted, "|"+IdleToken) {
		t.Fatalf("tryPaste still pastes the bare IdleToken: %q", pasted)
	}
	if !strings.Contains(pasted, "does the paste carry the full summary now?") {
		t.Errorf("pasted notice missing the real message content (v2: no more terse-only paste): %q", pasted)
	}
	if !strings.Contains(pasted, "[QUESTION]") {
		t.Errorf("pasted notice missing the new [TAG] format: %q", pasted)
	}
}

// Waking a quota-dead session is pointless: while its kind is exhausted,
// WakeDue attempts neither native wake nor paste, and the message stays pending
// for the quota-reset flush.
func TestWakeDueSkipsExhaustedKind(t *testing.T) {
	s, tm, fa := newStore(t)
	fa.WakeOK = true // would succeed if attempted: last_wake_at proves it was not
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Skipped", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	s.Usage = fakeUsage{Fake: true}
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 0 {
		t.Fatalf("pasted while exhausted: %v", tm.pasted)
	}
	var wakeAt *int64
	s.DB.QueryRowContext(ctx, `SELECT last_wake_at FROM sessions WHERE id = ?`, ses.ID).Scan(&wakeAt)
	if wakeAt != nil {
		t.Fatal("last_wake_at set while exhausted: native wake must not be attempted")
	}
	var pending int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND state = 'pending'`, a.ID).Scan(&pending)
	if pending == 0 {
		t.Fatal("no pending message left: the wake must leave it for the reset flush")
	}
}

// On quota reset, each agent's held rows flush as exactly one digest before the
// wake, and the rows are deleted.
func TestWakeOnQuotaResetFlushesOneDigest(t *testing.T) {
	s, _, fa := newStore(t)
	fa.WakeOK = true
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Flushed", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.Usage = fakeUsage{Fake: true}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, ev := range []string{"no_ack", "progress_deadlock", "no_ack"} {
			held, err := s.holdIfExhausted(ctx, tx, a.ID, ev, json.RawMessage(`{"event":"`+ev+`"}`))
			if err != nil || !held {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Usage = fakeUsage{} // reset passed: nothing exhausted anymore
	n, err := s.WakeOnQuotaReset(ctx, Fake, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("woke %d sessions, want 1", n)
	}
	var digests int
	var payload string
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(payload_json), '') FROM messages
		WHERE to_agent_id = ? AND kind = 'digest' AND payload_json LIKE '%"suppressed":true%'`, a.ID).Scan(&digests, &payload)
	if digests != 1 {
		t.Fatalf("suppressed digest count = %d, want exactly 1", digests)
	}
	if !strings.Contains(payload, "no_ack x2") || !strings.Contains(payload, "progress_deadlock x1") {
		t.Fatalf("digest missing held counts: %s", payload)
	}
	var leftover int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppressed_relays WHERE agent_id = ?`, a.ID).Scan(&leftover)
	if leftover != 0 {
		t.Fatalf("leftover suppressed rows = %d, want 0 flushed", leftover)
	}
	var wakeAt *int64
	s.DB.QueryRowContext(ctx, `SELECT last_wake_at FROM sessions WHERE id = ?`, ses.ID).Scan(&wakeAt)
	if wakeAt == nil {
		t.Fatal("session not woken after the flush")
	}
}
