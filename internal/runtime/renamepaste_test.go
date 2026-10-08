package runtime

import (
	"context"
	"strings"
	"testing"
	"time"
)

const busyCapture = "✽ Beboppin'… (48s · ↓ 114 tokens)\n─────\n❯ \n─────\n"

func renameSpike(t *testing.T, busy bool) (*Store, *fakeTmux, Agent, Session) {
	t.Helper()
	s, tm, _ := renameStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Rename Me", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	if busy {
		tm.captures[a.Name] = []string{busyCapture}
	}
	return s, tm, a, ses
}

// TASK-769: the first idle tick after launch pastes the rename command and
// clears the pending name.
func TestIdlePaneGetsTheRenameCommandPastedOnce(t *testing.T) {
	s, tm, a, ses := renameSpike(t, false)
	ctx := context.Background()
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if want := a.Name + "|/rename " + a.Name; len(tm.pasted) != 1 || tm.pasted[0] != want {
		t.Fatalf("pasted = %v, want [%s]", tm.pasted, want)
	}
	if got := pendingName(t, s, ses.ID); got != "" {
		t.Fatalf("pending_name = %q after paste, want cleared", got)
	}
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 1 {
		t.Fatalf("rename pasted again: %v", tm.pasted)
	}
}

// A busy pane keeps the pending name, and the skip is not a wake failure.
func TestBusyPaneKeepsTheRenamePendingWithoutWakeBackoff(t *testing.T) {
	s, tm, _, ses := renameSpike(t, true)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(tm.pasted) != 0 {
		t.Fatalf("pasted into a busy pane: %v", tm.pasted)
	}
	if got := pendingName(t, s, ses.ID); got == "" {
		t.Fatal("pending_name cleared although nothing was pasted")
	}
	if n, at := s.getPasteAttempts(ses.ID); n != 0 || at != nil {
		t.Fatalf("a skipped rename recorded a wake failure: attempts=%d at=%v", n, at)
	}
	// The pane goes idle: the next tick pastes.
	tm.captures[tm.panes[0].Session] = nil
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 1 || !strings.Contains(tm.pasted[0], "|/rename ") {
		t.Fatalf("pasted = %v, want the rename", tm.pasted)
	}
}

// The rename goes before the wake notice, and a tick pastes once per pane.
func TestRenameIsPastedBeforeTheWakeNoticeOnePerTick(t *testing.T) {
	s, tm, a, _ := renameSpike(t, false)
	ctx := context.Background()
	tm.clk.Advance(25 * time.Second) // the kickoff message is now due for an idle paste
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 1 || tm.pasted[0] != a.Name+"|/rename "+a.Name {
		t.Fatalf("first tick pasted %v, want only the rename", tm.pasted)
	}
	tm.clk.Advance(10 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 2 || strings.Contains(tm.pasted[1], "/rename") {
		t.Fatalf("second tick pasted %v, want the wake notice", tm.pasted)
	}
}
