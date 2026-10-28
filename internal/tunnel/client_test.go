package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/AlexanderTar/swarmrun/internal/httpapi"
)

const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// tcpPair returns two connected ends; real sockets because yamux over
// net.Pipe can stall on its unbuffered writes.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() { c, err := ln.Accept(); ch <- res{c, err} }()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { a.Close(); r.c.Close() })
	return a, r.c
}

// clock is a controllable time source; Sleep records the delay and returns at once.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	return ctx.Err()
}
func (c *clock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

func newClock() *clock { return &clock{now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)} }

func newClient(clk *clock, h http.Handler, dial func(context.Context, string, string) (net.Conn, error)) *Client {
	return &Client{
		URL: "wss://relay.example", Secret: testSecret, Handler: h,
		Dial: dial, Now: clk.Now, Sleep: clk.Sleep, Rand: func() float64 { return 0 },
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// relaySide plays the relay: a yamux client over its end of the pair.
func relaySide(t *testing.T, conn net.Conn) *http.Client {
	t.Helper()
	sess, err := yamux.Client(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) { return sess.Open() },
	}}
}

func TestClientServesTunnelOriginAndTracksStatus(t *testing.T) {
	clk := newClock()
	var gotOrigin httpapi.Origin
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrigin = httpapi.RequestOrigin(r)
		io.WriteString(w, "hi")
	})
	cliEnd, relayEnd := tcpPair(t)
	var gotURL, gotSecret string
	c := newClient(clk, h, func(_ context.Context, url, secret string) (net.Conn, error) {
		gotURL, gotSecret = url, secret
		return cliEnd, nil
	})
	if st := c.Status(); st.State != "disabled" {
		t.Fatalf("before Run: %+v", st)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	hc := relaySide(t, relayEnd)
	// Host localhost and a loopback RemoteAddr must still classify as tunnel
	req, _ := http.NewRequest("GET", "http://tunnel/x", nil)
	req.Host = "localhost"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "hi" || gotOrigin != httpapi.OriginTunnel {
		t.Fatalf("body %q origin %q", b, gotOrigin)
	}
	if gotURL != "wss://relay.example/_tunnel" || gotSecret != testSecret {
		t.Fatalf("dialed %q with a different secret", gotURL)
	}
	st := c.Status()
	if st.State != "connected" || st.URL != "wss://relay.example" || !st.ConnectedSince.Equal(clk.Now()) || st.Reconnects != 0 {
		t.Fatalf("status %+v", st)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if st := c.Status(); st.State != "disabled" {
		t.Fatalf("after cancel: %+v", st)
	}
}

func TestClientAnswers501ToUpgrade(t *testing.T) {
	cliEnd, relayEnd := tcpPair(t)
	called := false
	c := newClient(newClock(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }),
		func(context.Context, string, string) (net.Conn, error) { return cliEnd, nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	hc := relaySide(t, relayEnd)
	req, _ := http.NewRequest("GET", "http://tunnel/api/events", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented || called {
		t.Fatalf("status %d, handler called %v", resp.StatusCode, called)
	}
}

func TestClientBackoffGrowsToSixtySecondsWithJitter(t *testing.T) {
	for name, tc := range map[string]struct {
		rand float64
		want []time.Duration
	}{
		"no jitter": {0, secs(1, 2, 4, 8, 16, 32, 60, 60)},
		"max jitter": {1, []time.Duration{1250 * time.Millisecond, 2500 * time.Millisecond, 5 * time.Second,
			10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second, 60 * time.Second}},
	} {
		clk := newClock()
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		c := newClient(clk, http.NotFoundHandler(), func(context.Context, string, string) (net.Conn, error) {
			calls++
			if calls == 9 {
				cancel()
			}
			return nil, errors.New("boom")
		})
		c.Rand = func() float64 { return tc.rand }
		if err := c.Run(ctx); err != nil {
			t.Fatalf("%s: Run = %v", name, err)
		}
		got := clk.slept()
		if len(got) < len(tc.want) {
			t.Fatalf("%s: slept %v", name, got)
		}
		for i, w := range tc.want {
			if got[i] != w {
				t.Errorf("%s: sleep %d = %v, want %v", name, i, got[i], w)
			}
		}
	}
}

func secs(n ...int) []time.Duration {
	var out []time.Duration
	for _, s := range n {
		out = append(out, time.Duration(s)*time.Second)
	}
	return out
}

func TestClientStatusErrorAndReconnects(t *testing.T) {
	clk := newClock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	block := make(chan struct{})
	c := newClient(clk, http.NotFoundHandler(), func(context.Context, string, string) (net.Conn, error) {
		if calls.Add(1) == 3 {
			<-block
		}
		return nil, errors.New("relay rejected the secret (401)")
	})
	go c.Run(ctx)
	eventually(t, "third dial", func() bool { return calls.Load() == 3 })
	st := c.Status()
	if st.State != "connecting" || st.Reconnects != 2 || st.LastError != "relay rejected the secret (401)" {
		t.Fatalf("while dialing: %+v", st)
	}
	close(block)
	eventually(t, "error state", func() bool { return c.Status().State == "error" })
	if st := c.Status(); st.LastError == "" || strings.Contains(st.LastError, testSecret) {
		t.Fatalf("error status %+v", st)
	}
}

func TestClientResetsBackoffAfterAStableSession(t *testing.T) {
	clk := newClock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ends []net.Conn
	var mu sync.Mutex
	calls := 0
	c := newClient(clk, http.NotFoundHandler(), func(context.Context, string, string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch calls {
		case 1, 2: // fail twice: delays 1s, 2s
			return nil, errors.New("down")
		case 3, 4: // sessions that drop
			a, b := tcpPair(t)
			ends = append(ends, b)
			return a, nil
		}
		cancel()
		return nil, errors.New("done")
	})
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	// call 3 drops after 61 s up (stable), call 4 drops at once (flapping)
	eventually(t, "session 1", func() bool { mu.Lock(); defer mu.Unlock(); return len(ends) == 1 })
	eventually(t, "connected", func() bool { return c.Status().State == "connected" })
	clk.advance(61 * time.Second)
	ends[0].Close()
	eventually(t, "session 2", func() bool { mu.Lock(); defer mu.Unlock(); return len(ends) == 2 })
	eventually(t, "connected again", func() bool { return c.Status().State == "connected" })
	ends[1].Close()
	<-done
	// 1s, 2s (two failures), then after the stable session 1s again, then 2s for the flap
	want := secs(1, 2, 1, 2)
	got := clk.slept()
	if len(got) != len(want) {
		t.Fatalf("slept %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slept %v, want %v", got, want)
		}
	}
}

func TestDefaultDialerRejectedSecretIsReportedWithoutLeakingIt(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := dialWebSocket(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/_tunnel", testSecret)
	if err == nil {
		t.Fatal("dial succeeded")
	}
	if gotAuth != "Bearer "+testSecret {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), testSecret) {
		t.Errorf("error %q", err)
	}
}
