package relay_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"github.com/AlexanderTar/swarmrun/internal/relay"
)

const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// seen is what the fake daemon reports back about the request it served.
type seen struct {
	Daemon string
	Host   string
	Method string
	Path   string
	Header http.Header
	Body   string
}

// fakeDaemon is the daemon side of the tunnel: a yamux server that answers
// every request by describing it.
type fakeDaemon struct {
	sess *yamux.Session
	hits atomic.Int32
}

func dialDaemon(t *testing.T, relayURL, name string) *fakeDaemon {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(relayURL, "http")+"/_tunnel", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}},
	})
	if err != nil {
		t.Fatalf("dial /_tunnel: %v", err)
	}
	c.SetReadLimit(1 << 20)
	sess, err := yamux.Server(websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{sess: sess}
	go http.Serve(sess, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(seen{name, r.Host, r.Method, r.URL.Path, r.Header, string(b)})
	}))
	t.Cleanup(func() { sess.Close() })
	return d
}

func newRelay(t *testing.T, opts relay.Options) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(relay.New(secret, opts))
	t.Cleanup(srv.Close)
	return srv
}

// get polls until the relay has a tunnel (status != 503) and decodes the daemon's report.
func get(t *testing.T, req *http.Request) (*http.Response, seen) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.DefaultClient.Do(req.Clone(req.Context()))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
			var s seen
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			_ = json.Unmarshal(b, &s)
			return resp, s
		}
		resp.Body.Close()
		time.Sleep(10 * time.Millisecond)
	}
}

func newReq(t *testing.T, method, url string, body io.Reader, hdr map[string]string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return req
}

func TestHealthzWithoutTunnel(t *testing.T) {
	srv := newRelay(t, relay.Options{})
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
}

func TestTunnelRejectsBadSecretBeforeUpgrade(t *testing.T) {
	srv := newRelay(t, relay.Options{})
	for name, auth := range map[string]string{
		"missing": "", "wrong": "Bearer nope", "no scheme": secret, "prefix": "Bearer " + secret[:63],
	} {
		hdr := map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ=="}
		if auth != "" {
			hdr["Authorization"] = auth
		}
		resp, err := http.DefaultClient.Do(newReq(t, "GET", srv.URL+"/_tunnel", nil, hdr))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s: status %d, want 401 (no upgrade)", name, resp.StatusCode)
		}
	}
}

func TestTunnelPathIsNeverProxied(t *testing.T) {
	srv := newRelay(t, relay.Options{})
	d := dialDaemon(t, srv.URL, "a")
	get(t, newReq(t, "GET", srv.URL+"/x", nil, nil)) // wait until registered
	before := d.hits.Load()
	// authenticated but not a websocket handshake: must not reach the daemon
	for _, p := range []string{"/_tunnel", "/_tunnel/"} {
		resp, err := http.DefaultClient.Do(newReq(t, "GET", srv.URL+p, nil, map[string]string{"Authorization": "Bearer " + secret}))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("%s answered 200", p)
		}
	}
	if d.hits.Load() != before {
		t.Fatal("/_tunnel request was proxied to the daemon")
	}
}

func TestNewestSessionWins(t *testing.T) {
	srv := newRelay(t, relay.Options{})
	a := dialDaemon(t, srv.URL, "a")
	if _, s := get(t, newReq(t, "GET", srv.URL+"/who", nil, nil)); s.Daemon != "a" {
		t.Fatalf("first request served by %q", s.Daemon)
	}
	dialDaemon(t, srv.URL, "b")
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, s := get(t, newReq(t, "GET", srv.URL+"/who", nil, nil))
		if s.Daemon == "b" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still served by %q", s.Daemon)
		}
		time.Sleep(10 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for !a.sess.IsClosed() {
		if time.Now().After(deadline) {
			t.Fatal("old session was not closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProxyKeepsHostAndStripsForwardingHeaders(t *testing.T) {
	srv := newRelay(t, relay.Options{})
	dialDaemon(t, srv.URL, "a")
	req := newReq(t, "POST", srv.URL+"/api/x?y=1", strings.NewReader("hello"), map[string]string{
		"X-Forwarded-For": "6.6.6.6, 9.9.9.9", "X-Forwarded-Host": "evil", "X-Forwarded-Proto": "http",
		"X-Forwarded-Port": "1", "Forwarded": "for=6.6.6.6", "X-Tunnel-Origin": "local", "X-Tunnel-Anything": "1",
		"X-Real-IP": "7.7.7.7", "Cookie": "swarm_session=abc", "Authorization": "Bearer user",
	})
	req.Host = "swarm.example.com"
	resp, s := get(t, req)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if s.Host != "swarm.example.com" || s.Method != "POST" || s.Path != "/api/x" || s.Body != "hello" {
		t.Errorf("request not passed through: %+v", s)
	}
	h := s.Header
	if got := h.Get("X-Forwarded-For"); got != "7.7.7.7" {
		t.Errorf("X-Forwarded-For = %q, want only the real client", got)
	}
	if got := h.Get("X-Forwarded-Proto"); got != "https" {
		t.Errorf("X-Forwarded-Proto = %q", got)
	}
	for _, k := range []string{"X-Forwarded-Host", "X-Forwarded-Port", "Forwarded", "X-Tunnel-Origin", "X-Tunnel-Anything", "X-Real-Ip"} {
		if v := h.Get(k); v != "" {
			t.Errorf("%s leaked: %q", k, v)
		}
	}
	if h.Get("Cookie") == "" || h.Get("Authorization") == "" {
		t.Error("cookie and authorization must pass through to the daemon")
	}
}

func TestClientIPChain(t *testing.T) {
	cases := []struct {
		name string
		hdr  map[string]string
		want string // "" means the peer (127.0.0.1)
	}{
		{"X-Real-IP wins", map[string]string{"X-Real-IP": "7.7.7.7", "X-Forwarded-For": "1.1.1.1, 2.2.2.2"}, "7.7.7.7"},
		{"right-most XFF hop", map[string]string{"X-Forwarded-For": "6.6.6.6, 1.1.1.1, 2.2.2.2"}, "2.2.2.2"},
		{"peer", nil, "127.0.0.1"},
		{"garbage X-Real-IP falls through", map[string]string{"X-Real-IP": "not-an-ip", "X-Forwarded-For": "3.3.3.3"}, "3.3.3.3"},
	}
	srv := newRelay(t, relay.Options{})
	dialDaemon(t, srv.URL, "a")
	for _, c := range cases {
		_, s := get(t, newReq(t, "GET", srv.URL+"/ip", nil, c.hdr))
		if got := s.Header.Get("X-Forwarded-For"); got != c.want {
			t.Errorf("%s: XFF = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestOfflinePage(t *testing.T) {
	srv := newRelay(t, relay.Options{})
	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 503 || !strings.Contains(string(b), "Swarm is offline") {
		t.Fatalf("got %d %q", resp.StatusCode, b)
	}
}

// The offline page carries the same app icon as the board's favicon, inline, so it needs no second request.
func TestOfflinePageHasTheAppIcon(t *testing.T) {
	srv := newRelay(t, relay.Options{})
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	m := regexp.MustCompile(`<link rel=icon href="data:image/svg\+xml;base64,([A-Za-z0-9+/=]+)">`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("offline page has no inline icon link: %s", b)
	}
	got, err := base64.StdEncoding.DecodeString(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../apps/menubar/Resources/AppIcon.svg")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("inline icon differs from apps/menubar/Resources/AppIcon.svg")
	}
}

func TestOfflineAfterDaemonDisconnects(t *testing.T) {
	srv := newRelay(t, relay.Options{})
	d := dialDaemon(t, srv.URL, "a")
	get(t, newReq(t, "GET", srv.URL+"/x", nil, nil))
	d.sess.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(srv.URL + "/x")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 503 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status %d after disconnect, want 503", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBodyCap(t *testing.T) {
	srv := newRelay(t, relay.Options{MaxBody: 10})
	d := dialDaemon(t, srv.URL, "a")
	get(t, newReq(t, "GET", srv.URL+"/x", nil, nil))
	before := d.hits.Load()

	resp, err := http.Post(srv.URL+"/up", "text/plain", strings.NewReader(strings.Repeat("x", 11)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("declared oversize: %d, want 413", resp.StatusCode)
	}
	if d.hits.Load() != before {
		t.Error("oversize request reached the daemon")
	}

	// chunked (unknown length) bodies are capped while streaming
	req := newReq(t, "POST", srv.URL+"/up", io.NopCloser(strings.NewReader(strings.Repeat("x", 100))), nil)
	req.ContentLength = -1
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var s seen
	if json.Unmarshal(b, &s) == nil && len(s.Body) > 10 {
		t.Errorf("daemon received %d body bytes, cap is 10", len(s.Body))
	}

	resp, err = http.Post(srv.URL+"/up", "text/plain", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("at-cap body: %d, want 200", resp.StatusCode)
	}
}

func TestUpgradeRequestsGet501(t *testing.T) {
	srv := newRelay(t, relay.Options{})
	d := dialDaemon(t, srv.URL, "a")
	get(t, newReq(t, "GET", srv.URL+"/x", nil, nil))
	before := d.hits.Load()
	resp, err := http.DefaultClient.Do(newReq(t, "GET", srv.URL+"/api/events", nil, map[string]string{
		"Upgrade": "websocket", "Connection": "Upgrade",
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d, want 501", resp.StatusCode)
	}
	if d.hits.Load() != before {
		t.Error("upgrade request reached the daemon")
	}
}

func TestServerLimits(t *testing.T) {
	s := relay.NewServer(":0", relay.New(secret, relay.Options{}))
	if s.ReadHeaderTimeout != 10*time.Second || s.MaxHeaderBytes != 32<<10 || s.WriteTimeout != 0 {
		t.Fatalf("limits: rht=%v mhb=%d wt=%v", s.ReadHeaderTimeout, s.MaxHeaderBytes, s.WriteTimeout)
	}
}

func TestValidSecret(t *testing.T) {
	for secret, want := range map[string]bool{
		"":                             false,
		"abc":                          false,
		strings.Repeat("a", 63):        false,
		strings.Repeat("g", 64):        false,
		strings.Repeat("a", 64):        true,
		strings.Repeat("A1", 32):       true,
		strings.Repeat("a", 64) + "\n": false,
	} {
		if got := relay.ValidSecret(secret); got != want {
			t.Errorf("ValidSecret(%q) = %v, want %v", secret, got, want)
		}
	}
}
