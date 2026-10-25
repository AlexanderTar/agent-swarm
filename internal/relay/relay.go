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

// offlineIcon is apps/menubar/Resources/AppIcon.svg, base64-inlined so the page needs no second
// request; TestOfflinePageHasTheAppIcon fails if the two drift apart.
const offlineIcon = "PHN2ZyB3aWR0aD0iMTAyNCIgaGVpZ2h0PSIxMDI0IiB2aWV3Qm94PSIwIDAgMTAyNCAxMDI0IiB4bWxucz0iaHR0cDovL3d3dy53" +
	"My5vcmcvMjAwMC9zdmciPgogIDxkZWZzPgogICAgPGxpbmVhckdyYWRpZW50IGlkPSJiYXNlIiB4MT0iMCUiIHkxPSIwJSIgeDI9" +
	"IjEwMCUiIHkyPSIxMDAlIj4KICAgICAgPHN0b3Agb2Zmc2V0PSIwJSIgc3RvcC1jb2xvcj0iIzVCN0NGQSIvPgogICAgICA8c3Rv" +
	"cCBvZmZzZXQ9IjU1JSIgc3RvcC1jb2xvcj0iIzdCNUNGMCIvPgogICAgICA8c3RvcCBvZmZzZXQ9IjEwMCUiIHN0b3AtY29sb3I9" +
	"IiM5QjRGRTAiLz4KICAgIDwvbGluZWFyR3JhZGllbnQ+CiAgICA8bGluZWFyR3JhZGllbnQgaWQ9InNoZWVuIiB4MT0iMCUiIHkx" +
	"PSIwJSIgeDI9IjAlIiB5Mj0iMTAwJSI+CiAgICAgIDxzdG9wIG9mZnNldD0iMCUiIHN0b3AtY29sb3I9IiNGRkZGRkYiIHN0b3At" +
	"b3BhY2l0eT0iMC41NSIvPgogICAgICA8c3RvcCBvZmZzZXQ9IjQ1JSIgc3RvcC1jb2xvcj0iI0ZGRkZGRiIgc3RvcC1vcGFjaXR5" +
	"PSIwLjA4Ii8+CiAgICAgIDxzdG9wIG9mZnNldD0iMTAwJSIgc3RvcC1jb2xvcj0iI0ZGRkZGRiIgc3RvcC1vcGFjaXR5PSIwIi8+" +
	"CiAgICA8L2xpbmVhckdyYWRpZW50PgogICAgPHJhZGlhbEdyYWRpZW50IGlkPSJpbm5lckdsb3ciIGN4PSIzMCUiIGN5PSIyMiUi" +
	"IHI9Ijc1JSI+CiAgICAgIDxzdG9wIG9mZnNldD0iMCUiIHN0b3AtY29sb3I9IiNGRkZGRkYiIHN0b3Atb3BhY2l0eT0iMC4zNSIv" +
	"PgogICAgICA8c3RvcCBvZmZzZXQ9IjM1JSIgc3RvcC1jb2xvcj0iI0ZGRkZGRiIgc3RvcC1vcGFjaXR5PSIwLjA4Ii8+CiAgICAg" +
	"IDxzdG9wIG9mZnNldD0iMTAwJSIgc3RvcC1jb2xvcj0iI0ZGRkZGRiIgc3RvcC1vcGFjaXR5PSIwIi8+CiAgICA8L3JhZGlhbEdy" +
	"YWRpZW50PgogICAgPHJhZGlhbEdyYWRpZW50IGlkPSJmbG9vckdsb3ciIGN4PSI1MCUiIGN5PSIxMDUlIiByPSI2MCUiPgogICAg" +
	"ICA8c3RvcCBvZmZzZXQ9IjAlIiBzdG9wLWNvbG9yPSIjMkExNjYwIiBzdG9wLW9wYWNpdHk9IjAuNDUiLz4KICAgICAgPHN0b3Ag" +
	"b2Zmc2V0PSIxMDAlIiBzdG9wLWNvbG9yPSIjMkExNjYwIiBzdG9wLW9wYWNpdHk9IjAiLz4KICAgIDwvcmFkaWFsR3JhZGllbnQ+" +
	"CiAgICA8ZmlsdGVyIGlkPSJzb2Z0Qmx1ciIgeD0iLTUwJSIgeT0iLTUwJSIgd2lkdGg9IjIwMCUiIGhlaWdodD0iMjAwJSI+CiAg" +
	"ICAgIDxmZUdhdXNzaWFuQmx1ciBzdGREZXZpYXRpb249IjE4Ii8+CiAgICA8L2ZpbHRlcj4KICAgIDxmaWx0ZXIgaWQ9ImdseXBo" +
	"U2hhZG93IiB4PSItNjAlIiB5PSItNjAlIiB3aWR0aD0iMjIwJSIgaGVpZ2h0PSIyMjAlIj4KICAgICAgPGZlRHJvcFNoYWRvdyBk" +
	"eD0iMCIgZHk9IjE2IiBzdGREZXZpYXRpb249IjE4IiBmbG9vZC1jb2xvcj0iIzE4MEI0QSIgZmxvb2Qtb3BhY2l0eT0iMC40NSIv" +
	"PgogICAgICA8ZmVEcm9wU2hhZG93IGR4PSIwIiBkeT0iNCIgc3RkRGV2aWF0aW9uPSI1IiBmbG9vZC1jb2xvcj0iIzE4MEI0QSIg" +
	"Zmxvb2Qtb3BhY2l0eT0iMC4zIi8+CiAgICA8L2ZpbHRlcj4KICAgIDxyYWRpYWxHcmFkaWVudCBpZD0ibm9kZUdyYWQiIGN4PSIz" +
	"NSUiIGN5PSIyOCUiIHI9Ijc4JSI+CiAgICAgIDxzdG9wIG9mZnNldD0iMCUiIHN0b3AtY29sb3I9IiNGRkZGRkYiLz4KICAgICAg" +
	"PHN0b3Agb2Zmc2V0PSI1MCUiIHN0b3AtY29sb3I9IiNGMkVFRkYiLz4KICAgICAgPHN0b3Agb2Zmc2V0PSIxMDAlIiBzdG9wLWNv" +
	"bG9yPSIjQzdCRUYyIi8+CiAgICA8L3JhZGlhbEdyYWRpZW50PgogICAgPGxpbmVhckdyYWRpZW50IGlkPSJhcm1HcmFkIiB4MT0i" +
	"MCUiIHkxPSIwJSIgeDI9IjAlIiB5Mj0iMTAwJSI+CiAgICAgIDxzdG9wIG9mZnNldD0iMCUiIHN0b3AtY29sb3I9IiNGRkZGRkYi" +
	"Lz4KICAgICAgPHN0b3Agb2Zmc2V0PSIxMDAlIiBzdG9wLWNvbG9yPSIjRENENEY3Ii8+CiAgICA8L2xpbmVhckdyYWRpZW50Pgog" +
	"ICAgPGNsaXBQYXRoIGlkPSJzcXVpcmNsZSI+CiAgICAgIDxyZWN0IHg9IjAiIHk9IjAiIHdpZHRoPSIxMDI0IiBoZWlnaHQ9IjEw" +
	"MjQiIHJ4PSIyMjkiIHJ5PSIyMjkiLz4KICAgIDwvY2xpcFBhdGg+CiAgPC9kZWZzPgoKICA8ZyBjbGlwLXBhdGg9InVybCgjc3F1" +
	"aXJjbGUpIj4KICAgIDxyZWN0IHg9IjAiIHk9IjAiIHdpZHRoPSIxMDI0IiBoZWlnaHQ9IjEwMjQiIGZpbGw9InVybCgjYmFzZSki" +
	"Lz4KICAgIDxyZWN0IHg9IjAiIHk9IjAiIHdpZHRoPSIxMDI0IiBoZWlnaHQ9IjEwMjQiIGZpbGw9InVybCgjZmxvb3JHbG93KSIv" +
	"PgogICAgPHJlY3QgeD0iMCIgeT0iMCIgd2lkdGg9IjEwMjQiIGhlaWdodD0iMTAyNCIgZmlsbD0idXJsKCNpbm5lckdsb3cpIi8+" +
	"CgogICAgPCEtLSBmcm9zdGVkIGdsYXNzIGhpZ2hsaWdodCBibG9icyAtLT4KICAgIDxlbGxpcHNlIGN4PSIzMzAiIGN5PSIxNTAi" +
	"IHJ4PSI0MjAiIHJ5PSIyMDAiIGZpbGw9IiNGRkZGRkYiIG9wYWNpdHk9IjAuMjAiIGZpbHRlcj0idXJsKCNzb2Z0Qmx1cikiLz4K" +
	"ICAgIDxlbGxpcHNlIGN4PSI4MDAiIGN5PSIxMjAiIHJ4PSIyMjAiIHJ5PSIxMjAiIGZpbGw9IiNGRkZGRkYiIG9wYWNpdHk9IjAu" +
	"MTQiIGZpbHRlcj0idXJsKCNzb2Z0Qmx1cikiLz4KCiAgICA8IS0tIHRvcCBnbGFzcyBzaGVlbiBiYW5kIC0tPgogICAgPHBhdGgg" +
	"ZD0iTTAgMCBIMTAyNCBWNDIwIEMgNzgwIDUyMCwgMjYwIDUyMCwgMCAzODAgWiIgZmlsbD0idXJsKCNzaGVlbikiLz4KCiAgICA8" +
	"IS0tIGdseXBoOiBwcm9wb3J0aW9uYWwgc2NhbGUtdXAgKHgyNS4zMzMpIG9mIGFzc2V0cy9pY29ucy9zd2FybS5zdmcncwogICAg" +
	"ICAgICAyNHgyNCBnbHlwaCwgY2VudGVyZWQgaW4gdGhlIDEwMjQgY2FudmFzLCBzbyB0aGUgYXJtcyBzdGF5IHN5bW1ldHJpYywK" +
	"ICAgICAgICAgdGhlbiBlbmxhcmdlZCBhIGZ1cnRoZXIgeDEuMyBhYm91dCB0aGUgY2VudHJlIHRvIGZpbGwgfjYwJSBvZiB0aGUg" +
	"dGlsZS4gLS0+CiAgICA8ZyBmaWx0ZXI9InVybCgjZ2x5cGhTaGFkb3cpIj4KICAgICAgPGcgdHJhbnNmb3JtPSJ0cmFuc2xhdGUo" +
	"NTEyIDUxMikgc2NhbGUoMS4zKSB0cmFuc2xhdGUoLTUxMiAtNTEyKSI+CiAgICAgIDxwYXRoIGQ9Ik00ODYuNyA0MjMuMyBoNTAu" +
	"NyB2ODEuMSBsMTE2LjUgMTA4LjkgLTM1LjUgMzUuNSAtMTA2LjQtOTguOCAtMTA2LjQgOTguOCAtMzUuNS0zNS41IDExNi41LTEw" +
	"OC45IHoiIGZpbGw9InVybCgjYXJtR3JhZCkiLz4KICAgICAgPGNpcmNsZSBjeD0iNTEyIiBjeT0iMzYwIiByPSI3NiIgZmlsbD0i" +
	"dXJsKCNub2RlR3JhZCkiLz4KICAgICAgPGNpcmNsZSBjeD0iMzM0LjciIGN5PSI2NjQiIHI9Ijc2IiBmaWxsPSJ1cmwoI25vZGVH" +
	"cmFkKSIvPgogICAgICA8Y2lyY2xlIGN4PSI2ODkuMyIgY3k9IjY2NCIgcj0iNzYiIGZpbGw9InVybCgjbm9kZUdyYWQpIi8+CiAg" +
	"ICAgIDxlbGxpcHNlIGN4PSI0ODYiIGN5PSIzMzIiIHJ4PSIyNiIgcnk9IjE2IiBmaWxsPSIjRkZGRkZGIiBvcGFjaXR5PSIwLjki" +
	"IHRyYW5zZm9ybT0icm90YXRlKC0yNSA0ODYgMzMyKSIvPgogICAgICA8ZWxsaXBzZSBjeD0iMzA4LjciIGN5PSI2MzYiIHJ4PSIy" +
	"NiIgcnk9IjE2IiBmaWxsPSIjRkZGRkZGIiBvcGFjaXR5PSIwLjkiIHRyYW5zZm9ybT0icm90YXRlKC0yNSAzMDguNyA2MzYpIi8+" +
	"CiAgICAgIDxlbGxpcHNlIGN4PSI2NjMuMyIgY3k9IjYzNiIgcng9IjI2IiByeT0iMTYiIGZpbGw9IiNGRkZGRkYiIG9wYWNpdHk9" +
	"IjAuOSIgdHJhbnNmb3JtPSJyb3RhdGUoLTI1IDY2My4zIDYzNikiLz4KICAgICAgPGNpcmNsZSBjeD0iNTEyIiBjeT0iMzYwIiBy" +
	"PSI3NiIgZmlsbD0ibm9uZSIgc3Ryb2tlPSIjNUI0QTlFIiBzdHJva2Utb3BhY2l0eT0iMC4yNSIgc3Ryb2tlLXdpZHRoPSIzIi8+" +
	"CiAgICAgIDxjaXJjbGUgY3g9IjMzNC43IiBjeT0iNjY0IiByPSI3NiIgZmlsbD0ibm9uZSIgc3Ryb2tlPSIjNUI0QTlFIiBzdHJv" +
	"a2Utb3BhY2l0eT0iMC4yNSIgc3Ryb2tlLXdpZHRoPSIzIi8+CiAgICAgIDxjaXJjbGUgY3g9IjY4OS4zIiBjeT0iNjY0IiByPSI3" +
	"NiIgZmlsbD0ibm9uZSIgc3Ryb2tlPSIjNUI0QTlFIiBzdHJva2Utb3BhY2l0eT0iMC4yNSIgc3Ryb2tlLXdpZHRoPSIzIi8+CiAg" +
	"ICAgIDwvZz4KICAgIDwvZz4KCiAgICA8IS0tIHJpbSBsaWdodCBmb3IgZ2xhc3MgZWRnZSBkZWZpbml0aW9uIC0tPgogICAgPHJl" +
	"Y3QgeD0iNiIgeT0iNiIgd2lkdGg9IjEwMTIiIGhlaWdodD0iMTAxMiIgcng9IjIyMyIgcnk9IjIyMyIgZmlsbD0ibm9uZSIgc3Ry" +
	"b2tlPSIjRkZGRkZGIiBzdHJva2Utb3BhY2l0eT0iMC4zNSIgc3Ryb2tlLXdpZHRoPSI0Ii8+CiAgPC9nPgo8L3N2Zz4K"

var offlinePage = `<!doctype html><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1">
<link rel=icon href="data:image/svg+xml;base64,` + offlineIcon + `">
<title>Swarm is offline</title><body style="font:16px system-ui;max-width:28em;margin:20vh auto;padding:0 1em">
<h1>Swarm is offline</h1><p>The computer running Swarm is not connected. Try again in a minute.</p>`

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
