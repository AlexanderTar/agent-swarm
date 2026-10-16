package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

func wakeTarget() WakeTarget {
	return WakeTarget{SessionID: "ses_01", AgentID: "ag_01", ProviderSessionID: "11111111-2222-4333-8444-555555555555",
		TmuxName: "grok-coder", Notice: "You have 1 new message. Call swarm_sync."}
}

// leaderUp serves the session's own leader socket until the script returns.
func leaderUp(t *testing.T, a *Grok, sessionID string, script func(f *fakeLeaderConn)) <-chan struct{} {
	t.Helper()
	sock, ok := a.leaderSocket(sessionID)
	if !ok {
		t.Fatal("test home is too long for a leader socket")
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	return fakeLeaderAt(t, sock, script)
}

func TestGrokWakeViaLeader(t *testing.T) {
	t.Run("sends session/prompt and returns without waiting for the turn", func(t *testing.T) {
		d := testDeps(t)
		a := newGrok(d)
		w := wakeTarget()
		got := make(chan map[string]any, 1)
		release := make(chan struct{})
		done := leaderUp(t, a, w.SessionID, func(f *fakeLeaderConn) {
			f.handshake()
			p := f.acp()
			got <- p
			<-release // the queued turn is still running
			f.reply(p["id"], map[string]any{"stopReason": "end_turn"})
		})
		delivered, err := a.Wake(context.Background(), w)
		if err != nil || !delivered {
			t.Fatalf("Wake = %v, %v, want true, nil while the turn is still running", delivered, err)
		}
		select {
		case p := <-got:
			if p["method"] != "session/prompt" {
				t.Errorf("method = %v", p["method"])
			}
			want := `{"prompt":[{"text":"` + w.Notice + `","type":"text"}],"sessionId":"` + w.ProviderSessionID + `"}`
			if mustJSON(p["params"]) != want {
				t.Errorf("params = %s, want %s", mustJSON(p["params"]), want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the leader never saw the prompt")
		}
		close(release)
		<-done
	})

	t.Run("a rate-limited prompt still counts as delivered", func(t *testing.T) {
		// The prompt reached the session (probe: -32003 after the user message
		// showed in the pane); a paste on top would duplicate it.
		a := newGrok(testDeps(t))
		w := wakeTarget()
		var logs struct {
			sync.Mutex
			s []string
		}
		a.d.Log = func(f string, _ ...any) { logs.Lock(); logs.s = append(logs.s, f); logs.Unlock() }
		done := leaderUp(t, a, w.SessionID, func(f *fakeLeaderConn) {
			f.handshake()
			p := f.acp()
			f.write(map[string]any{"type": "acp", "payload": `{"jsonrpc":"2.0","id":` + mustJSON(p["id"]) +
				`,"error":{"code":-32003,"message":"Rate limited"}}`})
		})
		if delivered, err := a.Wake(context.Background(), w); err != nil || !delivered {
			t.Fatalf("Wake = %v, %v", delivered, err)
		}
		<-done
	})

	t.Run("falls back to the paste when the leader is down", func(t *testing.T) {
		d := testDeps(t)
		d.Start = func(context.Context, string, ...string) (*execx.Proc, error) {
			t.Error("Wake must not start a process")
			return nil, nil
		}
		a := newGrok(d)
		delivered, err := a.Wake(context.Background(), wakeTarget())
		if err != nil || delivered {
			t.Fatalf("Wake = %v, %v, want false, nil so the runtime pastes", delivered, err)
		}
	})

	t.Run("falls back when the session has no provider id yet", func(t *testing.T) {
		a := newGrok(testDeps(t))
		w := wakeTarget()
		w.ProviderSessionID = ""
		leaderUp(t, a, w.SessionID, func(f *fakeLeaderConn) { f.handshake() })
		if delivered, err := a.Wake(context.Background(), w); err != nil || delivered {
			t.Fatalf("Wake = %v, %v, want false, nil", delivered, err)
		}
	})

	t.Run("falls back, without a wake failure, when the leader is a version nobody probed", func(t *testing.T) {
		a := newGrok(testDeps(t))
		var logged string
		a.d.Log = func(f string, _ ...any) { logged = f }
		w := wakeTarget()
		leaderUp(t, a, w.SessionID, func(f *fakeLeaderConn) {
			f.read()
			f.registered(1, "9.9.9")
		})
		// An error would make the runtime back off the paste too; the paste is
		// exactly what this case needs.
		delivered, err := a.Wake(context.Background(), w)
		if err != nil || delivered {
			t.Fatalf("Wake = %v, %v, want false, nil", delivered, err)
		}
		if !strings.Contains(logged, "leader") {
			t.Errorf("the refusal must be logged, got %q", logged)
		}
	})
}

func TestLeaderClientRefusalIsErrUnsupportedLeader(t *testing.T) {
	path, _ := fakeLeader(t, func(f *fakeLeaderConn) {
		f.read()
		f.registered(2, "1.0.60")
	})
	_, err := dialLeader(context.Background(), path)
	if !errors.Is(err, ErrUnsupportedLeader) {
		t.Fatalf("err = %v, want ErrUnsupportedLeader", err)
	}
}

func TestGrokPasteReadyOnlyWithoutACard(t *testing.T) {
	a := newGrok(testDeps(t))
	for fixture, want := range map[string]bool{
		"idle": true, "busy-tool": true, "busy-paste-queued": true, // mid-turn paste + Enter queues
		"multiline-pasted":      true,
		"usage-modal":           false,
		"free-usage-limit-card": false,
	} {
		if got := a.PasteReady(grokPane(t, fixture)); got != want {
			t.Errorf("PasteReady(%s) = %v, want %v", fixture, got, want)
		}
	}
}

func TestGrokRenameViaLeader(t *testing.T) {
	w := wakeTarget()
	t.Run("renames over the socket when the leader is up", func(t *testing.T) {
		a := newGrok(testDeps(t))
		got := make(chan map[string]any, 1)
		done := leaderUp(t, a, w.SessionID, func(f *fakeLeaderConn) {
			f.handshake()
			p := f.acp()
			got <- p
			f.reply(p["id"], map[string]any{"success": true})
		})
		if !a.RenameViaLeader(context.Background(), w.SessionID, w.ProviderSessionID, "grok-coder") {
			t.Fatal("RenameViaLeader = false with the leader up")
		}
		p := <-got
		if p["method"] != "_x.ai/session/rename" ||
			mustJSON(p["params"]) != `{"sessionId":"`+w.ProviderSessionID+`","title":"grok-coder"}` {
			t.Errorf("call = %v", p)
		}
		<-done
	})
	t.Run("false when the leader is down or the session unknown", func(t *testing.T) {
		a := newGrok(testDeps(t))
		if a.RenameViaLeader(context.Background(), w.SessionID, w.ProviderSessionID, "x") {
			t.Error("true with no leader")
		}
		if a.RenameViaLeader(context.Background(), w.SessionID, "", "x") {
			t.Error("true with no provider session id")
		}
	})
}

// A working session already gets its mail through the hooks (additionalContext
// on tool use, the once-per-turn Stop block), so a queued session/prompt would
// only run an extra metered turn after it has synced. delivered=false hands the
// notice back to the runtime, which retries the native wake next tick.
func TestGrokWakeWaitsOutARunningTurn(t *testing.T) {
	a := newGrok(testDeps(t))
	w := wakeTarget()
	prompted := make(chan struct{}, 1)
	leaderUp(t, a, w.SessionID, func(f *fakeLeaderConn) {
		f.handshake()
		p := f.acp()
		prompted <- struct{}{}
		f.reply(p["id"], map[string]any{"stopReason": "end_turn"})
	})
	a.leader.busy = map[string]map[string]bool{w.SessionID: {w.ProviderSessionID: true}}
	delivered, err := a.Wake(context.Background(), w)
	if err != nil || delivered {
		t.Fatalf("Wake = %v, %v, want false, nil while the leader reports a turn running", delivered, err)
	}
	select {
	case <-prompted:
		t.Fatal("a session/prompt was sent while a turn was running")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestGrokUsageLimitCardMarksExhaustion(t *testing.T) {
	a := newGrok(Deps{})
	var got []string
	a.OnExhausted(func(text string) { got = append(got, text) })
	a.ObserveCapture("ses_01", grokPane(t, "idle"))
	a.ObserveCapture("ses_01", grokPane(t, "question-card"))
	if len(got) != 0 {
		t.Fatalf("exhaustion noted for a pane without the usage-limit card: %q", got)
	}
	a.ObserveCapture("ses_01", grokPane(t, "free-usage-limit-card"))
	if len(got) != 1 || !grokUsageLimit.MatchString(got[0]) {
		t.Fatalf("exhaustion notes = %q, want one carrying the card's usage-limit line", got)
	}
}
