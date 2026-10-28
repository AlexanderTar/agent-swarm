package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/webauth"
)

const (
	testEmail    = "me@example.com"
	testPassword = "correct horse battery"
)

// authEnv is a server with a login set. Its store uses bcrypt's minimum cost for speed.
func authEnv(t *testing.T) (*env, *webauth.Store) {
	t.Helper()
	withOwnIPs(t, "192.168.1.20")
	var store *webauth.Store
	e := newEnv(t, func(d *Deps) { store = webauth.New(d.DB, time.Now).WithCost(4); d.Auth = store })
	if err := store.SetCredentials(bg, testEmail, testPassword); err != nil {
		t.Fatal(err)
	}
	return e, store
}

func serve(e *env, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, r)
	return rec
}

// asRemote re-targets a request onto the lan or tunnel connection remoteReq models.
func asRemote(origin Origin, method, path, body string) *http.Request {
	r := remoteReq(origin, method, path, "")
	if body != "" {
		r2 := httptest.NewRequest(method, path, strings.NewReader(body))
		r2.RemoteAddr, r2.Host, r2.Header = r.RemoteAddr, r.Host, r.Header
		r2.Header.Set("Content-Type", "application/json")
		r = r2.WithContext(r.Context())
	}
	// a browser on the same origin sends this on a write
	if method != "GET" && method != "HEAD" {
		r.Header.Set("Origin", "http://"+r.Host)
	}
	return r
}

func loginBody(email, pw string) string {
	b, _ := json.Marshal(map[string]string{"email": email, "password": pw})
	return string(b)
}

// login signs in from origin and returns the session cookie.
func login(t *testing.T, e *env, origin Origin) *http.Cookie {
	t.Helper()
	rec := serve(e, asRemote(origin, "POST", "/api/auth/login", loginBody(testEmail, testPassword)))
	if rec.Code != 204 {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "swarm_session" {
			return c
		}
	}
	t.Fatal("login set no swarm_session cookie")
	return nil
}

func withCookie(r *http.Request, c *http.Cookie) *http.Request {
	r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	return r
}

func TestAuthRoutes(t *testing.T) {
	e, store := authEnv(t)

	for _, origin := range []Origin{OriginLAN, OriginTunnel} {
		t.Run(string(origin), func(t *testing.T) {
			// bad logins are 401 and set no cookie
			for _, body := range []string{loginBody(testEmail, "wrong password!!"), loginBody("x@y.co", testPassword), `{}`} {
				rec := serve(e, asRemote(origin, "POST", "/api/auth/login", body))
				wantErr(t, rec.Code, rec.Body.Bytes(), 401, "unauthorized", "Wrong email or password.")
				if len(rec.Result().Cookies()) != 0 {
					t.Fatalf("a failed login set a cookie: %v", rec.Result().Cookies())
				}
			}

			// me before login
			rec := serve(e, asRemote(origin, "GET", "/api/auth/me", ""))
			if rec.Code != 200 || rec.Body.String() != "{\"authenticated\":false,\"origin\":\""+string(origin)+"\"}\n" {
				t.Fatalf("me = %d %q", rec.Code, rec.Body)
			}

			// login: 204 and a cookie with the right attributes
			rec = serve(e, asRemote(origin, "POST", "/api/auth/login", loginBody(" ME@example.com ", testPassword)))
			if rec.Code != 204 {
				t.Fatalf("login = %d %s", rec.Code, rec.Body)
			}
			cs := rec.Result().Cookies()
			if len(cs) != 1 {
				t.Fatalf("cookies = %v", cs)
			}
			c := cs[0]
			wantSecure := origin == OriginTunnel
			if c.Name != "swarm_session" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
				c.Secure != wantSecure || c.MaxAge != 30*24*3600 || len(c.Value) < 43 {
				t.Fatalf("cookie = %+v; want HttpOnly SameSite=Lax Path=/ Secure=%v MaxAge=30d", c, wantSecure)
			}
			if strings.Contains(rec.Body.String(), c.Value) {
				t.Fatal("the token must only travel in the cookie")
			}

			// me with the cookie
			rec = serve(e, withCookie(asRemote(origin, "GET", "/api/auth/me", ""), c))
			var me struct {
				Origin        string
				Authenticated bool
				Email         string
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || me.Origin != string(origin) || !me.Authenticated || me.Email != testEmail {
				t.Fatalf("me = %s", rec.Body)
			}

			// logout clears the cookie and revokes the session
			rec = serve(e, withCookie(asRemote(origin, "POST", "/api/auth/logout", ""), c))
			cs = rec.Result().Cookies()
			if rec.Code != 204 || len(cs) != 1 || cs[0].Name != "swarm_session" || cs[0].MaxAge >= 0 || cs[0].Value != "" ||
				!cs[0].HttpOnly || cs[0].Secure != wantSecure {
				t.Fatalf("logout = %d %+v", rec.Code, cs)
			}
			if _, ok := store.LookupSession(bg, c.Value); ok {
				t.Fatal("logout must delete the session")
			}
			rec = serve(e, withCookie(asRemote(origin, "GET", "/api/settings", ""), c))
			wantErr(t, rec.Code, rec.Body.Bytes(), 401, "unauthorized", "Missing or invalid token.")
		})
	}

	// this Mac needs no login
	rec := serve(e, localReq("GET", "/api/auth/me", nil))
	if rec.Code != 200 || rec.Body.String() != "{\"authenticated\":true,\"origin\":\"local\"}\n" {
		t.Fatalf("local me = %d %q", rec.Code, rec.Body)
	}
}

func TestCookieAuthAcceptedOnAllowedRoutesOnly(t *testing.T) {
	e, _ := authEnv(t)
	for _, origin := range []Origin{OriginLAN, OriginTunnel} {
		c := login(t, e, origin)
		// a normal route accepts the cookie in place of the daemon token
		if rec := serve(e, withCookie(asRemote(origin, "GET", "/api/settings", ""), c)); rec.Code != 200 {
			t.Errorf("%s: settings with cookie = %d %s", origin, rec.Code, rec.Body)
		}
		// no cookie, a forged cookie and a cookie on a write without matching Origin... (see CSRF test) all fail
		if rec := serve(e, asRemote(origin, "GET", "/api/settings", "")); rec.Code != 401 {
			t.Errorf("%s: settings without cookie = %d", origin, rec.Code)
		}
		if rec := serve(e, withCookie(asRemote(origin, "GET", "/api/settings", ""), &http.Cookie{Name: "swarm_session", Value: "forged"})); rec.Code != 401 {
			t.Errorf("%s: forged cookie = %d", origin, rec.Code)
		}
		// a daemon token in the Bearer slot is still not accepted remotely
		if rec := serve(e, withCookie(remoteReq(origin, "GET", "/api/settings", "not-the-token"), c)); rec.Code != 200 {
			t.Errorf("%s: a stray Bearer must not override a valid cookie, got %d", origin, rec.Code)
		}
		// local-only routes stay invisible even with a valid cookie
		for _, p := range []string{"/api/bootstrap", "/api/bugs", "/api/kb/status"} {
			rec := serve(e, withCookie(asRemote(origin, "GET", p, ""), c))
			wantErr(t, rec.Code, rec.Body.Bytes(), 404, "not_found", "Unknown API route.")
		}
	}
	// a cookie from a local origin is not how this Mac authenticates: it needs the daemon token
	c := login(t, e, OriginLAN)
	r := localReq("GET", "/api/settings", nil)
	r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	if rec := serve(e, r); rec.Code != 401 {
		t.Fatalf("local with only a cookie = %d, want 401", rec.Code)
	}
}

func TestEventsStreamAcceptsSessionCookie(t *testing.T) {
	e, _ := authEnv(t)
	for _, origin := range []Origin{OriginLAN, OriginTunnel} {
		c := login(t, e, origin)
		r := withCookie(asRemote(origin, "GET", "/api/events", ""), c)
		ctx, cancel := context.WithTimeout(r.Context(), 150*time.Millisecond) // the stream never ends on its own
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, r.WithContext(ctx))
		cancel()
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" {
			t.Errorf("%s: events with only the cookie = %d %s", origin, rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec := serve(e, asRemote(origin, "GET", "/api/events", "")); rec.Code != 401 {
			t.Errorf("%s: events without a cookie = %d, want 401", origin, rec.Code)
		}
	}
}

func TestRemoteHasCredentialsComesFromTheStore(t *testing.T) {
	withOwnIPs(t, "192.168.1.20")
	var store *webauth.Store
	e := newEnv(t, func(d *Deps) { store = webauth.New(d.DB, time.Now).WithCost(4); d.Auth = store })
	rec := serve(e, asRemote(OriginTunnel, "GET", "/api/auth/me", ""))
	wantErr(t, rec.Code, rec.Body.Bytes(), 503, "setup_required", "")
	if err := store.SetCredentials(bg, testEmail, testPassword); err != nil {
		t.Fatal(err)
	}
	if rec := serve(e, asRemote(OriginTunnel, "GET", "/api/auth/me", "")); rec.Code != 200 {
		t.Fatalf("me after SetCredentials = %d %s", rec.Code, rec.Body)
	}
}

// failLogin sends a bad login. ip is the tunnel's X-Forwarded-For, or the lan peer; "" sends none.
func failLogin(e *env, origin Origin, ip string) *httptest.ResponseRecorder {
	r := asRemote(origin, "POST", "/api/auth/login", loginBody(testEmail, "wrong password!!"))
	r.Header.Del("X-Forwarded-For")
	r.Header.Del("X-Real-IP")
	switch {
	case origin == OriginTunnel && ip != "":
		r.Header.Set("X-Forwarded-For", ip)
	case origin == OriginLAN:
		r.RemoteAddr = ip + ":5000"
		r.Header.Set("X-Forwarded-For", "10.9.9."+ip[len(ip)-1:]) // a lan client can send anything; it must not matter
	}
	return serve(e, r)
}

func TestLoginRateLimit(t *testing.T) {
	for _, origin := range []Origin{OriginLAN, OriginTunnel} {
		t.Run(string(origin), func(t *testing.T) {
			e, _ := authEnv(t)
			now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
			e.s.limiter = webauth.NewLimiter(func() time.Time { return now })
			for i := 0; i < 5; i++ {
				if rec := failLogin(e, origin, "192.168.1.31"); rec.Code != 401 {
					t.Fatalf("failure %d = %d", i+1, rec.Code)
				}
			}
			rec := failLogin(e, origin, "192.168.1.31")
			wantErr(t, rec.Code, rec.Body.Bytes(), 429, "rate_limited", "")
			if ra := rec.Header().Get("Retry-After"); ra != "900" {
				t.Fatalf("Retry-After = %q, want 900", ra)
			}
			// even the right password waits out the block
			good := asRemote(origin, "POST", "/api/auth/login", loginBody(testEmail, testPassword))
			good.Header.Del("X-Forwarded-For")
			if origin == OriginTunnel {
				good.Header.Set("X-Forwarded-For", "192.168.1.31")
			} else {
				good.RemoteAddr = "192.168.1.31:5000"
			}
			if rec := serve(e, good); rec.Code != 429 {
				t.Fatalf("right password while blocked = %d, want 429", rec.Code)
			}
			// another client is unaffected
			if rec := failLogin(e, origin, "192.168.1.32"); rec.Code != 401 {
				t.Fatalf("other client = %d, want 401", rec.Code)
			}
			now = now.Add(16 * time.Minute)
			if rec := failLogin(e, origin, "192.168.1.31"); rec.Code != 401 {
				t.Fatalf("after the window = %d, want 401", rec.Code)
			}
		})
	}
}

func TestLanIgnoresForwardedFor(t *testing.T) {
	e, _ := authEnv(t)
	// one lan peer rotating X-Forwarded-For is still one client
	for i := 0; i < 5; i++ {
		r := asRemote(OriginLAN, "POST", "/api/auth/login", loginBody(testEmail, "wrong password!!"))
		r.Header.Set("X-Forwarded-For", "10.1.1."+string(rune('1'+i)))
		serve(e, r)
	}
	r := asRemote(OriginLAN, "POST", "/api/auth/login", loginBody(testEmail, "wrong password!!"))
	r.Header.Set("X-Forwarded-For", "10.1.1.99")
	if rec := serve(e, r); rec.Code != 429 {
		t.Fatalf("6th attempt from one lan peer with a fresh XFF = %d, want 429", rec.Code)
	}
}

func TestTunnelWithoutForwardedForFallsBackToTheGlobalCap(t *testing.T) {
	e, _ := authEnv(t)
	for i := 0; i < 19; i++ {
		if rec := failLogin(e, OriginTunnel, ""); rec.Code != 401 {
			t.Fatalf("failure %d = %d, want 401: an unknown client has no per-client limit", i+1, rec.Code)
		}
	}
	failLogin(e, OriginTunnel, "") // the 20th trips the global lock
	rec := failLogin(e, OriginTunnel, "203.0.113.9")
	wantErr(t, rec.Code, rec.Body.Bytes(), 429, "rate_limited", "")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("global lock must send Retry-After")
	}
}

func TestSuccessfulLoginClearsTheClientsFailures(t *testing.T) {
	e, _ := authEnv(t)
	for i := 0; i < 4; i++ {
		failLogin(e, OriginTunnel, "203.0.113.5")
	}
	good := asRemote(OriginTunnel, "POST", "/api/auth/login", loginBody(testEmail, testPassword))
	good.Header.Set("X-Forwarded-For", "203.0.113.5")
	if rec := serve(e, good); rec.Code != 204 {
		t.Fatalf("login = %d", rec.Code)
	}
	for i := 0; i < 4; i++ {
		if rec := failLogin(e, OriginTunnel, "203.0.113.5"); rec.Code != 401 {
			t.Fatalf("failure %d after success = %d", i+1, rec.Code)
		}
	}
}

func TestCookieWritesNeedAMatchingOrigin(t *testing.T) {
	e, store := authEnv(t)
	for _, origin := range []Origin{OriginLAN, OriginTunnel} {
		c := login(t, e, origin)
		write := func(path, originHdr string) *httptest.ResponseRecorder {
			r := withCookie(asRemote(origin, "PUT", path, `{}`), c)
			r.Header.Del("Origin")
			if originHdr != "" {
				r.Header.Set("Origin", originHdr)
			}
			return serve(e, r)
		}
		host := asRemote(origin, "GET", "/", "").Host
		for _, bad := range []string{"", "null", "https://evil.example", "http://" + host + ".evil.example", "http://other:7777"} {
			rec := write("/api/settings", bad)
			wantErr(t, rec.Code, rec.Body.Bytes(), 403, "forbidden", "")
		}
		if rec := write("/api/settings", "http://"+host); rec.Code == 403 {
			t.Errorf("%s: a matching Origin was refused: %s", origin, rec.Body)
		}
		if rec := write("/api/settings", "https://"+host); rec.Code == 403 {
			t.Errorf("%s: https Origin with the same host was refused: %s", origin, rec.Body)
		}
		// reads need no Origin
		if rec := serve(e, withCookie(asRemote(origin, "GET", "/api/settings", ""), c)); rec.Code != 200 {
			t.Errorf("%s: GET = %d", origin, rec.Code)
		}
		// logout is a cookie write too, and a forged one must not revoke the session
		r := withCookie(asRemote(origin, "POST", "/api/auth/logout", ""), c)
		r.Header.Set("Origin", "https://evil.example")
		if rec := serve(e, r); rec.Code != 403 {
			t.Errorf("%s: cross-origin logout = %d, want 403", origin, rec.Code)
		}
		if _, ok := store.LookupSession(bg, c.Value); !ok {
			t.Errorf("%s: a refused logout revoked the session", origin)
		}
	}
	// local callers use the Bearer token and are not subject to the cookie check
	if rec := serve(e, func() *http.Request {
		r := localReq("PUT", "/api/settings", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+daemonToken)
		return r
	}()); rec.Code == 403 {
		t.Fatal("local Bearer writes need no Origin")
	}
}

func TestCredentialsRoutesAreLocalOnlyAndNeedTheDaemonToken(t *testing.T) {
	e, store := authEnv(t)
	c := login(t, e, OriginLAN)
	routes := []struct{ method, path, body string }{
		{"PUT", "/api/auth/credentials", `{"email":"new@example.com","password":"a brand new password"}`},
		{"GET", "/api/auth/credentials", ""},
		{"DELETE", "/api/auth/sessions", ""},
	}
	for _, rt := range routes {
		// no daemon token: 401 locally
		r := localReq(rt.method, rt.path, strings.NewReader(rt.body))
		if rec := serve(e, r); rec.Code != 401 {
			t.Errorf("%s %s without the token = %d, want 401", rt.method, rt.path, rec.Code)
		}
		// remote: 404, with a valid cookie and even with the daemon token
		for _, origin := range []Origin{OriginLAN, OriginTunnel} {
			for _, tok := range []string{"", daemonToken} {
				r := withCookie(asRemote(origin, rt.method, rt.path, rt.body), c)
				if tok != "" {
					r.Header.Set("Authorization", "Bearer "+tok)
				}
				rec := serve(e, r)
				wantErr(t, rec.Code, rec.Body.Bytes(), 404, "not_found", "Unknown API route.")
			}
		}
	}
	if !store.Verify(bg, testEmail, testPassword) {
		t.Fatal("a refused request changed the login")
	}
	if _, ok := store.LookupSession(bg, c.Value); !ok {
		t.Fatal("a refused request revoked a session")
	}
}

func TestCredentialsRoutes(t *testing.T) {
	e, store := authEnv(t)
	c := login(t, e, OriginLAN)
	local := func(method, path, body string) *httptest.ResponseRecorder {
		r := localReq(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+daemonToken)
		return serve(e, r)
	}

	rec := local("GET", "/api/auth/credentials", "")
	if rec.Code != 200 || rec.Body.String() != "{\"configured\":true,\"email\":\""+testEmail+"\",\"sessions\":1}\n" {
		t.Fatalf("status = %d %q", rec.Code, rec.Body)
	}

	// a short password is refused and changes nothing
	rec = local("PUT", "/api/auth/credentials", `{"email":"new@example.com","password":"short"}`)
	wantErr(t, rec.Code, rec.Body.Bytes(), 400, "bad_request", "The password must be at least 12 characters.")
	if !store.Verify(bg, testEmail, testPassword) {
		t.Fatal("a refused PUT changed the login")
	}

	// a good PUT replaces the login and signs everyone out
	rec = local("PUT", "/api/auth/credentials", `{"email":"new@example.com","password":"a brand new password"}`)
	if rec.Code != 204 {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	if !store.Verify(bg, "new@example.com", "a brand new password") || store.Verify(bg, testEmail, testPassword) {
		t.Fatal("PUT did not replace the login")
	}
	if rec := serve(e, withCookie(asRemote(OriginLAN, "GET", "/api/settings", ""), c)); rec.Code != 401 {
		t.Fatalf("old cookie after reset = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "brand new") {
		t.Fatal("password echoed")
	}

	// logout-all
	c2 := login2(t, e)
	if n, _ := store.SessionCount(bg); n != 1 {
		t.Fatalf("sessions = %d, want 1", n)
	}
	if rec := local("DELETE", "/api/auth/sessions", ""); rec.Code != 204 {
		t.Fatalf("DELETE = %d", rec.Code)
	}
	if _, ok := store.LookupSession(bg, c2.Value); ok {
		t.Fatal("logout-all left a session")
	}
	if rec := local("GET", "/api/auth/credentials", ""); !strings.Contains(rec.Body.String(), `"sessions":0`) {
		t.Fatalf("status after logout-all = %s", rec.Body)
	}
}

// login2 signs in with the credentials TestCredentialsRoutes set.
func login2(t *testing.T, e *env) *http.Cookie {
	t.Helper()
	rec := serve(e, asRemote(OriginTunnel, "POST", "/api/auth/login", loginBody("new@example.com", "a brand new password")))
	if rec.Code != 204 {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	return rec.Result().Cookies()[0]
}

func TestCredentialsStatusWhenNoLoginIsSet(t *testing.T) {
	withOwnIPs(t, "192.168.1.20")
	e := newEnv(t, func(d *Deps) { d.Auth = webauth.New(d.DB, time.Now).WithCost(4) })
	rec := serve(e, func() *http.Request {
		r := localReq("GET", "/api/auth/credentials", nil)
		r.Header.Set("Authorization", "Bearer "+daemonToken)
		return r
	}())
	if rec.Code != 200 || rec.Body.String() != "{\"configured\":false,\"sessions\":0}\n" {
		t.Fatalf("status = %d %q", rec.Code, rec.Body)
	}
}
