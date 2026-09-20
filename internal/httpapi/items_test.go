package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

type itemJSON struct {
	Key       string   `json:"key"`
	Type      string   `json:"type"`
	Status    string   `json:"status"`
	Title     string   `json:"title"`
	Revision  int      `json:"revision"`
	ParentKey string   `json:"parent_key"`
	BlockedBy []string `json:"blocked_by"`
	Context   bool     `json:"context"`
}

func (e *env) create(body map[string]any) itemJSON {
	e.t.Helper()
	status, b := e.api("POST", "/api/items", body)
	if status != 201 {
		e.t.Fatalf("create %v = %d %s", body, status, b)
	}
	return decode[itemJSON](e.t, b)
}

func TestCreateItem(t *testing.T) {
	e := newEnv(t)
	epic := e.create(map[string]any{"type": "epic", "title": "Auth", "brief": "b", "acceptance": []string{"works"}})
	if epic.Key != "EPIC-1" || epic.Status != "draft" || epic.Revision != 1 {
		t.Fatalf("epic = %+v", epic)
	}
	story := e.create(map[string]any{"type": "story", "title": "Login", "parent_key": "EPIC-1"})
	if story.ParentKey != "EPIC-1" {
		t.Fatalf("story = %+v", story)
	}
	cases := []struct {
		body      map[string]any
		status    int
		code, msg string
	}{
		{map[string]any{"type": "spike", "title": "Explore"}, 400, "bad_request", "Spikes start with an intent. Use New spike."},
		{map[string]any{"type": "story", "title": "Orphan"}, 400, "bad_request", "A story needs a parent epic."},
		{map[string]any{"type": "task", "title": "Orphan"}, 400, "bad_request", "A task needs a parent story, bug, spike or chore."},
		{map[string]any{"type": "saga", "title": "x"}, 400, "bad_request", `Unknown item type "saga".`},
		{map[string]any{"type": "task", "title": "x", "parent_key": "STORY-9"}, 404, "not_found", "No item STORY-9."},
		{map[string]any{"type": "epic", "title": ""}, 400, "bad_request", "Title must be 1–200 characters."},
		{map[string]any{"type": "epic", "title": "x", "request_id": strings.Repeat("r", 65)}, 400, "bad_request", "request_id must be at most 64 characters."},
	}
	for _, c := range cases {
		status, body := e.api("POST", "/api/items", c.body)
		wantErr(t, status, body, c.status, c.code, c.msg)
	}
}

func TestCreateItemIsIdempotent(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"type": "bug", "title": "Crash", "request_id": "req-123"}
	first := e.create(body)
	second := e.create(body)
	if first.Key != second.Key || first.Key != "BUG-1" {
		t.Fatalf("replay = %+v vs %+v", first, second)
	}
	var n int
	e.s.DB.QueryRow(`SELECT count(*) FROM items`).Scan(&n)
	if n != 1 {
		t.Fatalf("items = %d", n)
	}
	type result struct {
		status int
		body   []byte
	}
	done := make(chan result, 5)
	for range 5 {
		go func() { // no t.Fatal in here: report back to the test goroutine
			req := map[string]any{"type": "bug", "title": "Double click", "request_id": "req-456"}
			status, body := e.callNoFatal("POST", "/api/items", req)
			done <- result{status, body}
		}()
	}
	keys := map[string]bool{}
	for range 5 {
		r := <-done
		if r.status != 201 {
			t.Fatalf("replay = %d %s", r.status, r.body)
		}
		keys[decode[itemJSON](t, r.body).Key] = true
	}
	if len(keys) != 1 {
		t.Fatalf("concurrent replays created %v", keys)
	}
}

// A client that aborts between the item write and its idempotency record would
// let a retry create a second item, so both run on a context it can't cancel.
func TestIdempotentRecordSurvivesAClientAbort(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	r := httptest.NewRequest("POST", "/api/items", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	var inner context.Context
	e.s.idempotent(w, r, "req-abort", "POST /api/items", http.StatusCreated, func(c context.Context) (any, error) {
		inner = c
		return map[string]string{"key": "BUG-1"}, nil
	})
	if inner == nil || inner.Err() != nil {
		t.Fatalf("fn ran on the client's context: %v", inner)
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d %s", w.Code, w.Body)
	}
	var n int
	if err := e.s.DB.QueryRow(`SELECT count(*) FROM idempotency WHERE request_id = 'req-abort'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("idempotency rows = %d, %v", n, err)
	}
}

func TestListAndGetItems(t *testing.T) {
	e := newEnv(t)
	e.create(map[string]any{"type": "epic", "title": "Authentication"})
	e.create(map[string]any{"type": "story", "title": "Login", "parent_key": "EPIC-1"})
	e.create(map[string]any{"type": "task", "title": "Build login form", "parent_key": "STORY-1"})
	e.create(map[string]any{"type": "task", "title": "Persist session", "parent_key": "STORY-1"})
	e.create(map[string]any{"type": "bug", "title": "Crash"})

	status, b := e.api("GET", "/api/items?view=tree&q=form", nil)
	list := decode[struct {
		Items   []itemJSON `json:"items"`
		Matches int        `json:"matches"`
	}](t, b)
	if status != 200 || list.Matches != 1 || len(list.Items) != 3 || !list.Items[0].Context || list.Items[2].Key != "TASK-1" {
		t.Fatalf("tree = %d %s", status, b)
	}
	_, b = e.api("GET", "/api/items?view=flat&type=task", nil)
	flat := decode[struct {
		Items   []itemJSON `json:"items"`
		Matches int        `json:"matches"`
	}](t, b)
	if flat.Matches != 2 || len(flat.Items) != 2 {
		t.Fatalf("flat = %s", b)
	}
	_, b = e.api("GET", "/api/items?root=BUG-1", nil)
	if n := decode[struct{ Matches int }](t, b).Matches; n != 1 {
		t.Fatalf("root filter = %s", b)
	}
	status, b = e.api("GET", "/api/items?status=doing", nil)
	wantErr(t, status, b, 400, "bad_request", `Unknown status "doing".`)
	_, b = e.api("GET", "/api/items?q=nothing-matches", nil)
	if !strings.Contains(string(b), `"items":[]`) {
		t.Fatalf("empty list must be [] not null: %s", b)
	}

	if err := e.items.AddDep(bg, "TASK-2", "TASK-1", items.User("board")); err != nil {
		t.Fatal(err)
	}
	status, b = e.api("GET", "/api/items/STORY-1", nil)
	detail := decode[struct {
		Item      itemJSON   `json:"item"`
		Ancestors []itemJSON `json:"ancestors"`
		Children  []itemJSON `json:"children"`
		Deps      struct {
			BlockedBy []itemJSON `json:"blocked_by"`
			Blocks    []itemJSON `json:"blocks"`
		} `json:"deps"`
	}](t, b)
	if status != 200 || detail.Item.Key != "STORY-1" || len(detail.Ancestors) != 1 || len(detail.Children) != 2 {
		t.Fatalf("detail = %d %s", status, b)
	}
	for _, empty := range []string{`"agents":[]`, `"requests":[]`, `"artifacts":[]`, `"blocked_by":[]`, `"blocks":[]`} {
		if !strings.Contains(string(b), empty) {
			t.Errorf("detail lacks %s: %s", empty, b)
		}
	}
	_, b = e.api("GET", "/api/items/TASK-2", nil)
	if !strings.Contains(string(b), `"blocked_by":["TASK-1"]`) {
		t.Fatalf("task detail = %s", b)
	}
	status, b = e.api("GET", "/api/items/EPIC-9", nil)
	wantErr(t, status, b, 404, "not_found", "No item EPIC-9.")
}

func TestPatchItem(t *testing.T) {
	e := newEnv(t)
	epic := e.create(map[string]any{"type": "epic", "title": "Old"})
	status, b := e.call("PATCH", "/api/items/EPIC-1", map[string]any{"title": "New", "priority": 1, "revision": epic.Revision}, daemonToken, "X-Swarm-Via", "cli")
	got := decode[itemJSON](t, b)
	if status != 200 || got.Title != "New" || got.Revision != 2 {
		t.Fatalf("patch = %d %s", status, b)
	}
	status, b = e.api("PATCH", "/api/items/EPIC-1", map[string]any{"title": "Again", "revision": 1})
	wantErr(t, status, b, 409, "conflict", "This item changed elsewhere. Showing its latest status.")
	status, b = e.api("PATCH", "/api/items/EPIC-1", map[string]any{"title": "No revision"})
	wantErr(t, status, b, 400, "bad_request", "revision is required.")
	status, b = e.api("PATCH", "/api/items/EPIC-1", map[string]any{"status": "ready", "revision": 2})
	if status != 200 || decode[itemJSON](t, b).Status != "ready" {
		t.Fatalf("move = %d %s", status, b)
	}
	status, b = e.api("PATCH", "/api/items/EPIC-1", map[string]any{"status": "done", "revision": 3})
	wantErr(t, status, b, 422, "transition_denied", "Accept this epic to mark it Done.")
	if decode[errBody](t, b).Error.Reason != "Accept this epic to mark it Done." {
		t.Fatalf("reason missing: %s", b)
	}
	status, b = e.api("PATCH", "/api/items/EPIC-1", "not an object")
	wantErr(t, status, b, 400, "bad_request", "Invalid JSON body.")
}

func TestDepsAndGraphRoutes(t *testing.T) {
	e := newEnv(t)
	e.create(map[string]any{"type": "epic", "title": "E"})
	e.create(map[string]any{"type": "story", "title": "S", "parent_key": "EPIC-1"})
	e.create(map[string]any{"type": "task", "title": "A", "parent_key": "STORY-1"})
	e.create(map[string]any{"type": "task", "title": "B", "parent_key": "STORY-1"})

	if status, b := e.api("POST", "/api/items/TASK-2/deps", map[string]any{"blocked_by": "TASK-1"}); status != 204 || len(b) != 0 {
		t.Fatalf("add = %d %s", status, b)
	}
	status, b := e.api("POST", "/api/items/TASK-1/deps", map[string]any{"blocked_by": "TASK-2"})
	wantErr(t, status, b, 409, "conflict", "This dependency would create a cycle.")
	status, b = e.api("POST", "/api/items/TASK-1/deps", map[string]any{"blocked_by": "STORY-1"})
	wantErr(t, status, b, 409, "conflict", "A task can't depend on its own story or epic.")
	status, b = e.api("POST", "/api/items/TASK-1/deps", map[string]any{})
	wantErr(t, status, b, 400, "bad_request", "blocked_by is required.")

	status, b = e.api("GET", "/api/items/TASK-2/graph?scope=neighbourhood&hops=1", nil)
	g := decode[items.Graph](t, b)
	if status != 200 || len(g.Nodes) != 2 || len(g.Edges) != 1 || g.Edges[0] != (items.GraphEdge{From: "TASK-1", To: "TASK-2"}) {
		t.Fatalf("graph = %d %s", status, b)
	}
	status, b = e.api("GET", "/api/items/TASK-2/graph", nil)
	if g := decode[items.Graph](t, b); status != 200 || len(g.Nodes) != 4 {
		t.Fatalf("root graph = %s", b)
	}
	status, b = e.api("GET", "/api/items/TASK-2/graph?hops=x", nil)
	wantErr(t, status, b, 400, "bad_request", "hops must be a number.")

	if status, _ := e.api("DELETE", "/api/items/TASK-2/deps/TASK-1", nil); status != 204 {
		t.Fatalf("delete = %d", status)
	}
	_, b = e.api("GET", "/api/items/TASK-2", nil)
	if !strings.Contains(string(b), `"blocked_by":[]`) {
		t.Fatalf("after delete = %s", b)
	}
}

func TestItemWireShape(t *testing.T) {
	e := newEnv(t)
	e.create(map[string]any{"type": "epic", "title": "E"})
	_, b := e.api("GET", "/api/items/EPIC-1", nil)
	raw := decode[struct {
		Item map[string]any `json:"item"`
	}](t, b)
	for _, k := range []string{"parent_id", "parent_key", "status_before_block", "role_hint", "tdd_exempt",
		"spike_intent", "origin_spike_id", "origin_spike_key", "legacy_key", "archived_at"} {
		if v, ok := raw.Item[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", k, v, ok)
		}
	}
	for _, k := range []string{"created_at", "updated_at"} {
		if v, ok := raw.Item[k].(float64); !ok || v < 1e12 {
			t.Errorf("%s = %v, want integer ms", k, raw.Item[k])
		}
	}
	for _, k := range []string{"acceptance", "repos", "suggested_repos", "blocked_by"} {
		if _, ok := raw.Item[k].([]any); !ok {
			t.Errorf("%s = %v, want an array", k, raw.Item[k])
		}
	}
	if raw.Item["context"] != false || raw.Item["id"] == "" || raw.Item["root_key"] != "EPIC-1" {
		t.Errorf("item = %v", raw.Item)
	}

	spike, err := e.items.Create(bg, items.CreateInput{Type: items.Spike, Title: "Explore", SpikeIntent: "feature"}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.items.Create(bg, items.CreateInput{Type: items.Epic, Title: "From spike", OriginSpikeID: spike.ID}, items.Daemon()); err != nil {
		t.Fatal(err)
	}
	_, b = e.api("GET", "/api/items?view=flat&q=From", nil)
	if !strings.Contains(string(b), `"origin_spike_key":"SPIKE-1"`) || !strings.Contains(string(b), `"spike_intent":null`) {
		t.Fatalf("spike-born item = %s", b)
	}
}
