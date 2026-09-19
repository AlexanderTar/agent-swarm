package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/advisor"
	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/hook"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/mcpserver"
	"github.com/AlexanderTar/agent-swarm/internal/notify"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/usage"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

var bg = context.Background()

const daemonToken = "daemon-token-for-tests"

type stubFetcher struct{ fetches atomic.Int64 }

func (f *stubFetcher) Kind() runtime.AgentKind                 { return runtime.Claude }
func (f *stubFetcher) Version(context.Context) (string, error) { return "2.1.274", nil }
func (f *stubFetcher) Fetch(context.Context) (catalog.Fetched, error) {
	f.fetches.Add(1)
	return catalog.Fetched{Models: catalog.ClaudeAliasFallback(), Source: "stub"}, nil
}

type stubEmb struct{ down atomic.Bool }

func (e *stubEmb) Model() string { return "stub" }
func (e *stubEmb) Check(context.Context) error {
	if e.down.Load() {
		return kb.ErrUnavailable
	}
	return nil
}
func (e *stubEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	var out [][]float32
	for _, t := range texts {
		out = append(out, []float32{float32(len(t)), 1})
	}
	return out, nil
}

type env struct {
	t       *testing.T
	srv     *httptest.Server
	s       *Server
	home    string
	items   *items.Store
	events  *events.Store
	repos   *repos.Service
	cat     *catalog.Service
	fetcher *stubFetcher
	emb     *stubEmb
}

func mkfile(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newEnv builds a server over temp stores; opts adjust the Deps before New.
func newEnv(t *testing.T, opts ...func(*Deps)) *env {
	t.Helper()
	d := dbtest.Open(t)
	now := time.Now
	ev := events.New(d, now)
	home, _ := filepath.EvalSymlinks(t.TempDir())
	for _, r := range []string{"GitHub/app", "GitHub/web", "GitHub/tools"} {
		os.MkdirAll(filepath.Join(home, r, ".git"), 0o755)
	}
	os.MkdirAll(filepath.Join(home, "Workspaces/proj"), 0o755)
	os.Symlink(filepath.Join(home, "GitHub/app"), filepath.Join(home, "Workspaces/proj/app"))
	os.Symlink(filepath.Join(home, "GitHub/web"), filepath.Join(home, "Workspaces/proj/web"))
	mkfile(t, filepath.Join(home, "proj.code-workspace"), `{"folders":[{"path":"GitHub/tools"}]}`)
	rp := &repos.Service{DB: d, Events: ev, Run: (&execx.Fake{}).Runner(), Home: home, Now: now,
		Excludes: func(context.Context) []string { return nil }, DirtyTimeout: 50 * time.Millisecond}
	f := &stubFetcher{}
	cat := &catalog.Service{DB: d, Events: ev, Fetchers: []catalog.Fetcher{f}, Now: now}
	if _, err := cat.Refresh(bg, false); err != nil {
		t.Fatal(err)
	}
	st := &settings.Store{DB: d, Events: ev, Now: now, ModelsFor: cat.ModelsFor, Installed: cat.Installed}
	kbDir := filepath.Join(home, "kb")
	mkfile(t, filepath.Join(kbDir, "specs/alpha.md"), "---\ntitle: Alpha spec\n---\n# Alpha\nalpha board design.\n")
	mkfile(t, filepath.Join(kbDir, "notes/beta.md"), "# Beta\nbeta kanban notes.\n")
	emb := &stubEmb{}
	idx := &kb.Index{DB: d, Dir: kbDir, Emb: emb, Now: now}
	if err := idx.Load(bg); err != nil {
		t.Fatal(err)
	}
	if err := idx.Sync(bg); err != nil {
		t.Fatal(err)
	}
	it := &items.Store{DB: d, Events: ev, Now: now}
	deps := Deps{Version: "test", Token: daemonToken, DB: d, Events: ev, Items: it, Repos: rp,
		Settings: st, Catalog: cat, KB: idx, WriteTimeout: 200 * time.Millisecond,
		// Run and After are P1-irrelevant seams P2 (R11) added to Deps; New panics
		// on a nil Run, so every test gets a safe default here before opts run.
		Run: (&execx.Fake{}).Runner(), After: time.After}
	for _, o := range opts {
		o(&deps)
	}
	s := New(deps)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return &env{t: t, srv: srv, s: s, home: home, items: it, events: ev, repos: rp, cat: cat, fetcher: f, emb: emb}
}

// call sends a request with an optional bearer token and JSON body.
func (e *env) call(method, path string, body any, token string, headers ...string) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (e *env) api(method, path string, body any) (int, []byte) {
	e.t.Helper()
	return e.call(method, path, body, daemonToken)
}

// callNoFatal is safe to use from spawned goroutines; transport errors come back as status 0.
func (e *env) callNoFatal(method, path string, body any) (int, []byte) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+daemonToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	} `json:"error"`
}

// wantErr checks status, code and message of an error response.
func wantErr(t *testing.T, status int, body []byte, wantStatus int, code, msg string) {
	t.Helper()
	e := decode[errBody](t, body)
	if status != wantStatus || e.Error.Code != code || (msg != "" && e.Error.Message != msg) {
		t.Fatalf("got %d %s, want %d %s %q", status, body, wantStatus, code, msg)
	}
}

// ---- added by Task 31: the P2 runtime harness ----

// rec is the {status, body} pair in the shape the P2 tests read. It is not a
// second transport: get and post below go through P1's env.api, which uses
// the same httptest.Server and the same token as every P1 test in this package.
type rec struct {
	Code int
	Body *bytes.Buffer
}

func (r *rec) String() string { return r.Body.String() }

// httpSeed is what the P2 route tests name.
type httpSeed struct {
	RootKey, StoryKey, TaskKey, SpikeKey, EpicKey string
	AgentName, SessionID, SessionToken            string
	RepoID                                        string
}

type runtimeEnv struct {
	*env
	RT    *runtime.Store
	Seed  httpSeed
	DB    *db.DB
	Token string
}

// Handler forwards to the wrapped Server, so tests can drive the mux directly
// (the wake-stream and auth tests bypass the httptest.Server transport).
func (e *runtimeEnv) Handler() http.Handler { return e.s.Handler() }

func (e *runtimeEnv) get(t *testing.T, path string) *rec {
	t.Helper()
	code, body := e.api(http.MethodGet, path, nil)
	return &rec{Code: code, Body: bytes.NewBuffer(body)}
}

// post takes the body as a raw JSON string, because every P2 route test writes
// its body as a literal. env.api marshals a value, so the string is wrapped in
// a json.RawMessage to reach the wire unchanged.
func (e *runtimeEnv) post(t *testing.T, path, body string) *rec {
	t.Helper()
	code, out := e.api(http.MethodPost, path, json.RawMessage(body))
	return &rec{Code: code, Body: bytes.NewBuffer(out)}
}

// getNoToken and postSession are the two variants the auth tests need.
func (e *runtimeEnv) getNoToken(t *testing.T, path string) *rec {
	t.Helper()
	code, out := e.call(http.MethodGet, path, nil, "")
	return &rec{Code: code, Body: bytes.NewBuffer(out)}
}

func (e *runtimeEnv) postSession(t *testing.T, path, token, body string) *rec {
	t.Helper()
	code, out := e.call(http.MethodPost, path, json.RawMessage(body), token)
	return &rec{Code: code, Body: bytes.NewBuffer(out)}
}

// postToken is env.call with a session token instead of the daemon token (Task 34).
func (e *runtimeEnv) postToken(t *testing.T, token, path, body string) *rec {
	t.Helper()
	// Content-Type and Accept are explicit here (unlike post/postVia) because
	// /mcp's SDK handler refuses a POST without both; every other P2 route
	// ignores them.
	code, out := e.call(http.MethodPost, path, json.RawMessage(body), token,
		"Content-Type", "application/json", "Accept", "application/json, text/event-stream")
	return &rec{Code: code, Body: bytes.NewBuffer(out)}
}

// postVia is post with an explicit X-Swarm-Via header (W7).
func (e *runtimeEnv) postVia(t *testing.T, via, path, body string) *rec {
	t.Helper()
	code, out := e.call(http.MethodPost, path, json.RawMessage(body), daemonToken, "X-Swarm-Via", via)
	return &rec{Code: code, Body: bytes.NewBuffer(out)}
}

// testTmux is a minimal runtime.Tmux double: every P2 fixture in this package
// seeds sessions with raw SQL rather than a real spawn (matching
// internal/mcpserver's own harness, D-style precedent), so this only needs to
// satisfy the interface and never touch a real process (S-1, S-2).
type testTmux struct {
	panes  []runtime.Pane
	killed []string
	keys   []string
}

func (f *testTmux) Start(context.Context, string, string, map[string]string, []string) error {
	return nil
}
func (f *testTmux) Panes(context.Context) ([]runtime.Pane, error) { return f.panes, nil }

// Capture always reports the idle prompt (matches internal/adapter.Fake's
// IdlePrompt): watchStartup then moves a freshly spawned session straight to
// "running" instead of polling capture-pane for up to 30 s per spawn.
func (f *testTmux) Capture(context.Context, string, int) (string, error) {
	return "─────\n❯ \n─────\n", nil
}
func (f *testTmux) PasteLine(context.Context, string, string) error { return nil }
func (f *testTmux) Keys(ctx context.Context, name string, keys ...string) error {
	f.keys = append(f.keys, name)
	return nil
}
func (f *testTmux) Env(context.Context, string, string) (string, error) { return "", nil }
func (f *testTmux) Kill(ctx context.Context, name string) error {
	f.killed = append(f.killed, name)
	return nil
}

func seedFakeAgentCatalog(t *testing.T, d *db.DB) {
	t.Helper()
	if _, err := d.ExecContext(bg, `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('fake', 'fake-1', '[{"id":"fake-1","label":"Fake 1","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":false}]', 'fake-1', 'test', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(bg, `INSERT INTO settings (key, value_json, updated_at)
		VALUES ('enabled_agents', '["fake"]', 1)`); err != nil {
		t.Fatal(err)
	}
}

// newServicesOnly is newEnv plus every P2 service, wired but with nothing
// seeded — newRuntimeServerWith adds the tree; newEmptyServer stops here.
func newServicesOnly(t *testing.T, extra func(d *Deps)) *runtimeEnv {
	t.Helper()
	var rt *runtime.Store
	home := t.TempDir()
	e := newEnv(t, func(d *Deps) {
		seedFakeAgentCatalog(t, d.DB)
		fa := adapter.NewFake(adapter.Deps{Home: home, UserHome: t.TempDir(), Bin: "/usr/local/bin/swarm",
			Run: execx.Run, Log: func(string, ...any) {}})
		tm := &testTmux{}
		nt := &notify.Service{DB: d.DB, Events: d.Events, Now: time.Now, Log: func(string, ...any) {}}
		rt = &runtime.Store{DB: d.DB, Events: d.Events, Items: d.Items, Repos: d.Repos, Settings: d.Settings,
			Catalog: d.Catalog, Home: home, Now: time.Now, Log: func(string, ...any) {}, Tmux: tm,
			Worktree: &worktree.Service{DB: d.DB, Run: execx.Run, Now: time.Now, Log: func(string, ...any) {}, Home: home},
			Notify:   nt, Adapters: map[runtime.AgentKind]adapter.Adapter{runtime.Fake: fa},
			Bin: "/usr/local/bin/swarm", DaemonURL: "http://127.0.0.1:0",
			OSEnv: func(string) string { return "" },
			BaseEnv: func(getenv func(string) string) map[string]string {
				return map[string]string{"PATH": "/usr/bin"}
			},
			After: time.After, Go: func(f func()) { f() },
			// S-1: never the production socket, even by accident.
			TmuxPath: "tmux", TmuxSocketName: fakeTmuxSocket(t)}
		adv := &advisor.Service{DB: d.DB, Events: d.Events, Home: home, UserHome: t.TempDir(),
			Adapters: rt.Adapters, MaxConcurrent: 2, Timeout: 5 * time.Minute, Now: time.Now,
			Log: func(string, ...any) {},
			Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(`{"result":"advice"}`), nil }}
		rt.Advisor = adv
		d.RT, d.Notify, d.Advisor = rt, nt, adv
		d.Usage = &usage.Poller{DB: d.DB, Events: d.Events, Settings: d.Settings, Now: time.Now}
		d.MCP = &mcpserver.Server{RT: rt, KB: d.KB, Advisor: adv, Version: "test", Log: func(string, ...any) {}}
		d.Hook = &hook.Handler{DB: d.DB, RT: rt, Adapters: rt.Adapters, Advisor: adv, Now: time.Now, Log: func(string, ...any) {}}
		d.Run = (&execx.Fake{}).Runner()
		// After never fires by default: only TestTerminalFallbackRunsGhosttyThroughTheInjectedRunner
		// wants the 1.5 s terminal-fallback timer to actually run, and it overrides
		// this itself. Every other test that hits POST …/terminal would otherwise
		// leave a real 1.5 s timer running past the end of the test.
		d.After = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
		if extra != nil {
			extra(d)
		}
	})
	return &runtimeEnv{env: e, RT: rt, DB: e.s.DB, Token: e.s.Token}
}

func fakeTmuxSocket(t *testing.T) string { return "swarm-test-" + t.Name() }

// newEmptyServer is every P2 service wired over an otherwise-empty database:
// a fresh daemon that has never spawned anything (TestStateIsEmptyButWellShapedOnAFreshDaemon).
func newEmptyServer(t *testing.T) *runtimeEnv {
	t.Helper()
	return newServicesOnly(t, nil)
}

// newRuntimeServerWith is newServicesOnly plus the seeded tree (Task 31's
// brief); every other named constructor in this file is a thin layer over it,
// including the literal call in TestTerminalFallbackRunsGhosttyThroughTheInjectedRunner.
func newRuntimeServerWith(t *testing.T, extra func(d *Deps)) (*runtimeEnv, httpSeed) {
	t.Helper()
	e := newServicesOnly(t, extra)
	seed := seedRuntimeTree(t, e)
	e.Seed = seed
	return e, seed
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// seedSession inserts one session row and returns its id and plaintext bearer
// token; sessionAuth hashes what it reads off the wire the same way.
func seedSession(t *testing.T, e *runtimeEnv, agentID, tmuxName string, generation int, state string) (sessionID, token string) {
	t.Helper()
	sessionID = ids.New("ses")
	token = "test-token-" + sessionID
	now := db.Millis(time.Now())
	if _, err := e.s.DB.ExecContext(bg, `INSERT INTO sessions
		(id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES (?, ?, 1, ?, ?, ?, ?, ?, 'neutral', ?)`,
		sessionID, agentID, generation, hashToken(token), tmuxName, filepath.Join(t.TempDir(), tmuxName), state, now); err != nil {
		t.Fatal(err)
	}
	return sessionID, token
}

// seedAgent inserts one agent row (raw SQL, like internal/mcpserver's own
// fixtures: no test in this package spawns for real, S-1/S-2).
func seedAgent(t *testing.T, e *runtimeEnv, role runtime.Role, itemID, rootItemID, parentAgentID, name string) string {
	t.Helper()
	agentID := ids.New("agt")
	now := db.Millis(time.Now())
	var parent any
	if parentAgentID != "" {
		parent = parentAgentID
	}
	if _, err := e.s.DB.ExecContext(bg, `INSERT INTO agents
		(id, name, kind, model, role, item_id, root_item_id, parent_agent_id, brief, state, created_at)
		VALUES (?, ?, 'fake', 'fake-1', ?, ?, ?, ?, 'seeded', 'active', ?)`,
		agentID, name, string(role), itemID, rootItemID, parent, now); err != nil {
		t.Fatal(err)
	}
	return agentID
}

func seedRepo(t *testing.T, e *runtimeEnv, name, path string) string {
	t.Helper()
	id := ids.New("repo")
	now := db.Millis(time.Now())
	if _, err := e.s.DB.ExecContext(bg, `INSERT INTO repos (id, path, name, source, created_at, updated_at)
		VALUES (?, ?, ?, 'manual', ?, ?)`, id, path, name, now, now); err != nil {
		t.Fatal(err)
	}
	return id
}

// gitRepoSigningOn is a real, deterministic git repo with commit.gpgsign set
// locally (not read from whatever the host's global git config happens to
// have) — required fix 1 threads a spawn-time repo pick into Preflight's own
// §11.4 signing check, so any test that needs that check to actually pass
// needs a real repo, not the fake `.git` directories newEnv scatters around
// for the repo-scan tests (those never run a real git command against them).
func gitRepoSigningOn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "T"},
		{"config", "commit.gpgsign", "true"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

// seedRuntimeTree builds one EPIC with one story, one task, one spike, one
// confirmed repo and one running orchestrator session (Task 31's brief).
func seedRuntimeTree(t *testing.T, e *runtimeEnv) httpSeed {
	t.Helper()
	ctx := bg
	repoID := seedRepo(t, e, "app", filepath.Join(e.home, "GitHub", "app"))
	epic, err := e.items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Root epic", Brief: "The root."}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DB.ExecContext(ctx, `UPDATE items SET confirmed_repos_json = ?, repos_version = 1 WHERE id = ?`,
		`["`+repoID+`"]`, epic.ID); err != nil {
		t.Fatal(err)
	}
	story, err := e.items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: epic.Key, Title: "Story"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := e.items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: "Task"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	spike, err := e.items.Create(ctx, items.CreateInput{Type: items.Spike, Title: "Spike", SpikeIntent: "feature"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	orchID := seedAgent(t, e, runtime.RoleOrchestrator, epic.ID, epic.ID, "", "root-orchestrator")
	sessionID, token := seedSession(t, e, orchID, "root-orchestrator", 1, "running")
	// a worker on the task so item-detail's own agents list (D13) is non-empty.
	workerID := seedAgent(t, e, runtime.RoleCoder, task.ID, epic.ID, orchID, "task-worker")
	seedSession(t, e, workerID, "task-worker", 1, "running")
	return httpSeed{RootKey: epic.Key, StoryKey: story.Key, TaskKey: task.Key, SpikeKey: spike.Key,
		AgentName: "root-orchestrator", SessionID: sessionID, SessionToken: token, RepoID: repoID}
}

// newRuntimeServer is newRuntimeServerWith with no extra Deps tweak.
func newRuntimeServer(t *testing.T) (*runtimeEnv, httpSeed) {
	t.Helper()
	return newRuntimeServerWith(t, nil)
}

// newRuntimeServerNoSuperpowers is the base tree with the fake adapter's
// superpowers check turned off, so a spawn's own Preflight call fails at
// request time (Task 32's TestCreateSpikeWithAPreflightFailure).
func newRuntimeServerNoSuperpowers(t *testing.T) (*runtimeEnv, httpSeed) {
	t.Helper()
	e, seed := newRuntimeServerWith(t, nil)
	e.RT.Adapters[runtime.Fake].(*adapter.Fake).NoSuperpowers = true
	return e, seed
}

// newRuntimeServerWithPreflightFailure is the base tree plus one spike whose
// preflight already failed (an agent with preflight_error set and no session).
func newRuntimeServerWithPreflightFailure(t *testing.T) (*runtimeEnv, httpSeed) {
	t.Helper()
	e, seed := newRuntimeServer(t)
	fa := e.RT.Adapters[runtime.Fake].(*adapter.Fake)
	fa.NoSuperpowers = true
	if _, _, _, err := e.RT.StartSpike(bg, runtime.SpikeInput{Name: "Broken spike", Intent: "feature",
		Kind: runtime.Fake, Model: "fake-1"}); err != nil {
		t.Fatal(err)
	}
	fa.NoSuperpowers = false
	return e, seed
}

// newRuntimeServerWithCheckpoints is the base tree plus n checkpoints on
// TaskKey, oldest first in created_at so the newest-first assertion is real.
func newRuntimeServerWithCheckpoints(t *testing.T, n int) (*runtimeEnv, httpSeed) {
	t.Helper()
	e, seed := newRuntimeServer(t)
	var agentID, sessionID string
	if err := e.s.DB.QueryRowContext(bg, `SELECT id FROM agents WHERE name = 'task-worker'`).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if err := e.s.DB.QueryRowContext(bg, `SELECT id FROM sessions WHERE agent_id = ?`, agentID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	var itemID string
	if err := e.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.TaskKey).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		id := ids.New("ckp")
		ms := db.Millis(base.Add(time.Duration(i) * time.Minute))
		if _, err := e.s.DB.ExecContext(bg, `INSERT INTO checkpoints
			(id, session_id, agent_id, item_id, kind, attempt, summary, created_at)
			VALUES (?, ?, ?, ?, 'progress', 1, ?, ?)`, id, sessionID, agentID, itemID, "progress "+id, ms); err != nil {
			t.Fatal(err)
		}
	}
	return e, seed
}

// newRuntimeServerWithEpic adds a second, orchestrator-free epic (for
// POST /api/items/{key}/orchestrator tests) alongside the base tree.
func newRuntimeServerWithEpic(t *testing.T) (*runtimeEnv, httpSeed) {
	t.Helper()
	e, seed := newRuntimeServer(t)
	epic, err := e.items.Create(bg, items.CreateInput{Type: items.Epic, Title: "Second epic"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	seed.EpicKey = epic.Key
	e.Seed = seed
	return e, seed
}

// newServerWithSessionState seeds one agent whose latest session is in `state`
// and returns the server and the agent's name. "queued" is the one state with
// no session row at all (an agent waiting for a slot has never been spawned),
// so it sets agents.state = 'queued' and writes no session; every other value
// is a sessions.state with the agent left `active` (Task 32).
func newServerWithSessionState(t *testing.T, state string) (*runtimeEnv, string) {
	t.Helper()
	e, seed := newRuntimeServer(t)
	name := "agent-" + state
	agentState := "active"
	if state == "queued" {
		agentState = "queued"
	}
	agentID := ids.New("agt")
	now := db.Millis(time.Now())
	if _, err := e.s.DB.ExecContext(bg, `INSERT INTO agents
		(id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		SELECT ?, ?, 'fake', 'fake-1', 'coder', id, root_id, 'seeded', ?, ?
		FROM items WHERE key = ?`, agentID, name, agentState, now, seed.TaskKey); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		seedSession(t, e, agentID, name, 1, state)
	}
	return e, name
}

// seedRunningAgent seeds one fresh running agent+session and returns its name
// (Task 32's terminal-fallback test).
func seedRunningAgent(t *testing.T, e *runtimeEnv) string {
	t.Helper()
	name := "running-" + ids.New("agt")
	var itemID, rootID string
	if err := e.s.DB.QueryRowContext(bg, `SELECT id, root_id FROM items WHERE key = ?`, e.Seed.TaskKey).Scan(&itemID, &rootID); err != nil {
		t.Fatal(err)
	}
	agentID := ids.New("agt")
	now := db.Millis(time.Now())
	if _, err := e.s.DB.ExecContext(bg, `INSERT INTO agents
		(id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES (?, ?, 'fake', 'fake-1', 'coder', ?, ?, 'seeded', 'active', ?)`,
		agentID, name, itemID, rootID, now); err != nil {
		t.Fatal(err)
	}
	seedSession(t, e, agentID, name, 1, "running")
	return name
}

// waitUntilHTTP is the 5-second poll every P2 test in this package shares
// (the same body as internal/advisor's waitUntil).
func waitUntilHTTP(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the condition")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- added by Task 33: requests, artifacts, notifications, usage, advice ----

func agentIDByName(t *testing.T, e *runtimeEnv, name string) string {
	t.Helper()
	var id string
	if err := e.s.DB.QueryRowContext(bg, `SELECT id FROM agents WHERE name = ?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func itemIDByKey(t *testing.T, e *runtimeEnv, key string) string {
	t.Helper()
	var id string
	if err := e.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type requestSeed struct {
	httpSeed
	QuestionID, ApprovalID, SecondApprovalID, SectionHash string
}

// newRequestServer seeds one open question and two open approve_section
// requests on the same artifact section, all raised by the base tree's
// orchestrator (Task 33).
func newRequestServer(t *testing.T) (*runtimeEnv, requestSeed) {
	t.Helper()
	e, base := newRuntimeServer(t)
	ctx := bg
	agentID := agentIDByName(t, e, base.AgentName)
	itemID := itemIDByKey(t, e, base.RootKey)
	now := db.Millis(time.Now())

	questionID := ids.New("req")
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, agent_id, item_id, prompt, options_json, state, created_at)
		VALUES (?, 'question', ?, ?, 'Keep it?', '[]', 'open', ?)`, questionID, agentID, itemID, now); err != nil {
		t.Fatal(err)
	}

	body := "The flow is three screens.\n"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	artID := ids.New("art")
	sections := fmt.Sprintf(`[{"id":"overview","heading":"Overview","sha256":%q,"start":0,"end":%d}]`, sum, len(body))
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_at)
		VALUES (?, ?, 'spec', 'docs/specs/flow.md', 1, ?)`, artID, itemID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO artifact_revisions (artifact_id, revision, sha256, content, sections_json, created_at)
		VALUES (?, 1, ?, ?, ?, ?)`, artID, sum, body, sections, now); err != nil {
		t.Fatal(err)
	}

	approvalID := ids.New("req")
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO requests
		(id, kind, agent_id, item_id, artifact_id, section_id, section_sha256, artifact_revision, prompt, options_json, state, created_at)
		VALUES (?, 'approve_section', ?, ?, ?, 'overview', ?, 1, 'Approve the overview.', '[]', 'open', ?)`,
		approvalID, agentID, itemID, artID, sum, now); err != nil {
		t.Fatal(err)
	}
	secondApprovalID := ids.New("req")
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO requests
		(id, kind, agent_id, item_id, artifact_id, section_id, section_sha256, artifact_revision, prompt, options_json, state, created_at)
		VALUES (?, 'approve_section', ?, ?, ?, 'overview', ?, 1, 'Approve again.', '[]', 'open', ?)`,
		secondApprovalID, agentID, itemID, artID, sum, now); err != nil {
		t.Fatal(err)
	}
	return e, requestSeed{httpSeed: base, QuestionID: questionID, ApprovalID: approvalID,
		SecondApprovalID: secondApprovalID, SectionHash: sum}
}

type acceptSeed struct {
	httpSeed
	AcceptID, CurrentBindingBody string
}

// newAcceptServer seeds a daemon-opened accept_epic request whose binding
// matches the epic's current integrated checkpoint and revision (Task 33's
// TestAcceptEpicChecksTheBinding). The epic's own story is marked done by raw
// SQL, matching internal/runtime's own test precedent for driving a status
// the real transition machinery would otherwise take many more steps to reach.
func newAcceptServer(t *testing.T) (*runtimeEnv, acceptSeed) {
	t.Helper()
	e, base := newRuntimeServer(t)
	ctx := bg
	epic, err := e.items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Accept me"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := e.items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: epic.Key, Title: "Only story"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DB.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE id = ?`, story.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DB.ExecContext(ctx, `UPDATE items SET status = 'in_review' WHERE id = ?`, epic.ID); err != nil {
		t.Fatal(err)
	}
	epicNow, err := e.items.Get(ctx, epic.Key)
	if err != nil {
		t.Fatal(err)
	}
	agentID := agentIDByName(t, e, base.AgentName)
	sessionID := base.SessionID
	ckpID := ids.New("ckp")
	now := db.Millis(time.Now())
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, created_at)
		VALUES (?, ?, ?, ?, 'integrated', 1, 'Integrated.', ?)`, ckpID, sessionID, agentID, epic.ID, now); err != nil {
		t.Fatal(err)
	}
	binding := map[string]any{"item_revision": epicNow.Revision, "integrated_checkpoint": ckpID, "git": []any{}}
	bindingBytes, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	reqID := ids.New("req")
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, item_id, prompt, options_json, state, binding_json, created_at)
		VALUES (?, 'accept_epic', ?, 'Accept this epic?', '[]', 'open', ?, ?)`, reqID, epic.ID, string(bindingBytes), now); err != nil {
		t.Fatal(err)
	}
	seed := base
	seed.EpicKey = epic.Key
	currentBody := fmt.Sprintf(`{"binding":%s,"via":"board"}`, string(bindingBytes))
	return e, acceptSeed{httpSeed: seed, AcceptID: reqID, CurrentBindingBody: currentBody}
}

type confirmSeed struct {
	httpSeed
	RequestID, SecondRequestID, RepoA, RepoB string
}

// newConfirmServer seeds a fresh, unconfirmed epic (repos_version starts at 0,
// unlike the base tree's already-confirmed root) with two open confirm_repos
// requests on it (Task 33).
func newConfirmServer(t *testing.T) (*runtimeEnv, confirmSeed) {
	t.Helper()
	e, base := newRuntimeServer(t)
	ctx := bg
	epic, err := e.items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Confirm me"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	agentID := agentIDByName(t, e, base.AgentName)
	repoA := seedRepo(t, e, "web", filepath.Join(e.home, "GitHub", "web"))
	repoB := seedRepo(t, e, "tools", filepath.Join(e.home, "GitHub", "tools"))
	now := db.Millis(time.Now())
	reqID := ids.New("req")
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, agent_id, item_id, prompt, options_json, state, created_at)
		VALUES (?, 'confirm_repos', ?, ?, 'Confirm repos', '[]', 'open', ?)`, reqID, agentID, epic.ID, now); err != nil {
		t.Fatal(err)
	}
	req2ID := ids.New("req")
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, agent_id, item_id, prompt, options_json, state, created_at)
		VALUES (?, 'confirm_repos', ?, ?, 'Confirm repos again', '[]', 'open', ?)`, req2ID, agentID, epic.ID, now); err != nil {
		t.Fatal(err)
	}
	return e, confirmSeed{httpSeed: base, RequestID: reqID, SecondRequestID: req2ID, RepoA: repoA, RepoB: repoB}
}

type artifactSeed struct {
	httpSeed
	ArtifactID, SectionID, NewText, OtherSectionText string
}

// newArtifactServer seeds one artifact at head revision 2, revision 1 holding
// different text, so ?revision=1 proves it serves the snapshot (Task 33).
func newArtifactServer(t *testing.T) (*runtimeEnv, artifactSeed) {
	t.Helper()
	e, base := newRuntimeServer(t)
	ctx := bg
	itemID := itemIDByKey(t, e, base.RootKey)
	artID := ids.New("art")
	now := db.Millis(time.Now())
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_at)
		VALUES (?, ?, 'spec', 'docs/specs/x.md', 2, ?)`, artID, itemID, now); err != nil {
		t.Fatal(err)
	}
	const oldOverview, oldOther = "OLDTEXT overview.\n", "OTHERTEXT-OLD detail.\n"
	oldText := oldOverview + oldOther
	oldSections := fmt.Sprintf(`[{"id":"overview","heading":"Overview","sha256":"r1o","start":0,"end":%d},
		{"id":"other","heading":"Other","sha256":"r1x","start":%d,"end":%d}]`, len(oldOverview), len(oldOverview), len(oldText))
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO artifact_revisions (artifact_id, revision, sha256, content, sections_json, created_at)
		VALUES (?, 1, 'r1', ?, ?, ?)`, artID, oldText, oldSections, now); err != nil {
		t.Fatal(err)
	}
	const newOverview, newOther = "NEWTEXT overview.\n", "OTHERTEXT detail.\n"
	newText := newOverview + newOther
	newSections := fmt.Sprintf(`[{"id":"overview","heading":"Overview","sha256":"r2o","start":0,"end":%d},
		{"id":"other","heading":"Other","sha256":"r2x","start":%d,"end":%d}]`, len(newOverview), len(newOverview), len(newText))
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO artifact_revisions (artifact_id, revision, sha256, content, sections_json, created_at)
		VALUES (?, 2, 'r2', ?, ?, ?)`, artID, newText, newSections, now); err != nil {
		t.Fatal(err)
	}
	return e, artifactSeed{httpSeed: base, ArtifactID: artID, SectionID: "overview",
		NewText: "NEWTEXT", OtherSectionText: "OTHERTEXT detail"}
}

type notificationSeed struct {
	httpSeed
	FirstID string
}

// newNotificationServer seeds n unread notifications, oldest first.
func newNotificationServer(t *testing.T, n int) (*runtimeEnv, notificationSeed) {
	t.Helper()
	e, base := newRuntimeServer(t)
	base_now := time.Now()
	var first string
	for i := 0; i < n; i++ {
		id := ids.New("ntf")
		if i == 0 {
			first = id
		}
		ms := db.Millis(base_now.Add(time.Duration(i) * time.Second))
		if _, err := e.s.DB.ExecContext(bg, `INSERT INTO notifications (id, level, kind, title, body, dedup_key, created_at)
			VALUES (?, 'info', 'test', 'Title', 'Body', ?, ?)`, id, id, ms); err != nil {
			t.Fatal(err)
		}
	}
	return e, notificationSeed{httpSeed: base, FirstID: first}
}

// newUsageServer wires one "fake" usage source and seeds a stale snapshot so
// the test's own manual refresh is the first one within the 60 s gate.
func newUsageServer(t *testing.T) (*runtimeEnv, httpSeed) {
	t.Helper()
	e, seed := newRuntimeServerWith(t, func(d *Deps) {
		d.Usage.Sources = []usage.Source{{Agent: runtime.Fake, Fetch: func(context.Context) ([]usage.Meter, string, error) {
			return []usage.Meter{{ID: "m1", Label: "5h", Window: "5h", UsedPct: 10}}, "m1", nil
		}}}
	})
	old := db.Millis(time.Now().Add(-2 * time.Hour))
	if _, err := e.s.DB.ExecContext(bg, `INSERT INTO usage_snapshots (agent_kind, meters_json, headline_id, source, fetched_at, attempted_at)
		VALUES ('fake', '[]', 'm1', 'test', ?, ?)`, old, old); err != nil {
		t.Fatal(err)
	}
	return e, seed
}

type adviceSeed struct {
	httpSeed
	AgentName string
}

// ---- added by Task 34: hook, mcp and wake-stream fixtures ----

// agentIOSeed carries the tokens the agent-facing routes need.
type agentIOSeed struct {
	httpSeed
	OldGenerationToken string // a token from a superseded generation: every route must 401 it
	TranscriptPath     string
}

// newAgentIOServer is newRuntimeServer plus a second, superseded generation on
// the base orchestrator (so the fail-closed token check has something to
// reject) and one pending message on it (so SessionStart's hook has a notice
// to report, matching §11.2's decision table).
func newAgentIOServer(t *testing.T) (*runtimeEnv, agentIOSeed) {
	t.Helper()
	e, base := newRuntimeServer(t)
	ctx := bg
	agentID := agentIDByName(t, e, base.AgentName)
	rootItemID := itemIDByKey(t, e, base.RootKey)
	_, oldToken := seedSession(t, e, agentID, base.AgentName, 0, "running")

	var seq int64
	if err := e.s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM messages`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	now := db.Millis(time.Now())
	if _, err := e.s.DB.ExecContext(ctx, `INSERT INTO messages
		(id, seq, kind, wake_class, priority, origin, to_agent_id, root_item_id, payload_json, state, created_at)
		VALUES (?, ?, 'assignment', 'immediate', 1, 'daemon', ?, ?, '{}', 'pending', ?)`,
		ids.New("msg"), seq, agentID, rootItemID, now); err != nil {
		t.Fatal(err)
	}
	return e, agentIOSeed{httpSeed: base, OldGenerationToken: oldToken}
}

// newAgentIOServerWithAdvisorTranscript points a copy of
// internal/advisor/testdata/claude/transcript-advisor-entries.jsonl (a real
// P0-14 fixture: five transcript lines that dedupe to one native advisor
// call) at TranscriptPath, in t.TempDir().
func newAgentIOServerWithAdvisorTranscript(t *testing.T) (*runtimeEnv, agentIOSeed) {
	t.Helper()
	e, seed := newAgentIOServer(t)
	data, err := os.ReadFile(filepath.Join("..", "advisor", "testdata", "claude", "transcript-advisor-entries.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	seed.TranscriptPath = dst
	return e, seed
}

// newAdviceServer seeds one answered advice row for the base tree's orchestrator.
func newAdviceServer(t *testing.T) (*runtimeEnv, adviceSeed) {
	t.Helper()
	e, base := newRuntimeServer(t)
	itemID := itemIDByKey(t, e, base.RootKey)
	now := db.Millis(time.Now())
	if _, err := e.s.DB.ExecContext(bg, `INSERT INTO advice
		(id, session_id, item_id, advisor_kind, advisor_model, question, state, mode, answer, created_at, finished_at)
		VALUES (?, ?, ?, 'fake', 'fake-1', 'Question?', 'answered', 'simulated', 'Answer.', ?, ?)`,
		ids.New("adv"), base.SessionID, itemID, now, now); err != nil {
		t.Fatal(err)
	}
	return e, adviceSeed{httpSeed: base, AgentName: base.AgentName}
}
