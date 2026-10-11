// Package tunnel is the daemon's side of the relay tunnel: it dials the relay
// over WebSocket, runs yamux as the server and serves the daemon's HTTP handler
// on every stream, tagged as tunnel origin so a tunnelled request can never
// look local.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"github.com/AlexanderTar/agent-swarm/internal/httpapi"
)

// Status is what `swarm tunnel status` reports.
type Status struct {
	State          string // disabled | connecting | connected | error
	URL            string
	ConnectedSince time.Time
	// Reconnects counts redials after a failed or dropped connection.
	Reconnects int
	LastError  string
}

const (
	minBackoff   = time.Second
	maxBackoff   = 60 * time.Second
	stableAfter  = 60 * time.Second
	jitterFactor = 0.25
)

// Client keeps one tunnel to the relay up until Run's context ends. Dial, Now,
// Sleep and Rand are test seams; the zero values do the real thing.
type Client struct {
	URL     string // relay base, e.g. wss://relay.example
	Secret  string
	Handler http.Handler
	Log     func(string, ...any)

	Dial  func(ctx context.Context, url, secret string) (net.Conn, error)
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	Rand  func() float64

	mu sync.Mutex
	st Status
}

// Status returns a snapshot. Before Run and after it returns, State is disabled.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.State == "" {
		return Status{State: "disabled", URL: c.URL}
	}
	return c.st
}

func (c *Client) update(f func(*Status)) {
	c.mu.Lock()
	f(&c.st)
	c.mu.Unlock()
}

func (c *Client) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Run connects and reconnects until ctx ends, then returns nil.
func (c *Client) Run(ctx context.Context) error {
	c.update(func(s *Status) { *s = Status{State: "connecting", URL: c.URL} })
	defer c.update(func(s *Status) { *s = Status{State: "disabled", URL: c.URL} })
	attempt := 0
	for {
		c.update(func(s *Status) { s.State = "connecting" })
		err := c.once(ctx, &attempt)
		if ctx.Err() != nil {
			return nil
		}
		c.logf("tunnel: %v", err)
		c.update(func(s *Status) { s.State, s.LastError, s.ConnectedSince = "error", err.Error(), time.Time{} })
		d := c.backoff(attempt)
		attempt++
		c.update(func(s *Status) { s.Reconnects++ })
		if c.sleep(ctx, d) != nil {
			return nil
		}
	}
}

// once makes one connection and serves it until it drops. It resets *attempt
// when the session stayed up long enough to count as healthy.
func (c *Client) once(ctx context.Context, attempt *int) error {
	dial := c.Dial
	if dial == nil {
		dial = dialWebSocket
	}
	conn, err := dial(ctx, tunnelURL(c.URL), c.Secret)
	if err != nil {
		return err
	}
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.LogOutput = io.Discard
	sess, err := yamux.Server(conn, cfg)
	if err != nil {
		conn.Close()
		return err
	}
	up := c.now()
	c.update(func(s *Status) { s.State, s.ConnectedSince, s.LastError = "connected", up, "" })
	srv := &http.Server{
		Handler:           noUpgrades(c.Handler),
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext:       httpapi.ConnContext(httpapi.ListenerTunnel),
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
		}
		srv.Close()
		sess.Close()
	}()
	err = srv.Serve(sess)
	if c.now().Sub(up) >= stableAfter {
		*attempt = 0
	}
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, yamux.ErrSessionShutdown) {
		err = errors.New("relay closed the tunnel")
	}
	return err
}

func (c *Client) backoff(attempt int) time.Duration {
	d := minBackoff
	for i := 0; i < attempt && d < maxBackoff; i++ {
		d *= 2
	}
	r := rand.Float64
	if c.Rand != nil {
		r = c.Rand
	}
	d += time.Duration(float64(d) * jitterFactor * r())
	return min(d, maxBackoff)
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// tunnelURL is the relay's /_tunnel endpoint for a configured base URL.
func tunnelURL(base string) string {
	return strings.TrimRight(base, "/") + "/_tunnel"
}

// noUpgrades answers 501 to protocol upgrades: the relay does not carry
// WebSockets, and this keeps the daemon from hijacking a tunnel stream.
func noUpgrades(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			http.Error(w, "upgrades are not supported", http.StatusNotImplemented)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// dialWebSocket opens the relay's /_tunnel WebSocket with the Bearer secret.
// Errors never include the secret.
func dialWebSocket(ctx context.Context, url, secret string) (net.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(dctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}},
	})
	if err != nil {
		switch {
		case resp != nil && resp.StatusCode == http.StatusUnauthorized:
			return nil, errors.New("relay rejected the secret (401)")
		case resp != nil:
			return nil, fmt.Errorf("relay answered %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("dial relay: %w", err)
	}
	ws.SetReadLimit(1 << 20)
	// the dial context only covers the handshake; the conn lives until closed
	return websocket.NetConn(context.Background(), ws, websocket.MessageBinary), nil
}
