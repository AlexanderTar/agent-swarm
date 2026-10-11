package tunnel_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/httpapi"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/relay"
	"github.com/AlexanderTar/agent-swarm/internal/tunnel"
	"github.com/AlexanderTar/agent-swarm/internal/webauth"
)

const (
	secret   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	email    = "me@example.com"
	password = "correct horse battery"
)

// liveRelay is a relay on a fixed address so a test can stop and restart it.
type liveRelay struct {
	t    *testing.T
	addr string
	srv  *http.Server
	ln   *trackingListener
}

// trackingListener remembers accepted conns: http.Server.Close leaves hijacked
// (WebSocket) conns open, but a dead relay process takes them all down.
type trackingListener struct {
	net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.mu.Lock()
		l.conns = append(l.conns, c)
		l.mu.Unlock()
	}
	return c, err
}

func (l *trackingListener) kill() {
	l.Listener.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		c.Close()
	}
}

func startRelay(t *testing.T, addr string) *liveRelay {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	tl := &trackingListener{Listener: ln}
	r := &liveRelay{t: t, addr: ln.Addr().String(), srv: relay.NewServer("", relay.New(secret, relay.Options{})), ln: tl}
	go r.srv.Serve(tl)
	t.Cleanup(r.stop)
	return r
}

func (r *liveRelay) stop() { r.srv.Close(); r.ln.kill() }

func (r *liveRelay) url() string { return "http://" + r.addr }

type daemon struct {
	handler http.Handler
	events  *events.Store
}

// newDaemon is the real httpapi handler over a temp database with a login set.
func newDaemon(t *testing.T) *daemon {
	t.Helper()
	d := dbtest.Open(t)
	ev := events.New(d, time.Now)
	auth := webauth.New(d, time.Now).WithCost(4)
	if err := auth.SetCredentials(context.Background(), email, password); err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(httpapi.Deps{Version: "test", Token: "daemon-token-for-tests", DB: d, Events: ev,
		Items: &items.Store{DB: d, Events: ev, Now: time.Now}, Auth: auth,
		Run: (&execx.Fake{}).Runner(), WriteTimeout: time.Second})
	return &daemon{handler: api.Handler(), events: ev}
}

func startClient(t *testing.T, relayURL, sec string, h http.Handler) (*tunnel.Client, context.CancelFunc) {
	t.Helper()
	c := &tunnel.Client{URL: "ws" + strings.TrimPrefix(relayURL, "http"), Secret: sec, Handler: h,
		Sleep: func(ctx context.Context, _ time.Duration) error { // fast reconnects
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
				return nil
			}
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return c, cancel
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// do sends one request through the relay and returns status and body.
func do(t *testing.T, method, url, host, cookie string, body io.Reader, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// connected waits until a request through the relay reaches the daemon.
func connected(t *testing.T, relayURL string) {
	t.Helper()
	waitFor(t, "tunnel", func() bool {
		resp, _ := do(t, "GET", relayURL+"/api/health", "", "", nil)
		return resp.StatusCode == 200
	})
}

func login(t *testing.T, relayURL string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, _ := do(t, "POST", relayURL+"/api/auth/login", "", "", strings.NewReader(string(body)),
		"Content-Type", "application/json", "X-Real-IP", "203.0.113.9")
	if resp.StatusCode != 204 {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "swarm_session" {
			if !c.Secure || !c.HttpOnly {
				t.Errorf("tunnel cookie must be Secure and HttpOnly: %+v", c)
			}
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func TestRoundTripThroughRelayAndDaemon(t *testing.T) {
	r := startRelay(t, "127.0.0.1:0")
	d := newDaemon(t)
	startClient(t, r.url(), secret, d.handler)
	connected(t, r.url())

	// no cookie: protected routes are closed
	if resp, _ := do(t, "GET", r.url()+"/api/items", "", "", nil); resp.StatusCode != 401 {
		t.Errorf("anonymous /api/items = %d, want 401", resp.StatusCode)
	}
	cookie := login(t, r.url()) // POST with a body

	resp, b := do(t, "GET", r.url()+"/api/auth/me", "", cookie, nil)
	var me struct {
		Origin        string
		Authenticated bool
	}
	json.Unmarshal(b, &me)
	if resp.StatusCode != 200 || me.Origin != "tunnel" || !me.Authenticated {
		t.Fatalf("me = %d %s", resp.StatusCode, b)
	}
	if resp, _ := do(t, "GET", r.url()+"/api/items", "", cookie, nil); resp.StatusCode != 200 {
		t.Errorf("cookie /api/items = %d", resp.StatusCode)
	}
}

func TestHostLocalhostThroughRelayIsStillTunnel(t *testing.T) {
	r := startRelay(t, "127.0.0.1:0")
	d := newDaemon(t)
	startClient(t, r.url(), secret, d.handler)
	connected(t, r.url())
	cookie := login(t, r.url())

	_, b := do(t, "GET", r.url()+"/api/auth/me", "localhost", cookie, nil, "X-Forwarded-For", "127.0.0.1")
	var me struct{ Origin string }
	json.Unmarshal(b, &me)
	if me.Origin != "tunnel" {
		t.Fatalf("origin = %q, want tunnel", me.Origin)
	}
	for _, p := range []string{"/api/bootstrap", "/mcp", "/api/dev/seed", "/api/auth/credentials"} {
		if resp, _ := do(t, "GET", r.url()+p, "localhost", cookie, nil); resp.StatusCode != 404 {
			t.Errorf("%s via tunnel with Host localhost = %d, want 404", p, resp.StatusCode)
		}
	}
	// the daemon token is local-only: Bearer through the tunnel is refused
	if resp, _ := do(t, "GET", r.url()+"/api/items", "localhost", "", nil, "Authorization", "Bearer daemon-token-for-tests"); resp.StatusCode == 200 {
		t.Error("daemon Bearer accepted over the tunnel")
	}
}

func TestSSEStreamsThroughRelay(t *testing.T) {
	r := startRelay(t, "127.0.0.1:0")
	d := newDaemon(t)
	startClient(t, r.url(), secret, d.handler)
	connected(t, r.url())
	cookie := login(t, r.url())

	req, _ := http.NewRequest("GET", r.url()+"/api/events", nil)
	req.Header.Set("Cookie", cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	got := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "TASK-77") {
				got <- sc.Text()
				return
			}
		}
	}()
	d.events.Publish(context.Background(), events.ItemChanged, map[string]string{"key": "TASK-77", "root_key": "EPIC-1"})
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("event did not arrive within 1s")
	}
}

func TestReconnectsAfterRelayRestart(t *testing.T) {
	r := startRelay(t, "127.0.0.1:0")
	d := newDaemon(t)
	c, _ := startClient(t, r.url(), secret, d.handler)
	connected(t, r.url())

	r.stop()
	waitFor(t, "client to notice", func() bool { return c.Status().State != "connected" })
	startRelay(t, r.addr)
	connected(t, r.url())
	if st := c.Status(); st.State != "connected" || st.Reconnects < 1 {
		t.Fatalf("status %+v", st)
	}
}

func TestWrongSecretIsRejectedAndStaysDisconnected(t *testing.T) {
	r := startRelay(t, "127.0.0.1:0")
	d := newDaemon(t)
	wrong := strings.Repeat("f", 64)
	c, _ := startClient(t, r.url(), wrong, d.handler)
	waitFor(t, "error state", func() bool { return c.Status().State == "error" })
	time.Sleep(100 * time.Millisecond)
	st := c.Status()
	if st.State == "connected" || !strings.Contains(st.LastError, "401") || strings.Contains(st.LastError, wrong) {
		t.Fatalf("status %+v", st)
	}
	resp, b := do(t, "GET", r.url()+"/api/health", "", "", nil)
	if resp.StatusCode != 503 || !strings.Contains(string(b), "Swarm is offline") {
		t.Fatalf("relay with no tunnel = %d %s", resp.StatusCode, b)
	}
}

func TestUpgradeThroughRelayGets501(t *testing.T) {
	r := startRelay(t, "127.0.0.1:0")
	d := newDaemon(t)
	startClient(t, r.url(), secret, d.handler)
	connected(t, r.url())
	resp, _ := do(t, "GET", r.url()+"/api/events", "", "", nil, "Connection", "Upgrade", "Upgrade", "websocket")
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d, want 501", resp.StatusCode)
	}
}
