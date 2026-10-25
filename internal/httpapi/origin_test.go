package httpapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

// localReq is a request from this Mac's loopback, as a browser or the CLI sends it.
func localReq(method, path string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, path, body)
	r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:7777"
	return r
}

func kp(k ListenerKind) *ListenerKind { return &k }

// remoteReq builds a request as the lan or tunnel listener would deliver it,
// carrying every header a spoofer would try.
func remoteReq(origin Origin, method, path, token string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	kind := ListenerLAN
	r.RemoteAddr, r.Host = "192.168.1.77:5000", "swarm.local"
	if origin == OriginTunnel {
		kind = ListenerTunnel
		r.RemoteAddr, r.Host = "127.0.0.1:5000", "localhost"
	}
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	r.Header.Set("X-Real-IP", "127.0.0.1")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r.WithContext(ConnContext(kind)(context.Background(), nil))
}

func TestRemoteOriginPolicy(t *testing.T) {
	withOwnIPs(t, "192.168.1.20")
	e := newEnv(t, func(d *Deps) { d.HasCredentials = func(context.Context) bool { return true } })
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, r)
		return rec
	}
	localOnly := []struct{ method, path string }{
		{"GET", "/api/bootstrap"},
		{"POST", "/mcp"}, {"GET", "/mcp"}, {"DELETE", "/mcp"},
		{"POST", "/hook/claude/PreToolUse"},
		{"GET", "/api/sessions/self/wake"},
		{"POST", "/api/agents/a/terminal"},
		{"POST", "/api/agents/a/terminal-opened"},
		{"GET", "/api/agents/a/replacement"},
		{"POST", "/api/dev/seed"},
		{"GET", "/api/bugs"},
		{"GET", "/api/kb/status"}, {"GET", "/api/kb/search"}, {"GET", "/api/kb/specs/alpha"},
	}
	for _, origin := range []Origin{OriginLAN, OriginTunnel} {
		for _, rt := range localOnly {
			// 404 whether or not the caller presents the daemon token
			for _, tok := range []string{"", daemonToken} {
				rec := serve(remoteReq(origin, rt.method, rt.path, tok))
				wantErr(t, rec.Code, rec.Body.Bytes(), 404, "not_found", "Unknown API route.")
			}
		}
		// the daemon Bearer token is local-only: a non-local-only route says 401
		rec := serve(remoteReq(origin, "GET", "/api/settings", daemonToken))
		wantErr(t, rec.Code, rec.Body.Bytes(), 401, "unauthorized", "Missing or invalid token.")
	}
	// every route that authenticates with a session token or the loopback check
	// must be on the local-only list, so a new one cannot be forgotten
	for _, rt := range e.s.routes {
		if (rt.auth == authSession || rt.auth == authLoopback) && !localOnlyRoute(rt) {
			t.Errorf("%s %s is not local-only", rt.method, rt.pattern)
		}
	}
	// local behaviour is unchanged
	r := localReq("GET", "/api/settings", nil)
	r.Header.Set("Authorization", "Bearer "+daemonToken)
	if rec := serve(r); rec.Code != 200 {
		t.Fatalf("local settings = %d %s", rec.Code, rec.Body)
	}
	if rec := serve(localReq("GET", "/api/health", nil)); rec.Code != 200 {
		t.Fatalf("local health = %d", rec.Code)
	}
}

func TestRemoteWithoutCredentials(t *testing.T) {
	withOwnIPs(t, "192.168.1.20")
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "board") })
	has := false
	e := newEnv(t, func(d *Deps) {
		d.Web = web
		d.HasCredentials = func(context.Context) bool { return has }
	})
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, r)
		return rec
	}
	for _, origin := range []Origin{OriginLAN, OriginTunnel} {
		// pages get HTML, API paths get the JSON envelope; known and unknown routes alike
		for _, p := range []string{"/", "/assets/app.js"} {
			rec := serve(remoteReq(origin, "GET", p, ""))
			if rec.Code != 503 || !strings.Contains(rec.Body.String(), "swarm auth set") ||
				!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || strings.Contains(rec.Body.String(), "board") {
				t.Errorf("%s %s = %d %q (%s)", origin, p, rec.Code, rec.Body, rec.Header().Get("Content-Type"))
			}
		}
		for _, p := range []string{"/api/health", "/api/items", "/api/nope"} {
			rec := serve(remoteReq(origin, "GET", p, daemonToken))
			wantErr(t, rec.Code, rec.Body.Bytes(), 503, "setup_required", "Run `swarm auth set` on the computer running Swarm to turn on remote access.")
		}
		// local-only routes stay invisible
		wantErrRec := serve(remoteReq(origin, "GET", "/api/bootstrap", ""))
		wantErr(t, wantErrRec.Code, wantErrRec.Body.Bytes(), 404, "not_found", "Unknown API route.")
	}
	// this Mac never needs credentials
	if rec := serve(localReq("GET", "/api/health", nil)); rec.Code != 200 {
		t.Fatalf("local health = %d", rec.Code)
	}
	if rec := serve(localReq("GET", "/", nil)); rec.Code != 200 || rec.Body.String() != "board" {
		t.Fatalf("local board = %d %q", rec.Code, rec.Body)
	}
	// once credentials exist the gate opens
	has = true
	if rec := serve(remoteReq(OriginLAN, "GET", "/api/health", "")); rec.Code != 200 {
		t.Fatalf("lan health with credentials = %d", rec.Code)
	}
	if rec := serve(remoteReq(OriginTunnel, "GET", "/", "")); rec.Code != 200 {
		t.Fatalf("tunnel board with credentials = %d", rec.Code)
	}
}
