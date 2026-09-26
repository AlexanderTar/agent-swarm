package mcpserver

// Batch 4 (B10) read-path regressions: validated filter.kind, field
// projection, bounded pagination with a stable cursor, and per-ref errors.
// These tests pin the F6 remainders; the general refs/filter/repos/since_seq
// behavior and the Batch 2 recovery query are otherwise unchanged.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func readOut(t *testing.T, out any) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(mustJSON(out), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func readArr(m map[string]any, key string) []any {
	a, _ := m[key].([]any)
	if a == nil {
		return []any{}
	}
	return a
}

// TestReadKindAndProjection: kind selects which collections come back,
// fields projects allowlisted fields with identity retained, and unknown
// kinds/fields/filters are explicit errors rather than ignored input.
func TestReadKindAndProjection(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)

	// kind=agent excludes items and checkpoints.
	out, err := s.call(ctx, seed.Caller, "swarm_read",
		`{"refs":["`+seed.TaskKey+`","`+worker.Name+`"],"filter":{"kind":"agent"}}`)
	if err != nil {
		t.Fatal(err)
	}
	m := readOut(t, out)
	if n := len(readArr(m, "items")); n != 0 {
		t.Fatalf("kind=agent items = %d, want 0", n)
	}
	if n := len(readArr(m, "checkpoints")); n != 0 {
		t.Fatalf("kind=agent checkpoints = %d, want 0", n)
	}
	agents := readArr(m, "agents")
	if len(agents) != 1 {
		t.Fatalf("kind=agent agents = %d, want 1", len(agents))
	}

	// Unknown kinds are refused explicitly.
	if _, err := s.call(ctx, seed.Caller, "swarm_read", `{"filter":{"kind":"bogus"}}`); err == nil {
		t.Fatal("an unknown filter.kind must be refused")
	} else if !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("err = %q, want it to name the unknown kind", err)
	}

	// Projection keeps identity and drops everything else.
	out, err = s.call(ctx, seed.Caller, "swarm_read",
		`{"refs":["`+seed.TaskKey+`"],"fields":["key","title"]}`)
	if err != nil {
		t.Fatal(err)
	}
	m = readOut(t, out)
	got := readArr(m, "items")
	if len(got) != 1 {
		t.Fatalf("items = %d, want 1", len(got))
	}
	projected, _ := got[0].(map[string]any)
	if projected["key"] != seed.TaskKey || projected["title"] == nil || projected["title"] == "" {
		t.Fatalf("projected item = %v, want key+title retained", projected)
	}
	if _, ok := projected["brief"]; ok {
		t.Fatalf("projected item must omit brief: %v", projected)
	}

	// The B10 unit-1 shape: fields name/state on an agent omits the rest.
	out, err = s.call(ctx, seed.Caller, "swarm_read",
		`{"refs":["`+worker.Name+`"],"fields":["name","state"]}`)
	if err != nil {
		t.Fatal(err)
	}
	m = readOut(t, out)
	agents = readArr(m, "agents")
	if len(agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(agents))
	}
	am, _ := agents[0].(map[string]any)
	if am["name"] != worker.Name || am["state"] == nil {
		t.Fatalf("projected agent = %v, want name+state", am)
	}
	if _, ok := am["model"]; ok {
		t.Fatalf("projected agent must omit model: %v", am)
	}

	// Unknown fields are refused explicitly, not ignored.
	if _, err := s.call(ctx, seed.Caller, "swarm_read",
		`{"refs":["`+seed.TaskKey+`"],"fields":["nope"]}`); err == nil {
		t.Fatal("an unknown field must be refused")
	} else if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err = %q, want it to name the unknown field", err)
	}

	// Unknown filter keys are explicit unsupported-filter errors.
	if _, err := s.call(ctx, seed.Caller, "swarm_read", `{"filter":{"bogus":1}}`); err == nil {
		t.Fatal("an unsupported filter must be refused")
	} else if !strings.Contains(err.Error(), "unsupported filter") {
		t.Fatalf("err = %q, want an unsupported-filter error", err)
	}
}

// TestReadPaginationHasNoDrops: 250 same-timestamp records across pages,
// one insert after page 1, every original record exactly once; default 50,
// max 200 cap, invalid cursor refused.
func TestReadPaginationHasNoDrops(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()

	var created []string
	for i := 0; i < 250; i++ {
		it, err := s.RT.Items.Create(ctx, items.CreateInput{
			Type: items.Task, ParentKey: seed.StoryKey,
			Title: "Paginated task",
		}, items.Daemon())
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, it.Key)
	}
	// One shared timestamp: the tie-break (id) carries the whole order.
	fixed := db.Millis(s.RT.Now())
	if _, err := s.RT.DB.ExecContext(ctx, `UPDATE items SET updated_at = ?, created_at = ?`, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	created = append(created, seed.TaskKey, seed.OtherTaskKey)

	// Default page is bounded at 50.
	m := readOut(t, mustCall(t, s, ctx, seed.Caller, `{"filter":{"type":"task"}}`))
	if n := len(readArr(m, "items")); n != 50 {
		t.Fatalf("default page = %d items, want 50", n)
	}
	if more, _ := m["has_more"].(bool); !more {
		t.Fatal("default page must report has_more")
	}

	// Excess limit is capped at 200.
	m = readOut(t, mustCall(t, s, ctx, seed.Caller, `{"filter":{"type":"task"},"limit":500}`))
	if n := len(readArr(m, "items")); n != 200 {
		t.Fatalf("capped page = %d items, want 200", n)
	}

	// An invalid cursor is an explicit error.
	if _, err := s.call(ctx, seed.Caller, "swarm_read", `{"filter":{"type":"task"},"cursor":"!!!"}`); err == nil {
		t.Fatal("an invalid cursor must be refused")
	} else if !strings.Contains(err.Error(), "invalid cursor") {
		t.Fatalf("err = %q, want an invalid-cursor error", err)
	}

	// Walk pages of 100; insert one more after page 1. The newcomer sorts
	// first (newest), so the continuation still reads the original set
	// exactly once.
	m = readOut(t, mustCall(t, s, ctx, seed.Caller, `{"filter":{"type":"task"},"limit":100}`))
	first := readArr(m, "items")
	if len(first) != 100 {
		t.Fatalf("page 1 = %d items, want 100", len(first))
	}
	assertOrdered(t, first)
	cursor, _ := m["page_cursor"].(string)
	if cursor == "" {
		t.Fatal("page 1 must return a cursor while has_more")
	}
	fresh, err := s.RT.Items.Create(ctx, items.CreateInput{
		Type: items.Task, ParentKey: seed.StoryKey, Title: "Late task",
	}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, it := range first {
		seen[it.(map[string]any)["key"].(string)] = true
	}
	for cursor != "" {
		m = readOut(t, mustCall(t, s, ctx, seed.Caller,
			`{"filter":{"type":"task"},"limit":100,"cursor":"`+cursor+`"}`))
		page := readArr(m, "items")
		assertOrdered(t, page)
		for _, it := range page {
			k := it.(map[string]any)["key"].(string)
			if seen[k] {
				t.Fatalf("duplicate %q across pages", k)
			}
			seen[k] = true
		}
		cursor, _ = m["page_cursor"].(string)
	}
	if len(seen) != len(created) {
		t.Fatalf("paged set = %d items, want the original %d exactly once", len(seen), len(created))
	}
	for _, k := range created {
		if !seen[k] {
			t.Fatalf("dropped %q across pages", k)
		}
	}
	if seen[fresh.Key] {
		t.Fatalf("the post-page-1 insert %q must not appear in the continuation", fresh.Key)
	}
	// A fresh read starts at the newest row: the late insert is first.
	m = readOut(t, mustCall(t, s, ctx, seed.Caller, `{"filter":{"type":"task"},"limit":1}`))
	top := readArr(m, "items")
	if len(top) != 1 || top[0].(map[string]any)["key"] != fresh.Key {
		t.Fatalf("fresh page 1 must start with the late insert: %v", top)
	}
}

func mustCall(t *testing.T, s *Server, ctx context.Context, c Caller, args string) any {
	t.Helper()
	out, err := s.call(ctx, c, "swarm_read", args)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertOrdered checks the page contract: updated_at DESC, id ASC on ties.
func assertOrdered(t *testing.T, page []any) {
	t.Helper()
	var lastU string
	lastID := ""
	for _, it := range page {
		m, _ := it.(map[string]any)
		u, _ := m["updated_at"].(string)
		id, _ := m["id"].(string)
		if lastU != "" && (u > lastU || (u == lastU && id < lastID)) {
			t.Fatalf("page out of order at %v after (%s %s)", m, lastU, lastID)
		}
		lastU, lastID = u, id
	}
}

// TestReadMixedRefsReturnsKnownResults: one unknown ref no longer discards
// the valid results; successes and per-ref errors come back together, with
// no raw driver strings.
func TestReadMixedRefsReturnsKnownResults(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	workerSes, err := s.RT.LatestSession(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := writeSpec(t, "# Spec\n\n## One\n\na\n")
	artOut, err := s.call(ctx, seed.Caller, "swarm_artifact",
		`{"op":"register","item":"`+seed.RootKey+`","kind":"spec","path":"`+p+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var art struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal(mustJSON(artOut), &art); err != nil {
		t.Fatal(err)
	}
	task, err := s.RT.Items.Get(ctx, seed.TaskKey)
	if err != nil {
		t.Fatal(err)
	}
	reqID := ids.New("req")
	now := db.Millis(s.RT.Now())
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO requests
		(id, kind, is_hitl, agent_id, session_id, item_id, prompt, options_json, state, created_at)
		VALUES (?, 'question', 1, ?, ?, ?, 'Which API?', '[]', 'open', ?)`,
		reqID, worker.ID, workerSes.ID, task.ID, now); err != nil {
		t.Fatal(err)
	}
	wtID := ids.New("wt")
	root, err := s.RT.Items.Get(ctx, seed.RootKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO worktrees
		(id, repo_id, path, branch, base_ref, base_sha, owner_agent_id, root_item_id, state, created_at)
		VALUES (?, ?, ?, 'task/x', 'main', 'abc1234', ?, ?, 'active', ?)`,
		wtID, seed.RepoID, t.TempDir()+"/wt-x", worker.ID, root.ID, now); err != nil {
		t.Fatal(err)
	}

	out, err := s.call(ctx, seed.Caller, "swarm_read", `{"refs":["`+seed.TaskKey+`","`+
		worker.Name+`","`+art.ArtifactID+`","`+reqID+`","`+wtID+`","nope-missing"]}`)
	if err != nil {
		t.Fatalf("mixed refs must succeed with per-ref errors, not fail the call: %v", err)
	}
	m := readOut(t, out)
	if n := len(readArr(m, "items")); n != 1 {
		t.Fatalf("items = %d, want 1", n)
	}
	if n := len(readArr(m, "agents")); n != 1 {
		t.Fatalf("agents = %d, want 1", n)
	}
	if n := len(readArr(m, "artifacts")); n != 1 {
		t.Fatalf("artifacts = %d, want 1", n)
	}
	reqs := readArr(m, "requests")
	if len(reqs) != 1 || reqs[0].(map[string]any)["request_id"] != reqID {
		t.Fatalf("requests = %v, want the question", reqs)
	}
	wts := readArr(m, "worktrees")
	if len(wts) != 1 || wts[0].(map[string]any)["worktree_id"] != wtID {
		t.Fatalf("worktrees = %v, want the worktree", wts)
	}
	errs := readArr(m, "errors")
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly the unknown ref", errs)
	}
	em, _ := errs[0].(map[string]any)
	if em["ref"] != "nope-missing" || em["code"] == nil || em["code"] == "" || em["message"] == nil {
		t.Fatalf("error entry = %v, want {ref,code,message}", em)
	}
	if strings.Contains(strings.ToLower(em["message"].(string)), "sql") {
		t.Fatalf("error message must not leak driver strings: %q", em["message"])
	}

	// All-unknown still succeeds, with errors only and no driver strings.
	out, err = s.call(ctx, seed.Caller, "swarm_read", `{"refs":["nope-1","nope-2"]}`)
	if err != nil {
		t.Fatalf("all-unknown refs must return errors, not fail: %v", err)
	}
	m = readOut(t, out)
	if n := len(readArr(m, "errors")); n != 2 {
		t.Fatalf("errors = %v, want 2", readArr(m, "errors"))
	}
	for _, e := range readArr(m, "errors") {
		if strings.Contains(strings.ToLower(e.(map[string]any)["message"].(string)), "sql") {
			t.Fatalf("error message must not leak driver strings: %v", e)
		}
	}
	if n := len(readArr(m, "items")); n != 0 {
		t.Fatalf("items = %d, want 0", n)
	}
}

// TestItemsUpdateRejectsRevisionLatest (F11): optimistic concurrency stays
// explicit. A "latest" shortcut is refused with a message naming the
// integer contract instead of decoding into zero or guessing current.
func TestItemsUpdateRejectsRevisionLatest(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	_, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"update","key":"`+seed.TaskKey+`","title":"Renamed","revision":"latest"}`)
	if err == nil {
		t.Fatal(`revision "latest" must be refused`)
	} else if !strings.Contains(err.Error(), "must be") || !strings.Contains(err.Error(), "integer") {
		t.Fatalf("err = %q, want an explicit integer-revision refusal", err)
	}
	// The explicit integer path still works and still conflicts when stale.
	it, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"update","key":"`+seed.TaskKey+`","title":"Renamed","revision":1}`)
	if err != nil {
		t.Fatalf("integer revision update: %v", err)
	}
	var got struct {
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal(mustJSON(it), &got); err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 {
		t.Fatalf("revision = %d, want 2", got.Revision)
	}
}
