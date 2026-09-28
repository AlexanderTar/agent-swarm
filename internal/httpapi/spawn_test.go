package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// onePNGBase64 is internal/attachments/testdata/one.png (a valid 1x1 PNG),
// base64-encoded, so these tests don't reach across packages for it.
const onePNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4DwAAAQEABRjYTgAAAABJRU5ErkJggg=="

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

// spec 2026-09-28 Locked Decision 3: both Name and Request empty is a 400.
func TestCreateSpikeWithNoNameAndNoRequestIs400(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/spikes", `{"request_id":"r","intent":"debug","agent":"fake","model":"fake-1"}`)
	if rec.Code != 400 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "bad_request" || body.Error.Message != "Give a name or a request." {
		t.Fatalf("error = %+v", body.Error)
	}
}

// spec 2026-09-28: an empty Name with a Request marks the item's wire
// title_pending, and the item's title is the inferred placeholder.
func TestCreateSpikeWithNoNameSetsTitlePendingOnTheWire(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/spikes", `{"request_id":"r","intent":"debug","agent":"fake","model":"fake-1",
		"request":"Fix the login redirect loop that happens after SSO sign-in on Safari"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Item map[string]any `json:"item"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Item["title_pending"] != true {
		t.Fatalf("item = %v, want title_pending true", body.Item)
	}
	if body.Item["title"] != "Fix the login redirect loop that happens after SSO sign-in…" {
		t.Fatalf("title = %v", body.Item["title"])
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

// (a) happy path: two images land on disk, the brief lists both paths.
func TestCreateSpikeWithAttachmentsSavesFilesAndListsThemInTheBrief(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/spikes", fmt.Sprintf(`{"request_id":"r1","name":"With pics",
		"intent":"debug","agent":"fake","model":"fake-1","request":"See attached.",
		"attachments":[{"name":"Login bug.png","data":%q},{"name":"second.png","data":%q}]}`,
		onePNGBase64, onePNGBase64))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Item              map[string]any `json:"item"`
		AttachmentsFailed bool           `json:"attachments_failed"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.AttachmentsFailed {
		t.Fatal("attachments_failed = true on a happy save")
	}
	key, _ := body.Item["key"].(string)
	dir := filepath.Join(s.RT.Home, "attachments", key)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("attachments dir = %v, %v", entries, err)
	}
	brief, _ := body.Item["brief"].(string)
	if !strings.Contains(brief, "## Attachments") || !strings.Contains(brief, filepath.Join(dir, "01-login-bug.png")) {
		t.Fatalf("brief missing attachments section: %q", brief)
	}
}

// (b) 11 images is a 400 and creates no item.
func TestCreateSpikeWithElevenAttachmentsIs400AndCreatesNoItem(t *testing.T) {
	s, _ := newRuntimeServer(t)
	var n int
	s.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM items`).Scan(&n)
	before := n
	var atts strings.Builder
	for i := 0; i < 11; i++ {
		if i > 0 {
			atts.WriteString(",")
		}
		fmt.Fprintf(&atts, `{"name":"a%d.png","data":%q}`, i, onePNGBase64)
	}
	rec := s.post(t, "/api/spikes", fmt.Sprintf(`{"request_id":"r2","name":"Too many",
		"intent":"debug","agent":"fake","model":"fake-1","attachments":[%s]}`, atts.String()))
	if rec.Code != 400 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Message != "At most 10 images." {
		t.Fatalf("message = %q", body.Error.Message)
	}
	s.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM items`).Scan(&n)
	if n != before {
		t.Fatalf("an item was created despite the 400: %d -> %d", before, n)
	}
}

// (c) a non-image is a 400 and creates no item.
func TestCreateSpikeWithUnsupportedAttachmentIs400AndCreatesNoItem(t *testing.T) {
	s, _ := newRuntimeServer(t)
	var before int
	s.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM items`).Scan(&before)
	notImage := base64.StdEncoding.EncodeToString([]byte("just text, not an image"))
	rec := s.post(t, "/api/spikes", fmt.Sprintf(`{"request_id":"r3","name":"Bad file",
		"intent":"debug","agent":"fake","model":"fake-1","attachments":[{"name":"notes.png","data":%q}]}`, notImage))
	if rec.Code != 400 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Message != `Image "notes.png" is not a PNG, JPEG, GIF or WebP.` {
		t.Fatalf("message = %q", body.Error.Message)
	}
	var after int
	s.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM items`).Scan(&after)
	if after != before {
		t.Fatalf("an item was created despite the 400: %d -> %d", before, after)
	}
}

// (d) a replayed request_id with images returns the same result and writes
// nothing a second time (one directory, two files).
func TestCreateSpikeAttachmentsReplayWritesOnce(t *testing.T) {
	s, _ := newRuntimeServer(t)
	reqBody := fmt.Sprintf(`{"request_id":"r4","name":"Replay me",
		"intent":"debug","agent":"fake","model":"fake-1",
		"attachments":[{"name":"a.png","data":%q},{"name":"b.png","data":%q}]}`, onePNGBase64, onePNGBase64)
	first := s.post(t, "/api/spikes", reqBody)
	second := s.post(t, "/api/spikes", reqBody)
	if first.Body.String() != second.Body.String() {
		t.Fatal("a replayed request_id must return the first result")
	}
	var body struct {
		Item map[string]any `json:"item"`
	}
	json.Unmarshal(first.Body.Bytes(), &body)
	key, _ := body.Item["key"].(string)
	entries, err := os.ReadDir(filepath.Join(s.RT.Home, "attachments", key))
	if err != nil || len(entries) != 2 {
		t.Fatalf("attachments dir = %v, %v", entries, err)
	}
}

// (e) Save failing (home/attachments is a file, not a dir) still creates the
// item, with attachments_failed true and the failure section in the brief.
func TestCreateSpikeAttachmentsSaveFailureStillCreatesTheItem(t *testing.T) {
	s, _ := newRuntimeServer(t)
	if err := os.WriteFile(filepath.Join(s.RT.Home, "attachments"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rec := s.post(t, "/api/spikes", fmt.Sprintf(`{"request_id":"r5","name":"Blocked home",
		"intent":"debug","agent":"fake","model":"fake-1","attachments":[{"name":"a.png","data":%q}]}`, onePNGBase64))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Item              map[string]any `json:"item"`
		AttachmentsFailed bool           `json:"attachments_failed"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if !body.AttachmentsFailed {
		t.Fatal("attachments_failed should be true")
	}
	brief, _ := body.Item["brief"].(string)
	if !strings.Contains(brief, "could not save them") {
		t.Fatalf("brief missing FailureSection: %q", brief)
	}
}

// (f) createSpike's readJSONLimit is raised past readJSON's 1 MiB cap; other
// routes (POST /api/items) keep the old limit.
func TestCreateSpikeAcceptsABodyLargerThanTheOldOneMiBLimit(t *testing.T) {
	s, _ := newRuntimeServer(t)
	// a valid PNG followed by padding bytes: http.DetectContentType only
	// sniffs the prefix, so this is still a valid, decodable PNG.
	pngBytes, err := base64.StdEncoding.DecodeString(onePNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	padded := append(pngBytes, make([]byte, 1500*1024)...)
	big := base64.StdEncoding.EncodeToString(padded)
	rec := s.post(t, "/api/spikes", fmt.Sprintf(`{"request_id":"r6","name":"Big body",
		"intent":"debug","agent":"fake","model":"fake-1","attachments":[{"name":"big.png","data":%q}]}`, big))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

// A body over the limit is now a distinguishable 413, not a generic 400 (the
// decoder never even reaches malformed-JSON territory: MaxBytesReader cuts it
// off first).
func TestCreateItemStillRefusesABodyOverOneMiB(t *testing.T) {
	s, _ := newRuntimeServer(t)
	big := strings.Repeat("a", 2<<20)
	rec := s.post(t, "/api/items", fmt.Sprintf(`{"type":"epic","title":"t","brief":%q}`, big))
	if rec.Code != 413 {
		t.Fatalf("status = %d, want 413 (readJSON keeps its 1 MiB limit)", rec.Code)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "request_too_large" || body.Error.Message != "The request is too large." {
		t.Fatalf("error = %+v", body.Error)
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
