// Package relay is the public side of the Swarm tunnel: it accepts the
// daemon's outbound WebSocket, runs yamux over it and reverse-proxies public
// HTTP into the daemon. It does no auth on public traffic; the daemon enforces
// login. Its only secret is the one the daemon presents on /_tunnel.
package relay

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Options tunes the relay. The zero value is usable.
type Options struct {
	// MaxBody caps a proxied request body in bytes. 0 means 32 MB.
	MaxBody int64
}

const defaultMaxBody = 32 << 20

// ValidSecret reports whether s is at least 64 hex characters (32 bytes, as
// `openssl rand -hex 32` makes).
func ValidSecret(s string) bool {
	if len(s) < 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && len(s)%2 == 0
}

// NewServer returns an http.Server with the relay's size and time caps. There
// is no write timeout: responses may be SSE streams.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
}

// tunnel is one live daemon session and the proxy that dials through it.
type tunnel struct {
	sess  *yamux.Session
	proxy *httputil.ReverseProxy
	tr    *http.Transport
}

type handler struct {
	secretHash [32]byte
	maxBody    int64

	mu  sync.Mutex
	cur *tunnel
}

// New returns the relay handler. secret is the Bearer token the daemon must
// present on /_tunnel.
func New(secret string, opts Options) http.Handler {
	h := &handler{secretHash: sha256.Sum256([]byte(secret)), maxBody: opts.MaxBody}
	if h.maxBody <= 0 {
		h.maxBody = defaultMaxBody
	}
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	case r.URL.Path == "/_tunnel":
		h.accept(w, r)
	case r.URL.Path == "/_tunnel/" || strings.HasPrefix(r.URL.Path, "/_tunnel/"):
		http.NotFound(w, r)
	default:
		h.proxy(w, r)
	}
}

func (h *handler) authorized(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	got := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare(got[:], h.secretHash[:]) == 1
}

// accept authenticates the daemon, upgrades, and makes the new session the
// only one. The check comes first so a wrong secret never gets a WebSocket.
func (h *handler) accept(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept already replied
	}
	c.SetReadLimit(1 << 20)
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.LogOutput = io.Discard
	// r.Context() ends when this handler returns, so the conn gets its own.
	sess, err := yamux.Client(websocket.NetConn(context.Background(), c, websocket.MessageBinary), cfg)
	if err != nil {
		c.Close(websocket.StatusInternalError, "")
		return
	}
	t := h.newTunnel(sess)
	h.mu.Lock()
	old := h.cur
	h.cur = t
	h.mu.Unlock()
	if old != nil {
		old.sess.Close()
		old.tr.CloseIdleConnections()
	}
	<-sess.CloseChan()
	h.mu.Lock()
	if h.cur == t {
		h.cur = nil
	}
	h.mu.Unlock()
	t.tr.CloseIdleConnections()
}

func (h *handler) newTunnel(sess *yamux.Session) *tunnel {
	t := &tunnel{sess: sess}
	t.tr = &http.Transport{
		DialContext:        func(context.Context, string, string) (net.Conn, error) { return sess.Open() },
		DisableCompression: true,
		IdleConnTimeout:    90 * time.Second,
	}
	t.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			ip := clientIP(pr.In)
			for k := range out.Header {
				if strings.HasPrefix(k, "X-Forwarded-") || strings.HasPrefix(k, "X-Tunnel-") {
					delete(out.Header, k)
				}
			}
			out.Header.Del("Forwarded")
			out.Header.Del("X-Real-Ip")
			out.Header.Set("X-Forwarded-For", ip)
			out.Header.Set("X-Forwarded-Proto", "https")
			// the dial goes over the session; only the Host the daemon sees matters
			out.URL.Scheme = "http"
			out.URL.Host = "tunnel"
			out.Host = pr.In.Host
		},
		Transport:     t.tr,
		FlushInterval: -1,
		// a client leaving mid-stream (SSE) is routine, not worth a log line
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}
	return t
}

// clientIP is the caller's address: X-Real-IP (Railway's edge sets it), else
// the right-most X-Forwarded-For hop (the one the edge appended), else the peer.
func clientIP(r *http.Request) string {
	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
		return ip.String()
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		hops := strings.Split(xff, ",")
		if ip := net.ParseIP(strings.TrimSpace(hops[len(hops)-1])); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

var offlinePage = `<!doctype html><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1">
<title>Swarm is offline</title><body style="font:16px system-ui;max-width:28em;margin:20vh auto;padding:0 1em">
<h1>Swarm is offline</h1><p>The Mac running Swarm is not connected. Try again in a minute.</p>`

func (h *handler) proxy(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "" {
		http.Error(w, "upgrades are not supported", http.StatusNotImplemented)
		return
	}
	h.mu.Lock()
	t := h.cur
	h.mu.Unlock()
	if t == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, offlinePage)
		return
	}
	if r.ContentLength > h.maxBody {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	t.proxy.ServeHTTP(w, r)
}
