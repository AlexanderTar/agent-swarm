package mcpshim

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// safeBuffer is a bytes.Buffer a test goroutine reads while the shim's own
// goroutine writes. Every test in this file runs under `go test -race` (Step 4),
// and an unguarded bytes.Buffer would fail there — correctly, because the read
// really is concurrent with the write.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitUntil polls a condition for up to 5 s. The shim is a goroutine pumping
// lines, so there is no synchronous point to assert at.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the condition")
}

func TestUnset(t *testing.T) {
	for _, v := range []string{"", "${env:SWARM_SESSION}", "${env:ANYTHING}"} {
		if !Unset(v) {
			t.Errorf("%q must count as unset (L16, P0-1)", v)
		}
	}
	for _, v := range []string{"ses_1", "/path/to/token", "x${env:y}"} {
		if Unset(v) {
			t.Errorf("%q is set", v)
		}
	}
}

func TestTokenFromEnvPrefersTheSessionToken(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "ses_1")
	os.WriteFile(tok, []byte("session-secret\n"), 0o600)
	url, token, kind, bound := TokenFromEnv(func(k string) string {
		return map[string]string{"SWARM_URL": "http://127.0.0.1:17778", "SWARM_SESSION": "ses_1",
			"SWARM_TOKEN_FILE": tok, "SWARM_AGENT_KIND": "claude"}[k]
	})
	if !bound || token != "session-secret" || kind != "claude" || url != "http://127.0.0.1:17778" {
		t.Fatalf("got %q %q %q %v", url, token, kind, bound)
	}
	// cursor's literal placeholders mean unbound
	_, _, _, bound = TokenFromEnv(func(k string) string {
		return map[string]string{"SWARM_SESSION": "${env:SWARM_SESSION}",
			"SWARM_TOKEN_FILE": "${env:SWARM_TOKEN_FILE}", "SWARM_AGENT_KIND": "cursor"}[k]
	})
	if bound {
		t.Fatal("a ${env:…} placeholder means the shim is unbound (L16)")
	}
}

func TestRunForwardsJSONRPCUnchanged(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
		if r.Header.Get("Authorization") != "Bearer session-secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer srv.Close()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var out strings.Builder
	s := &Shim{In: in, Out: &out, Err: io.Discard, URL: srv.URL, Token: "session-secret",
		AgentKind: "codex", HTTP: srv.Client(), Log: func(string, ...any) {}}
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0], `"method":"tools/list"`) {
		t.Fatalf("forwarded %v", got)
	}
	if !strings.Contains(out.String(), `"result":{"tools":[]}`) {
		t.Fatalf("stdout = %q", out.String())
	}
}

// §11.3: only for claude, and only on the initialize response.
func TestInitializeGetsTheChannelCapabilityForClaudeOnly(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"serverInfo":{"name":"swarm"}}}`
	for _, c := range []struct {
		kind string
		want bool
	}{{"claude", true}, {"codex", false}, {"cursor", false}, {"agy", false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		var out strings.Builder
		s := &Shim{In: strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"),
			Out: &out, Err: io.Discard, URL: srv.URL, Token: "t", AgentKind: c.kind,
			HTTP: srv.Client(), Log: func(string, ...any) {}}
		s.Run(context.Background())
		var resp struct {
			Result struct {
				Capabilities struct {
					Experimental map[string]any `json:"experimental"`
				} `json:"capabilities"`
			} `json:"result"`
		}
		json.Unmarshal([]byte(strings.TrimSpace(out.String())), &resp)
		_, has := resp.Result.Capabilities.Experimental["claude/channel"]
		if has != c.want {
			t.Errorf("%s: channel capability = %v, want %v (%s)", c.kind, has, c.want, out.String())
		}
		srv.Close()
	}
}

// The brief's handler for this test used `for notice := range wake` on a
// channel that is never closed: httptest.Server.Close() blocks until every
// handler goroutine returns, so that form deadlocks the test on teardown. The
// handler here selects on the request's own context instead, which the shim's
// bridgeWake cancels (via ctx passed to http.NewRequestWithContext) once
// cancel()+pw.Close() run at the end of the test.
func TestWakeEventBecomesExactlyOneChannelNotification(t *testing.T) {
	wake := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions/self/wake" {
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			fl.Flush()
			for {
				select {
				case notice := <-wake:
					io.WriteString(w, "event: wake\ndata: "+notice+"\n\n")
					fl.Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()
	pr, pw := io.Pipe()
	var out safeBuffer
	s := &Shim{In: pr, Out: &out, Err: io.Discard, URL: srv.URL, Token: "t",
		AgentKind: "claude", HTTP: srv.Client(), Log: func(string, ...any) {}}
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	wake <- `{"content":"[swarm] 1 new message(s) for a (TASK-1). Call swarm_sync.","msg_id":"msg_1"}`
	waitUntil(t, func() bool { return strings.Contains(out.String(), "notifications/claude/channel") })
	var n int
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.Contains(line, "notifications/claude/channel") {
			n++
			var m struct {
				Method string `json:"method"`
				Params struct {
					Content string            `json:"content"`
					Meta    map[string]string `json:"meta"`
				} `json:"params"`
			}
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("channel frame is not JSON: %q", line)
			}
			if m.Params.Meta["swarm_msg"] != "msg_1" || !strings.HasPrefix(m.Params.Content, "[swarm]") {
				t.Fatalf("frame = %s", line)
			}
		}
	}
	if n != 1 {
		t.Fatalf("emitted %d channel frames for one wake event", n)
	}
	cancel()
	pw.Close()
}

// The shim reconnects the wake stream after the daemon restarts.
func TestWakeStreamReconnects(t *testing.T) {
	var gets int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/self/wake" {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			return
		}
		n := atomic.AddInt32(&gets, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		if n == 1 {
			return // the daemon "restarts": the first stream simply ends
		}
		<-release // hold the second one open so the test can observe it
	}))
	defer srv.Close()
	pr, pw := io.Pipe()
	defer pw.Close()
	var out safeBuffer
	s := &Shim{In: pr, Out: &out, Err: io.Discard, URL: srv.URL, Token: "t",
		AgentKind: "claude", HTTP: srv.Client(), Backoff: time.Millisecond,
		Log: func(string, ...any) {}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitUntil(t, func() bool { return atomic.LoadInt32(&gets) >= 2 })
	close(release)
}

// A daemon that is down gives an MCP error, not a hang.
func TestToolCallWithTheDaemonDownReturnsAnError(t *testing.T) {
	pr, pw := io.Pipe()
	var out safeBuffer
	s := &Shim{In: pr, Out: &out, Err: io.Discard, URL: "http://127.0.0.1:1", Token: "t",
		AgentKind: "codex", HTTP: &http.Client{Timeout: 200 * time.Millisecond},
		Log: func(string, ...any) {}}
	go s.Run(context.Background())
	io.WriteString(pw, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"swarm_sync"}}`+"\n")
	waitUntil(t, func() bool { return strings.Contains(out.String(), `"error"`) })
	var resp struct {
		ID    int `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal([]byte(strings.TrimSpace(out.String())), &resp)
	if resp.ID != 7 || resp.Error.Message == "" {
		t.Fatalf("response = %q", out.String())
	}
	pw.Close()
}

// A notification (no id) gets no response line.
func TestNotificationsGetNoResponseLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	var out strings.Builder
	s := &Shim{In: strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"),
		Out: &out, Err: io.Discard, URL: srv.URL, Token: "t", AgentKind: "codex",
		HTTP: srv.Client(), Log: func(string, ...any) {}}
	s.Run(context.Background())
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("stdout = %q", out.String())
	}
}
