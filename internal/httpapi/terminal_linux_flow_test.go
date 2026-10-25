package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/desktop"
)

// terminalSpy records what the Linux terminal flow and the macOS Ghostty
// fallback were asked to run. After fires at once, so a fallback that was
// wrongly armed would run before the test looks.
type terminalSpy struct {
	mu        sync.Mutex
	opened    [][]string
	fallbacks [][]string
	err       error
}

func (sp *terminalSpy) deps(d *Deps) {
	d.Terminal = func(_ context.Context, command []string) error {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		sp.opened = append(sp.opened, command)
		return sp.err
	}
	d.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		sp.fallbacks = append(sp.fallbacks, append([]string{name}, args...))
		return nil, nil
	}
	d.After = func(time.Duration) <-chan time.Time {
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
}

func (sp *terminalSpy) calls() ([][]string, [][]string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return append([][]string(nil), sp.opened...), append([][]string(nil), sp.fallbacks...)
}

// Linux: no menu bar will answer, so the daemon opens the terminal before it
// replies, on the store's tmux binary and socket (R11, S-1, S-2), and the
// Ghostty fallback never runs.
func TestTerminalOpensADesktopTerminalWhenOneIsWired(t *testing.T) {
	sp := &terminalSpy{}
	s, _ := newRuntimeServerWith(t, sp.deps)
	name := seedRunningAgent(t, s)
	rec := s.post(t, "/api/agents/"+name+"/terminal", `{}`)
	body := decode[terminalWire](t, rec.Body.Bytes())
	if rec.Code != 200 || body.OpenedBy != "daemon" || body.Tmux != name || body.TmuxSocket != s.RT.TmuxSocket() {
		t.Fatalf("status = %d, body = %+v", rec.Code, body)
	}
	opened, _ := sp.calls()
	want := []string{s.RT.TmuxBin(), "-L", s.RT.TmuxSocket(), "attach", "-t", name}
	if len(opened) != 1 || !slices.Equal(opened[0], want) {
		t.Fatalf("opened = %q, want %q", opened, want)
	}
	time.Sleep(50 * time.Millisecond) // a wrongly armed fallback would have fired by now
	if _, fb := sp.calls(); len(fb) != 0 {
		t.Fatalf("the Ghostty fallback ran on Linux: %q", fb)
	}
}

func TestTerminalReportsWhyNoWindowOpened(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w. Install one of ghostty, xterm.", desktop.ErrNoTerminal), 409, "conflict"},
		{desktop.ErrNoSession, 409, "conflict"},
		{errors.New("kitty exited at once: exit status 1: cannot open display"), 500, "internal"},
	} {
		sp := &terminalSpy{err: tc.err}
		s, _ := newRuntimeServerWith(t, sp.deps)
		name := seedRunningAgent(t, s)
		rec := s.post(t, "/api/agents/"+name+"/terminal", `{}`)
		wantErr(t, rec.Code, rec.Body.Bytes(), tc.status, tc.code, tc.err.Error())
	}
}

// swarm attach attaches in its own terminal: the daemon opens no window for
// it, neither the Linux launcher nor the macOS Ghostty fallback. (The
// terminal.open event is still published, so a macOS menu bar still reacts
// to it as before; that is outside the daemon.)
func TestTerminalInPlaceMakesTheDaemonOpenNoWindow(t *testing.T) {
	for _, wired := range []bool{true, false} {
		sp := &terminalSpy{}
		s, _ := newRuntimeServerWith(t, func(d *Deps) {
			sp.deps(d)
			if !wired {
				d.Terminal = nil
			}
		})
		name := seedRunningAgent(t, s)
		rec := s.post(t, "/api/agents/"+name+"/terminal", `{"in_place":true}`)
		body := decode[terminalWire](t, rec.Body.Bytes())
		if rec.Code != 200 || body.OpenedBy != "cli" || body.Tmux != name || body.TmuxSocket != s.RT.TmuxSocket() {
			t.Fatalf("wired=%v: status = %d, body = %+v", wired, rec.Code, body)
		}
		time.Sleep(50 * time.Millisecond)
		if opened, fb := sp.calls(); len(opened)+len(fb) != 0 {
			t.Fatalf("wired=%v: in-place attach opened %q / fell back to %q", wired, opened, fb)
		}
	}
}

func TestTerminalRejectsABadBody(t *testing.T) {
	sp := &terminalSpy{}
	s, _ := newRuntimeServerWith(t, sp.deps)
	name := seedRunningAgent(t, s)
	// Raw bytes: s.post re-marshals its body, which can't carry malformed JSON.
	req, _ := http.NewRequest(http.MethodPost, s.env.srv.URL+"/api/agents/"+name+"/terminal", strings.NewReader(`{"in_place":`))
	req.Header.Set("Authorization", "Bearer "+daemonToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	wantErr(t, resp.StatusCode, body, 400, "bad_request", "")
	if opened, fb := sp.calls(); len(opened)+len(fb) != 0 {
		t.Fatalf("a rejected request opened %q / %q", opened, fb)
	}
}
