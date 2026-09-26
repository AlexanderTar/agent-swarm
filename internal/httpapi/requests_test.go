package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func TestAnswerApproveAndRequestChanges(t *testing.T) {
	s, seed := newRequestServer(t)
	rec := s.post(t, "/api/requests/"+seed.QuestionID+"/answer", `{"text":"Yes, keep it.","via":"menubar"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var req map[string]any
	json.Unmarshal(rec.Body.Bytes(), &req)
	if req["state"] != "answered" || req["responded_via"] != "menubar" {
		t.Fatalf("request = %v", req)
	}
	// contracts §3.3: the full key set
	for _, k := range []string{"id", "kind", "agent_name", "item_key", "item_title", "root_key",
		"artifact_id", "artifact_revision", "section_id", "section_title", "section_sha256",
		"prompt", "options", "state", "confirmed", "binding", "response_text", "responded_via",
		"responded_at", "created_at", "approval_evidence", "native_pending"} {
		if _, ok := req[k]; !ok {
			t.Errorf("Request is missing %q: %s", k, rec.Body)
		}
	}
	// Task 13e: an answer resolved via menubar (not native_answer) carries
	// no approval evidence -- it is a user action by definition (spec 2.3.6).
	if req["approval_evidence"] != nil {
		t.Errorf("approval_evidence = %v, want null for a menubar answer", req["approval_evidence"])
	}
	ok := s.post(t, "/api/requests/"+seed.ApprovalID+"/approve",
		`{"section_sha256":"`+seed.SectionHash+`","via":"board"}`)
	if ok.Code != 200 {
		t.Fatalf("approve status = %d: %s", ok.Code, ok.Body)
	}
	stale := s.post(t, "/api/requests/"+seed.SecondApprovalID+"/approve",
		`{"section_sha256":"nope","via":"board"}`)
	if stale.Code != 409 {
		t.Fatalf("a stale hash = %d", stale.Code)
	}
	var body struct{ Error struct{ Message string } }
	json.Unmarshal(stale.Body.Bytes(), &body)
	if body.Error.Message != "This request changed. Review the latest version." {
		t.Fatalf("message = %q", body.Error.Message)
	}
	empty := s.post(t, "/api/requests/"+seed.SecondApprovalID+"/request-changes", `{"comment":"","via":"board"}`)
	if empty.Code != 400 {
		t.Fatalf("an empty comment = %d", empty.Code)
	}
}

// D10, P3 A7: accept_* carries the binding it showed, and a changed one is 409.
func TestAcceptEpicChecksTheBinding(t *testing.T) {
	s, seed := newAcceptServer(t)
	stale := `{"binding":{"item_revision":1,"integrated_checkpoint":"ckp_old","git":[]},"via":"board"}`
	rec := s.post(t, "/api/requests/"+seed.AcceptID+"/approve", stale)
	if rec.Code != 409 {
		t.Fatalf("a stale binding = %d: %s", rec.Code, rec.Body)
	}
	if rec := s.post(t, "/api/requests/"+seed.AcceptID+"/approve", seed.CurrentBindingBody); rec.Code != 200 {
		t.Fatalf("the current binding = %d: %s", rec.Code, rec.Body)
	}
	var it map[string]any
	json.Unmarshal(s.get(t, "/api/items/"+seed.EpicKey).Body.Bytes(), &it)
	if it["item"].(map[string]any)["status"] != "done" {
		t.Fatalf("the epic should be done: %v", it["item"])
	}
}

// I13: confirm-repos carries repos_version; a stale one is 409.
func TestConfirmReposRoute(t *testing.T) {
	s, seed := newConfirmServer(t)
	rec := s.post(t, "/api/requests/"+seed.RequestID+"/confirm-repos",
		`{"repos":["`+seed.RepoA+`","`+seed.RepoB+`"],"repos_version":0,"comment":"both","via":"board"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	stale := s.post(t, "/api/requests/"+seed.SecondRequestID+"/confirm-repos",
		`{"repos":["`+seed.RepoA+`"],"repos_version":0,"via":"board"}`)
	if stale.Code != 409 {
		t.Fatalf("a stale version = %d", stale.Code)
	}
	empty := s.post(t, "/api/requests/"+seed.SecondRequestID+"/confirm-repos",
		`{"repos":[],"repos_version":1,"via":"board"}`)
	if empty.Code != 400 {
		t.Fatalf("an empty set = %d", empty.Code)
	}
}

// D10: ?revision= serves the snapshot; no revision means head; ?section= trims.
func TestArtifactRoute(t *testing.T) {
	s, seed := newArtifactServer(t)
	head := s.get(t, "/api/artifacts/"+seed.ArtifactID)
	var body struct {
		Artifact map[string]any `json:"artifact"`
		Markdown string         `json:"markdown"`
	}
	json.Unmarshal(head.Body.Bytes(), &body)
	if body.Artifact["revision"] != body.Artifact["head_revision"] {
		t.Errorf("no revision means head: %v", body.Artifact)
	}
	if !strings.Contains(body.Markdown, seed.NewText) {
		t.Errorf("head serves the current text")
	}
	old := s.get(t, "/api/artifacts/"+seed.ArtifactID+"?revision=1")
	json.Unmarshal(old.Body.Bytes(), &body)
	if strings.Contains(body.Markdown, seed.NewText) {
		t.Error("revision 1 serves the snapshot, even after the file changed (C2)")
	}
	sec := s.get(t, "/api/artifacts/"+seed.ArtifactID+"?section="+seed.SectionID)
	json.Unmarshal(sec.Body.Bytes(), &body)
	if strings.Contains(body.Markdown, seed.OtherSectionText) {
		t.Error("?section= serves only that section")
	}
	for _, k := range []string{"id", "item_key", "kind", "path", "head_revision", "revision",
		"sections", "created_at"} {
		if _, ok := body.Artifact[k]; !ok {
			t.Errorf("Artifact is missing %q", k)
		}
	}
}

func TestNotificationRoutes(t *testing.T) {
	s, seed := newNotificationServer(t, 3)
	rec := s.get(t, "/api/notifications?unread=1&limit=2")
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("got %d notifications", len(list))
	}
	for _, k := range []string{"id", "level", "kind", "title", "body", "agent_name", "item_key",
		"request_id", "read_at", "created_at"} {
		if _, ok := list[0][k]; !ok {
			t.Errorf("Notification is missing %q", k)
		}
	}
	if rec := s.post(t, "/api/notifications/"+seed.FirstID+"/read", `{}`); rec.Code != 204 {
		t.Fatalf("read status = %d", rec.Code)
	}
	all := s.post(t, "/api/notifications/read-all", `{}`)
	var body struct {
		Read int `json:"read"`
	}
	json.Unmarshal(all.Body.Bytes(), &body)
	if body.Read != 2 {
		t.Fatalf("read-all = %d, want the 2 still unread", body.Read)
	}
}

// contracts §4: refresh is 202, and at most once every 60 s per agent.
func TestUsageRoutes(t *testing.T) {
	s, _ := newUsageServer(t)
	rec := s.get(t, "/api/usage")
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) == 0 {
		t.Fatal("no usage snapshots")
	}
	for _, k := range []string{"agent", "meters", "headline_id", "source", "error",
		"fetched_at", "attempted_at", "stale"} {
		if _, ok := list[0][k]; !ok {
			t.Errorf("UsageSnapshot is missing %q", k)
		}
	}
	if rec := s.post(t, "/api/usage/refresh", `{"agent":"fake"}`); rec.Code != 202 {
		t.Fatalf("refresh status = %d", rec.Code)
	}
	again := s.post(t, "/api/usage/refresh", `{"agent":"fake"}`)
	if again.Code != 429 {
		t.Fatalf("a second refresh within 60 s = %d, want 429", again.Code)
	}
}

// Required fix 2 (I-4): an absent "agent" must not 500 (sourceFor("") misses,
// which used to fall through wrapUsageErr as a plain, unwrapped error).
func TestUsageRefreshWithNoAgentIsRefusedNotA500(t *testing.T) {
	s, _ := newUsageServer(t)
	rec := s.post(t, "/api/usage/refresh", `{}`)
	if rec.Code != 400 {
		t.Fatalf("status = %d: %s, want 400", rec.Code, rec.Body)
	}
}

func TestAdviceRoute(t *testing.T) {
	s, seed := newAdviceServer(t)
	rec := s.get(t, "/api/agents/"+seed.AgentName+"/advice")
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) == 0 {
		t.Fatal("no advice rows")
	}
	for _, k := range []string{"id", "session_id", "item_key", "advisor_kind", "advisor_model",
		"advisor_effort", "question", "answer", "error", "state", "mode", "duration_ms",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
		"cost_usd", "created_at", "finished_at"} {
		if _, ok := list[0][k]; !ok {
			t.Errorf("Advice is missing %q: %s", k, rec.Body)
		}
	}
}

// contracts §4: GET /api/requests filters by state and kind.
func TestRequestsListFilters(t *testing.T) {
	s, _ := newRequestServer(t)
	open := s.get(t, "/api/requests?state=open")
	var list []map[string]any
	json.Unmarshal(open.Body.Bytes(), &list)
	for _, r := range list {
		if r["state"] != "open" {
			t.Fatalf("state filter returned %v", r["state"])
		}
	}
	q := s.get(t, "/api/requests?state=open&kind=question")
	json.Unmarshal(q.Body.Bytes(), &list)
	for _, r := range list {
		if r["kind"] != "question" {
			t.Fatalf("kind filter returned %v", r["kind"])
		}
	}
}

type mockTmux struct {
	runtime.Tmux
	keys map[string][]string
}

func (m *mockTmux) Keys(ctx context.Context, name string, keys ...string) error {
	if m.keys == nil {
		m.keys = make(map[string][]string)
	}
	m.keys[name] = append(m.keys[name], keys...)
	return nil
}

type testAPI struct {
	*runtimeEnv
}

type testResponse struct {
	StatusCode int
	Body       []byte
}

func (a *testAPI) post(t *testing.T, path, body string) testResponse {
	t.Helper()
	rec := a.runtimeEnv.post(t, path, body)
	return testResponse{StatusCode: rec.Code, Body: rec.Body.Bytes()}
}

type testRT struct {
	*runtime.Store
}

func (r *testRT) RequestWire(ctx context.Context, id string) (runtime.RequestWire, error) {
	return r.Store.RequestWireByID(ctx, id)
}

func newTestAPI(t *testing.T) (*testAPI, *testRT, *mockTmux) {
	t.Helper()
	e, _ := newRuntimeServer(t)
	tm := &mockTmux{Tmux: e.RT.Tmux, keys: make(map[string][]string)}
	e.RT.Tmux = tm
	return &testAPI{runtimeEnv: e}, &testRT{Store: e.RT}, tm
}

func TestResolveRouteIsGone(t *testing.T) {
	api, rt, tm := newTestAPI(t)
	ctx := context.Background()
	_, a, _, err := rt.StartSpike(ctx, runtime.SpikeInput{Name: "HTTP prompt", Intent: "feature", Kind: runtime.Fake, Model: "fake-1"})
	if err != nil {
		t.Fatalf("StartSpike failed: %v", err)
	}
	ses, err := rt.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatalf("LatestSession failed: %v", err)
	}
	req, err := rt.AskPrompt(ctx, ses.ID, "Confirm delete?", []string{"y"})
	if err != nil {
		t.Fatalf("AskPrompt failed: %v", err)
	}

	resp := api.post(t, "/api/requests/"+req.ID+"/resolve", `{"action":"y","via":"board"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found, got %d", resp.StatusCode)
	}
	wire, err := rt.RequestWire(ctx, req.ID)
	if err != nil {
		t.Fatalf("RequestWire failed: %v", err)
	}
	if wire.State != "open" {
		t.Fatalf("the removed route must not resolve the request, got state=%s", wire.State)
	}
	if len(tm.keys) != 0 {
		t.Fatalf("the removed route must not type into a pane, got %v", tm.keys)
	}
}

func TestArtifactRouteReturnsRevisionWarnings(t *testing.T) {
	s, seed := newArtifactServer(t)
	_, err := s.RT.DB.ExecContext(context.Background(), `UPDATE artifact_revisions SET warnings_json = '["old warning"]' WHERE artifact_id = ? AND revision = 1`, seed.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RT.DB.ExecContext(context.Background(), `UPDATE artifact_revisions SET warnings_json = '["new warning"]' WHERE artifact_id = ? AND revision = 2`, seed.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ revision, want string }{{"1", "old warning"}, {"2", "new warning"}} {
		rec := s.get(t, "/api/artifacts/"+seed.ArtifactID+"?revision="+tc.revision)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var body struct {
			Warnings []string `json:"warnings"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Warnings) != 1 || body.Warnings[0] != tc.want {
			t.Fatalf("revision %s warnings=%v", tc.revision, body.Warnings)
		}
	}
}
