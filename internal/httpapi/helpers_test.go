package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
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
		Settings: st, Catalog: cat, KB: idx, WriteTimeout: 200 * time.Millisecond}
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
