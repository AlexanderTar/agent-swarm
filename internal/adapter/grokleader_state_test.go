package adapter

import (
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
