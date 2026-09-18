//go:build e2e

// Package e2e is the fake-adapter end-to-end suite (spec §23.1, §23.2). Every
// test in this package drives the real HTTP API and MCP surface of a real
// daemon, spawned by scripts/e2e.sh on its own port and tmux socket
// (SWARM_E2E_URL/SWARM_E2E_TOKEN/SWARM_E2E_HOME), with adapter.Fake standing
// in for a model. No test here may call t.Parallel(): they share one daemon,
// one tmux socket, one GNUPGHOME and one process environment.
package e2e

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var idSeq int64

// unique returns a short, test-run-unique suffix so parallel-ish item/agent
// names across different tests in the same daemon never collide.
func unique() string {
	return strconv.FormatInt(atomic.AddInt64(&idSeq, 1), 10) + "-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 36)
}

type harness struct {
	url, token, home string
	http             *http.Client
}

// newHarness reads the three env vars scripts/e2e.sh exports and makes sure
// the fake agent kind is enabled — settings.Defaults never includes it
// (nothing installs "fake"), so every scenario would fail at preflight
// (E1146: EnabledAgents) without this one-time PUT.
func newHarness(t *testing.T) *harness {
	t.Helper()
	url, token, home := os.Getenv("SWARM_E2E_URL"), os.Getenv("SWARM_E2E_TOKEN"), os.Getenv("SWARM_E2E_HOME")
	if url == "" || token == "" || home == "" {
		t.Fatal("SWARM_E2E_URL/SWARM_E2E_TOKEN/SWARM_E2E_HOME are not set — run through `make e2e`, not `go test` directly")
	}
	h := &harness{url: url, token: token, home: home, http: &http.Client{Timeout: 30 * time.Second}}
	h.enableFake(t)
	return h
}

// enableFake writes the enabled_agents row directly: kinds.AgentKinds
// (settings.go's validate) deliberately excludes "fake" — it's a test-only
// kind, never meant to reach a real user's settings picker — so PUT
// /api/settings 400s on it every time. internal/runtime's own tests hit the
// same wall and clear it the same way (agents_test.go:125): write the row
// with raw SQL instead of going through Store.Put's validation. The e2e
// harness is a separate process from the daemon, so this opens its own
// (short-lived, WAL-safe) writable connection rather than reusing h.db's
// read-only one.
func (h *harness) enableFake(t *testing.T) {
	t.Helper()
	var cur map[string]any
	h.doT(t, http.MethodGet, "/api/settings", nil, &cur)
	agents, _ := cur["enabled_agents"].([]any)
	for _, a := range agents {
		if a == "fake" {
			return
		}
	}
	agents = append(agents, "fake")
	raw, err := json.Marshal(agents)
	if err != nil {
		t.Fatal(err)
	}
	d, err := sql.Open("sqlite", "file:"+filepath.Join(h.home, "swarm.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now().UnixMilli()
	_, err = d.Exec(`INSERT INTO settings (key, value_json, updated_at) VALUES ('enabled_agents', ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		string(raw), now)
	if err != nil {
		t.Fatal(err)
	}
	// Preflight also checks Catalog.ModelsFor, which reads model_catalog — a
	// cache internal/catalog.Refresh fills from Fetchers, and "fake" has no
	// fetcher (it isn't a real installable agent). Seed the row it would
	// otherwise never get, the same way (raw SQL past the normal write path).
	_, err = d.Exec(`INSERT INTO model_catalog (agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('fake', 'fake-1', '[{"id":"fake-1","label":"Fake 1","efforts":[],"effort_encoding":"flag"}]', 'fake-1', 'fake', ?, ?)
		ON CONFLICT(agent_kind) DO UPDATE SET models_json = excluded.models_json, default_model = excluded.default_model,
		fetched_at = excluded.fetched_at, attempted_at = excluded.attempted_at`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	// The whole suite shares one daemon and never tears an agent down between
	// tests, so the production defaults (3 orchestrators, 8 agents, 4 per
	// root) run out well before scenario 30 — raise them generously here.
	// Scenario 11 (the concurrency queue) sets max_agents back down to 1 for
	// its own duration and restores it after, rather than everyone else
	// living with a tiny ceiling.
	for _, kv := range [][2]string{{"max_orchestrators", "200"}, {"max_agents", "200"}, {"max_agents_per_root", "50"}} {
		if _, err := d.Exec(`INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
			kv[0], kv[1], now); err != nil {
			t.Fatal(err)
		}
	}
}

// setMaxAgents overrides max_agents directly (raw SQL, same reasoning as
// enableFake: this is daemon-internal tuning for the test run, not a user
// setting change worth routing through Store.Put's validation).
func (h *harness) setMaxAgents(t *testing.T, n int) {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+filepath.Join(h.home, "swarm.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`INSERT INTO settings (key, value_json, updated_at) VALUES ('max_agents', ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		strconv.Itoa(n), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

// do is the raw HTTP call; status and body are the caller's to interpret
// (some tests want a 409 or 422, not a fatal).
func (h *harness) do(method, path string, body any) (status int, raw []byte, err error) {
	var rd io.Reader
	if body != nil {
		b, merr := json.Marshal(body)
		if merr != nil {
			return 0, nil, merr
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.url+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

// doT is do, but fatal on transport error or a non-2xx status: the harness's
// own setup calls (settings, item creation) are never the thing under test.
func (h *harness) doT(t *testing.T, method, path string, body, out any) {
	t.Helper()
	status, raw, err := h.do(method, path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if status >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, status, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode %v: %s", method, path, err, raw)
		}
	}
}

// db opens the daemon's own sqlite file read-only. The e2e harness reads it
// directly for state a route doesn't expose (session tokens, message rows) —
// §23.2's own text says each scenario "checks the DB state", and WAL mode
// (internal/db.Open) makes that safe alongside the daemon's own writer.
func (h *harness) db(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+filepath.Join(h.home, "swarm.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// sessionToken reads an agent's own latest session token off disk
// (run/tokens/<session id>, written by runtime.Store.startSession) after
// finding that session's id in the DB. This is how the harness acts AS an
// agent over MCP without needing a real fake-agent process to drive the
// call itself.
func (h *harness) sessionToken(t *testing.T, agentName string) string {
	t.Helper()
	var sesID string
	err := h.db(t).QueryRow(`SELECT s.id FROM sessions s JOIN agents a ON a.id = s.agent_id
		WHERE a.name = ? ORDER BY s.generation DESC, s.attempt DESC LIMIT 1`, agentName).Scan(&sesID)
	if err != nil {
		t.Fatalf("session for %s: %v", agentName, err)
	}
	b, err := os.ReadFile(filepath.Join(h.home, "run", "tokens", sesID))
	if err != nil {
		t.Fatalf("token file for %s: %v", agentName, err)
	}
	return strings.TrimSpace(string(b))
}

// tool calls one MCP tool as agentName's own session and reports only
// whether it succeeded, carrying the tool's own message text (never a Go
// network error wrapped around it) when the call is IsError. Use toolOut
// when the step's result matters (e.g. an artifact id to save).
func (h *harness) tool(t *testing.T, agentName, name string, args map[string]any) error {
	t.Helper()
	_, err := h.toolOut(t, agentName, name, args)
	return err
}

func (h *harness) toolOut(t *testing.T, agentName, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}})
	req, err := http.NewRequest(http.MethodPost, h.url+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.sessionToken(t, agentName))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("%s: bad /mcp response %s: %v", name, raw, err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("%s", env.Error.Message)
	}
	if env.Result == nil || len(env.Result.Content) == 0 {
		t.Fatalf("%s: empty /mcp result: %s", name, raw)
	}
	text := env.Result.Content[0].Text
	if env.Result.IsError {
		return nil, fmt.Errorf("%s", text)
	}
	var out map[string]any
	if len(text) > 0 {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("%s: result %q: %v", name, text, err)
		}
	}
	return out, nil
}

// mustTool is toolOut with a t.Fatal on error, for steps a scenario has no
// business seeing fail.
func (h *harness) mustTool(t *testing.T, agentName, name string, args map[string]any) map[string]any {
	t.Helper()
	out, err := h.toolOut(t, agentName, name, args)
	if err != nil {
		t.Fatalf("%s (as %s): %v", name, agentName, err)
	}
	return out
}

// materializedEpic creates an EPIC/STORY/TASK tree directly through P1's
// items API — not by running the full happy-feature-spike flow, which is
// scenario 1's own job — so every other scenario gets a ready-made tree to
// work against without depending on the fake agent's own scripted behaviour.
func (h *harness) materializedEpic(t *testing.T) string {
	t.Helper()
	var epic map[string]any
	h.doT(t, http.MethodPost, "/api/items",
		map[string]any{"type": "epic", "title": "E2E epic " + unique()}, &epic)
	epicKey, _ := epic["key"].(string)
	var story map[string]any
	h.doT(t, http.MethodPost, "/api/items",
		map[string]any{"type": "story", "title": "Story", "parent_key": epicKey}, &story)
	storyKey, _ := story["key"].(string)
	var task map[string]any
	h.doT(t, http.MethodPost, "/api/items",
		map[string]any{"type": "task", "title": "Task", "parent_key": storyKey}, &task)
	taskKey, _ := task["key"].(string)
	// POST /api/items has no status field at all (contracts §4: request_id,
	// type, title, brief?, acceptance?, parent_key? — that's the whole body),
	// so a fresh item is always Draft. PATCH it to Ready so a worker has
	// something to actually work; Update carries its own transition rules,
	// which is why this goes through the real route rather than raw SQL.
	rev, _ := task["revision"].(float64)
	h.doT(t, http.MethodPatch, "/api/items/"+taskKey,
		map[string]any{"status": "ready", "revision": int(rev)}, nil)
	return epicKey
}

// firstTask returns the key of the first task item under root.
func (h *harness) firstTask(t *testing.T, root string) string {
	t.Helper()
	var list struct {
		Items []map[string]any `json:"items"`
	}
	h.doT(t, http.MethodGet, "/api/items?view=flat&root="+root, nil, &list)
	for _, it := range list.Items {
		if it["type"] == "task" {
			return it["key"].(string)
		}
	}
	t.Fatalf("no task under %s: %+v", root, list.Items)
	return ""
}

// startOrchestrator spawns a fake orchestrator on root. Its kebab name (from
// root's title) has no matching scenario file, so it runs Task 37's
// _default.json (idle forever) — plenty for a test that drives the
// orchestrator itself over MCP rather than through a scripted pane.
func (h *harness) startOrchestrator(t *testing.T, root string) string {
	t.Helper()
	return h.startNamedOrchestrator(t, root, "")
}

// startNamedOrchestrator is startOrchestrator with an explicit agent name, for
// a scenario that needs the real fake-agent process to run a specific script
// (e.g. scripts/e2e/scenarios/happy-feature-spike.json).
func (h *harness) startNamedOrchestrator(t *testing.T, root, name string) string {
	t.Helper()
	var node map[string]any
	h.doT(t, http.MethodPost, "/api/items/"+root+"/orchestrator",
		map[string]any{"request_id": "req-" + unique(), "agent": "fake", "model": "fake-1", "name": name}, &node)
	got, _ := node["name"].(string)
	if got == "" {
		t.Fatalf("orchestrator start returned no name: %+v", node)
	}
	return got
}

// spawn calls swarm_spawn as orchestrator, over MCP with its own session
// token, so no real fake-agent process needs to run any particular script to
// raise a child — the harness plays the orchestrator's part directly. It then
// sends the new worker's own "accepted" checkpoint, the same first thing a
// real agent does once it syncs its assignment: nothing in Store.Spawn itself
// moves the item off Ready (internal/runtime/checkpoint.go:296-304 is what
// moves Ready -> InProgress, on kind:"accepted"), so a caller that skipped
// this step would see the task sitting at Ready, not the in-progress state
// every downstream scenario actually wants to test against.
func (h *harness) spawn(t *testing.T, orchestrator, itemKey, role string) string {
	t.Helper()
	out := h.mustTool(t, orchestrator, "swarm_spawn", map[string]any{
		"item": itemKey, "role": role, "agent": "fake", "model": "fake-1",
	})
	name, _ := out["agent"].(string)
	if name == "" {
		t.Fatalf("swarm_spawn returned no agent name: %+v", out)
	}
	h.mustTool(t, name, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	return name
}

// itemStatus is the item's current status string. GET /api/items/{key}
// answers ItemDetail ({"item": Item, "agents", "requests", "artifacts"}), not
// a bare Item, so the status is one level down.
func (h *harness) itemStatus(t *testing.T, key string) string {
	t.Helper()
	var detail struct {
		Item map[string]any `json:"item"`
	}
	h.doT(t, http.MethodGet, "/api/items/"+key, nil, &detail)
	st, _ := detail.Item["status"].(string)
	return st
}

// headSHA is a plausible-looking, but not real, commit sha: runtime.GitRef
// is stored as the caller reports it (internal/runtime/checkpoint.go never
// runs git to check it), so a scenario that isn't specifically about git
// state (20, 21, 24) needs no real repository to exercise the TDD gate.
func (h *harness) headSHA(t *testing.T) string {
	return fmt.Sprintf("%040x", time.Now().UnixNano())
}

// waitForEvent polls the events table (§23.1: "checks the DB state and SSE
// events" — the harness reads the same row an SSE client would have seen,
// which is simpler than holding open a stream per test) for a row of typ
// whose payload satisfies match, within timeout.
func (h *harness) waitForEvent(t *testing.T, typ string, match func(payload []byte) bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		rows, err := h.db(t).Query(`SELECT payload_json FROM events WHERE type = ? ORDER BY seq DESC LIMIT 200`, typ)
		if err == nil {
			for rows.Next() {
				var p string
				if rows.Scan(&p) == nil && (match == nil || match([]byte(p))) {
					rows.Close()
					return true
				}
			}
			rows.Close()
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForNotification is waitForEvent's counterpart against the notifications
// table, which carries the exact §17.5 `kind` string rather than an event
// envelope.
func (h *harness) waitForNotification(t *testing.T, kind string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var n int
		h.db(t).QueryRow(`SELECT count(*) FROM notifications WHERE kind = ?`, kind).Scan(&n)
		if n > 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// requestByKind returns the id of the first open request of kind for item,
// or "" if there is none yet.
func (h *harness) openRequest(t *testing.T, itemKey, kind string) string {
	t.Helper()
	var list []map[string]any
	h.doT(t, http.MethodGet, "/api/requests", nil, &list)
	for _, r := range list {
		if r["item_key"] == itemKey && r["kind"] == kind && r["state"] == "open" {
			id, _ := r["id"].(string)
			return id
		}
	}
	return ""
}

// waitForRequest polls openRequest until it appears.
func (h *harness) waitForRequest(t *testing.T, itemKey, kind string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if id := h.openRequest(t, itemKey, kind); id != "" {
			return id
		}
		if time.Now().After(deadline) {
			t.Fatalf("no open %s request on %s within %s", kind, itemKey, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// gitRepo creates a bare-bones repo under the daemon's own scan root, with a
// user identity and (unless signOff is false) commit signing configured
// against the throwaway keyring, and one commit so HEAD exists.
func gitRepo(t *testing.T, sign bool, keyID string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "Swarm E2E")
	run("config", "user.email", "e2e@example.invalid")
	if sign {
		run("config", "user.signingkey", keyID)
		run("config", "commit.gpgsign", "true")
		run("config", "gpg.program", "gpg")
	} else {
		run("config", "commit.gpgsign", "false")
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# e2e\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	commitArgs := []string{"commit", "-q", "-m", "init"}
	if !sign {
		commitArgs = append(commitArgs, "--no-gpg-sign")
	}
	run(commitArgs...)
	return dir
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// throwawayGPGKey returns the keyring scripts/e2e.sh created and the key id
// in it. It does NOT create one: a keyring made here would live in the Go
// test process's environment and never reach the tmux pane the agent runs in
// (R10).
func throwawayGPGKey(t *testing.T) (gnupgHome, keyID string) {
	t.Helper()
	dir := os.Getenv("GNUPGHOME")
	if dir == "" {
		t.Skip("scripts/e2e.sh did not export GNUPGHOME; this scenario needs the throwaway keyring")
	}
	if home := os.Getenv("SWARM_E2E_HOME"); home != "" && !strings.HasPrefix(dir, home) {
		t.Fatalf("GNUPGHOME is %q, outside the e2e home %q — this run would sign with the user's key", dir, home)
	}
	out, err := exec.Command("gpg", "--list-secret-keys", "--with-colons").CombinedOutput()
	if err != nil {
		t.Skipf("gpg is unavailable: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, ":")
		if len(f) > 4 && f[0] == "sec" {
			keyID = f[4]
		}
	}
	if keyID == "" {
		t.Skip("the throwaway keyring has no secret key")
	}
	if strings.Contains(string(out), "@") && !strings.Contains(string(out), "e2e@example.invalid") {
		t.Fatalf("the keyring in use is not the throwaway one:\n%s", out)
	}
	return dir, keyID
}
