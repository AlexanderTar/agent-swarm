package lan

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// fakeDNSSD plays the exec seam: each start is a process whose stdout the
// test writes to, and whose exit is closing that pipe.
type fakeDNSSD struct {
	mu     sync.Mutex
	argv   []string // one joined argv per start
	procs  []*fakeProc
	failOn int // starts up to this count fail, like a missing dns-sd
	waits  []time.Duration
	hold   bool // After never fires
}

type fakeProc struct {
	out    *io.PipeWriter
	kills  int
	killMu sync.Mutex
}

func (f *fakeDNSSD) start(_ context.Context, name string, args ...string) (*execx.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argv = append(f.argv, strings.Join(append([]string{name}, args...), " "))
	if len(f.argv) <= f.failOn {
		return nil, errors.New("exec: \"dns-sd\": executable file not found in $PATH")
	}
	pr, pw := io.Pipe()
	p := &fakeProc{out: pw}
	f.procs = append(f.procs, p)
	return &execx.Proc{Stdout: pr, Stdin: nopCloser{}, Kill: func() {
		p.killMu.Lock()
		p.kills++
		p.killMu.Unlock()
		pw.Close()
	}}, nil
}

type nopCloser struct{}

func (nopCloser) Write(b []byte) (int, error) { return len(b), nil }
func (nopCloser) Close() error                { return nil }

func (f *fakeDNSSD) after(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waits = append(f.waits, d)
	ch := make(chan time.Time, 1)
	if !f.hold {
		ch <- time.Time{}
	}
	return ch
}

func (f *fakeDNSSD) argvAt(i int) string { f.mu.Lock(); defer f.mu.Unlock(); return f.argv[i] }
func (f *fakeDNSSD) starts() int         { f.mu.Lock(); defer f.mu.Unlock(); return len(f.argv) }
func (f *fakeDNSSD) proc(i int) *fakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[i]
}
func (f *fakeDNSSD) line(i int, s string) { f.proc(i).out.Write([]byte(s + "\n")) }
func (f *fakeDNSSD) backoffs() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.waits...)
}

func newDNSSD(f *fakeDNSSD) *DNSSD { return &DNSSD{StartProc: f.start, After: f.after} }

var lanIP = net.ParseIP("192.168.1.20")

const (
	registeredLine = "14:32:05.054  Got a reply for service Swarm._http._tcp.local.: Name now registered and active"
	conflictLine   = "14:32:06.100  Got a reply for service Swarm._http._tcp.local.: Name in use, please choose another"
)

func TestDNSSDRunsTheRegistrationCommand(t *testing.T) {
	f := &fakeDNSSD{}
	r := newDNSSD(f)
	t.Cleanup(func() { r.Stop() })
	if err := r.Start(lanIP, 7777); err != nil {
		t.Fatal(err)
	}
	eventually(t, "start", func() bool { return f.starts() == 1 })
	if want := "dns-sd -P Swarm _http._tcp local 7777 swarm.local 192.168.1.20"; f.argvAt(0) != want {
		t.Fatalf("argv = %q, want %q", f.argvAt(0), want)
	}
	if st := r.Status(); st.Registered || st.Err != "" {
		t.Fatalf("before any reply: %+v", st)
	}
}

func TestDNSSDParsesRegisteredAndConflict(t *testing.T) {
	f := &fakeDNSSD{}
	r := newDNSSD(f)
	t.Cleanup(func() { r.Stop() })
	r.Start(lanIP, 7777)
	eventually(t, "start", func() bool { return f.starts() == 1 })

	f.line(0, "Registering Service Swarm._http._tcp.local host swarm.local port 7777")
	f.line(0, registeredLine)
	eventually(t, "registered", func() bool { return r.Status().Registered })
	if st := r.Status(); st.Err != "" {
		t.Fatalf("%+v", st)
	}

	f.line(0, conflictLine)
	eventually(t, "conflict", func() bool { return r.Status().Err != "" })
	st := r.Status()
	if st.Registered || !strings.Contains(st.Err, "swarm.local") || !strings.Contains(st.Err, "already") {
		t.Fatalf("conflict: %+v", st)
	}
	if f.starts() != 1 {
		t.Fatal("a conflict must not restart the process in a loop")
	}

	f.line(0, registeredLine) // dns-sd picked another name and recovered
	eventually(t, "recovered", func() bool { return r.Status().Registered && r.Status().Err == "" })

	f.line(0, "Got a reply for service Swarm._http._tcp.local.: Error -65537")
	eventually(t, "error", func() bool { return strings.Contains(r.Status().Err, "Error -65537") })
}

func TestDNSSDRestartsWhenItExits(t *testing.T) {
	f := &fakeDNSSD{}
	r := newDNSSD(f)
	t.Cleanup(func() { r.Stop() })
	r.Start(lanIP, 7777)
	eventually(t, "start", func() bool { return f.starts() == 1 })
	f.line(0, registeredLine)
	eventually(t, "registered", func() bool { return r.Status().Registered })

	f.proc(0).out.Close() // dns-sd dies
	eventually(t, "restart", func() bool { return f.starts() == 2 })
	if f.argvAt(1) != f.argvAt(0) {
		t.Fatalf("restart argv %q != %q", f.argvAt(1), f.argvAt(0))
	}
	if st := r.Status(); st.Registered {
		t.Fatalf("still reporting registered after the process died: %+v", st)
	}

	// it keeps dying without ever registering: the wait grows, up to a cap
	for i := 1; i < 8; i++ {
		f.proc(i).out.Close()
		n := i + 2
		eventually(t, "restart", func() bool { return f.starts() == n })
	}
	w := f.backoffs()
	if w[0] != time.Second || w[1] != 2*time.Second || w[2] != 4*time.Second || w[3] != 8*time.Second {
		t.Fatalf("backoff = %v (1s after a run that registered, then it doubles)", w)
	}
	if last := w[len(w)-1]; last != 30*time.Second {
		t.Fatalf("backoff = %v, want a 30s cap", w)
	}
}

func TestDNSSDRetriesWhenTheCommandCannotStart(t *testing.T) {
	f := &fakeDNSSD{failOn: 2}
	r := newDNSSD(f)
	t.Cleanup(func() { r.Stop() })
	if err := r.Start(lanIP, 7777); err != nil {
		t.Fatalf("a missing dns-sd must not fail Start: %v", err)
	}
	eventually(t, "third start", func() bool { return f.starts() == 3 })
	eventually(t, "running", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.procs) == 1 })
	f.line(0, registeredLine)
	eventually(t, "registered", func() bool { return r.Status().Registered && r.Status().Err == "" })
}

func TestDNSSDReportsAMissingBinary(t *testing.T) {
	f := &fakeDNSSD{failOn: 100, hold: true}
	r := newDNSSD(f)
	t.Cleanup(func() { r.Stop() })
	r.Start(lanIP, 7777)
	eventually(t, "error", func() bool { return strings.Contains(r.Status().Err, "dns-sd") })
}

func TestDNSSDStopKillsAndDoesNotRestart(t *testing.T) {
	f := &fakeDNSSD{}
	r := newDNSSD(f)
	r.Start(lanIP, 7777)
	eventually(t, "start", func() bool { return f.starts() == 1 })
	f.line(0, registeredLine)
	eventually(t, "registered", func() bool { return r.Status().Registered })

	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	p := f.proc(0)
	p.killMu.Lock()
	kills := p.kills
	p.killMu.Unlock()
	if kills == 0 {
		t.Fatal("Stop left dns-sd running")
	}
	time.Sleep(30 * time.Millisecond)
	if f.starts() != 1 {
		t.Fatalf("restarted after Stop: %d starts", f.starts())
	}
	if st := r.Status(); st.Registered || st.Err != "" {
		t.Fatalf("after Stop: %+v", st)
	}
	if err := r.Stop(); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestDNSSDStopWhileWaitingToRestart(t *testing.T) {
	f := &fakeDNSSD{hold: true}
	r := newDNSSD(f)
	r.Start(lanIP, 7777)
	eventually(t, "start", func() bool { return f.starts() == 1 })
	f.proc(0).out.Close()
	eventually(t, "backing off", func() bool { return len(f.backoffs()) == 1 })
	done := make(chan struct{})
	go func() { r.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung while waiting to restart")
	}
	if f.starts() != 1 {
		t.Fatal("restarted after Stop")
	}
}

func TestDNSSDStartAgainMovesTheAdvertisement(t *testing.T) {
	f := &fakeDNSSD{}
	r := newDNSSD(f)
	t.Cleanup(func() { r.Stop() })
	r.Start(lanIP, 7777)
	eventually(t, "start", func() bool { return f.starts() == 1 })
	f.line(0, registeredLine)
	eventually(t, "registered", func() bool { return r.Status().Registered })

	r.Start(net.ParseIP("10.0.0.5"), 7777)
	eventually(t, "second start", func() bool { return f.starts() == 2 })
	if want := "dns-sd -P Swarm _http._tcp local 7777 swarm.local 10.0.0.5"; f.argvAt(1) != want {
		t.Fatalf("argv = %q, want %q", f.argvAt(1), want)
	}
	if st := r.Status(); st.Registered {
		t.Fatalf("the old registration must not carry over: %+v", st)
	}
	p := f.proc(0)
	p.killMu.Lock()
	defer p.killMu.Unlock()
	if p.kills == 0 {
		t.Fatal("the old dns-sd is still running")
	}
}
