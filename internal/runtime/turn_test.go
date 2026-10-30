package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/adapter"
)

// A daemon paste that submits a prompt opens the turn before any hook lands,
// including when the turn was already closed.
func TestDaemonPasteOpensTheTurn(t *testing.T) {
	s, tm, a, ses := museWakeSetup(t)
	tm.captures[ses.TmuxName] = []string{museBusyPane}
	r := wakeRow{SessionID: ses.ID, AgentID: a.ID, AgentName: a.Name, TmuxName: ses.TmuxName, PaneCommand: "swarm-fake-agent"}
	if check := s.tryPaste(context.Background(), s.Adapters[Fake], r, "notice", false); check != nil {
		check()
	}
	if len(tm.pasted) != 1 {
		t.Fatalf("pasted = %v, want the notice delivered", tm.pasted)
	}
	got := s.HookTurn(ses.ID)
	if !got.Known || !got.Open {
		t.Fatalf("after paste: %+v, want the turn open", got)
	}

	fresh := &Store{DB: s.DB, Now: s.Now, Log: s.Log}
	if got := fresh.HookTurn(ses.ID); !got.Known || !got.Open {
		t.Fatalf("after a new Store: %+v, want the paste still open", got)
	}
}

type leaderAd struct {
	adapter.Adapter
	busy bool
}

func (a leaderAd) LeaderBusy(string) bool { return a.busy }

// SessionIdle trusts a hook close, a hook open, the pane when no hook has
// spoken, and an idle pane when the open turn's last hook is stale. A grok
// leader that still has a turn running is never idle.
func TestSessionIdleTrustsTheHookThenThePane(t *testing.T) {
	s, tm, _, ses := museWakeSetup(t)
	ad := s.Adapters[Fake]
	const (
		idlePane = "─────\n❯ \n─────\n"
		busyPane = "still working on the edit\n"
	)
	if !ad.Idle(idlePane) || ad.Idle(busyPane) {
		t.Fatalf("fixture panes are wrong: idle=%v busy=%v", ad.Idle(idlePane), ad.Idle(busyPane))
	}

	if !s.SessionIdle(ses.ID, ad, idlePane) {
		t.Fatal("no hook state and an idle pane should be idle")
	}
	if s.SessionIdle(ses.ID, ad, busyPane) {
		t.Fatal("no hook state and a busy pane should not be idle")
	}

	s.MarkPromptSubmitted(ses.ID)
	if s.SessionIdle(ses.ID, ad, idlePane) {
		t.Fatal("an open turn should not be idle while the pane still shows a prompt")
	}

	s.NoteHookTurn(ses.ID, Claude, "Stop", false, "")
	if !s.SessionIdle(ses.ID, ad, busyPane) {
		t.Fatal("a hook-closed turn should be idle even when the pane looks busy")
	}

	s.NoteHookTurn(ses.ID, Claude, "UserPromptSubmit", false, "")
	tm.clk.Advance(15*time.Minute + time.Second)
	if !s.SessionIdle(ses.ID, ad, idlePane) {
		t.Fatal("an open turn whose last hook is older than 15m and an idle pane is a lost Stop")
	}
	if s.SessionIdle(ses.ID, ad, busyPane) {
		t.Fatal("a stale open turn with a busy pane should not be idle")
	}

	s.NoteHookTurn(ses.ID, Grok, "Stop", false, "")
	led := leaderAd{Adapter: ad, busy: true}
	if s.SessionIdle(ses.ID, led, idlePane) {
		t.Fatal("grok LeaderBusy should win over a hook-closed turn")
	}
}
