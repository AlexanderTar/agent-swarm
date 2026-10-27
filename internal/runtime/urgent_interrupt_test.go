package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

const pauseControl = `{"action":"pause","scope":"session","deadline_at":"2026-09-17T12:02:00Z"}`

// urgentSession is a live worker of the given kind running a tool, with the
// real adapter for the kind (Fake keeps the test adapter), a pane whose process
// the adapter recognises, and the given pane capture.
func urgentSession(t *testing.T, kind AgentKind, paneCommand, capture string) (*Store, *fakeTmux, Session, Agent) {
	t.Helper()
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Urgent" + string(kind), Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if kind != Fake {
		if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = ? WHERE id = ?`, kind, a.ID); err != nil {
			t.Fatal(err)
		}
		real, err := adapter.New(kinds.AgentKind(kind), adapter.Deps{Home: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		s.Adapters[kind] = real
		a.Kind = kind
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[ses.TmuxName] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: ses.TmuxName, Command: paneCommand})
	tm.captures[ses.TmuxName] = []string{capture}
	s.ToolStarted(ses.ID)
	return s, tm, ses, a
}

func interruptsSent(tm *fakeTmux, tmux string) []string {
	var out []string
	for _, k := range tm.keys {
		if strings.HasPrefix(k, tmux+"|") {
			out = append(out, strings.TrimPrefix(k, tmux+"|"))
		}
	}
	return out
}

func tickWake(t *testing.T, s *Store, tm *fakeTmux, d time.Duration) {
	t.Helper()
	tm.clk.Advance(d)
	if err := s.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A busy worker with a pending pause is interrupted once, after the grace and
// not before, then left to read the message: later ticks never interrupt again.
func TestUrgentMessageInterruptsABusyWorkerOnceAfterTheGrace(t *testing.T) {
	s, tm, ses, a := urgentSession(t, Fake, "swarm-fake-agent", "running a long build…\n")
	enq(t, s, a.ID, a.RootItemID, "control", pauseControl, 0)

	tickWake(t, s, tm, urgentGrace-time.Second)
	if got := interruptsSent(tm, ses.TmuxName); len(got) != 0 {
		t.Fatalf("interrupted inside the grace: %v", got)
	}
	tickWake(t, s, tm, 2*time.Second)
	if got := interruptsSent(tm, ses.TmuxName); len(got) != 1 || got[0] != "Escape" {
		t.Fatalf("after the grace want one Escape, got %v", got)
	}
	for i := 0; i < 5; i++ {
		tickWake(t, s, tm, 5*time.Second)
	}
	if got := interruptsSent(tm, ses.TmuxName); len(got) != 1 {
		t.Fatalf("interrupted more than once: %v", got)
	}
	if n := notifiedCount(s, "agent.urgent_interrupt"); n != 1 {
		t.Fatalf("board events = %d, want 1", n)
	}
}

// A worker whose hooks are still firing reads its mail at the next tool
// boundary, so the grace restarts with every hook call.
func TestUrgentInterruptWaitsForHookSilence(t *testing.T) {
	s, tm, ses, a := urgentSession(t, Fake, "swarm-fake-agent", "running a long build…\n")
	enq(t, s, a.ID, a.RootItemID, "control", pauseControl, 0)

	tm.clk.Advance(urgentGrace + 10*time.Second)
	if _, err := s.DB.Exec(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, db.Millis(s.Now().Add(-5*time.Second)), ses.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := interruptsSent(tm, ses.TmuxName); len(got) != 0 {
		t.Fatalf("interrupted a worker whose hook fired 5 s ago: %v", got)
	}
	tickWake(t, s, tm, urgentGrace)
	if got := interruptsSent(tm, ses.TmuxName); len(got) != 1 {
		t.Fatalf("after 30 s of hook silence want one interrupt, got %v", got)
	}
}

// Ordinary mail never interrupts, however long the worker is busy.
func TestOrdinaryMessagesNeverInterruptABusyWorker(t *testing.T) {
	s, tm, ses, a := urgentSession(t, Fake, "swarm-fake-agent", "running a long build…\n")
	enq(t, s, a.ID, a.RootItemID, "finding", `{"body":"hello"}`, 1)
	enq(t, s, a.ID, a.RootItemID, "answer", `{"body":"yes"}`, 1)
	enq(t, s, a.ID, a.RootItemID, "assignment_update", `{"note":"x"}`, 1)
	for i := 0; i < 6; i++ {
		tickWake(t, s, tm, 30*time.Second)
	}
	if got := interruptsSent(tm, ses.TmuxName); len(got) != 0 {
		t.Fatalf("ordinary mail interrupted the worker: %v", got)
	}
}

// An idle worker (no open tool call, idle pane) reads the message on its own.
func TestUrgentMessageNeverInterruptsAnIdleWorker(t *testing.T) {
	// no busy evidence at all
	s, tm, ses, a := urgentSession(t, Fake, "swarm-fake-agent", "running a long build…\n")
	s.TurnEnded(ses.ID)
	enq(t, s, a.ID, a.RootItemID, "control", pauseControl, 0)
	tickWake(t, s, tm, 2*urgentGrace)
	if got := interruptsSent(tm, ses.TmuxName); len(got) != 0 {
		t.Fatalf("interrupted a worker with no open tool call: %v", got)
	}

	// busy evidence is stale (a missed PostToolUse) but the pane is at its idle prompt
	s2, tm2, ses2, a2 := urgentSession(t, Fake, "swarm-fake-agent", "─────\n❯ \n─────\n")
	enq(t, s2, a2.ID, a2.RootItemID, "control", pauseControl, 0)
	tickWake(t, s2, tm2, 2*urgentGrace)
	if got := interruptsSent(tm2, ses2.TmuxName); len(got) != 0 {
		t.Fatalf("interrupted a worker sitting at its idle prompt: %v", got)
	}
}

// A worker waiting on a question card is waiting on a person: the dismissal
// path owns that case, an interrupt would only throw its answer away.
func TestUrgentMessageNeverInterruptsAWorkerWaitingOnAQuestion(t *testing.T) {
	s, tm, ses, a := urgentSession(t, Fake, "swarm-fake-agent", "running a long build…\n")
	if _, err := s.AskQuestion(context.Background(), ses.ID, "Which branch?", nil); err != nil {
		t.Fatal(err)
	}
	enq(t, s, a.ID, a.RootItemID, "control", pauseControl, 0)
	tickWake(t, s, tm, 2*urgentGrace)
	if got := interruptsSent(tm, ses.TmuxName); len(got) != 0 {
		t.Fatalf("interrupted a worker waiting on a question: %v", got)
	}
}

// The interrupt key is the adapter's: Escape for claude and codex, and for
// grok exactly one C-c (the TASK-1126 probe: C-c mid-turn cancels the turn, a
// second C-c at idle quits grok), never sent to a grok sitting at its prompt.
func TestUrgentInterruptUsesTheAdapterKeyPerKind(t *testing.T) {
	cases := []struct {
		name    string
		kind    AgentKind
		command string
		capture string
		want    string
	}{
		{"claude", Claude, "claude", "✻ Running… (esc to interrupt)\n", "Escape"},
		{"codex", Codex, "codex", "• Working (12s • esc to interrupt)\n", "Escape"},
		{"grok", Grok, "grok-1.0.70-macos", grokCapture(t, "busy-tool"), "C-c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, tm, ses, a := urgentSession(t, c.kind, c.command, c.capture)
			enq(t, s, a.ID, a.RootItemID, "control", pauseControl, 0)
			for i := 0; i < 8; i++ {
				tickWake(t, s, tm, 10*time.Second)
			}
			if got := interruptsSent(tm, ses.TmuxName); len(got) != 1 || got[0] != c.want {
				t.Fatalf("keys = %v, want exactly [%s]", got, c.want)
			}
		})
	}

	t.Run("grok at its idle prompt is never sent C-c", func(t *testing.T) {
		s, tm, ses, a := urgentSession(t, Grok, "grok-1.0.70-macos", grokCapture(t, "idle"))
		enq(t, s, a.ID, a.RootItemID, "control", pauseControl, 0)
		for i := 0; i < 8; i++ {
			tickWake(t, s, tm, 10*time.Second)
		}
		if got := interruptsSent(tm, ses.TmuxName); len(got) != 0 {
			t.Fatalf("C-c sent to an idle grok: %v", got)
		}
	})
}
