package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/adapter"
)

// museLikeFake is a Fake that gates pastes on PasteReady and reports queued
// input the way Muse does.
type museLikeFake struct{ pasteReadyFake }

func (museLikeFake) ComposerHolds(capture, _ string) bool { return strings.Contains(capture, "HELD") }

func (museLikeFake) QueuedInput(capture, marker string) bool {
	return strings.Contains(capture, "Queued input") && strings.Contains(capture, marker)
}

const (
	museBusyPane        = "◇ Running command · Run sleep 25 — running (2s · esc to interrupt)\n─────\n❯\n─────\n"
	museOtherQueuedPane = "◇ Running command · Run sleep 25 — running (12s · esc to interrupt)\n◆ Queued input · ↑ to edit\n└ please also look at the flaky test\n─────\n❯\n─────\n"
)

func museQueuedPane(name string) string {
	return "◇ Running command · Run sleep 25 — running (12s · esc to interrupt)\n◆ Queued input · ↑ to edit\n└ 1 new message(s) for " + name + "\n─────\n❯\n─────\n"
}

func museWakeSetup(t *testing.T) (*Store, *fakeTmux, Agent, Session) {
	t.Helper()
	s, tm, fa := newStore(t)
	s.Adapters[Fake] = museLikeFake{pasteReadyFake{fa}}
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "MuseBusy", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	return s, tm, a, ses
}

// A pending message for a busy but paste-ready pane (Muse mid-turn) is pasted
// within one paste delay; it does not wait for the turn to end. Characterizes
// the live 2026-10-29 muse delivery (notice created 11:00:09, steered 11:00:31).
func TestWakeDuePastesIntoABusyPasteReadyPaneWithinOnePasteDelay(t *testing.T) {
	s, tm, a, ses := museWakeSetup(t)
	tm.captures[ses.TmuxName] = []string{museBusyPane}
	var logs []string
	s.Log = func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }
	tm.clk.Advance(pasteDelay + time.Second)
	if err := s.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 1 {
		t.Fatalf("pasted = %v, want one notice within the paste delay", tm.pasted)
	}
	if want := "wake: pasted " + a.Name + " (1 pending)"; !strings.Contains(strings.Join(logs, "\n"), want) {
		t.Errorf("logs %v missing %q", logs, want)
	}
}

// The first paste sits as "Queued input" until the model's next boundary, so
// a re-paste after the 30 s cooldown only queues a duplicate notice (seen live
// 2026-10-29 11:01:02, 31 s after the first). While the pane still shows the
// notice queued, the batch is not pasted again; once it drains, the normal
// cooldown applies.
func TestWakeDueDoesNotRepasteWhileMuseShowsQueuedInput(t *testing.T) {
	s, tm, a, ses := museWakeSetup(t)
	ctx := context.Background()
	tm.captures[ses.TmuxName] = []string{museBusyPane}
	tm.clk.Advance(pasteDelay + time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	tm.captures[ses.TmuxName] = []string{museQueuedPane(a.Name)}
	for i := 0; i < 3; i++ {
		tm.clk.Advance(pasteRetry + 5*time.Second)
		if err := s.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(tm.pasted) != 1 {
		t.Fatalf("pasted = %d times while the notice is still queued, want 1: %v", len(tm.pasted), tm.pasted)
	}
	tm.captures[ses.TmuxName] = []string{museBusyPane} // drained, agent still has not synced
	tm.clk.Advance(repasteCooldown(1) + time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 2 {
		t.Fatalf("pasted = %d after the queue drained, want a re-paste: %v", len(tm.pasted), tm.pasted)
	}
}

// Input the agent or operator queued themselves is not Swarm's notice: it must
// not hold the re-paste of an unanswered batch for repasteCap.
func TestWakeDueRepastesWhenTheQueuedInputIsNotTheNotice(t *testing.T) {
	s, tm, _, ses := museWakeSetup(t)
	ctx := context.Background()
	tm.captures[ses.TmuxName] = []string{museBusyPane}
	tm.clk.Advance(pasteDelay + time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	tm.captures[ses.TmuxName] = []string{museOtherQueuedPane}
	tm.clk.Advance(repasteCooldown(1) + time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 2 {
		t.Fatalf("pasted = %d, want a re-paste: someone else's queued input is not our notice (%v)", len(tm.pasted), tm.pasted)
	}
}

// The post-paste Enter check waits submitCheckGap per round; it must not stall
// the pastes of the sessions after it in the same tick.
func TestWakeDuePastesEverySessionBeforeTheSubmitChecksWait(t *testing.T) {
	s, tm, fa := newStore(t)
	s.Adapters[Fake] = museLikeFake{pasteReadyFake{fa}}
	ctx := context.Background()
	for _, name := range []string{"MuseOne", "MuseTwo"} {
		_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: name, Intent: "feature", Kind: Fake, Model: "fake-1"})
		if err != nil {
			t.Fatal(err)
		}
		ses, _ := s.LatestSession(ctx, a.ID)
		tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
		tm.captures[ses.TmuxName] = []string{museBusyPane}
		panes(tm, append(tm.panes, Pane{Session: a.Name, Command: "swarm-fake-agent"})...)
	}
	var mu sync.Mutex
	pastedAtFirstWait := -1
	s.After = func(time.Duration) <-chan time.Time {
		mu.Lock()
		defer mu.Unlock()
		if pastedAtFirstWait < 0 {
			pastedAtFirstWait = len(tm.pasted)
		}
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
	tm.clk.Advance(pasteDelay + time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 2 || pastedAtFirstWait != 2 {
		t.Fatalf("pasted %d, %d of them before the first submit-check wait; want both before it", len(tm.pasted), pastedAtFirstWait)
	}
}

// Muse sometimes leaves a pasted line sitting in its composer when the Enter
// lands too early (probed 2026-10-29). After the paste Swarm looks at the
// composer and sends Enter again while the text is still there.
func TestTryPasteResendsEnterWhileTheComposerStillHoldsTheNotice(t *testing.T) {
	s, tm, a, ses := museWakeSetup(t)
	tm.captures[ses.TmuxName] = []string{museBusyPane, "HELD", "HELD", museBusyPane}
	r := wakeRow{SessionID: ses.ID, AgentID: a.ID, AgentName: a.Name, TmuxName: ses.TmuxName, PaneCommand: "swarm-fake-agent"}
	if check := s.tryPaste(context.Background(), s.Adapters[Fake], r, "notice", false); check != nil {
		check()
	}
	if got := countEnters(tm); got != 2 {
		t.Fatalf("Enter re-sent %d times, want 2 (keys %v)", got, tm.keys)
	}
}

func TestTryPasteResendsEnterAtMostThreeTimes(t *testing.T) {
	s, tm, a, ses := museWakeSetup(t)
	tm.captures[ses.TmuxName] = []string{museBusyPane, "HELD"}
	r := wakeRow{SessionID: ses.ID, AgentID: a.ID, AgentName: a.Name, TmuxName: ses.TmuxName, PaneCommand: "swarm-fake-agent"}
	if check := s.tryPaste(context.Background(), s.Adapters[Fake], r, "notice", false); check != nil {
		check()
	}
	if got := countEnters(tm); got != 3 {
		t.Fatalf("Enter re-sent %d times for a notice that never leaves, want the bound of 3", got)
	}
}

func TestTryPasteSendsNoExtraEnterWhenTheComposerIsClear(t *testing.T) {
	s, tm, a, ses := museWakeSetup(t)
	tm.captures[ses.TmuxName] = []string{museBusyPane}
	r := wakeRow{SessionID: ses.ID, AgentID: a.ID, AgentName: a.Name, TmuxName: ses.TmuxName, PaneCommand: "swarm-fake-agent"}
	if check := s.tryPaste(context.Background(), s.Adapters[Fake], r, "notice", false); check != nil {
		check()
	}
	if got := countEnters(tm); got != 0 {
		t.Fatalf("Enter re-sent %d times with a clear composer", got)
	}
}

func countEnters(tm *fakeTmux) int {
	n := 0
	for _, k := range tm.keys {
		if strings.HasSuffix(k, "|Enter") {
			n++
		}
	}
	return n
}

var _ adapter.Adapter = museLikeFake{}
