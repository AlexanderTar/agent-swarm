package httpapi

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// contracts §4: POST /api/spikes returns {item, agent, queued}.
func TestCreateSpike(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/spikes", `{"request_id":"r1","name":"Investigate login crash",
		"intent":"debug","agent":"fake","model":"fake-1",
		"request":"Users see a crash after the second login attempt."}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Item   map[string]any `json:"item"`
		Agent  map[string]any `json:"agent"`
		Queued bool           `json:"queued"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Item["type"] != "spike" || body.Item["spike_intent"] != "debug" {
		t.Fatalf("item = %v", body.Item)
	}
	if body.Agent["name"] != "investigate-login-crash" {
		t.Fatalf("agent = %v", body.Agent)
	}
	// I15: a repeated request_id returns the first result
	again := s.post(t, "/api/spikes", `{"request_id":"r1","name":"Investigate login crash",
		"intent":"debug","agent":"fake","model":"fake-1"}`)
	if again.Body.String() != rec.Body.String() {
		t.Fatal("a replayed request_id must return the first result")
	}
	var n int
	s.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM items WHERE type = 'spike'`).Scan(&n)
	if n != 2 { // the base tree already seeds one spike (SpikeKey)
		t.Fatalf("%d spikes were created", n)
	}
}

// §17.3: a duplicate user-typed name is a 409 with the exact sentence.
func TestCreateSpikeWithATakenNameIs409(t *testing.T) {
	s, _ := newRuntimeServer(t)
	s.post(t, "/api/spikes", `{"request_id":"a","name":"Login crash","intent":"debug","agent":"fake","model":"fake-1"}`)
	rec := s.post(t, "/api/spikes", `{"request_id":"b","name":"Login crash","intent":"debug","agent":"fake","model":"fake-1"}`)
	if rec.Code != 409 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Message != "This agent name is already in use." {
		t.Fatalf("message = %q", body.Error.Message)
	}
}

// §10.2: a preflight failure still returns 200 with a failed agent (I15).
func TestCreateSpikeWithAPreflightFailure(t *testing.T) {
	s, _ := newRuntimeServerNoSuperpowers(t)
	rec := s.post(t, "/api/spikes", `{"request_id":"r","name":"Nope","intent":"feature","agent":"fake","model":"fake-1"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Item  map[string]any `json:"item"`
		Agent map[string]any `json:"agent"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Item["status"] != "draft" {
		t.Errorf("the spike stays draft: %v", body.Item["status"])
	}
	if body.Agent["preflight_error"] == nil || body.Agent["session"] != nil {
		t.Errorf("agent = %v", body.Agent)
	}
}

// StartSpike's "no default kind" error is a plain error, not an *items.Error,
// so createSpike must wrap it (like startOrchestrator wraps its own preflight
// failure) into a 422 preflight_failed the user can actually read -- not let
// writeErr's default case turn it into a bare 500 "Something went wrong."
func TestCreateSpikeWithNoDefaultKindIs422WithGuidance(t *testing.T) {
	s, _ := newRuntimeServer(t)
	if _, err := s.DB.ExecContext(bg, `UPDATE settings SET value_json = '[]' WHERE key = 'enabled_agents'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(bg, `INSERT INTO settings (key, value_json, updated_at)
		VALUES ('roles', '{"orchestrator":{"agent":"","model":"","effort":""}}', 1)`); err != nil {
		t.Fatal(err)
	}
	rec := s.post(t, "/api/spikes", `{"request_id":"r","name":"Nope","intent":"feature"}`)
	if rec.Code != 422 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "preflight_failed" {
		t.Fatalf("code = %q", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "--agent") {
		t.Fatalf("message must guide the user to pass --agent: %q", body.Error.Message)
	}
}

func TestStartOrchestratorAndTheSecondOneIs409(t *testing.T) {
	s, seed := newRuntimeServerWithEpic(t)
	// A real, signed repo (not seed.RepoID's fake `.git` directory): required
	// fix 1 makes this route feed the repo into Preflight's own signing check
	// (§11.4 step 7), which a bare directory can't pass deterministically.
	signedRepo := seedRepo(t, s, "signed-app", gitRepoSigningOn(t))
	rec := s.post(t, "/api/items/"+seed.EpicKey+"/orchestrator",
		`{"request_id":"o1","agent":"fake","model":"fake-1","repos":["`+signedRepo+`"],"repos_version":0}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	// L25: a non-empty repos list confirms that set, because it is a UI action
	var it map[string]any
	json.Unmarshal(s.get(t, "/api/items/"+seed.EpicKey).Body.Bytes(), &it)
	item := it["item"].(map[string]any)
	if len(item["repos"].([]any)) != 1 {
		t.Fatalf("the spawn sheet's selection confirms the repos: %v", item["repos"])
	}
	second := s.post(t, "/api/items/"+seed.EpicKey+"/orchestrator",
		`{"request_id":"o2","agent":"fake","model":"fake-1","repos":[],"repos_version":1}`)
	if second.Code != 409 {
		t.Fatalf("status = %d", second.Code)
	}
	var body struct{ Error struct{ Message string } }
	json.Unmarshal(second.Body.Bytes(), &body)
	if body.Error.Message != "This item already has an orchestrator." {
		t.Fatalf("message = %q", body.Error.Message)
	}
}

// Required fix 1 (I-1): a stale repos_version must be refused BEFORE the
// orchestrator spawns — not spawned-then-409, which would leave the item
// permanently wedged (retry hits "already has an orchestrator").
func TestStartOrchestratorWithAStaleReposVersionSpawnsNothing(t *testing.T) {
	s, seed := newRuntimeServerWithEpic(t)
	rec := s.post(t, "/api/items/"+seed.EpicKey+"/orchestrator",
		`{"request_id":"o1","agent":"fake","model":"fake-1","repos":["`+seed.RepoID+`"],"repos_version":99}`)
	if rec.Code != 409 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var count int
	itemID := itemIDByKey(t, s, seed.EpicKey)
	if err := s.DB.QueryRowContext(bg, `SELECT COUNT(*) FROM agents WHERE root_item_id = ?`, itemID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("agents spawned = %d, want 0", count)
	}
	var version int
	if err := s.DB.QueryRowContext(bg, `SELECT repos_version FROM items WHERE id = ?`, itemID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("repos_version = %d, want unchanged 0", version)
	}
}

// Required fix 1 (I-1): an unknown repo id must be refused, not silently
// accepted as a confirmed repo.
func TestStartOrchestratorWithAnUnknownRepoIsRefused(t *testing.T) {
	s, seed := newRuntimeServerWithEpic(t)
	rec := s.post(t, "/api/items/"+seed.EpicKey+"/orchestrator",
		`{"request_id":"o1","agent":"fake","model":"fake-1","repos":["repo_does_not_exist"],"repos_version":0}`)
	if rec.Code >= 300 {
		// refused, as required — exact code isn't the point of this test
	} else {
		t.Fatalf("status = %d: %s, want an error", rec.Code, rec.Body)
	}
	var count int
	itemID := itemIDByKey(t, s, seed.EpicKey)
	if err := s.DB.QueryRowContext(bg, `SELECT COUNT(*) FROM agents WHERE root_item_id = ?`, itemID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("agents spawned = %d, want 0", count)
	}
}

// §10.7: the action table, state by state.
func TestActionsAllowedPerState(t *testing.T) {
	cases := []struct {
		state   string
		allowed []string
		refused []string
	}{
		{"queued", []string{"cancel"}, []string{"pause", "resume", "retry", "ack", "terminal"}},
		{"spawning", []string{"terminal", "cancel"}, []string{"resume", "retry", "ack"}},
		{"running", []string{"terminal", "pause", "cancel"}, []string{"resume", "retry", "ack"}},
		{"pause_requested", []string{"terminal"}, []string{"pause", "resume", "retry"}},
		{"quiescing", []string{"terminal"}, []string{"pause", "resume"}},
		{"stopping", []string{"terminal"}, []string{"pause", "resume"}},
		{"paused", []string{"resume", "cancel"}, []string{"pause", "retry"}},
		{"interrupted", []string{"resume", "ack", "cancel"}, []string{"pause"}},
		{"crashed", []string{"retry", "ack"}, []string{"pause", "resume"}},
		{"failed", []string{"retry", "ack"}, []string{"pause", "resume"}},
		{"completed", nil, []string{"pause", "resume", "cancel", "retry", "ack"}},
	}
	for _, c := range cases {
		for _, action := range c.allowed {
			s, name := newServerWithSessionState(t, c.state)
			rec := s.post(t, "/api/agents/"+name+"/"+action, `{"scope":"session"}`)
			if rec.Code >= 400 {
				t.Errorf("%s: %s should be allowed, got %d %s", c.state, action, rec.Code, rec.Body)
			}
		}
		for _, action := range c.refused {
			s, name := newServerWithSessionState(t, c.state)
			rec := s.post(t, "/api/agents/"+name+"/"+action, `{"scope":"session"}`)
			if rec.Code < 400 {
				t.Errorf("%s: %s should be refused, got %d", c.state, action, rec.Code)
			}
		}
	}
}

// C3: resume while stopping is a 409 with the §17.3 sentence.
func TestResumeWhileStoppingIs409(t *testing.T) {
	s, name := newServerWithSessionState(t, "stopping")
	rec := s.post(t, "/api/agents/"+name+"/resume", `{}`)
	if rec.Code != 409 {
		t.Fatalf("status = %d", rec.Code)
	}
	// wantErr is P1's helper in helpers_test.go; it checks status, code and message
	// in one line and decodes into a struct that actually has the json tags.
	wantErr(t, rec.Code, rec.Body.Bytes(), 409, "conflict", "Still stopping. Try again in a few seconds.")
}

func TestPauseAllReportsTheCount(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/pause-all", `{}`)
	body := decode[pauseAllWire](t, rec.Body.Bytes())
	if body.Requested == 0 {
		t.Fatalf("requested = %d", body.Requested)
	}
}

// loadAgentStatus maps sql.ErrNoRows to 404 (agent-hover-preview spec's pane
// route needed this; every other action route shares loadAgentStatus, so an
// unknown agent name must not fall through to a bare 500 on any of them).
func TestUnknownAgentNameIs404OnEveryActionRoute(t *testing.T) {
	s, _ := newRuntimeServer(t)
	for _, p := range []string{"pause", "resume", "cancel", "retry", "ack", "terminal"} {
		rec := s.post(t, "/api/agents/totally-unknown-agent/"+p, `{}`)
		wantErr(t, rec.Code, rec.Body.Bytes(), 404, "not_found", "")
	}
}

func TestAckReturns204AndTerminalReturnsTheTmuxName(t *testing.T) {
	s, name := newServerWithSessionState(t, "crashed")
	if rec := s.post(t, "/api/agents/"+name+"/ack", `{}`); rec.Code != 204 {
		t.Fatalf("ack status = %d", rec.Code)
	}
	s2, running := newServerWithSessionState(t, "running")
	rec := s2.post(t, "/api/agents/"+running+"/terminal", `{}`)
	// terminalWire has the json tags (W1: opened_by, not OpenedBy). An anonymous
	// untagged struct here decodes `opened_by` into nothing and compares "" — the
	// test would pass whatever the handler returned (D74).
	body := decode[terminalWire](t, rec.Body.Bytes())
	if body.Tmux != running || body.OpenedBy != "menubar" {
		t.Fatalf("body = %+v", body)
	}
	if rec := s2.post(t, "/api/agents/"+running+"/terminal-opened", `{}`); rec.Code != 204 {
		t.Fatalf("terminal-opened status = %d", rec.Code)
	}
}

// W7: the menubar's X-Swarm-Via reaches the store.
func TestViaHeaderIsHonoured(t *testing.T) {
	s, seed := newRuntimeServerWithEpic(t)
	rec := s.postVia(t, "menubar", "/api/items/"+seed.EpicKey+"/orchestrator",
		`{"request_id":"v1","agent":"fake","model":"fake-1","repos":[],"repos_version":0}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

// R11 / S-2: the fallback runs through the injected runner, and never against
// the production socket. If this test ever launches a process, the assertion
// on the recorded argv is what catches it.
func TestTerminalFallbackRunsGhosttyThroughTheInjectedRunner(t *testing.T) {
	// got is written from the fallback's own goroutine and read from this one
	// via waitUntilHTTP's poll; a mutex is the fix (D80-style: go test -race
	// flags the brief's literal unsynchronized slice).
	var mu sync.Mutex
	var got []string
	fire := make(chan time.Time, 1)
	s, _ := newRuntimeServerWith(t, func(d *Deps) {
		d.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			mu.Lock()
			got = append([]string{name}, args...)
			mu.Unlock()
			return nil, nil
		}
		d.After = func(time.Duration) <-chan time.Time { return fire }
	})
	name := seedRunningAgent(t, s)
	fire <- time.Now() // the menubar never answers
	if rec := s.post(t, "/api/agents/"+name+"/terminal", `{}`); rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	waitUntilHTTP(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) > 0
	})
	mu.Lock()
	defer mu.Unlock()
	if got[0] != "open" || !slices.Contains(got, "Ghostty") {
		t.Fatalf("argv = %v", got)
	}
	if slices.Contains(got, "swarm") {
		t.Fatalf("the fallback must not attach to the production socket: %v", got)
	}
	if !slices.Contains(got, s.RT.TmuxSocket()) {
		t.Fatalf("argv = %v, want the store's socket %q", got, s.RT.TmuxSocket())
	}
}

func TestStartOrchestratorWithRoleOverrides(t *testing.T) {
	s, seed := newRuntimeServerWithEpic(t)
	rec := s.post(t, "/api/items/"+seed.EpicKey+"/orchestrator",
		`{"request_id":"ro1","agent":"fake","model":"fake-1","repos":[],"repos_version":0,
		"roles":{"coder":{"agent":"fake","model":"fake-1","effort":"high"}}}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var resp struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	a, err := s.RT.Agent(bg, resp.Name)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if a.RoleOverrides[runtime.RoleCoder].Model != "fake-1" {
		t.Fatalf("RoleOverrides[coder].Model = %q, want fake-1", a.RoleOverrides[runtime.RoleCoder].Model)
	}
}
