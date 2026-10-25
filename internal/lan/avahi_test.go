package lan

import (
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

var avahiIP = net.IPv4(192, 168, 1, 42)

func (p *fakeProc) killed() bool {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	return p.kills > 0
}

func startAvahi(t *testing.T, f *fakeDNSSD) *Avahi {
	t.Helper()
	r := &Avahi{StartProc: f.start, After: f.after}
	if err := r.Start(avahiIP, 7777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Stop() })
	eventually(t, "both publishers to start", func() bool { return f.starts() >= 2 })
	return r
}

// The address and a service on that host, never Avahi's own host name.
func TestAvahiPublishesTheAddressAndAServiceOnIt(t *testing.T) {
	f := &fakeDNSSD{hold: true}
	startAvahi(t, f)
	if got := f.argvAt(0); got != "avahi-publish -a -R swarm.local 192.168.1.42" {
		t.Errorf("address argv = %q", got)
	}
	if got := f.argvAt(1); got != "avahi-publish -s -H swarm.local Swarm _http._tcp 7777" {
		t.Errorf("service argv = %q", got)
	}
}

// Registered only once both are established; Stop kills both and clears it.
func TestAvahiRegistersWhenBothAreEstablished(t *testing.T) {
	f := &fakeDNSSD{hold: true}
	r := startAvahi(t, f)
	f.line(0, "Established under name 'swarm.local'")
	time.Sleep(20 * time.Millisecond)
	if st := r.Status(); st.Registered {
		t.Fatalf("registered with only the address up: %+v", st)
	}
	f.line(1, "Established under name 'Swarm'")
	eventually(t, "registered", func() bool { return r.Status().Registered })
	r.Stop()
	for i := range 2 {
		if !f.proc(i).killed() {
			t.Errorf("publisher %d was not killed", i)
		}
	}
	if st := r.Status(); st != (RegStatus{}) {
		t.Errorf("status after Stop = %+v", st)
	}
}

// A taken swarm.local is an error, not a silent swarm-2.local: both
// publishers are stopped and the reason stays visible.
func TestAvahiAddressCollisionIsAnError(t *testing.T) {
	f := &fakeDNSSD{hold: true}
	r := startAvahi(t, f)
	f.line(1, "Established under name 'Swarm'")
	f.line(0, "Name collision, picking new name 'swarm-2.local'.")
	want := "Another device on this network already uses the name swarm.local."
	eventually(t, "the collision error", func() bool { return r.Status().Err == want })
	eventually(t, "both publishers killed", func() bool {
		return f.proc(0).killed() && f.proc(1).killed()
	})
	time.Sleep(20 * time.Millisecond)
	if st := r.Status(); st.Err != want || st.Registered {
		t.Errorf("status = %+v", st)
	}
}

// Avahi renames a clashing service itself; the host is still swarm.local.
func TestAvahiServiceRenameStillRegisters(t *testing.T) {
	f := &fakeDNSSD{hold: true}
	r := startAvahi(t, f)
	f.line(1, "Name collision, picking new name 'Swarm #2'.")
	f.line(1, "Established under name 'Swarm #2'")
	f.line(0, "Established under name 'swarm.local'")
	eventually(t, "registered", func() bool { return r.Status().Registered })
}

func TestAvahiReportsWhatAvahiSays(t *testing.T) {
	for line, want := range map[string]string{
		"Failed to create client object: Daemon not running": "Avahi refused swarm.local: Daemon not running",
		"Failed to add address: Invalid address":             "Avahi refused swarm.local: Invalid address",
		"Waiting for daemon ...":                             "avahi-daemon isn't running, so swarm.local can't be published.",
	} {
		f := &fakeDNSSD{hold: true}
		r := startAvahi(t, f)
		f.line(0, line)
		eventually(t, want, func() bool { return r.Status().Err == want })
		// avahi-publish exits after a failure; the reason outlives the exit.
		f.proc(0).out.Close()
		time.Sleep(20 * time.Millisecond)
		if st := r.Status(); st.Err != want {
			t.Errorf("%s: after exit status = %+v", line, st)
		}
		r.Stop()
	}
}

func TestAvahiReportsAMissingAvahiPublish(t *testing.T) {
	f := &fakeDNSSD{hold: true, failOn: 1}
	r := &Avahi{StartProc: f.start, After: f.after}
	r.Start(avahiIP, 7777)
	t.Cleanup(func() { r.Stop() })
	eventually(t, "the install hint", func() bool { return strings.Contains(r.Status().Err, "install avahi-utils") })
	if f.starts() != 1 {
		t.Errorf("starts = %d: the service publisher must not start without the address one", f.starts())
	}
}

// When either publisher dies both restart together, after 1 s, then 2 s, …
func TestAvahiRestartsBothWithBackoff(t *testing.T) {
	f := &fakeDNSSD{}
	startAvahi(t, f)
	f.proc(1).out.Close() // the service publisher dies
	eventually(t, "a restart", func() bool { return f.starts() >= 4 })
	if !f.proc(0).killed() {
		t.Error("the address publisher was not stopped with the service one")
	}
	if got := f.argvAt(2); got != "avahi-publish -a -R swarm.local 192.168.1.42" {
		t.Errorf("restart argv = %q", got)
	}
	f.proc(2).out.Close()
	eventually(t, "a second restart", func() bool { return f.starts() >= 6 })
	if b := f.backoffs(); len(b) < 2 || !slices.Equal(b[:2], []time.Duration{time.Second, 2 * time.Second}) {
		t.Errorf("backoffs = %v", b)
	}
}
