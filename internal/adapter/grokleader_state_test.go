package adapter

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func sessionsChanged(provider, activity string) map[string]any {
	return map[string]any{"upserted": []any{map[string]any{"sessionId": provider, "activity": activity, "cwd": "/w"}}}
}

// The leader pushes _x.ai/sessions/changed whenever a session starts or ends a
// turn (TASK-1129 probe 6 log), so the adapter knows busy without reading the pane.
func TestGrokLeaderPushesFeedBusyState(t *testing.T) {
	a := newGrok(testDeps(t))
	const sid, prov = "ses_01", "11111111-2222-4333-8444-555555555555"
	step := make(chan func(f *fakeLeaderConn))
	done := leaderUp(t, a, sid, func(f *fakeLeaderConn) {
		f.handshake()
		for fn := range step {
			fn(f)
		}
	})
	live := map[string]bool{sid: true}

	if a.LeaderBusy(sid) {
		t.Fatal("busy before any push")
	}
	a.SweepLeaders(live) // the reconcile tick starts the watcher
	step <- func(f *fakeLeaderConn) {
		f.notify("_x.ai/mcp/servers_updated", map[string]any{}) // unrelated pushes are ignored
		f.notify("_x.ai/sessions/changed", sessionsChanged(prov, "working"))
	}
	eventually(t, "busy after a working push", func() bool { return a.LeaderBusy(sid) })
	step <- func(f *fakeLeaderConn) { f.notify("_x.ai/sessions/changed", sessionsChanged(prov, "idle")) }
	eventually(t, "idle after an idle push", func() bool { return !a.LeaderBusy(sid) })

	// One working session is enough: a forked session finishing must not clear it.
	step <- func(f *fakeLeaderConn) {
		f.notify("_x.ai/sessions/changed", sessionsChanged(prov, "working"))
		f.notify("_x.ai/sessions/changed", sessionsChanged("fork-1", "idle"))
	}
	eventually(t, "busy again", func() bool { return a.LeaderBusy(sid) })
	time.Sleep(50 * time.Millisecond)
	if !a.LeaderBusy(sid) {
		t.Error("another session going idle cleared this one's busy state")
	}

	// A session that is no longer live loses its watcher and its state: the
	// leader sees the disconnect, and busy reads unknown (false) again.
	close(step)
	a.SweepLeaders(map[string]bool{})
	<-done
	if a.LeaderBusy(sid) {
		t.Error("busy state outlived the session")
	}
}

func TestGrokLeaderRetryStateExhaustedFeedsTheUsageSink(t *testing.T) {
	a := newGrok(testDeps(t))
	const sid = "ses_01"
	var mu sync.Mutex
	var got []string
	a.OnExhausted(func(text string) { mu.Lock(); got = append(got, text); mu.Unlock() })
	step := make(chan func(f *fakeLeaderConn))
	done := leaderUp(t, a, sid, func(f *fakeLeaderConn) {
		f.handshake()
		for fn := range step {
			fn(f)
		}
	})
	a.SweepLeaders(map[string]bool{sid: true})
	step <- func(f *fakeLeaderConn) {
		// A retry that is still going is not exhaustion.
		f.notify("session/update", map[string]any{"sessionId": "p", "update": map[string]any{"sessionUpdate": "retry_state", "type": "retrying"}})
		f.notify("session/update", map[string]any{"sessionId": "p", "update": map[string]any{"sessionUpdate": "retry_state", "type": "exhausted",
			"message": "subscription:free-usage-exhausted: tokens (actual/limit): 619434/600000"}})
	}
	eventually(t, "the exhaustion", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) > 0 })
	close(step)
	a.SweepLeaders(map[string]bool{})
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !strings.Contains(got[0], "actual/limit") || !strings.Contains(got[0], "exhausted") {
		t.Errorf("sink got %q, want exactly the exhausted update's text", got)
	}
}

// A leader this client refuses (a grok update nobody probed) is dialled and
// logged once, not on every reconcile tick.
func TestRefusedLeaderIsNotRedialledEveryTick(t *testing.T) {
	a := newGrok(testDeps(t))
	const sid = "ses_01"
	var logs struct {
		sync.Mutex
		n int
	}
	a.d.Log = func(string, ...any) { logs.Lock(); logs.n++; logs.Unlock() }
	sock, ok := a.leaderSocket(sid)
	if !ok {
		t.Fatal("test home is too long for a leader socket")
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var dials int
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				if _, err := br.Peek(1); err != nil {
					return
				}
				f := &fakeLeaderConn{t: t, c: c, r: br}
				f.read()
				mu.Lock()
				dials++ // a client that registered, not SweepLeaders' bare liveness probe
				mu.Unlock()
				f.registered(1, "9.9.9") // a version nobody probed
			}()
		}
	}()
	for i := 0; i < 5; i++ {
		a.SweepLeaders(map[string]bool{sid: true})
		time.Sleep(100 * time.Millisecond) // the refused watcher exits on its own
	}
	mu.Lock()
	defer mu.Unlock()
	logs.Lock()
	defer logs.Unlock()
	if dials != 1 || logs.n != 1 {
		t.Errorf("over 5 ticks the refused leader was dialled %d times and logged %d times, want once each", dials, logs.n)
	}
}
