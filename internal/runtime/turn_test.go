package runtime

import (
	"context"
	"strings"
	"sync"
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

func withRealAdapters(t *testing.T, s *Store) {
	t.Helper()
	s.Adapters = adapter.All(adapter.Deps{Home: t.TempDir(), UserHome: t.TempDir(),
		Bin: "/usr/local/bin/swarm", Log: func(string, ...any) {}})
}

func completeWorker(t *testing.T, s *Store, kind AgentKind) (Agent, Session) {
	t.Helper()
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	w, err := s.agentByID(ctx, wSes.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = ? WHERE id = ?`, string(kind), w.ID); err != nil {
		t.Fatal(err)
	}
	w.Kind = kind
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET tdd_exempt = 'docs' WHERE key = 'TASK-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	return w, wSes
}

// A completed session whose last hook closed the turn is reaped after the
// grace even when the pane text would not read idle. Muse's placeholder
// prompt is the case that used to live for the whole busy cap.
func TestCompletedHookClosedSessionIsReapedDespiteThePane(t *testing.T) {
	for _, kind := range []AgentKind{Claude, Codex, Grok, Cursor, Muse, Agy} {
		t.Run(string(kind), func(t *testing.T) {
			s, tm, at := clockStore(t)
			withRealAdapters(t, s)
			ctx := context.Background()
			w, wSes := completeWorker(t, s, kind)
			s.NoteHookTurn(wSes.ID, kind, "Stop", false, "")
			pane := "still working, no prompt\n"
			if kind == Muse {
				pane = "❯ Ask to monitor a running build or log and get pinged on changes\n"
			}
			panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"})
			tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
			showPane(tm, w.Name, pane)
			at.Advance(61 * time.Second)
			before := len(tm.killed)
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if len(tm.killed)-before != 1 {
				t.Fatalf("killed = %v, want the pane reaped 60s after the completed checkpoint", tm.killed[before:])
			}
		})
	}
}

// A daemon paste reopens the turn. The completed session stays up until that
// turn ends, even though the pane still shows an idle prompt.
func TestWokenCompletedSessionIsNotReapedMidTurn(t *testing.T) {
	s, tm, at := clockStore(t)
	withRealAdapters(t, s)
	ctx := context.Background()
	w, wSes := completeWorker(t, s, Muse)
	s.NoteHookTurn(wSes.ID, Muse, "Stop", false, "")
	s.MarkPromptSubmitted(wSes.ID)
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	showPane(tm, w.Name, paneIdlePrompt)
	at.Advance(61 * time.Second)
	before := len(tm.killed)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != before {
		t.Fatalf("killed = %v, a paste-opened turn must not be reaped", tm.killed[before:])
	}
}

func ackInbox(t *testing.T, s *Store, agentID string) {
	t.Helper()
	ctx := context.Background()
	ses, err := s.LatestSession(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, ses.ID, nil, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, agentID); err != nil {
		t.Fatal(err)
	}
}

// The waiting tab follows the hook. A closed turn that owes nothing is
// waiting even when the pane looks busy; an open turn is not, even when the
// pane shows a prompt.
func TestWaitingTitleFollowsTheHookTurn(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Waiting", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ackInbox(t, s, a.ID)
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}

	s.NoteHookTurn(ses.ID, Fake, "Stop", false, "")
	showPane(tm, a.Name, "still working, no prompt\n")
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LatestSession(ctx, a.ID)
	if !got.Waiting {
		t.Fatal("a hook-closed session that owes nothing should be waiting")
	}
	if len(tm.renamed) == 0 || !strings.Contains(tm.renamed[len(tm.renamed)-1], "⏸️") {
		t.Fatalf("titles = %v, want the waiting mark", tm.renamed)
	}

	s.MarkPromptSubmitted(ses.ID)
	showPane(tm, a.Name, paneIdlePrompt)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = s.LatestSession(ctx, a.ID)
	if got.Waiting {
		t.Fatal("an open turn should not be waiting")
	}
}

// Quota-reset paste follows the hook: a closed turn is woken even when the
// pane looks busy, and an open turn is left alone even when the pane is idle.
func TestQuotaResetWakeFollowsTheHookTurn(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Quota", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	showPane(tm, a.Name, "still working, no prompt\n")
	s.NoteHookTurn(ses.ID, Fake, "Stop", false, "")

	cutoff := tm.clk.Now().Add(-2 * time.Minute)
	n, err := s.WakeOnQuotaReset(ctx, Fake, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(tm.pasted) != 1 {
		t.Fatalf("woken = %d pasted = %v, want the closed turn pasted", n, tm.pasted)
	}

	s.MarkPromptSubmitted(ses.ID)
	showPane(tm, a.Name, paneIdlePrompt)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET last_wake_at = NULL WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}
	cutoff = tm.clk.Now().Add(-2 * time.Minute)
	n, err = s.WakeOnQuotaReset(ctx, Fake, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(tm.pasted) != 1 {
		t.Fatalf("woken = %d pasted = %v, an open turn must not be pasted", n, tm.pasted)
	}
}

// Paste gating follows the hook turn: a closed turn accepts a paste even while
// the pane looks busy, and a question dialog still refuses, including when the
// prompt line under it would read idle. An open turn on an idle pane still
// accepts one, because the paste that opened the turn has to be repeatable
// until the notice lands.
func TestPasteGateFollowsTheHookTurnAndRefusesADialog(t *testing.T) {
	s, tm, _ := newStore(t)
	withRealAdapters(t, s)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "PasteGate", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = ? WHERE id = ?`, Claude, a.ID); err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	ad := s.Adapters[Claude]
	const (
		idle   = "❯ \n"
		busy   = "✻ Beboppin'… (48s · ↓ 114 tokens)\n"
		dialog = "Is this a project you created or one you trust?\nYes, I trust this folder\n❯ \n"
	)
	if !ad.Idle(idle) || ad.Idle(busy) || !ad.Idle(dialog) {
		t.Fatalf("fixtures: idle=%v busy=%v dialog=%v", ad.Idle(idle), ad.Idle(busy), ad.Idle(dialog))
	}
	paste := func(capture string) bool {
		t.Helper()
		tm.pasted = nil
		tm.captures[ses.TmuxName] = []string{capture}
		r := wakeRow{SessionID: ses.ID, AgentName: a.Name, TmuxName: ses.TmuxName, PaneCommand: "claude"}
		if check := s.tryPaste(ctx, ad, r, "notice", false); check != nil {
			check()
		}
		return len(tm.pasted) > 0
	}
	s.NoteHookTurn(ses.ID, Claude, "UserPromptSubmit", false, "")
	if !paste(idle) {
		t.Error("an open turn on an idle pane was not pasted into")
	}
	if paste(busy) {
		t.Error("an open turn on a busy pane was pasted into")
	}
	s.NoteHookTurn(ses.ID, Claude, "Stop", false, "")
	if !paste(busy) {
		t.Error("a closed turn was not pasted into while the pane still looks busy")
	}
	s.NoteHookTurn(ses.ID, Claude, "Stop", false, "")
	if paste(dialog) {
		t.Error("a closed turn was pasted over a question dialog")
	}
}

// Concurrent hook writes and turn reads must not race: HookTurn copies the
// record's fields while holding bookkeepingMu instead of reading the shared
// record after the lock is released (setTurn and touchLastHook write those
// fields under the same lock). Run with -race: before the fix the detector
// reports a write at setTurn racing a read at HookTurn.
func TestHookTurnConcurrentWithNoteHookTurn(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Race", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			if i%2 == 0 {
				s.NoteHookTurn(ses.ID, Claude, "Stop", false, "")
			} else {
				s.NoteHookTurn(ses.ID, Claude, "PreToolUse", false, "")
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = s.HookTurn(ses.ID)
		}
	}()
	wg.Wait()
}

// A concurrent close and open must not persist out of order: after both land,
// the row must agree with memory, or a restart would read the row and reap a
// woken session mid-turn.
func TestTurnRowMatchesMemoryAfterConcurrentCloseOpen(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Order", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			s.setTurn(ses.ID, false, "Stop", s.Now())
		}()
		go func() {
			defer wg.Done()
			<-start
			s.setTurn(ses.ID, true, "", s.Now())
		}()
		close(start)
		wg.Wait()
		mem := s.HookTurn(ses.ID)
		disk := (&Store{DB: s.DB}).HookTurn(ses.ID)
		if mem.Known != disk.Known || mem.Open != disk.Open ||
			!mem.Since.Equal(disk.Since) || mem.CloseEvent != disk.CloseEvent {
			t.Fatalf("iter %d: memory %+v != row %+v", i, mem, disk)
		}
	}
}

// A muse reading is an end of turn when the hook closed it, even if the pane
// has no idle prompt. The placeholder prompt is the pane fallback's job.
func TestMuseTurnEndedFollowsAClosedHook(t *testing.T) {
	s, tm, _ := newStore(t)
	withRealAdapters(t, s)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "MuseEnd", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	tm.captures[ses.TmuxName] = []string{"still working, no prompt\n"}
	s.NoteHookTurn(ses.ID, Muse, "Stop", false, "")
	if !s.museTurnEnded(ctx, ses.ID, ses.TmuxName, 7) {
		t.Fatal("a closed hook did not end the muse turn while the pane looks busy")
	}
}
