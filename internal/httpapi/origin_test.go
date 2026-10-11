package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withOwnIPs swaps the own-IP source for one test.
func withOwnIPs(t *testing.T, ips ...string) {
	t.Helper()
	old := OwnIPs
	OwnIPs = func() []net.IP {
		var out []net.IP
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out
	}
	t.Cleanup(func() { OwnIPs = old })
}

func TestRequestOrigin(t *testing.T) {
	withOwnIPs(t, "192.168.1.20")
	cases := []struct {
		name string
		kind *ListenerKind // nil: no ConnContext at all
		peer string
		host string
		hdrs map[string]string
		want Origin
	}{
		{"loopback with localhost", kp(ListenerLoopback), "127.0.0.1:5000", "localhost:7777", nil, OriginLocal},
		{"loopback ipv6 literal host", kp(ListenerLoopback), "[::1]:5000", "[::1]:7777", nil, OriginLocal},
		{"no conn context counts as loopback", nil, "127.0.0.1:5000", "127.0.0.1:7777", nil, OriginLocal},
		{"loopback with foreign host (rebinding)", kp(ListenerLoopback), "127.0.0.1:5000", "evil.com", nil, OriginLAN},
		{"own IP with swarm.local", kp(ListenerLAN), "192.168.1.20:5000", "swarm.local:7777", nil, OriginLocal},
		{"own IP with own IP host", kp(ListenerLAN), "192.168.1.20:5000", "192.168.1.20:7777", nil, OriginLocal},
		{"own IP with foreign host", kp(ListenerLAN), "192.168.1.20:5000", "evil.com", nil, OriginLAN},
		{"LAN peer with swarm.local", kp(ListenerLAN), "192.168.1.77:5000", "swarm.local", nil, OriginLAN},
		{"LAN peer with localhost", kp(ListenerLAN), "192.168.1.77:5000", "localhost", nil, OriginLAN},
		{"LAN peer claiming loopback via XFF", kp(ListenerLAN), "192.168.1.77:5000", "swarm.local", map[string]string{"X-Forwarded-For": "127.0.0.1"}, OriginLAN},
		{"tunnel with localhost and XFF loopback", kp(ListenerTunnel), "127.0.0.1:5000", "localhost", map[string]string{"X-Forwarded-For": "127.0.0.1", "X-Real-IP": "127.0.0.1"}, OriginTunnel},
		{"tunnel with own IP peer and swarm.local", kp(ListenerTunnel), "192.168.1.20:5000", "swarm.local", nil, OriginTunnel},
		{"unparsable peer", kp(ListenerLoopback), "garbage", "localhost", nil, OriginLAN},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			r.RemoteAddr, r.Host = c.peer, c.host
			for k, v := range c.hdrs {
				r.Header.Set(k, v)
			}
			if c.kind != nil {
				r = r.WithContext(ConnContext(*c.kind)(context.Background(), nil))
			}
			if got := RequestOrigin(r); got != c.want {
				t.Fatalf("RequestOrigin = %q, want %q", got, c.want)
			}
		})
	}
}

func kp(k ListenerKind) *ListenerKind { return &k }
