package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/advisor"
	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// ---------- copied from internal/runtime's own test fixtures (I3 decision:
// copy, do not extract into a shared runtimetest package for one caller) ----------

// fakeTmux records every call and serves scripted captures. Copied from
// internal/runtime/agents_test.go.
type fakeTmux struct {
	started      []string
	env          map[string]map[string]string
	captures     map[string][]string
	pasted, keys []string
	killed       []string
	panes        []runtime.Pane
	n            map[string]int
	clk          *testClock
}

func newFakeTmux() *fakeTmux {
	return &fakeTmux{env: map[string]map[string]string{}, captures: map[string][]string{}, n: map[string]int{}}
}
func (f *fakeTmux) Start(ctx context.Context, name, cwd string, env map[string]string, argv []string) error {
	f.started = append(f.started, name+"|"+cwd+"|"+strings.Join(argv, " "))
	f.env[name] = env
	return nil
}
func (f *fakeTmux) Panes(context.Context) ([]runtime.Pane, error) { return f.panes, nil }
func (f *fakeTmux) Capture(ctx context.Context, name string, lines int) (string, error) {
	seq := f.captures[name]
	if len(seq) == 0 {
		return "─────\n❯ \n─────\n", nil
	}
	i := f.n[name]
	if i >= len(seq) {
		i = len(seq) - 1
	}
	f.n[name] = i + 1
	return seq[i], nil
}
func (f *fakeTmux) PasteLine(ctx context.Context, name, line string) error {
	f.pasted = append(f.pasted, name+"|"+line)
	return nil
}
func (f *fakeTmux) Keys(ctx context.Context, name string, keys ...string) error {
	f.keys = append(f.keys, name+"|"+strings.Join(keys, ","))
	return nil
}
func (f *fakeTmux) Env(ctx context.Context, name, key string) (string, error) {
	return f.env[name][key], nil
}
func (f *fakeTmux) Kill(ctx context.Context, name string) error {
	f.killed = append(f.killed, name)
	return nil
}

// testClock is the one clock every runtime test shares; copied from
// internal/runtime/agents_test.go.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
}
func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Millisecond)
	return c.t
}
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
func (c *testClock) After(d time.Duration) <-chan time.Time {
	c.Advance(d)
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

// fakeNotifier records what the runtime raised; copied from
// internal/runtime/agents_test.go.
type fakeNotifier struct {
	mu     sync.Mutex
	raised []runtime.NotifyInput
}

func (f *fakeNotifier) Raise(ctx context.Context, tx *sql.Tx, n runtime.NotifyInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raised = append(f.raised, n)
	return nil
}

func seedFakeCatalog(t *testing.T, d *db.DB) {
	t.Helper()
	_, err := d.ExecContext(context.Background(), `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('fake','fake-1','[{"id":"fake-1","label":"Fake 1","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":false}]','fake-1','test',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
}

// fakeEmbedder implements kb.Embedder without ever touching Ollama (hard
// safety rule): Check always succeeds and Embed returns one constant vector
// per text, which is enough for kb.Index.Search to run its full ranking path
// in a test.
type fakeEmbedder struct{}

func (fakeEmbedder) Model() string               { return "fake" }
func (fakeEmbedder) Check(context.Context) error { return nil }
func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0, 0}
	}
	return out, nil
}

// ---------- mcpserver's own fixtures ----------

// newTestServer wires a Server on a migrated temp DB with the fake adapter and
// tmux, a kb.Index over a temp directory, and an advisor.Service whose Run
// never exec's anything. enabled_agents is ["fake"] only, so Spawn's Kind
// default resolves to something with an adapter and "codex" stays disabled
// for the tests that need a disabled agent refused.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	d := dbtest.Open(t)
	home := t.TempDir()
	clk := newTestClock()
	ev := events.New(d, clk.Now)
	it := &items.Store{DB: d, Events: ev, Now: clk.Now}
	cat := &catalog.Service{DB: d, Events: ev, Now: clk.Now, Log: func(string, ...any) {}}
	seedFakeCatalog(t, d)
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO settings (key, value_json, updated_at) VALUES ('enabled_agents', '["fake"]', 1)`); err != nil {
		t.Fatal(err)
	}
	st := &settings.Store{DB: d, Events: ev, Now: clk.Now, ModelsFor: cat.ModelsFor,
		Installed: func(context.Context) []runtime.AgentKind { return []runtime.AgentKind{runtime.Fake} }}
	fa := adapter.NewFake(adapter.Deps{Home: home, UserHome: t.TempDir(),
		Bin: "/usr/local/bin/swarm", Run: execx.Run, Log: func(string, ...any) {}})
	tm := newFakeTmux()
	tm.clk = clk
	rt := &runtime.Store{DB: d, Events: ev, Items: it, Settings: st, Catalog: cat, Home: home,
		Now: clk.Now, Log: func(string, ...any) {}, Tmux: tm,
		Worktree:  &worktree.Service{DB: d, Run: execx.Run, Now: clk.Now, Log: func(string, ...any) {}},
		Notify:    &fakeNotifier{},
		Adapters:  map[runtime.AgentKind]adapter.Adapter{runtime.Fake: fa},
		Bin:       "/usr/local/bin/swarm",
		DaemonURL: "http://127.0.0.1:17778",
		OSEnv:     func(k string) string { return map[string]string{"USER": "u", "HOME": home, "PATH": "/usr/bin"}[k] },
		BaseEnv: func(getenv func(string) string) map[string]string {
			return map[string]string{"USER": getenv("USER"), "HOME": getenv("HOME"), "PATH": getenv("PATH")}
		},
		After: clk.After,
		Go:    func(f func()) { f() },
	}
	kbIndex := &kb.Index{DB: d, Dir: t.TempDir(), Emb: fakeEmbedder{}, Now: clk.Now}
	adv := &advisor.Service{DB: d, Events: ev, Home: t.TempDir(), UserHome: t.TempDir(),
		Adapters: rt.Adapters, MaxConcurrent: 2, Timeout: 5 * time.Minute, Now: clk.Now,
		Log: func(string, ...any) {},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"result":"advice"}`), nil
		}}
	return &Server{RT: rt, KB: kbIndex, Advisor: adv, Version: "test", Log: func(string, ...any) {}}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// call runs one tool for c through the same gate (dispatch, hence
// PauseAllowed) that Server.MCPServer wires into the SDK, without going
// through the SDK itself.
func (s *Server) call(ctx context.Context, c Caller, name string, args string) (any, error) {
	for _, d := range s.ToolsFor(c) {
		if d.Name == name {
			return s.dispatch(ctx, c, d, json.RawMessage(args))
		}
	}
	for _, d := range s.Tools() {
		if d.Name == name {
			return nil, &notVisibleError{name}
		}
	}
	return nil, &notVisibleError{name}
}

type notVisibleError struct{ name string }

func (e *notVisibleError) Error() string { return e.name + ": not available to this caller" }

// seedAgentAndSession inserts one items row (task, in_progress), one agents
// row and one running session, all with raw SQL (runtime's own seeds are
// unexported _test.go helpers and unreachable from this package).
func seedAgentAndSession(t *testing.T, s *Server, role runtime.Role, parentItemID, rootItemID string) (agentID, sessionID, itemID string) {
	t.Helper()
	ctx := context.Background()
	itemID = ids.New("itm")
	if rootItemID == "" {
		rootItemID = itemID
	}
	agentID = ids.New("agt")
	sessionID = ids.New("ses")
	now := db.Millis(s.RT.Now())
	var parent any
	if parentItemID != "" {
		parent = parentItemID
	}
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO items
		(id, key, type, parent_id, root_id, title, status, created_at, updated_at)
		VALUES (?, ?, 'task', ?, ?, 'Seeded task', 'in_progress', ?, ?)`,
		itemID, "TASK-"+itemID[len(itemID)-6:], parent, rootItemID, now, now); err != nil {
		t.Fatal(err)
	}
	name := "agent-" + agentID[len(agentID)-6:]
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO agents
		(id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES (?, ?, 'fake', 'fake-1', ?, ?, ?, 'brief', 'active', ?)`,
		agentID, name, string(role), itemID, rootItemID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO sessions
		(id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES (?, ?, 1, 1, ?, ?, ?, 'running', 'neutral', ?)`,
		sessionID, agentID, sessionID, name, filepath.Join(s.RT.Home, "work", name), now); err != nil {
		t.Fatal(err)
	}
	return agentID, sessionID, itemID
}

// serverSeed is one bound session + agent, plus what TestHandlerAuth needs to
// probe token resolution.
type serverSeed struct {
	Caller       Caller
	Resolve      func(*http.Request) (Caller, bool)
	RevokedToken string
	AgentID      string
}

// newServerWithSession seeds one running coder session on a fresh task item
// and returns a resolver good enough for the auth tests: it looks the token up
// by session and refuses one whose generation is no longer the agent's latest
// (a "revoked" token, without needing a real Pause/Resume cycle).
func newServerWithSession(t *testing.T) (*Server, serverSeed) {
	t.Helper()
	s := newTestServer(t)
	agentID, sessionID, _ := seedAgentAndSession(t, s, runtime.RoleCoder, "", "")
	ctx := context.Background()

	// token_hash is opaque to this fixture (production hashes the real
	// bearer token; this test skips that and stores the token itself), so
	// the session id doubles as its own "token" here.
	revokedToken := sessionID
	if _, err := s.RT.DB.ExecContext(ctx, `UPDATE sessions SET token_hash = ? WHERE id = ?`, revokedToken, sessionID); err != nil {
		t.Fatal(err)
	}
	// Simulate a resume: a second, newer-generation session for the same
	// agent, with its own token. The first session's token must then resolve
	// to nothing, because it is no longer the agent's latest generation.
	newSessionID := ids.New("ses")
	now := db.Millis(s.RT.Now())
	var agentName string
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, agentID).Scan(&agentName); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO sessions
		(id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES (?, ?, 1, 2, ?, ?, ?, 'running', 'neutral', ?)`,
		newSessionID, agentID, newSessionID, agentName, filepath.Join(s.RT.Home, "work", agentName), now); err != nil {
		t.Fatal(err)
	}

	var role string
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT role FROM agents WHERE id = ?`, agentID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	caller := Caller{SessionID: newSessionID, AgentID: agentID, AgentName: agentName, Role: runtime.Role(role)}

	// resolve is a test stand-in for the daemon's real token resolver
	// (cmd/swarm, outside this batch): it looks a session up by its
	// token_hash and refuses one whose generation is no longer the agent's
	// latest — a "revoked" token, without needing a real Pause/Resume cycle.
	resolve := func(r *http.Request) (Caller, bool) {
		auth := r.Header.Get("Authorization")
		tok, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || tok == "" {
			return Caller{}, false
		}
		var sesID, aID string
		if err := s.RT.DB.QueryRowContext(r.Context(),
			`SELECT id, agent_id FROM sessions WHERE token_hash = ?`, tok).Scan(&sesID, &aID); err != nil {
			return Caller{}, false
		}
		var latest string
		if err := s.RT.DB.QueryRowContext(r.Context(),
			`SELECT id FROM sessions WHERE agent_id = ? ORDER BY generation DESC LIMIT 1`, aID).Scan(&latest); err != nil {
			return Caller{}, false
		}
		if latest != sesID {
			return Caller{}, false // a stale generation's token is revoked
		}
		var name, rl string
		if err := s.RT.DB.QueryRowContext(r.Context(), `SELECT name, role FROM agents WHERE id = ?`, aID).Scan(&name, &rl); err != nil {
			return Caller{}, false
		}
		return Caller{SessionID: sesID, AgentID: aID, AgentName: name, Role: runtime.Role(rl)}, true
	}

	return s, serverSeed{Caller: caller, Resolve: resolve, RevokedToken: revokedToken, AgentID: agentID}
}

// seedOtherAgent seeds a second, unrelated agent+session under its own root
// item and returns its agent id.
func seedOtherAgent(t *testing.T, s *Server) string {
	t.Helper()
	id, _, _ := seedAgentAndSession(t, s, runtime.RoleCoder, "", "")
	return id
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---------- git fixtures ----------

// gitRepoWithCommit makes a temp repo with one unsigned commit on main. It
// deliberately turns commit.gpgsign off (like internal/runtime's
// gitRepoNoSigning): none of the worktree operations under test commit
// anything, so a repo that would fail Preflight's signing check is fine here
// — swarm_worktree does not re-run SigningOK (§11.4 step 7 already runs it at
// spawn time; no test in this batch exercises a repo confirmed after spawn
// with signing off, so re-checking it here is not exercised either).
func gitRepoWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "T"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func headSHA(t *testing.T, repoPath string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// seedRepo inserts a repos row pointing at a temp git repo and returns its id.
func seedRepo(t *testing.T, s *Server, name string) string {
	t.Helper()
	ctx := context.Background()
	id, now := ids.New("repo"), db.Millis(s.RT.Now())
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO repos
		(id, path, name, default_branch, source, created_at, updated_at)
		VALUES (?, ?, ?, 'main', 'manual', ?, ?)`,
		id, gitRepoWithCommit(t), name, now, now); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedRepoIn is a repos row deliberately not in any root's confirmed set.
func seedRepoIn(t *testing.T, s *Server, name string) string { return seedRepo(t, s, name) }

// ---------- orchestrator fixtures (Task 29) ----------

type orchSeed struct {
	Caller                                   Caller
	RootKey, StoryKey, TaskKey, OtherTaskKey string
	RepoID, RepoPath                         string
}

// newOrchestratorServer builds newTestServer(t) plus one seeded repo
// (confirmed on the root), an EPIC with one story and two tasks, and a
// running orchestrator session on the epic.
func newOrchestratorServer(t *testing.T) (*Server, orchSeed) {
	t.Helper()
	s := newTestServer(t)
	ctx := context.Background()
	repoPath := gitRepoWithCommit(t)
	repoID, now := ids.New("repo"), db.Millis(s.RT.Now())
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO repos
		(id, path, name, default_branch, source, created_at, updated_at)
		VALUES (?, ?, 'proj', 'main', 'manual', ?, ?)`, repoID, repoPath, now, now); err != nil {
		t.Fatal(err)
	}
	epic, err := s.RT.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Epic", Repos: []string{repoID}},
		items.User("cli"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.RT.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: epic.Key, Title: "Story"},
		items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	task1, err := s.RT.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: "Task one"},
		items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	task2, err := s.RT.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: "Task two"},
		items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	orch, _, err := s.RT.StartOrchestrator(ctx, runtime.OrchestratorInput{ItemKey: epic.Key, Kind: runtime.Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.RT.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	caller := Caller{SessionID: ses.ID, AgentID: orch.ID, AgentName: orch.Name, Role: runtime.RoleOrchestrator}
	return s, orchSeed{Caller: caller, RootKey: epic.Key, StoryKey: story.Key, TaskKey: task1.Key,
		OtherTaskKey: task2.Key, RepoID: repoID, RepoPath: repoPath}
}

func blockOn(t *testing.T, s *Server, key, blockedBy string) {
	t.Helper()
	if err := s.RT.Items.AddDep(context.Background(), key, blockedBy, items.Daemon()); err != nil {
		t.Fatal(err)
	}
}

func confirmRepo(t *testing.T, s *Server, rootKey, repoID string) {
	t.Helper()
	ctx := context.Background()
	it, err := s.RT.Items.Get(ctx, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	next := append(append([]string{}, it.Repos...), repoID)
	b, _ := json.Marshal(next)
	if _, err := s.RT.DB.ExecContext(ctx, `UPDATE items SET confirmed_repos_json = ? WHERE key = ?`, string(b), rootKey); err != nil {
		t.Fatal(err)
	}
}

// seedOtherOrchestrator seeds a second epic with its own orchestrator session.
func seedOtherOrchestrator(t *testing.T, s *Server) Caller {
	t.Helper()
	ctx := context.Background()
	epic, err := s.RT.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Other epic"}, items.User("cli"))
	if err != nil {
		t.Fatal(err)
	}
	orch, _, err := s.RT.StartOrchestrator(ctx, runtime.OrchestratorInput{ItemKey: epic.Key, Kind: runtime.Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.RT.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	return Caller{SessionID: ses.ID, AgentID: orch.ID, AgentName: orch.Name, Role: runtime.RoleOrchestrator}
}

func seedOtherRoot(t *testing.T, s *Server) string {
	t.Helper()
	epic, err := s.RT.Items.Create(context.Background(), items.CreateInput{Type: items.Epic, Title: "Unrelated"}, items.User("cli"))
	if err != nil {
		t.Fatal(err)
	}
	return epic.Key
}

// spawnWorker spawns a coder on seed.TaskKey under seed's orchestrator.
func spawnWorker(t *testing.T, s *Server, seed orchSeed) runtime.Agent {
	t.Helper()
	a, _, err := s.RT.Spawn(context.Background(), runtime.SpawnInput{
		ItemKey: seed.TaskKey, Role: runtime.RoleCoder, ParentAgentID: seed.Caller.AgentID,
		Brief: runtime.BriefInput{Objective: "Do the task."},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// itemKeyForAgent resolves the item key an agent (by name) belongs to.
func itemKeyForAgent(t *testing.T, s *Server, agentName string) string {
	t.Helper()
	a, err := s.RT.Agent(context.Background(), agentName)
	if err != nil {
		t.Fatal(err)
	}
	var key string
	if err := s.RT.DB.QueryRowContext(context.Background(), `SELECT key FROM items WHERE id = ?`, a.ItemID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

// seedOtherWorkerName spawns a worker belonging to a different orchestrator's
// subtree, for the "only your own subtree" checks.
func seedOtherWorkerName(t *testing.T, s *Server) string {
	t.Helper()
	ctx := context.Background()
	other := seedOtherOrchestrator(t, s)
	epicKey := itemKeyForAgent(t, s, other.AgentName)
	story, err := s.RT.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: epicKey, Title: "S"}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.RT.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: "T"}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.RT.Spawn(ctx, runtime.SpawnInput{ItemKey: task.Key, Role: runtime.RoleCoder,
		ParentAgentID: other.AgentID, Brief: runtime.BriefInput{Objective: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	return a.Name
}
