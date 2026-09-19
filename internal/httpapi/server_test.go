package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/web"
)

func samplePath(pattern string) string {
	r := strings.NewReplacer("{key}", "EPIC-1", "{blockedBy}", "EPIC-2", "{slug...}", "specs/alpha")
	return r.Replace(pattern)
}

// seedSessionToken stores a session whose token is `token` in state `state`.
func seedSessionToken(t *testing.T, e *env, token, state string) string {
	t.Helper()
	it, err := e.items.Create(bg, items.CreateInput{Type: items.Epic, Title: "Host"}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	agent, ses := ids.New("agt"), ids.New("ses")
	sum := sha256.Sum256([]byte(token))
	if _, err := e.s.DB.Exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES (?, ?, 'fake', 'm', 'coder', ?, ?, 'b', 'active', 1)`, agent, agent, it.ID, it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DB.Exec(`INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES (?, ?, 1, 1, ?, 'x', '/tmp', ?, 'neutral', 1)`, ses, agent, hex.EncodeToString(sum[:]), state); err != nil {
		t.Fatal(err)
	}
	return ses
}

func TestEveryAPIRouteNeedsTheDaemonToken(t *testing.T) {
	e := newEnv(t)
	seedSessionToken(t, e, "session-token", "running")
	checked := 0
	for _, rt := range e.s.routes {
		if rt.auth != authDaemon {
			continue
		}
		checked++
		for _, token := range []string{"", "wrong", "session-token"} {
			status, body := e.call(rt.method, samplePath(rt.pattern), map[string]any{}, token)
			wantErr(t, status, body, 401, "unauthorized", "Missing or invalid token.")
		}
		if rt.method == "GET" && rt.pattern != "/api/events" {
			if status, _ := e.api(rt.method, samplePath(rt.pattern), nil); status == 401 {
				t.Errorf("%s %s rejects the daemon token", rt.method, rt.pattern)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no daemon routes registered")
	}
}

func TestHealthNeedsNoToken(t *testing.T) {
	e := newEnv(t)
	status, body := e.call("GET", "/api/health", nil, "")
	h := decode[map[string]any](t, body)
	if status != 200 || h["ok"] != true || h["version"] != "test" || h["schema"] != float64(1) {
		t.Fatalf("health = %d %s", status, body)
	}
}

func TestBootstrapIsLoopbackOnly(t *testing.T) {
	e := newEnv(t)
	status, body := e.call("GET", "/api/bootstrap", nil, "")
	if status != 200 || decode[map[string]string](t, body)["token"] != daemonToken {
		t.Fatalf("loopback bootstrap = %d %s", status, body)
	}
	cases := []struct{ remote, host string }{
		{"10.0.0.5:5000", "127.0.0.1:7777"},     // remote peer, even with a spoofed header
		{"127.0.0.1:5000", "evil.example:7777"}, // DNS rebinding
		{"[::1]:5000", "attacker.test"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/api/bootstrap", nil)
		req.RemoteAddr, req.Host = c.remote, c.host
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		wantErr(t, rec.Code, rec.Body.Bytes(), 401, "unauthorized", "Bootstrap is only available on this Mac.")
	}
	for _, host := range []string{"localhost:7777", "[::1]:7777", "127.0.0.1", "localhost", "[::1]"} {
		req := httptest.NewRequest("GET", "/api/bootstrap", nil)
		req.RemoteAddr, req.Host = "[::1]:5000", host
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("host %s = %d", host, rec.Code)
		}
	}
}

func TestErrorEnvelope(t *testing.T) {
	cases := []struct {
		err       error
		status    int
		code, msg string
		reason    string
	}{
		{apiErr(400, "bad_request", "Bad."), 400, "bad_request", "Bad.", ""},
		{apiErr(401, "unauthorized", "No."), 401, "unauthorized", "No.", ""},
		{apiErr(404, "not_found", "Gone."), 404, "not_found", "Gone.", ""},
		{apiErr(409, "conflict", "Clash."), 409, "conflict", "Clash.", ""},
		{apiErr(429, "limit_reached", "Slow down."), 429, "limit_reached", "Slow down.", ""},
		{apiErr(422, "preflight_failed", "Claude isn't installed on this Mac."), 422, "preflight_failed", "Claude isn't installed on this Mac.", ""},
		{&items.Error{Code: "transition_denied", Message: "Accept this epic to mark it Done."}, 422, "transition_denied", "Accept this epic to mark it Done.", "Accept this epic to mark it Done."},
		{&items.Error{Code: "conflict", Message: items.CycleMessage}, 409, "conflict", items.CycleMessage, ""},
		{&items.Error{Code: "not_found", Message: "No item EPIC-9."}, 404, "not_found", "No item EPIC-9.", ""},
		{&items.Error{Code: "bad_request", Message: "Title must be 1–200 characters."}, 400, "bad_request", "Title must be 1–200 characters.", ""},
		{&settings.ValidationError{Message: "At least one agent must stay enabled."}, 400, "bad_request", "At least one agent must stay enabled.", ""},
		{&kb.NotFoundError{Slug: "x"}, 404, "not_found", "No document x.", ""},
		{repos.ErrNotRepo, 422, "bad_request", "No git repository found in this folder.", ""},
		{kb.ErrUnavailable, 503, "internal", "Search unavailable: run `ollama pull qwen3-embedding:0.6b`", ""},
		{&items.Error{Code: "mystery", Message: "Odd."}, 500, "internal", "Odd.", ""},
		{errors.New("disk on fire"), 500, "internal", "Something went wrong.", ""},
	}
	var logged []string
	s := New(Deps{Token: "t", Run: (&execx.Fake{}).Runner(),
		Log: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }})
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.writeErr(rec, c.err)
		b := decode[errBody](t, rec.Body.Bytes())
		if rec.Code != c.status || b.Error.Code != c.code || b.Error.Message != c.msg || b.Error.Reason != c.reason ||
			rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%v → %d %s", c.err, rec.Code, rec.Body.String())
		}
	}
	if len(logged) != 2 || !strings.Contains(logged[0], "mystery") || !strings.Contains(logged[1], "disk on fire") {
		t.Errorf("logged = %q, want only the two unmapped errors", logged)
	}
}

func TestEmptyTokenNeverAuthenticates(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New accepted an empty daemon token")
		}
	}()
	// a Server built without New (or a zeroed token) still refuses an empty bearer and never bootstraps ""
	s := &Server{Deps: Deps{Log: t.Logf}}
	for _, rt := range []route{{"GET", "/api/events", authDaemon, s.events}, {"GET", "/api/bootstrap", authLoopback, s.bootstrap}} {
		for _, auth := range []string{"", "Bearer ", "Bearer"} {
			req := httptest.NewRequest("GET", rt.pattern, nil)
			req.RemoteAddr, req.Host = "127.0.0.1:5000", "127.0.0.1"
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			rec := httptest.NewRecorder()
			s.wrap(rt)(rec, req)
			if rec.Code == 200 || strings.Contains(rec.Body.String(), `"token"`) {
				t.Errorf("%s with %q = %d %s", rt.pattern, auth, rec.Code, rec.Body.String())
			}
		}
	}
	New(Deps{})
}

func TestWebHandlerServesNonAPIPaths(t *testing.T) {
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "board "+r.URL.Path)
	})
	e := newEnv(t, func(d *Deps) { d.Web = web })
	for _, p := range []string{"/", "/index.html", "/assets/app.js", "/apiary"} {
		status, body := e.call("GET", p, nil, "")
		if status != 200 || string(body) != "board "+p {
			t.Errorf("GET %s = %d %s", p, status, body)
		}
	}
	for _, p := range []string{"/api", "/api/nope"} {
		status, body := e.api("GET", p, nil)
		wantErr(t, status, body, 404, "not_found", "Unknown API route.")
	}
	status, body := e.api("GET", "/api/health", nil)
	if status != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Errorf("health with Web = %d %s", status, body)
	}
}

func TestUnknownRoutesAndBadJSON(t *testing.T) {
	e := newEnv(t)
	status, body := e.api("GET", "/api/nope", nil)
	wantErr(t, status, body, 404, "not_found", "Unknown API route.")
	status, body = e.call("GET", "/", nil, "")
	wantErr(t, status, body, 404, "not_found", "The board isn't built yet.")

	req := httptest.NewRequest("POST", "/x", strings.NewReader("{not json"))
	var v map[string]any
	err := readJSON(req, &v)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Message != "Invalid JSON body." {
		t.Fatalf("readJSON = %v", err)
	}
	// an empty body decodes to the zero value (POST /api/repos/rescan takes `{}` or no body)
	if err := readJSON(httptest.NewRequest("POST", "/x", nil), &v); err != nil {
		t.Fatalf("readJSON(empty) = %v", err)
	}
	for header, want := range map[string]string{"": "board", "cli": "cli", "menubar": "menubar", "evil": "board"} {
		r := httptest.NewRequest("GET", "/", nil)
		if header != "" {
			r.Header.Set("X-Swarm-Via", header)
		}
		if got := via(r); got != want {
			t.Errorf("via(%q) = %q", header, got)
		}
	}
}

func TestSessionAuth(t *testing.T) {
	e := newEnv(t)
	live := seedSessionToken(t, e, "live-token", "running")
	seedSessionToken(t, e, "old-token", "completed")
	h := e.s.wrap(route{method: "GET", pattern: "/hook/x", auth: authSession, h: func(w http.ResponseWriter, r *http.Request) {
		c, _ := e.s.sessionAuth(r)
		w.Write([]byte(c.SessionID))
	}})
	for token, want := range map[string]int{"live-token": 200, "old-token": 401, daemonToken: 401, "": 401} {
		req := httptest.NewRequest("POST", "/hook/x", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != want {
			t.Errorf("token %q = %d", token, rec.Code)
		}
		if want == 200 && rec.Body.String() != live {
			t.Errorf("session id = %q", rec.Body.String())
		}
	}
}

// The board (Task 29's web.Handler/web.Dist) is mounted at "/" for every
// non-/api path once Deps.Web is wired (cmd/swarm daemon.go, P3 Task 33).
func TestBoardServedAtRoot(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Web = web.Handler(web.Dist) })
	status, body := e.call("GET", "/kanban", nil, "")
	if status != 200 {
		t.Fatalf("GET /kanban = %d", status)
	}
	if !strings.Contains(string(body), "<!doctype html") && !strings.Contains(string(body), "<!DOCTYPE html") {
		t.Errorf("GET /kanban body doesn't look like the board's index.html: %s", body)
	}
	if ct := e.headOf(t, "/kanban"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET /kanban content-type = %q", ct)
	}
}

func (e *env) headOf(t *testing.T, path string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.Header.Get("Content-Type")
}
