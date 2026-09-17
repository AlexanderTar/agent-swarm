package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	_ "modernc.org/sqlite"
)

type offlineEmb struct{}

func (offlineEmb) Model() string               { return "offline" }
func (offlineEmb) Check(context.Context) error { return kb.ErrUnavailable }
func (offlineEmb) Embed(context.Context, []string) ([][]float32, error) {
	return nil, kb.ErrUnavailable
}

func swarm(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestDispatch(t *testing.T) {
	if code, _, e := swarm(); code != 2 || !strings.Contains(e, "Usage: swarm <command>") {
		t.Fatalf("no args = %d %q", code, e)
	}
	if code, _, e := swarm("fly"); code != 2 || !strings.Contains(e, `unknown command "fly"`) {
		t.Fatalf("unknown = %d %q", code, e)
	}
	if code, out, _ := swarm("version"); code != 0 || out != "swarm dev\n" {
		t.Fatalf("version = %d %q", code, out)
	}
	if code, out, _ := swarm("help"); code != 0 || !strings.Contains(out, "kb search QUERY") {
		t.Fatalf("help = %d %q", code, out)
	}
	for _, cmd := range []string{"doctor", "status", "items", "repos", "daemon", "install"} {
		code, _, e := swarm(cmd, "--help")
		if code != 0 || !strings.Contains(e, "-home") {
			t.Errorf("%s --help = %d %q", cmd, code, e)
		}
	}
	if code, _, e := swarm("kb"); code != 2 || !strings.Contains(e, "kb search QUERY | kb status") {
		t.Fatalf("kb = %d %q", code, e)
	}
}

// startDaemon runs serve on a random port and returns its URL and home.
func startDaemon(t *testing.T) (url, home string, stop func()) {
	t.Helper()
	home = t.TempDir()
	scan, _ := filepath.EvalSymlinks(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, daemonConfig{Home: home, Port: 0, ScanRoot: scan, Embedder: offlineEmb{}, Log: t.Logf,
			Ready: func(addr string) { ready <- addr }})
	}()
	select {
	case addr := <-ready:
		url = "http://" + addr
	case err := <-done:
		t.Fatalf("serve: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not start")
	}
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve returned %v", err)
			}
		case <-time.After(7 * time.Second):
			t.Error("daemon did not stop")
		}
	}
	t.Cleanup(stop)
	return url, home, stop
}

func TestDaemonAndClientCommands(t *testing.T) {
	userHome, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", userHome) // `repos add ~/…` expands against this, never the real home
	url, home, stop := startDaemon(t)
	fi, err := os.Stat(filepath.Join(home, "run", "daemon.token"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", fi, err)
	}
	if fi, _ := os.Stat(filepath.Join(home, "run")); fi.Mode().Perm() != 0o700 {
		t.Fatalf("run dir mode = %v", fi.Mode().Perm())
	}
	token, _ := os.ReadFile(filepath.Join(home, "run", "daemon.token"))
	if len(strings.TrimSpace(string(token))) != 64 {
		t.Fatalf("token = %q", token)
	}
	resp, err := http.Get(url + "/api/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %v %v", resp, err)
	}
	resp.Body.Close()

	// flags go before positional arguments: Go's flag package stops at the first one
	cli := func(cmd string, rest ...string) (int, string, string) {
		return swarm(append([]string{cmd, "--home", home, "--url", url}, rest...)...)
	}
	code, out, _ := cli("items")
	if code != 0 || out != "No work items yet.\n" {
		t.Fatalf("items (empty) = %d %q", code, out)
	}
	for _, body := range []string{`{"type":"epic","title":"Authentication"}`, `{"type":"bug","title":"Login crash"}`} {
		req, _ := http.NewRequest("POST", url+"/api/items", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 201 {
			t.Fatalf("create: %v %v", resp, err)
		}
		resp.Body.Close()
	}
	code, out, _ = cli("items")
	if code != 0 || !strings.Contains(out, "KEY") || !strings.Contains(out, "EPIC-1") || !strings.Contains(out, "Draft") ||
		!strings.Contains(out, "Login crash") {
		t.Fatalf("items = %d %q", code, out)
	}
	code, out, _ = cli("items", "--type", "bug")
	if code != 0 || strings.Contains(out, "EPIC-1") || !strings.Contains(out, "BUG-1") {
		t.Fatalf("items --type bug = %q", out)
	}
	code, out, _ = cli("items", "-q", "nothing")
	if code != 0 || out != "No items match these filters.\n" {
		t.Fatalf("items -q = %q", out)
	}
	code, _, e := cli("items", "--status", "doing")
	if code != 1 || !strings.Contains(e, `Unknown status "doing".`) {
		t.Fatalf("bad status = %d %q", code, e)
	}

	code, out, _ = cli("status")
	for _, want := range []string{"Daemon: running at " + url + " (version dev, schema 1)", "Items: 2 (draft 2)",
		"Repositories: 0 (not scanned yet)", "Knowledge base: 0 documents, 0 chunks, 0 embedded",
		"Search unavailable: run `ollama pull qwen3-embedding:0.6b`"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if code != 0 {
		t.Fatalf("status = %d", code)
	}

	code, out, _ = cli("repos")
	if code != 0 || out != "No repositories found. Run swarm repos --rescan.\n" {
		t.Fatalf("repos (empty) = %d %q", code, out)
	}
	repo := filepath.Join(t.TempDir(), "demo")
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Fatal(err)
	}
	code, out, _ = cli("repos", "add", repo)
	if code != 0 || !strings.HasPrefix(out, "Added demo (") {
		t.Fatalf("repos add = %d %q", code, out)
	}
	code, _, e = cli("repos", "add", t.TempDir())
	if code != 1 || !strings.Contains(e, "No git repository found in this folder.") {
		t.Fatalf("repos add non-repo = %d %q", code, e)
	}
	// relative paths resolve against the CLI's cwd; ~ against the user's home
	parent := t.TempDir()
	if err := exec.Command("git", "init", "-q", filepath.Join(parent, "rel")).Run(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)
	code, out, e = cli("repos", "add", "rel")
	if code != 0 || !strings.HasPrefix(out, "Added rel (") {
		t.Fatalf("repos add relative = %d %q %q", code, out, e)
	}
	if err := exec.Command("git", "init", "-q", filepath.Join(userHome, "tilde")).Run(); err != nil {
		t.Fatal(err)
	}
	code, out, e = cli("repos", "add", "~/tilde")
	if code != 0 || out != "Added tilde (~/tilde).\n" {
		t.Fatalf("repos add ~ = %d %q %q", code, out, e)
	}
	code, out, _ = cli("repos", "--rescan")
	if code != 0 || !strings.Contains(out, "Found 0 repositories (0 missing).") || !strings.Contains(out, "demo") {
		t.Fatalf("repos --rescan = %d %q", code, out)
	}

	code, out, _ = cli("kb", "status")
	if code != 0 || !strings.Contains(out, "0 documents, 0 chunks, 0 embedded, 0 pending") {
		t.Fatalf("kb status = %d %q", code, out)
	}
	code, _, e = cli("kb", "search", "board", "views")
	if code != 1 || !strings.Contains(e, "Search unavailable") {
		t.Fatalf("kb search offline = %d %q", code, e)
	}

	stop()
	code, _, e = cli("status")
	if code != 1 || !strings.Contains(e, "The daemon isn't running at "+url+". Run swarm install.") {
		t.Fatalf("status when down = %d %q", code, e)
	}
	code, _, e = swarm("items", "--home", t.TempDir(), "--url", url)
	if code != 1 || !strings.Contains(e, "Can't read the daemon token") {
		t.Fatalf("missing token = %d %q", code, e)
	}
}

func TestTokenIsReusedAndLegacyDataRefused(t *testing.T) {
	_, home, stop := startDaemon(t)
	first, _ := os.ReadFile(filepath.Join(home, "run", "daemon.token"))
	stop()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, daemonConfig{Home: home, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf,
			Ready: func(a string) { ready <- a }})
	}()
	<-ready
	second, _ := os.ReadFile(filepath.Join(home, "run", "daemon.token"))
	cancel()
	<-done
	if string(first) != string(second) {
		t.Fatal("token must survive restarts")
	}

	legacy := t.TempDir()
	d, _ := sql.Open("sqlite", "file:"+filepath.Join(legacy, "swarm.db"))
	d.Exec(`CREATE TABLE schema_meta (version INTEGER)`)
	d.Close()
	err := serve(context.Background(), daemonConfig{Home: legacy, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err == nil || err.Error() != "Agent Swarm 1.x data found. Run `swarm migrate` first." {
		t.Fatalf("legacy = %v", err)
	}
	code, _, e := swarm("install", "--dry-run", "--home", legacy)
	if code != 1 || !strings.Contains(e, "Run `swarm migrate` first.") {
		t.Fatalf("install on legacy = %d %q", code, e)
	}
}

func TestTokenEmptyIsReplacedUnreadableFails(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "run", "daemon.token")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("\n"), 0o644)
	dm, err := openDaemon(context.Background(), daemonConfig{Home: home, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	dm.db.Close()
	b, _ := os.ReadFile(path)
	fi, _ := os.Stat(path)
	if len(strings.TrimSpace(string(b))) != 64 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token = %q mode %v", b, fi.Mode().Perm())
	}

	os.Chmod(path, 0o000)
	defer os.Chmod(path, 0o600)
	_, err = openDaemon(context.Background(), daemonConfig{Home: home, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err == nil || !strings.Contains(err.Error(), "Can't read the daemon token ("+path+")") {
		t.Fatalf("unreadable token = %v", err)
	}
}

func TestDaemonLoggersWired(t *testing.T) {
	var mu sync.Mutex
	var got []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, format)
	}
	dm, err := openDaemon(context.Background(), daemonConfig{Home: t.TempDir(), ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: logf})
	if err != nil {
		t.Fatal(err)
	}
	defer dm.db.Close()
	dm.api.Log("api")
	dm.cat.Log("catalog")
	dm.rp.Log("repos")
	if strings.Join(got, ",") != "api,catalog,repos" {
		t.Fatalf("logged %v", got)
	}
}

func TestPrune(t *testing.T) {
	d := dbtest.Open(t)
	now := time.Now()
	ev := events.New(d, func() time.Time { return now })
	for _, age := range []time.Duration{8 * 24 * time.Hour, time.Hour} {
		at := db.Millis(now.Add(-age))
		d.Exec(`INSERT INTO events (type, payload_json, created_at) VALUES ('x', '{}', ?)`, at)
		d.Exec(`INSERT INTO idempotency (caller, request_id, tool, result_json, created_at) VALUES ('c', ?, 't', '{}', ?)`,
			age.String(), at)
	}
	prune(context.Background(), ev, d, now, t.Errorf)
	for _, table := range []string{"events", "idempotency"} {
		var n int
		d.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
		if n != 1 {
			t.Errorf("%s rows = %d, want 1", table, n)
		}
	}
}

func TestSyncLoopPicksUpNewDocs(t *testing.T) {
	d := dbtest.Open(t)
	idx := &kb.Index{DB: d, Dir: t.TempDir(), Emb: offlineEmb{}, Now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { syncLoop(ctx, idx, 10*time.Millisecond, t.Errorf); close(done) }()
	defer func() { cancel(); <-done }()
	os.WriteFile(filepath.Join(idx.Dir, "note.md"), []byte("# Note\n\nBody.\n"), 0o644)
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := idx.Status(ctx)
		if err == nil && st.Docs == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v %v", st, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAbsPath(t *testing.T) {
	cwd, _ := os.Getwd()
	for in, want := range map[string]string{
		"~":        "/u",
		"~/x":      "/u/x",
		"/abs/./y": "/abs/y",
		"rel":      filepath.Join(cwd, "rel"),
	} {
		if got, err := absPath(in, "/u"); err != nil || got != want {
			t.Errorf("absPath(%q) = %q %v, want %q", in, got, err, want)
		}
	}
}

func TestClientSendsViaOnMutations(t *testing.T) {
	var via, auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		via = append(via, r.Method+" "+r.Header.Get("X-Swarm-Via"))
		auth = append(auth, r.Header.Get("Authorization"))
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "run"), 0o700)
	os.WriteFile(filepath.Join(home, "run", "daemon.token"), []byte("tok\n"), 0o600)
	c, err := newClient(home, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.do("POST", "/api/repos", map[string]string{"path": "/x"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.do("GET", "/api/repos", nil, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(via, ",") != "POST cli,GET " || auth[0] != "Bearer tok" {
		t.Fatalf("via = %v auth = %v", via, auth)
	}
}

func TestInstallDryRun(t *testing.T) {
	home := t.TempDir()
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USER", "tester")
	code, out, _ := swarm("install", "--dry-run", "--home", home)
	if code != 0 || !strings.Contains(out, "Would write "+filepath.Join(userHome, "Library", "LaunchAgents")) ||
		!strings.Contains(out, "<string>daemon</string>") ||
		!strings.Contains(out, "<key>SWARM_HOME</key><string>"+home+"</string>") ||
		!strings.Contains(out, "<key>USER</key><string>tester</string>") ||
		!strings.Contains(out, "<key>HOME</key><string>"+userHome+"</string>") ||
		!strings.Contains(out, "launchctl bootstrap gui/") {
		t.Fatalf("dry run = %d %q", code, out)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("dry run wrote into home: %v", entries)
	}
	if entries, _ := os.ReadDir(userHome); len(entries) != 0 {
		t.Fatalf("dry run wrote into user home: %v", entries)
	}
}

func TestNewDoctorSetsEveryField(t *testing.T) {
	d := newDoctor("/h", "http://127.0.0.1:1")
	v := reflect.ValueOf(d)
	for i := range v.NumField() {
		f, name := v.Field(i), v.Type().Field(i).Name
		switch f.Kind() {
		case reflect.Func, reflect.Pointer:
			if f.IsNil() {
				t.Errorf("%s is nil", name)
			}
		case reflect.Slice, reflect.String:
			if f.Len() == 0 {
				t.Errorf("%s is empty", name)
			}
		}
	}
	if d.HTTP != nil && d.HTTP.Timeout <= 0 {
		t.Error("doctor HTTP client has no timeout")
	}
}

func TestDoctorOutput(t *testing.T) {
	orig := doctorChecks
	defer func() { doctorChecks = orig }()
	checks := []install.Check{{Name: "tmux", OK: true, Detail: "tmux 3.7c"}, {Name: "Ollama", OK: true, Detail: "The embedding model is ready."}}
	doctorChecks = func(context.Context, install.Doctor) []install.Check { return checks }
	code, out, _ := swarm("doctor")
	if code != 0 || out != "✓ tmux: tmux 3.7c\n✓ Ollama: The embedding model is ready.\n" {
		t.Fatalf("doctor = %d %q", code, out)
	}
	checks = append(checks, install.Check{Name: "Daemon", Detail: "The daemon isn't running. Run swarm install."})
	code, out, _ = swarm("doctor", "--json")
	var got []install.Check
	if code != 1 || json.Unmarshal([]byte(out), &got) != nil || len(got) != 3 || got[2].OK {
		t.Fatalf("doctor --json = %d %q", code, out)
	}
}
