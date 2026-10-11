package lan

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/httpapi"
)

var bg = context.Background()

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

type fakeRegistrar struct {
	mu      sync.Mutex
	started []string
	stops   int
	reg     RegStatus
}

func (f *fakeRegistrar) Start(ip net.IP, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	return nil
}
func (f *fakeRegistrar) Stop() error { f.mu.Lock(); f.stops++; f.mu.Unlock(); return nil }
func (f *fakeRegistrar) Status() RegStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reg
}
func (f *fakeRegistrar) calls() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...), f.stops
}

// closeTracker signals Close on a real listener. Waiting on this instead of dialling the freed
// port avoids a flake: under load another process can reuse the ephemeral port the test just freed.
// http.Server closes the listener from its Serve goroutine, so the signal can land a moment late.
type closeTracker struct {
	net.Listener
	once   sync.Once
	closed chan struct{}
}

func (c *closeTracker) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Listener.Close()
}

type harness struct {
	m     *Manager
	reg   *fakeRegistrar
	ticks chan time.Time

	mu      sync.Mutex
	ip      net.IP
	ipErr   error
	listens []string // what the manager asked to bind
	real    []net.Listener
}

func (h *harness) setIP(ip string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ip, h.ipErr = net.ParseIP(ip), err
}

func (h *harness) bound() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.listens...)
}

// newHarness never touches a real LAN address: Listen records what the manager
// asked for and hands back a loopback listener.
func newHarness(t *testing.T, ip string) *harness {
	t.Helper()
	h := &harness{reg: &fakeRegistrar{}, ticks: make(chan time.Time)}
	h.setIP(ip, nil)
	h.m = &Manager{
		DB: dbtest.Open(t), Port: 7777, Registrar: h.reg, Tick: h.ticks,
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, string(httpapi.RequestOrigin(r))) }),
		PrimaryIP: func() (net.IP, error) { h.mu.Lock(); defer h.mu.Unlock(); return h.ip, h.ipErr },
		Listen: func(network, addr string) (net.Listener, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.listens = append(h.listens, network+" "+addr)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return nil, err
			}
			tracked := &closeTracker{Listener: ln, closed: make(chan struct{})}
			h.real = append(h.real, tracked)
			return tracked, nil
		},
	}
	t.Cleanup(h.m.Stop)
	return h
}

func (h *harness) tick(t *testing.T) {
	t.Helper()
	select {
	case h.ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("the manager is not watching the IP")
	}
}

func (h *harness) state() httpapi.LANStatus { return h.m.Status(bg) }

func TestApplyBindsTheLANIPOnly(t *testing.T) {
	h := newHarness(t, "192.168.1.20")
	if st := h.state(); st.State != "disabled" || st.Enabled {
		t.Fatalf("fresh: %+v", st)
	}
	if err := h.m.Apply(bg, true); err != nil {
		t.Fatal(err)
	}
	if got := h.bound(); len(got) != 1 || got[0] != "tcp4 192.168.1.20:7777" {
		t.Fatalf("bound %v, want only tcp4 192.168.1.20:7777", got)
	}
	st := h.state()
	if !st.Enabled || st.State != "listening" || st.IP != "192.168.1.20" || st.LastError != "" {
		t.Fatalf("%+v", st)
	}
	if starts, _ := h.reg.calls(); len(starts) != 1 || starts[0] != "192.168.1.20:7777" {
		t.Fatalf("registrar starts = %v", starts)
	}
}

func TestRebindsWhenTheIPChanges(t *testing.T) {
	h := newHarness(t, "192.168.1.20")
	if err := h.m.Apply(bg, true); err != nil {
		t.Fatal(err)
	}
	old := h.real[0].Addr().String()
	h.tick(t) // same IP: nothing moves
	h.setIP("10.0.0.5", nil)
	h.tick(t)
	eventually(t, "rebind", func() bool { return h.state().IP == "10.0.0.5" })
	if got := h.bound(); len(got) != 2 || got[1] != "tcp4 10.0.0.5:7777" {
		t.Fatalf("bound %v", got)
	}
	if c, err := net.Dial("tcp", old); err == nil {
		c.Close()
		t.Fatal("the old listener is still accepting")
	}
	starts, stops := h.reg.calls()
	if len(starts) != 2 || starts[1] != "10.0.0.5:7777" || stops < 1 {
		t.Fatalf("registrar starts %v stops %d", starts, stops)
	}
}

func TestOfflineWaitsAndRecovers(t *testing.T) {
	h := newHarness(t, "")
	h.setIP("", errors.New("no default route"))
	if err := h.m.Apply(bg, true); err != nil {
		t.Fatalf("enabling while offline must succeed: %v", err)
	}
	st := h.state()
	if st.State != "waiting" || !st.Enabled || st.LastError == "" || st.IP != "" {
		t.Fatalf("offline: %+v", st)
	}
	if len(h.bound()) != 0 {
		t.Fatalf("bound while offline: %v", h.bound())
	}
	h.setIP("192.168.1.20", nil)
	h.tick(t)
	eventually(t, "listening", func() bool { return h.state().State == "listening" })
	if st := h.state(); st.LastError != "" || st.IP != "192.168.1.20" {
		t.Fatalf("online: %+v", st)
	}
	// losing the network closes the listener and goes back to waiting
	h.setIP("", errors.New("no default route"))
	h.tick(t)
	eventually(t, "waiting", func() bool { return h.state().State == "waiting" })
	if c, err := net.Dial("tcp", h.real[0].Addr().String()); err == nil {
		c.Close()
		t.Fatal("still accepting after the network went away")
	}
	if _, stops := h.reg.calls(); stops < 1 {
		t.Fatal("the registrar kept advertising an address that's gone")
	}
}

func TestNeverBindsAWildcardOrIPv6(t *testing.T) {
	for _, ip := range []string{"0.0.0.0", "::", "fe80::1", "127.0.0.1"} {
		h := newHarness(t, ip)
		if err := h.m.Apply(bg, true); err != nil {
			t.Fatal(err)
		}
		if st := h.state(); st.State != "waiting" || st.LastError == "" {
			t.Errorf("%s: %+v", ip, st)
		}
		if len(h.bound()) != 0 {
			t.Errorf("%s: bound %v", ip, h.bound())
		}
	}
}

func TestListenFailureIsAnErrorStateThatRetries(t *testing.T) {
	h := newHarness(t, "192.168.1.20")
	real := h.m.Listen
	fail := true
	h.m.Listen = func(n, a string) (net.Listener, error) {
		if fail {
			return nil, errors.New("address already in use")
		}
		return real(n, a)
	}
	if err := h.m.Apply(bg, true); err != nil {
		t.Fatal(err)
	}
	if st := h.state(); st.State != "error" || st.LastError == "" {
		t.Fatalf("%+v", st)
	}
	fail = false
	h.tick(t)
	eventually(t, "listening", func() bool { return h.state().State == "listening" })
}

func TestServesTheHandlerAndClassifiesOrigins(t *testing.T) {
	h := newHarness(t, "192.168.1.20")
	if err := h.m.Apply(bg, true); err != nil {
		t.Fatal(err)
	}
	// the Mac itself, reaching its own LAN address by name: local
	req, _ := http.NewRequest("GET", "http://"+h.real[0].Addr().String()+"/", nil)
	req.Host = "swarm.local"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "local" {
		t.Fatalf("own machine with Host swarm.local = %q, want local", b)
	}
	// a phone on the LAN asking for swarm.local is lan, not local
	oldOwn := httpapi.OwnIPs
	httpapi.OwnIPs = func() []net.IP { return []net.IP{net.ParseIP("192.168.1.20")} }
	t.Cleanup(func() { httpapi.OwnIPs = oldOwn })
	r, _ := http.NewRequest("GET", "http://swarm.local:7777/", nil)
	r = r.WithContext(httpapi.ConnContext(httpapi.ListenerLAN)(bg, nil))
	r.RemoteAddr = "192.168.1.50:51234"
	if got := httpapi.RequestOrigin(r); got != httpapi.OriginLAN {
		t.Fatalf("LAN peer with Host swarm.local = %q, want lan", got)
	}
	r.RemoteAddr = "192.168.1.20:51234"
	if got := httpapi.RequestOrigin(r); got != httpapi.OriginLocal {
		t.Fatalf("own IP with Host swarm.local = %q, want local", got)
	}
}

func TestApplyPersistsOutsideSettingsAndStartResumes(t *testing.T) {
	h := newHarness(t, "192.168.1.20")
	if err := h.m.Apply(bg, true); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := h.m.DB.QueryRowContext(bg, `SELECT value_json FROM settings WHERE key = 'lan.enabled'`).Scan(&v); err != nil || v != "true" {
		t.Fatalf("row = %q, %v", v, err)
	}
	h.m.Stop()

	h2 := newHarness(t, "192.168.1.20")
	h2.m.DB = h.m.DB
	if err := h2.m.Start(bg); err != nil {
		t.Fatal(err)
	}
	if st := h2.state(); st.State != "listening" || !st.Enabled {
		t.Fatalf("resumed: %+v", st)
	}

	if err := h2.m.Apply(bg, false); err != nil {
		t.Fatal(err)
	}
	if st := h2.state(); st.State != "disabled" || st.Enabled || st.IP != "" {
		t.Fatalf("disabled: %+v", st)
	}
	select {
	case <-h2.real[0].(*closeTracker).closed:
	case <-time.After(5 * time.Second):
		t.Fatal("listener not closed on disable")
	}
	if _, stops := h2.reg.calls(); stops < 1 {
		t.Fatal("registrar not stopped on disable")
	}
	if err := h.m.DB.QueryRowContext(bg, `SELECT value_json FROM settings WHERE key = 'lan.enabled'`).Scan(&v); err != nil || v != "false" {
		t.Fatalf("row = %q, %v", v, err)
	}
}

func TestStatusReportsTheRegistrar(t *testing.T) {
	h := newHarness(t, "192.168.1.20")
	if err := h.m.Apply(bg, true); err != nil {
		t.Fatal(err)
	}
	if st := h.state(); st.Hostname != "" {
		t.Fatalf("hostname before it is registered: %+v", st)
	}
	h.reg.mu.Lock()
	h.reg.reg = RegStatus{Registered: true}
	h.reg.mu.Unlock()
	if st := h.state(); st.Hostname != "swarm.local" || st.URL != "http://swarm.local:7777" || st.LastError != "" {
		t.Fatalf("%+v", st)
	}
	h.reg.mu.Lock()
	h.reg.reg = RegStatus{Err: "swarm.local is already taken on this network"}
	h.reg.mu.Unlock()
	if st := h.state(); st.State != "listening" || st.LastError == "" || st.Hostname != "" || st.URL != "http://192.168.1.20:7777" {
		t.Fatalf("%+v", st)
	}
}
