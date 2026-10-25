package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// Feed is the part of events.Store the notifier reads.
type Feed interface {
	Subscribe() (<-chan struct{}, func())
	Latest(ctx context.Context) (int64, error)
	After(ctx context.Context, after int64, limit int) ([]events.Event, error)
}

// Notifier shows every notification.created event as a desktop notification
// (notify-send) on Linux, where no menu bar app does. The daemon runs it only
// when SWARM_NOTIFY=desktop, which only the installed systemd unit sets, so
// a test's, e2e's or `make dev`'s daemon never pops a banner.
type Notifier struct {
	Events Feed
	// Prefs are the user's per-level settings (settings.Notifications): a
	// banner only when the level's Center is on; Sound adds a sound hint.
	Prefs func(context.Context) (map[string]settings.NotifyPref, error)
	// Launcher supplies the session environment and the starter: notify-send
	// talks to the session's notification daemon over D-Bus.
	Launcher Launcher
	Log      func(string, ...any)

	mu     sync.Mutex
	warned map[string]bool
}

// notifyBatch is how many events one read takes, as the SSE stream does.
const notifyBatch = 500

// Run shows notifications until ctx ends. Like the SSE handler it subscribes
// before reading the cursor and drains once before its first wait, so an
// event committed between those steps is still shown; notifications from
// before the start are not replayed.
func (n *Notifier) Run(ctx context.Context) {
	wake, unsubscribe := n.Events.Subscribe()
	defer unsubscribe()
	cursor, err := n.Events.Latest(ctx)
	if err != nil {
		n.logf("desktop notifications: %v", err)
		return
	}
	for {
		cursor = n.drain(ctx, cursor)
		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
}

// drain shows every notification after cursor and returns the new cursor.
func (n *Notifier) drain(ctx context.Context, cursor int64) int64 {
	for ctx.Err() == nil {
		evs, err := n.Events.After(ctx, cursor, notifyBatch)
		if err != nil {
			if ctx.Err() == nil {
				n.logf("desktop notifications: %v", err)
			}
			return cursor
		}
		for _, e := range evs {
			cursor = e.Seq
			if e.Type == events.NotificationCreated {
				n.show(ctx, e.Payload)
			}
		}
		if len(evs) < notifyBatch {
			break
		}
	}
	return cursor
}

func (n *Notifier) show(ctx context.Context, payload json.RawMessage) {
	var p struct {
		Level string `json:"level"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		n.logf("desktop notifications: %v", err)
		return
	}
	pref := settings.NotifyPref{Center: true}
	if prefs, err := n.Prefs(ctx); err != nil {
		n.logf("desktop notifications: settings: %v", err)
	} else if lv, ok := prefs[p.Level]; ok {
		pref = lv
	}
	if !pref.Center {
		return
	}
	if _, err := n.Launcher.LookPath("notify-send"); err != nil {
		n.once("notify-send", "desktop notifications: notify-send isn't installed (libnotify-bin); notifications only show on the board")
		return
	}
	session, err := n.Launcher.SessionEnv(ctx)
	if errors.Is(err, ErrNoSession) {
		n.once("session", "desktop notifications: no desktop session; notifications only show on the board")
		return
	} else if err != nil {
		n.logf("desktop notifications: %v", err)
		return
	}
	args := NotifyArgs(p.Level, p.Title, p.Body, pref.Sound)
	if _, err := n.Launcher.Start(childEnv(n.Launcher.Environ(), session), "notify-send", args...); err != nil {
		n.logf("desktop notifications: notify-send: %v", err)
	}
}

// NotifyArgs is notify-send's argv for one notification. Urgency follows the
// level: info is low, attention normal, and action (something needs you)
// critical, which most desktops keep on screen until it is dismissed.
func NotifyArgs(level, title, body string, sound bool) []string {
	urgency := map[string]string{"info": "low", "attention": "normal", "action": "critical"}[level]
	if urgency == "" {
		urgency = "normal"
	}
	args := []string{"--app-name=Swarm", "--urgency=" + urgency}
	if sound {
		args = append(args, "--hint=string:sound-name:message-new-instant")
	}
	// -- ends the options, so a title that starts with "-" stays a title.
	return append(args, "--", title, body)
}

// once logs a condition the first time it is seen, not on every notification.
func (n *Notifier) once(key, msg string) {
	n.mu.Lock()
	if n.warned == nil {
		n.warned = map[string]bool{}
	}
	seen := n.warned[key]
	n.warned[key] = true
	n.mu.Unlock()
	if !seen {
		n.logf("%s", msg)
	}
}

func (n *Notifier) logf(f string, a ...any) {
	if n.Log != nil {
		n.Log(f, a...)
	}
}
