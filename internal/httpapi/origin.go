package httpapi

import (
	"context"
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
