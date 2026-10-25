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

// Avahi advertises swarm.local on Linux by supervising two avahi-publish
// processes, one for the address and one for the service pointed at it:
//
//	avahi-publish -a -R swarm.local <ip>
//	avahi-publish -s -H swarm.local Swarm _http._tcp <port>
//
// avahi-daemon answers for both while the processes live and withdraws them
// when they die, so stopping the processes is the cleanup. avahi-publish
// reports on stderr, so its starter merges stderr into the stream read here.
type Avahi struct {
	StartProc execx.Starter                        // nil means execx.StartMerged
	After     func(time.Duration) <-chan time.Time // nil means time.After
	Log       func(string, ...any)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	procs  []*execx.Proc
	st     RegStatus
}

// Start (re)starts the advertisement for ip:port and returns at once; whether
// Avahi accepted it shows up in Status.
func (r *Avahi) Start(ip net.IP, port int) error {
	r.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.mu.Lock()
	r.cancel, r.done = cancel, done
	r.mu.Unlock()
	go func() { defer close(done); r.run(ctx, ip, port) }()
	return nil
}

// Stop ends the advertisement and waits for both processes to be gone.
func (r *Avahi) Stop() error {
	r.mu.Lock()
	cancel, done, procs := r.cancel, r.done, r.procs
	r.cancel, r.done, r.st = nil, nil, RegStatus{}
	r.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	for _, p := range procs {
		p.Kill()
	}
	<-done
	return nil
}

// Status is the latest word from avahi-publish.
func (r *Avahi) Status() RegStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

func (r *Avahi) set(ctx context.Context, st RegStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() == nil { // Stop has already cleared the status
		r.st = st
	}
}

// run keeps both publishers alive until ctx ends, restarting them together
// after 1 s, 2 s, … up to 30 s. A run that got registered starts the wait over.
func (r *Avahi) run(ctx context.Context, ip net.IP, port int) {
	start, after := r.StartProc, r.After
	if start == nil {
		start = execx.StartMerged
	}
	if after == nil {
		after = time.After
	}
	addr := []string{"-a", "-R", hostname, ip.String()}
	svc := []string{"-s", "-H", hostname, "Swarm", "_http._tcp", strconv.Itoa(port)}
	delay := minBackoff
	for ctx.Err() == nil {
		if r.once(ctx, start, addr, svc) {
			delay = minBackoff
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

// avahiLine is one line from a publisher; ok false means it exited.
type avahiLine struct {
	service bool
	text    string
	ok      bool
}

// once runs one pair of publishers until either exits or the address is
// taken, and reports whether both got established.
func (r *Avahi) once(ctx context.Context, start execx.Starter, addr, svc []string) (registered bool) {
	var procs []*execx.Proc
	for _, args := range [][]string{addr, svc} {
		p, err := start(ctx, "avahi-publish", args...)
		if err != nil {
			for _, p := range procs {
				p.Kill()
			}
			r.set(ctx, RegStatus{Err: "Can't run avahi-publish (install avahi-utils to publish " + hostname + "): " + err.Error()})
			return false
		}
		procs = append(procs, p)
	}
	r.mu.Lock()
	if ctx.Err() != nil {
		r.mu.Unlock()
		for _, p := range procs {
			p.Kill()
		}
		return false
	}
	r.procs = procs
	r.mu.Unlock()
	// why is the error the run ends with: what avahi-publish said, or that it stopped.
	var why string
	finished := make(chan struct{}) // releases the readers once this run is over
	defer func() {
		close(finished)
		for _, p := range procs {
			p.Kill() // reap them
		}
		r.mu.Lock()
		r.procs = nil
		r.mu.Unlock()
		if why == "" {
			why = "avahi-publish stopped; starting it again"
		}
		r.set(ctx, RegStatus{Err: why})
	}()

	lines := make(chan avahiLine)
	for i, p := range procs {
		go func(service bool, p *execx.Proc) {
			sc := bufio.NewScanner(p.Stdout)
			for sc.Scan() {
				select {
				case lines <- avahiLine{service, sc.Text(), true}:
				case <-finished:
					return
				}
			}
			select {
			case lines <- avahiLine{service: service}:
			case <-finished:
			}
		}(i == 1, p)
	}
	var addrUp, svcUp bool
	for {
		var l avahiLine
		select {
		case <-ctx.Done():
			return registered
		case l = <-lines:
		}
		if !l.ok {
			return registered
		}
		switch {
		case strings.HasPrefix(l.text, "Established under name "):
			if l.service {
				svcUp = true
			} else {
				addrUp = true
			}
			if addrUp && svcUp {
				registered = true
				r.set(ctx, RegStatus{Registered: true})
			}
		case strings.HasPrefix(l.text, "Name collision"):
			r.logf("lan: avahi-publish: %s", l.text)
			if !l.service {
				// avahi would carry on as swarm-2.local, which nothing points at.
				why = "Another device on this network already uses the name " + hostname + "."
				return registered
			}
		case strings.HasPrefix(l.text, "Failed"):
			_, msg, _ := strings.Cut(l.text, ": ")
			why = "Avahi refused " + hostname + ": " + msg
			r.set(ctx, RegStatus{Err: why})
			r.logf("lan: avahi-publish: %s", l.text)
		case strings.HasPrefix(l.text, "Waiting for daemon"):
			why = "avahi-daemon isn't running, so " + hostname + " can't be published."
			r.set(ctx, RegStatus{Err: why})
		}
	}
}

func (r *Avahi) logf(f string, a ...any) {
	if r.Log != nil {
		r.Log(f, a...)
	}
}
