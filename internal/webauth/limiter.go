package webauth

import (
	"sync"
	"time"
)

const (
	clientMax    = 5 // failures per client per clientWindow
	clientWindow = 15 * time.Minute
	globalMax    = 20 // failures per globalWindow, across all clients, before the lock
	globalWindow = time.Hour
	lockFor      = 15 * time.Minute
)

// Limiter throttles login attempts: 5 failures per client per 15 minutes, and a
// 15 minute lock on everyone after 20 failures in an hour. A client whose IP is
// unknown ("") has no per-client bucket and answers only to the global cap.
type Limiter struct {
	mu          sync.Mutex
	now         func() time.Time
	clients     map[string][]time.Time
	global      []time.Time
	lockedUntil time.Time
}

func NewLimiter(now func() time.Time) *Limiter {
	return &Limiter{now: now, clients: map[string][]time.Time{}}
}

// Allow reports whether clientIP may try a login now; when it may not, the
// duration is the Retry-After.
func (l *Limiter) Allow(clientIP string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Before(l.lockedUntil) {
		return false, l.lockedUntil.Sub(now)
	}
	if clientIP == "" {
		return true, 0
	}
	fails := recent(l.clients[clientIP], now, clientWindow)
	if len(fails) >= clientMax {
		// the block lifts when the failure that makes it 5 ages out
		return false, fails[len(fails)-clientMax].Add(clientWindow).Sub(now)
	}
	return true, 0
}

func (l *Limiter) Fail(clientIP string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for ip, fails := range l.clients { // bounded: Allow stops counting once the global lock is on
		if fails = recent(fails, now, clientWindow); len(fails) == 0 {
			delete(l.clients, ip)
		} else {
			l.clients[ip] = fails
		}
	}
	if clientIP != "" {
		l.clients[clientIP] = append(l.clients[clientIP], now)
	}
	l.global = append(recent(l.global, now, globalWindow), now)
	if len(l.global) >= globalMax {
		l.lockedUntil, l.global = now.Add(lockFor), nil
	}
}

// Success forgets clientIP's failures.
func (l *Limiter) Success(clientIP string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, clientIP)
}

// recent keeps the times inside window of now.
func recent(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(now.Add(-window)) {
		i++
	}
	return ts[i:]
}
