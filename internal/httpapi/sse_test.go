package httpapi

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/events"
)

type sseEvent struct {
	id   int64
	typ  string
	data string
}

// stream opens /api/events and returns a channel of parsed events.
func (e *env) stream(t *testing.T, query string, lastID string) (<-chan sseEvent, func()) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/events"+query, nil)
	req.Header.Set("Authorization", "Bearer "+daemonToken)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	ch := make(chan sseEvent, 1000)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				cur.id, _ = strconv.ParseInt(line[4:], 10, 64)
			case strings.HasPrefix(line, "event: "):
				cur.typ = line[7:]
			case strings.HasPrefix(line, "data: "):
				cur.data = line[6:]
			case line == "":
				if cur.typ != "" {
					ch <- cur
				}
				cur = sseEvent{}
			}
		}
	}()
	return ch, func() { resp.Body.Close() }
}

func next(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream closed")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return sseEvent{}
}

func TestEventsArriveInOrderAndReplay(t *testing.T) {
	e := newEnv(t)
	start, _ := e.events.Latest(bg)
	ch, stop := e.stream(t, fmt.Sprintf("?after=%d", start), "")
	defer stop()
	for i := 1; i <= 3; i++ {
		e.events.Publish(bg, events.ItemChanged, map[string]string{"key": fmt.Sprint("TASK-", i), "root_key": "EPIC-1"})
	}
	var last int64
	for i := 1; i <= 3; i++ {
		ev := next(t, ch)
		if ev.typ != "item.changed" || ev.id <= last || !strings.Contains(ev.data, fmt.Sprintf(`"key":"TASK-%d"`, i)) {
			t.Fatalf("event %d = %+v", i, ev)
		}
		last = ev.id
	}
	stop()

	// reconnect with Last-Event-ID: only what came after it
	e.events.Publish(bg, events.SettingsChanged, map[string]int{"n": 4})
	replay, stop2 := e.stream(t, "?after=0", strconv.FormatInt(last, 10))
	defer stop2()
	ev := next(t, replay)
	if ev.typ != "settings.changed" || ev.id != last+1 {
		t.Fatalf("replay = %+v", ev)
	}
}

func TestNoCursorStreamsOnlyNewEvents(t *testing.T) {
	e := newEnv(t)
	e.events.Publish(bg, events.ItemChanged, map[string]string{"key": "OLD-1"})
	latest, _ := e.events.Latest(bg)
	ch, stop := e.stream(t, "", "")
	defer stop()
	e.events.Publish(bg, events.ItemChanged, map[string]string{"key": "NEW-1"})
	if ev := next(t, ch); ev.id != latest+1 || !strings.Contains(ev.data, "NEW-1") {
		t.Fatalf("first event = %+v", ev)
	}
}

func TestExpiredCursorGetsReset(t *testing.T) {
	e := newEnv(t)
	latest, _ := e.events.Latest(bg)
	ch, stop := e.stream(t, fmt.Sprintf("?after=%d", latest+100), "")
	defer stop()
	if ev := next(t, ch); ev.typ != "reset" || ev.data != "{}" {
		t.Fatalf("first event = %+v", ev)
	}
	e.events.Publish(bg, events.ItemChanged, map[string]string{"key": "EPIC-1"})
	if ev := next(t, ch); ev.typ != "item.changed" || ev.id != latest+1 {
		t.Fatalf("after reset = %+v", ev)
	}
	if status, _ := e.api("GET", "/api/events?after=abc", nil); status != 400 {
		t.Fatalf("bad cursor = %d", status)
	}
}

func TestSlowClientDoesNotBlockOthers(t *testing.T) {
	e := newEnv(t)
	start, _ := e.events.Latest(bg)
	fast, stop := e.stream(t, fmt.Sprintf("?after=%d", start), "")
	defer stop()

	slow, err := net.Dial("tcp", strings.TrimPrefix(e.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	slow.(*net.TCPConn).SetReadBuffer(4096) // stall quickly
	fmt.Fprintf(slow, "GET /api/events?after=%d HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: Bearer %s\r\n\r\n", start, daemonToken)
	// wait for the status line: the handler has flushed its headers and is streaming
	slow.SetReadDeadline(time.Now().Add(3 * time.Second))
	if line, err := bufio.NewReader(slow).ReadString('\n'); err != nil || !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("slow client status = %q %v", line, err)
	}

	big := strings.Repeat("x", 64_000)
	const n = 300
	for i := range n {
		e.events.Publish(bg, events.ItemChanged, map[string]any{"i": i, "pad": big})
	}
	deadline := time.After(10 * time.Second)
	got := 0
	for got < n {
		select {
		case ev, ok := <-fast:
			if !ok {
				t.Fatalf("fast client dropped after %d events", got)
			}
			if ev.typ == "item.changed" {
				got++
			}
		case <-deadline:
			t.Fatalf("fast client got %d of %d events", got, n)
		}
	}
	// wait past the write deadline before draining (draining would unblock the stalled write);
	// this waits out a configured timeout, not a scheduling race
	time.Sleep(3 * e.s.WriteTimeout)
	// the stalled connection is closed by the server: draining it ends in EOF, not a timeout
	slow.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err = io.Copy(io.Discard, slow)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("server kept the stalled stream open")
	}
}
