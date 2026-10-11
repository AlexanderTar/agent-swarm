package lan

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// DNSSD advertises swarm.local by supervising
//
//	dns-sd -P Swarm _http._tcp local <port> swarm.local <ip>
//
// The Mac's mDNSResponder answers for the name while that process lives and
// withdraws it when the process dies, so stopping the process is the cleanup.
type DNSSD struct {
	StartProc execx.Starter                        // nil means execx.Start
	After     func(time.Duration) <-chan time.Time // nil means time.After
	Log       func(string, ...any)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	proc   *execx.Proc
	st     RegStatus
}

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// Start (re)starts the advertisement for ip:port and returns at once; whether
// Bonjour accepted it shows up in Status.
func (r *DNSSD) Start(ip net.IP, port int) error {
	r.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.mu.Lock()
	r.cancel, r.done = cancel, done
	r.mu.Unlock()
	go func() { defer close(done); r.run(ctx, ip, port) }()
	return nil
}

// Stop ends the advertisement and waits for dns-sd to be gone.
func (r *DNSSD) Stop() error {
	r.mu.Lock()
	cancel, done, p := r.cancel, r.done, r.proc
	r.cancel, r.done, r.st = nil, nil, RegStatus{}
	r.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	if p != nil {
		p.Kill()
	}
	<-done
	return nil
}

// Status is the latest word from dns-sd.
func (r *DNSSD) Status() RegStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

func (r *DNSSD) set(ctx context.Context, st RegStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() == nil { // Stop has already cleared the status
		r.st = st
	}
}

// run keeps one dns-sd alive until ctx ends, waiting 1 s, 2 s, … up to 30 s
// between attempts. A run that got registered starts the wait over.
func (r *DNSSD) run(ctx context.Context, ip net.IP, port int) {
	start, after := r.StartProc, r.After
	if start == nil {
		start = execx.Start
	}
	if after == nil {
		after = time.After
	}
	args := []string{"-P", "Swarm", "_http._tcp", "local", strconv.Itoa(port), hostname, ip.String()}
	delay := minBackoff
	for ctx.Err() == nil {
		p, err := start(ctx, "dns-sd", args...)
		if err != nil {
			r.set(ctx, RegStatus{Err: "Can't run dns-sd: " + err.Error()})
		} else {
			r.mu.Lock()
			if ctx.Err() != nil {
				r.mu.Unlock()
				p.Kill()
				return
			}
			r.proc = p
			r.mu.Unlock()
			if r.watch(ctx, p) {
				delay = minBackoff
			}
			p.Kill() // reap it
			r.mu.Lock()
			r.proc = nil
			r.mu.Unlock()
			r.set(ctx, RegStatus{Err: "dns-sd stopped; starting it again"})
		}
		select {
		case <-ctx.Done():
			return
		case <-after(delay):
		}
		if delay *= 2; delay > maxBackoff {
			delay = maxBackoff
		}
	}
}

// watch reads dns-sd's replies until it exits, and reports whether it ever
// got registered.
func (r *DNSSD) watch(ctx context.Context, p *execx.Proc) (registered bool) {
	sc := bufio.NewScanner(p.Stdout)
	for sc.Scan() {
		line := sc.Text()
		_, reply, ok := strings.Cut(line, "Got a reply for service ")
		if !ok {
			continue
		}
		switch {
		case strings.Contains(reply, "now registered and active"):
			registered = true
			r.set(ctx, RegStatus{Registered: true})
		case strings.Contains(reply, "registration removed"):
			r.set(ctx, RegStatus{})
		case strings.Contains(reply, "Name in use"):
			r.set(ctx, RegStatus{Err: "Another device on this network already uses the name " + hostname + "."})
			r.logf("lan: %s", line)
		case strings.Contains(reply, "Error"):
			_, msg, _ := strings.Cut(reply, ": ")
			r.set(ctx, RegStatus{Err: "Bonjour refused " + hostname + ": " + msg})
			r.logf("lan: %s", line)
		}
	}
	return registered
}

func (r *DNSSD) logf(f string, a ...any) {
	if r.Log != nil {
		r.Log(f, a...)
	}
}
