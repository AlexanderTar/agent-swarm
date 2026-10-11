package httpapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Origin says where a request came from. It is decided from the connection,
// never from a header: no header a client sends can make a remote request local.
type Origin string

const (
	OriginLocal  Origin = "local"
	OriginLAN    Origin = "lan"
	OriginTunnel Origin = "tunnel"
)

// ListenerKind is which listener accepted a connection.
type ListenerKind int

const (
	ListenerLoopback ListenerKind = iota
	ListenerLAN
	ListenerTunnel
)

type listenerKey struct{}

// ConnContext is an http.Server.ConnContext that tags every request on the
// connection with the listener that accepted it.
func ConnContext(kind ListenerKind) func(context.Context, net.Conn) context.Context {
	return func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, listenerKey{}, kind)
	}
}

// OwnIPs lists this machine's addresses. Tests replace it.
var OwnIPs = func() []net.IP {
	addrs, _ := net.InterfaceAddrs()
	var out []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			out = append(out, n.IP)
		}
	}
	return out
}

// RequestOrigin classifies r. A tunnel connection is tunnel. Otherwise the
// request is local only when the peer is this machine (loopback or an own IP)
// and the Host names this machine (guards against DNS rebinding and a LAN
// client that merely sends Host: swarm.local); everything else is lan. A
// request without a ConnContext is treated as loopback-kind.
func RequestOrigin(r *http.Request) Origin {
	kind, _ := r.Context().Value(listenerKey{}).(ListenerKind)
	if kind == ListenerTunnel {
		return OriginTunnel
	}
	own := OwnIPs()
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	pip := net.ParseIP(peer)
	if err != nil || pip == nil || !(pip.IsLoopback() || containsIP(own, pip)) {
		return OriginLAN
	}
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	} else {
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	}
	switch strings.ToLower(h) {
	case "127.0.0.1", "localhost", "::1", "swarm.local":
		return OriginLocal
	}
	if hip := net.ParseIP(h); hip != nil && containsIP(own, hip) {
		return OriginLocal
	}
	return OriginLAN
}

func containsIP(ips []net.IP, ip net.IP) bool {
	for _, o := range ips {
		if o.Equal(ip) {
			return true
		}
	}
	return false
}

// localOnlyPaths are the routes that lan and tunnel callers must not learn
// exist: they hand out or accept the daemon token, or drive this Mac. Routes
// that authenticate with a session token or the loopback check are local-only
// too, whether or not they are listed. Add a new local-only route here.
var localOnlyPaths = map[string]bool{
	"/api/bootstrap":                     true,
	"/mcp":                               true,
	"/hook/{agent}/{event}":              true,
	"/api/sessions/self/wake":            true,
	"/api/agents/{name}/terminal":        true,
	"/api/agents/{name}/terminal-opened": true,
	"/api/agents/{name}/replacement":     true,
	"/api/dev/seed":                      true,
	"/api/bugs":                          true,
	"/api/auth/credentials":              true,
	"/api/auth/sessions":                 true,
	"/api/tunnel":                        true,
	"/api/lan":                           true,
}

func localOnlyRoute(rt route) bool {
	return localOnlyPaths[rt.pattern] || strings.HasPrefix(rt.pattern, "/api/kb/") ||
		rt.auth == authSession || rt.auth == authLoopback
}

const setupMessage = "Run `swarm auth set` on the Mac to turn on remote access."

// setupRequired answers 503 and reports true while a lan or tunnel caller has
// no login to use yet: HTML for pages, the JSON error envelope under /api/.
func (s *Server) setupRequired(w http.ResponseWriter, r *http.Request, origin Origin) bool {
	if origin == OriginLocal || (s.HasCredentials != nil && s.HasCredentials(r.Context())) {
		return false
	}
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		s.writeErr(w, apiErr(http.StatusServiceUnavailable, "setup_required", setupMessage))
		return true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Swarm</title>`+
		`<body style="font:16px system-ui;max-width:32rem;margin:4rem auto;padding:0 1rem"><h1>Swarm isn't ready for remote access</h1>`+
		`<p>Run <code>swarm auth set</code> on the Mac to choose an email and password, then reload this page.</p></body>`)
	return true
}
