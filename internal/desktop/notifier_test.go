package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// feed is an in-memory Feed. latest is what Latest reports, which a test can
// set below the real last event to play one committed right after the read.
type feed struct {
	mu     sync.Mutex
	evs    []events.Event
	latest int64
	wake   chan struct{}
	read   chan struct{} // closed once Latest has been read
	once   sync.Once
}

func newFeed() *feed { return &feed{wake: make(chan struct{}, 1), read: make(chan struct{})} }

func (f *feed) Subscribe() (<-chan struct{}, func()) { return f.wake, func() {} }

func (f *feed) Latest(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer f.once.Do(func() { close(f.read) })
	return f.latest, nil
}

func (f *feed) After(_ context.Context, after int64, limit int) ([]events.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []events.Event
	for _, e := range f.evs {
		if e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

// add appends an event; quiet leaves Latest and the wake-up alone.
func (f *feed) add(typ string, payload any, quiet bool) {
	b, _ := json.Marshal(payload)
	f.mu.Lock()
	seq := int64(len(f.evs) + 1)
	f.evs = append(f.evs, events.Event{Seq: seq, Type: typ, Payload: b})
	if !quiet {
		f.latest = seq
	}
	f.mu.Unlock()
	if !quiet {
		select {
		case f.wake <- struct{}{}:
		default:
		}
	}
}

func notification(level, title, body string) map[string]any {
	return map[string]any{"level": level, "title": title, "body": body, "kind": "agent.failed"}
}

type notifierEnv struct {
	// proc decides how each notify-send ends; nil exits at once with status 0.
	proc  func() *execx.Detached
	feed  *feed
	prefs map[string]settings.NotifyPref
	mu    sync.Mutex
	sent  [][]string
	envs  [][]string
	logs  []string
	n     *Notifier
}

func newNotifierEnv(t *testing.T) *notifierEnv {
	e := &notifierEnv{feed: newFeed(), prefs: map[string]settings.NotifyPref{
		"info": {Center: true}, "attention": {Center: true, Sound: true}, "action": {Center: true}}}
	l := (&launcherEnv{showEnv: execx.Result{Out: liveSession},
		exists: map[string]bool{"/tmp/.X11-unix/X1": true, "/run/user/1000/wayland-1": true}}).launcher()
	l.LookPath = have("notify-send")
	l.Start = func(env []string, name string, args ...string) (*execx.Detached, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.sent = append(e.sent, append([]string{name}, args...))
		e.envs = append(e.envs, env)
		if e.proc != nil {
			return e.proc(), nil
		}
		done := make(chan struct{})
		close(done)
		return &execx.Detached{Done: done, Err: func() error { return nil }, Kill: func() {}}, nil
	}
	e.n = &Notifier{Events: e.feed, Launcher: l,
		Prefs: func(context.Context) (map[string]settings.NotifyPref, error) { return e.prefs, nil },
		Log: func(f string, a ...any) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.logs = append(e.logs, fmt.Sprintf(f, a...))
		}}
	return e
}

func (e *notifierEnv) run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.n.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	<-e.feed.read // events added from here on are new to the notifier
}

func (e *notifierEnv) sends() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]string(nil), e.sent...)
}

func waitSends(t *testing.T, e *notifierEnv, n int) [][]string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := e.sends(); len(s) >= n {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("notify-send ran %d times, want %d", len(e.sends()), n)
	return nil
}

func TestNotifierShowsEachNewNotification(t *testing.T) {
	e := newNotifierEnv(t)
	e.feed.add(events.NotificationCreated, notification("info", "Old", "from before the start"), false)
	e.run(t)
	e.feed.add(events.ItemChanged, map[string]any{"key": "EPIC-1"}, false) // not a notification
	e.feed.add(events.NotificationCreated, notification("attention", "Agent failed", "coder couldn't finish TASK-2."), false)
	e.feed.add(events.NotificationCreated, notification("action", "-Approve spec", "SPIKE-1 needs you."), false)
	sent := waitSends(t, e, 2)
	time.Sleep(20 * time.Millisecond)
	want := [][]string{
		{"notify-send", "--app-name=Swarm", "--urgency=normal", "--hint=string:sound-name:message-new-instant", "--", "Agent failed", "coder couldn't finish TASK-2."},
		{"notify-send", "--app-name=Swarm", "--urgency=critical", "--", "-Approve spec", "SPIKE-1 needs you."},
	}
	if len(e.sends()) != 2 || !slices.Equal(sent[0], want[0]) || !slices.Equal(sent[1], want[1]) {
		t.Fatalf("sent = %q, want %q (and nothing from before the start)", e.sends(), want)
	}
	// notify-send gets the live session's bus and display, not stale daemon values.
	e.mu.Lock()
	env := e.envs[0]
	e.mu.Unlock()
	if !slices.Contains(env, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus") || slices.Contains(env, "XAUTHORITY=/run/stale/Xauthority") {
		t.Errorf("env = %q", env)
	}
}

// Subscribe, then Latest, then one drain before the first wait: an event
// committed between Latest and the wait is shown, though no wake-up for it
// ever arrives.
func TestNotifierShowsAnEventCommittedRightAfterItsStart(t *testing.T) {
	e := newNotifierEnv(t)
	// Quiet: committed, but Latest still says 0 and its wake-up never comes.
	e.feed.add(events.NotificationCreated, notification("info", "Old", "before"), true)
	e.run(t)
	if sent := waitSends(t, e, 1); sent[0][len(sent[0])-2] != "Old" {
		t.Fatalf("sent = %q", sent)
	}
}

func TestNotifierFollowsTheLevelPreferences(t *testing.T) {
	e := newNotifierEnv(t)
	e.prefs["info"] = settings.NotifyPref{Center: false, Sound: true}
	e.run(t)
	e.feed.add(events.NotificationCreated, notification("info", "Task completed", "TASK-1 is complete."), false)
	e.feed.add(events.NotificationCreated, notification("unknown-level", "Odd", "shown, normal urgency"), false)
	sent := waitSends(t, e, 1)
	time.Sleep(20 * time.Millisecond)
	if len(e.sends()) != 1 || sent[0][2] != "--urgency=normal" || sent[0][len(sent[0])-2] != "Odd" {
		t.Fatalf("sent = %q: an info banner was shown with center off", e.sends())
	}
}

// Without notify-send or a desktop session nothing runs, and each is logged
// once, not once per notification.
func TestNotifierLogsAMissingToolOrSessionOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*notifierEnv)
		log   string
	}{
		{"no notify-send", func(e *notifierEnv) { e.n.Launcher.LookPath = have() }, "notify-send isn't installed"},
		{"no session", func(e *notifierEnv) { e.n.Launcher.Exists = func(string) bool { return false } }, "no desktop session"},
	} {
		e := newNotifierEnv(t)
		tc.setup(e)
		e.run(t)
		for range 3 {
			e.feed.add(events.NotificationCreated, notification("attention", "Agent failed", "x"), false)
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond)
		e.mu.Lock()
		logs := strings.Join(e.logs, "\n")
		n := strings.Count(logs, tc.log)
		e.mu.Unlock()
		if len(e.sends()) != 0 || n != 1 {
			t.Errorf("%s: sent %q, logged %d times:\n%s", tc.name, e.sends(), n, logs)
		}
	}
}

// Unreadable settings fall back to showing the banner (center on, no sound).
func TestNotifierShowsWhenSettingsCantBeRead(t *testing.T) {
	e := newNotifierEnv(t)
	e.n.Prefs = func(context.Context) (map[string]settings.NotifyPref, error) { return nil, errors.New("db closed") }
	e.run(t)
	e.feed.add(events.NotificationCreated, notification("attention", "Agent failed", "x"), false)
	if sent := waitSends(t, e, 1); slices.Contains(sent[0], "--hint=string:sound-name:message-new-instant") {
		t.Fatalf("sent = %q", sent)
	}
}

// A body that leaves the item out gets its key; one that has it does not get it twice.
func TestNotifierAddsTheItemKeyWhenTheBodyLacksIt(t *testing.T) {
	e := newNotifierEnv(t)
	e.run(t)
	queued := notification("info", "Agent queued", "coder starts when an agent slot becomes available.")
	queued["item_key"] = "TASK-7"
	failed := notification("info", "Agent failed", "coder couldn't finish TASK-2.")
	failed["item_key"] = "TASK-2"
	e.feed.add(events.NotificationCreated, queued, false)
	e.feed.add(events.NotificationCreated, failed, false)
	sent := waitSends(t, e, 2)
	if got := sent[0][len(sent[0])-1]; got != "coder starts when an agent slot becomes available. (TASK-7)" {
		t.Errorf("queued body = %q", got)
	}
	if got := sent[1][len(sent[1])-1]; got != "coder couldn't finish TASK-2." {
		t.Errorf("failed body = %q", got)
	}
}

// A notify-send that fails is logged with its reason; one that hangs is
// stopped after the timeout, and so is one still running at shutdown.
func TestNotifierWatchesEachNotifySend(t *testing.T) {
	e := newNotifierEnv(t)
	done := make(chan struct{})
	close(done)
	e.proc = func() *execx.Detached {
		return &execx.Detached{Done: done, Kill: func() {},
			Err: func() error { return errors.New("exit status 1: GDBus.Error: no notification daemon") }}
	}
	e.run(t)
	e.feed.add(events.NotificationCreated, notification("info", "One", "x"), false)
	waitLog(t, e, "no notification daemon")

	e2 := newNotifierEnv(t)
	e2.n.Timeout = 30 * time.Millisecond
	var killed sync.WaitGroup
	killed.Add(2)
	e2.proc = func() *execx.Detached {
		var once sync.Once
		return &execx.Detached{Done: make(chan struct{}), Err: func() error { return nil },
			Kill: func() { once.Do(killed.Done) }}
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); e2.n.Run(ctx) }()
	<-e2.feed.read
	e2.feed.add(events.NotificationCreated, notification("info", "Hangs", "x"), false)
	waitLog(t, e2, "took over 30ms; stopped it")
	e2.n.Timeout = time.Hour
	e2.feed.add(events.NotificationCreated, notification("info", "Hangs at shutdown", "x"), false)
	waitSends(t, e2, 2)
	cancel()
	<-stopped
	both := make(chan struct{})
	go func() { killed.Wait(); close(both) }()
	select {
	case <-both: // both hung processes were killed: by the timeout, then by shutdown
	case <-time.After(5 * time.Second):
		t.Fatal("a hung notify-send outlived the notifier")
	}
}

func waitLog(t *testing.T, e *notifierEnv, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		logs := strings.Join(e.logs, "\n")
		e.mu.Unlock()
		if strings.Contains(logs, want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never logged %q", want)
}
