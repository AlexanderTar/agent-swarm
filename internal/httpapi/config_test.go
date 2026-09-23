package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

func TestSettingsRoutes(t *testing.T) {
	e := newEnv(t)
	status, b := e.api("GET", "/api/settings", nil)
	cur := decode[settings.Settings](t, b)
	if status != 200 || cur.MaxConcurrentAgents != 4 || cur.Roles[runtime.RoleCoder].Model != "sonnet" {
		t.Fatalf("GET = %d %s", status, b)
	}
	before, _ := e.events.Latest(bg)
	cur.MaxConcurrentAgents = 10
	cur.Roles[runtime.RoleCoder] = settings.RoleDefault{Agent: runtime.Claude, Model: "opus", Effort: "xhigh"}
	status, b = e.api("PUT", "/api/settings", cur)
	saved := decode[settings.Settings](t, b)
	if status != 200 || saved.MaxConcurrentAgents != 10 || saved.Roles[runtime.RoleCoder].Effort != "xhigh" {
		t.Fatalf("PUT = %d %s", status, b)
	}
	evs, _ := e.events.After(bg, before, 10)
	if len(evs) != 1 || evs[0].Type != events.SettingsChanged {
		t.Fatalf("events = %+v", evs)
	}

	bad := func(edit func(*settings.Settings), msg string) {
		t.Helper()
		c := decode[settings.Settings](t, b) // fresh copy of the saved settings
		edit(&c)
		status, body := e.api("PUT", "/api/settings", c)
		wantErr(t, status, body, 400, "bad_request", msg)
	}
	bad(func(c *settings.Settings) {
		c.Roles[runtime.RoleCoder] = settings.RoleDefault{Agent: runtime.Codex, Model: "gpt-6-astra"}
	}, "Codex isn't enabled. Choose an enabled agent.")
	bad(func(c *settings.Settings) {
		c.Roles[runtime.RoleCoder] = settings.RoleDefault{Agent: runtime.Claude, Model: "claude-9"}
	}, "Choose a model available for this agent.")
	bad(func(c *settings.Settings) {
		c.Roles[runtime.RoleMechanical] = settings.RoleDefault{Agent: runtime.Claude, Model: "haiku", Effort: "low"}
	}, "low isn't available for Haiku (latest).")
	bad(func(c *settings.Settings) { c.MaxAgentsPerRoot = 0 }, "Maximum concurrent agents per item must be between 1 and 16.")
	bad(func(c *settings.Settings) { c.EnabledAgents = nil }, "At least one agent must stay enabled.")
	status, body := e.api("PUT", "/api/settings", "nope")
	wantErr(t, status, body, 400, "bad_request", "Invalid JSON body.")
}

func TestExcludesChangeTriggersRescan(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	e.repos.After = func(time.Duration) <-chan time.Time { return nil } // only triggers wake the loop
	e.repos.Excludes = func(ctx context.Context) []string {
		s, _ := e.s.Settings.Get(ctx)
		return s.ScanExcludes
	}
	go e.repos.Loop(ctx, func(context.Context) time.Duration { return time.Hour })
	waitUntil(t, func() bool { return len(repoNames(t, e, "")) == 3 })

	status, b := e.api("GET", "/api/settings", nil)
	cur := decode[settings.Settings](t, b)
	cur.ScanExcludes = append(cur.ScanExcludes, "~/GitHub/tools")
	os.MkdirAll(filepath.Join(e.home, "Later/extra/.git"), 0o755)
	if status, b = e.api("PUT", "/api/settings", cur); status != 200 {
		t.Fatalf("PUT = %d %s", status, b)
	}
	waitUntil(t, func() bool { return slices.Contains(repoNames(t, e, ""), "extra") })
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type reposBody struct {
	Recent    []repos.Repo      `json:"recent"`
	Groups    []repos.GroupView `json:"groups"`
	All       []repos.Repo      `json:"all"`
	ScannedAt int64             `json:"scanned_at"`
	Scanning  bool              `json:"scanning"`
}

func repoNames(t *testing.T, e *env, q string) []string {
	t.Helper()
	_, b := e.api("GET", "/api/repos?q="+q, nil)
	var out []string
	for _, r := range decode[reposBody](t, b).All {
		out = append(out, r.Name)
	}
	return out
}

func TestReposRoutes(t *testing.T) {
	e := newEnv(t)
	_, b := e.api("GET", "/api/repos", nil)
	if body := decode[reposBody](t, b); body.ScannedAt != 0 || len(body.All) != 0 || body.Recent == nil || body.Groups == nil {
		t.Fatalf("before scan = %s", b)
	}
	status, b := e.api("POST", "/api/repos/rescan", nil)
	if stats := decode[repos.ScanStats](t, b); status != 200 || stats.Found != 3 || stats.Missing != 0 {
		t.Fatalf("rescan = %d %s", status, b)
	}
	_, b = e.api("GET", "/api/repos", nil)
	body := decode[reposBody](t, b)
	if len(body.All) != 3 || body.ScannedAt == 0 || body.Scanning {
		t.Fatalf("after scan = %s", b)
	}
	if len(body.Groups) != 1 || body.Groups[0].Name != "proj" || len(body.Groups[0].Repos) != 3 {
		t.Fatalf("same-named groups must merge: %+v", body.Groups)
	}
	for _, want := range []string{`"remote_url":null`, `"remote_owner":null`, `"default_branch":null`, `"last_used_at":null`, `"source":"scan"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("repo wire lacks %s: %s", want, b)
		}
	}
	e.repos.MarkUsed(bg, body.All[0].ID) // app
	_, b = e.api("GET", "/api/repos?q=web", nil)
	body = decode[reposBody](t, b)
	if len(body.All) != 1 || body.All[0].Name != "web" || len(body.Recent) != 0 ||
		len(body.Groups) != 1 || len(body.Groups[0].Repos) != 1 {
		t.Fatalf("q=web = %s", b)
	}
	_, b = e.api("GET", "/api/repos?q=app", nil)
	if body := decode[reposBody](t, b); len(body.Recent) != 1 || body.Recent[0].Name != "app" {
		t.Fatalf("recent = %s", b)
	}
	_, b = e.api("GET", "/api/repos?q=zzz", nil)
	if body := decode[reposBody](t, b); len(body.All) != 0 || len(body.Groups) != 0 || body.All == nil {
		t.Fatalf("no match = %s", b)
	}

	hidden := filepath.Join(e.home, ".private/secret")
	os.MkdirAll(filepath.Join(hidden, ".git"), 0o755)
	status, b = e.api("POST", "/api/repos", map[string]string{"path": hidden})
	if r := decode[repos.Repo](t, b); status != 201 || r.Source != "manual" || r.Name != "secret" {
		t.Fatalf("add = %d %s", status, b)
	}
	os.MkdirAll(filepath.Join(e.home, "Later/tilde/.git"), 0o755)
	status, b = e.api("POST", "/api/repos", map[string]string{"path": "~/Later/tilde"})
	if r := decode[repos.Repo](t, b); status != 201 || r.Path != filepath.Join(e.home, "Later/tilde") {
		t.Fatalf("add ~ = %d %s", status, b)
	}
	status, b = e.api("POST", "/api/repos", map[string]string{"path": "GitHub/app"})
	wantErr(t, status, b, 422, "bad_request", "Use an absolute path.")
	for _, p := range []string{filepath.Join(e.home, "GitHub"), "", "~/GitHub"} {
		status, b = e.api("POST", "/api/repos", map[string]string{"path": p})
		wantErr(t, status, b, 422, "bad_request", "No git repository found in this folder.")
	}
}

func TestCatalogRoutes(t *testing.T) {
	e := newEnv(t)
	status, b := e.api("GET", "/api/catalog", nil)
	type entry struct {
		Kind             runtime.AgentKind      `json:"kind"`
		Models           []catalog.CatalogModel `json:"models"`
		CatalogFetchedAt int64                  `json:"catalog_fetched_at"` // integer ms (contracts §1)
	}
	entries := decode[[]entry](t, b)
	if status != 200 || len(entries) != 1 || entries[0].Kind != runtime.Claude || len(entries[0].Models) != 4 ||
		entries[0].CatalogFetchedAt < 1e12 || !strings.Contains(string(b), `"auth_error":""`) ||
		!strings.Contains(string(b), `"catalog_error":""`) {
		t.Fatalf("catalog = %d %s", status, b)
	}
	before := e.fetcher.fetches.Load()
	status, b = e.api("POST", "/api/catalog/refresh", nil)
	if status != 200 || e.fetcher.fetches.Load() != before+1 || !strings.Contains(string(b), `"catalog_source":"stub"`) {
		t.Fatalf("refresh = %d %s (fetches %d)", status, b, e.fetcher.fetches.Load())
	}
}

func TestKBRoutes(t *testing.T) {
	e := newEnv(t)
	status, b := e.api("GET", "/api/kb/search?q=kanban&limit=5", nil)
	hits := decode[[]kb.Hit](t, b)
	if status != 200 || len(hits) == 0 || hits[0].Slug != "notes/beta" {
		t.Fatalf("search = %d %s", status, b)
	}
	if _, b := e.api("GET", "/api/kb/search?q=", nil); string(b) != "[]\n" {
		t.Fatalf("empty query = %q", b)
	}
	for q, want := range map[string]int{"limit=0": 1, "limit=999": 2} {
		status, b := e.api("GET", "/api/kb/search?q=kanban+alpha&"+q, nil)
		if hits := decode[[]kb.Hit](t, b); status != 200 || len(hits) != want {
			t.Fatalf("%s = %d %s", q, status, b)
		}
	}
	status, b = e.api("GET", "/api/kb/search?q=x&limit=abc", nil)
	wantErr(t, status, b, 400, "bad_request", "limit must be a number.")

	status, b = e.api("GET", "/api/kb/status", nil)
	if st := decode[kb.Status](t, b); status != 200 || st.Docs != 2 || !st.Available {
		t.Fatalf("status = %d %s", status, b)
	}
	status, b = e.api("GET", "/api/kb/specs/alpha", nil)
	if d := decode[kb.DocView](t, b); status != 200 || d.Title != "Alpha spec" || !strings.HasPrefix(d.Markdown, "# Alpha") {
		t.Fatalf("get = %d %s", status, b)
	}
	if _, b := e.api("GET", "/api/kb/notes/beta", nil); !strings.Contains(string(b), `"frontmatter":{}`) {
		t.Fatalf("no-frontmatter doc = %s", b)
	}
	status, b = e.api("GET", "/api/kb/specs/missing", nil)
	wantErr(t, status, b, 404, "not_found", "No document specs/missing.")

	e.emb.down.Store(true)
	e.s.KB.Sync(bg) // marks search unavailable
	status, b = e.api("GET", "/api/kb/search?q=alpha", nil)
	wantErr(t, status, b, 503, "internal", "Search unavailable: run `ollama pull qwen3-embedding:0.6b`")

	e.s.DB.Close()
	status, b = e.api("GET", "/api/kb/specs/alpha", nil)
	wantErr(t, status, b, 500, "internal", "Something went wrong.")
}
