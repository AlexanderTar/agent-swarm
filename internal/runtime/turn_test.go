package runtime

import (
	"context"
	"testing"
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
