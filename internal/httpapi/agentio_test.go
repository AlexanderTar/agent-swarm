package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHookRouteReturnsTheAgentSpecificOutput(t *testing.T) {
	s, seed := newAgentIOServer(t)
	rec := s.postToken(t, seed.SessionToken, "/hook/claude/SessionStart", `{"session_id":"p1","source":"startup"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "additionalContext") {
		t.Fatalf("body = %s", rec.Body)
	}
}

// P0-10 in Go: a revoked token fails closed.
func TestHookRouteRejectsARevokedToken(t *testing.T) {
	s, seed := newAgentIOServer(t)
	rec := s.postToken(t, seed.OldGenerationToken, "/hook/claude/Stop", `{}`)
	if rec.Code != 401 {
		t.Fatalf("status = %d", rec.Code)
	}
	rec = s.postToken(t, "nonsense", "/hook/claude/Stop", `{}`)
	if rec.Code != 401 {
		t.Fatalf("status = %d", rec.Code)
	}
}

// §11.2: the daemon answers within 1500 ms.
func TestHookRouteIsFast(t *testing.T) {
	s, seed := newAgentIOServer(t)
	start := time.Now()
	s.postToken(t, seed.SessionToken, "/hook/claude/PostToolUse", `{"session_id":"p1"}`)
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("took %v", d)
	}
}

func TestMCPAcceptsASessionTokenAndTheDaemonToken(t *testing.T) {
	s, seed := newAgentIOServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	bound := s.postToken(t, seed.SessionToken, "/mcp", body)
	if bound.Code != 200 {
		t.Fatalf("session token = %d: %s", bound.Code, bound.Body)
	}
	if !strings.Contains(bound.Body.String(), "swarm_checkpoint") {
		t.Fatalf("a bound caller sees the full set: %s", bound.Body)
	}
	unbound := s.postToken(t, s.Token, "/mcp", body)
	if unbound.Code != 200 {
		t.Fatalf("daemon token = %d: %s", unbound.Code, unbound.Body)
	}
	if strings.Contains(unbound.Body.String(), "swarm_checkpoint") {
		t.Fatalf("an unbound caller sees only swarm_read and swarm_kb: %s", unbound.Body)
	}
	if !strings.Contains(unbound.Body.String(), "swarm_read") {
		t.Fatalf("unbound body = %s", unbound.Body)
	}
	none := httptest.NewRecorder()
	s.Handler().ServeHTTP(none, httptest.NewRequest("POST", "/mcp", strings.NewReader(body)))
	if none.Code != 401 {
		t.Fatalf("no token = %d", none.Code)
	}
}

// §7.1: wake events go only through this stream, never through /api/events.
// D80: an httptest.ResponseRecorder is NOT safe to read from the test goroutine
// while a handler goroutine writes it — `go test -race` fails on that, correctly,
// and Step 4 runs -race. A streaming response is read over a real connection: the
// harness already has an httptest.Server, so this goes through it and reads the
// body with a bufio.Scanner, which is what an SSE client actually does.
func TestWakeStreamDeliversOnlyToItsOwnSession(t *testing.T) {
	s, seed := newAgentIOServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.srv.URL+"/api/sessions/self/wake", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+seed.SessionToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if err := s.RT.PublishWake(context.Background(), seed.SessionID, "[swarm] 1 new message(s)"); err != nil {
		t.Fatal(err)
	}
	// Read until the wake event arrives. The server writes it as two lines, so read
	// on this goroutine and let the deadline below be the failure mode.
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			default:
			}
		}
		close(lines)
	}()
	var sawEvent, sawData bool
	deadline := time.After(5 * time.Second)
	for !(sawEvent && sawData) {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("the stream closed before the wake arrived")
			}
			if line == "event: wake" {
				sawEvent = true
			}
			if strings.Contains(line, "[swarm] 1 new message(s)") {
				sawData = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for the wake event")
		}
	}
	cancel()
	// and nothing about wakes appears on the general event stream. /api/events
	// is a long-lived stream (P1's own sse_test.go reads it with env.stream,
	// never with env.api/get, which would block forever on the open body), so
	// this drains whatever arrives in a short window rather than the full body.
	evStream, evUnsub := s.stream(t, "?after=0", "")
	defer evUnsub()
	drain := time.After(200 * time.Millisecond)
drainLoop:
	for {
		select {
		case ev, ok := <-evStream:
			if !ok {
				break drainLoop
			}
			if ev.typ == "wake" {
				t.Fatalf("wake events are never broadcast on /api/events (§7.1): %+v", ev)
			}
		case <-drain:
			break drainLoop
		}
	}
}

func TestWakeStreamRejectsTheDaemonToken(t *testing.T) {
	s, _ := newAgentIOServer(t)
	code, _ := s.call(http.MethodGet, "/api/sessions/self/wake", nil, daemonToken)
	if code != 401 {
		t.Fatalf("status = %d; the wake stream is per session", code)
	}
}

// The hook route also feeds the native advisor scanner (§11.6).
func TestHookRouteTriggersTheTranscriptScan(t *testing.T) {
	s, seed := newAgentIOServerWithAdvisorTranscript(t)
	s.postToken(t, seed.SessionToken, "/hook/claude/Stop",
		`{"session_id":"p1","transcript_path":"`+seed.TranscriptPath+`"}`)
	rec := s.get(t, "/api/agents/"+seed.AgentName+"/advice")
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	var native int
	for _, r := range list {
		if r["mode"] == "native" {
			native++
		}
	}
	if native != 1 {
		t.Fatalf("native advice rows = %d", native)
	}
}
