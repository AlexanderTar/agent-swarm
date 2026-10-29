package runtime

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/adapter"
)

const (
	paneBusyFooter = "✻ Thinking… (12s · esc to interrupt)\n"
	paneIdlePrompt = "─────\n❯ \n─────\n"
)

// showPane scripts what the session's pane looks like from the next capture on.
func showPane(tm *fakeTmux, name, text string) {
	tm.captures[name] = []string{text}
	tm.n[name] = 0
}

func reconcileOnce(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A kind with no hooks (muse) is busy when its pane shows the adapter's busy
// footer: the reconcile tick samples it, and the clock starts at the first sample.
func TestPaneBusyFooterReadsBusy(t *testing.T) {
	s, tm, at, sid := busySession(t, "PaneBusy")
	name := tm.panes[0].Session
	showPane(tm, name, paneBusyFooter)
	reconcileOnce(t, s)
	first, ok := s.BusySince(sid)
	if !ok {
		t.Fatal("pane shows the busy footer but the session reads idle")
	}
	at.Advance(2 * time.Minute)
	reconcileOnce(t, s)
	if since, ok := s.BusySince(sid); !ok || !since.Equal(first) {
		t.Fatalf("BusySince after a second busy sample = %v, %v; want %v (clock must not restart)", since, ok, first)
	}
	at.Advance(busyCeiling)
	reconcileOnce(t, s)
	if _, ok := s.BusyNow(sid); ok {
		t.Fatal("pane busy past the ceiling must read as stuck, not busy")
	}
}

// Back at the idle prompt, the pane evidence is gone.
func TestPaneIdlePromptClearsBusy(t *testing.T) {
	s, tm, _, sid := busySession(t, "PaneIdle")
	name := tm.panes[0].Session
	showPane(tm, name, paneBusyFooter)
	reconcileOnce(t, s)
	if _, ok := s.BusySince(sid); !ok {
		t.Fatal("setup: pane busy should read busy")
	}
	showPane(tm, name, paneIdlePrompt)
	reconcileOnce(t, s)
	if _, ok := s.BusySince(sid); ok {
		t.Fatal("pane at its idle prompt still reads busy")
	}
}

// A footer that sits next to a question or permission dialog is a worker waiting
// for a person, not working.
func TestPaneBusyWithDialogIsNotBusy(t *testing.T) {
	s, tm, _, sid := busySession(t, "PaneDialog")
	name := tm.panes[0].Session
	s.Adapters[Fake].(*adapter.Fake).PromptMatchers = []adapter.PromptMatcher{
		{Match: regexp.MustCompile(`Allow this command\?`), Title: "permission"},
	}
	showPane(tm, name, paneBusyFooter+"Allow this command? (y/n)\n")
	reconcileOnce(t, s)
	if _, ok := s.BusySince(sid); ok {
		t.Fatal("busy footer over a permission dialog reads busy")
	}
}

// Hook and leader evidence keep their own clock: a pane sample does not move it.
func TestPaneBusyDoesNotOverrideToolEvidence(t *testing.T) {
	s, tm, at, sid := busySession(t, "PaneAndTool")
	name := tm.panes[0].Session
	s.ToolStarted(sid)
	tool, _ := s.BusySince(sid)
	at.Advance(time.Minute)
	showPane(tm, name, paneBusyFooter)
	reconcileOnce(t, s)
	if since, ok := s.BusySince(sid); !ok || !since.Equal(tool) {
		t.Fatalf("BusySince = %v, %v; want the tool clock %v", since, ok, tool)
	}
	s.ToolFinished(sid)
	if _, ok := s.BusySince(sid); !ok {
		t.Fatal("tool finished but the pane still shows busy: want busy")
	}
}
