package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/migrate"
	_ "modernc.org/sqlite"
)

// TestMain is the C1 last-resort safety net: it points $HOME at a throwaway
// temp dir for the whole cmd/swarm test binary, so a test that opens a daemon
// (or runs any command touching os.UserHomeDir()) without its own explicit
// UserHome/HOME override still cannot reach the real operator's home. This
// already happened once on this machine (a daemon test relinked/recopied
// ~/.claude/skills and friends) -- individual tests still set
// daemonConfig.UserHome explicitly (belt and braces), this is the backstop
// for the one that forgets.
func TestMain(m *testing.M) {
	os.Exit(runCmdSwarmTests(m))
}

func runCmdSwarmTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "swarm-test-home")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	os.Setenv("HOME", dir)
	return m.Run()
}

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
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	home = t.TempDir()
	scan, _ := filepath.EvalSymlinks(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, daemonConfig{UserHome: t.TempDir(), Home: home, Port: 0, ScanRoot: scan, Embedder: offlineEmb{}, Log: t.Logf,
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
	for _, want := range []string{fmt.Sprintf("Daemon: running at %s (version dev, schema %d)", url, db.SchemaVersion), "Items: 2 (draft 2)",
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
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	_, home, stop := startDaemon(t)
	first, _ := os.ReadFile(filepath.Join(home, "run", "daemon.token"))
	stop()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, daemonConfig{UserHome: t.TempDir(), Home: home, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf,
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
	err := serve(context.Background(), daemonConfig{UserHome: t.TempDir(), Home: legacy, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err == nil || err.Error() != "Agent Swarm 1.x data found. Run `swarm migrate` first." {
		t.Fatalf("legacy = %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "run", "daemon.token")); !os.IsNotExist(err) {
		t.Fatalf("legacy home got a token: %v", err)
	}
	code, _, e := swarm("install", "--dry-run", "--home", legacy)
	if code != 1 || !strings.Contains(e, "Run `swarm migrate` first.") {
		t.Fatalf("install on legacy = %d %q", code, e)
	}
}

// A background loop that ignores cancellation must not hold shutdown past the grace period.
func TestServeBoundsWaitForBackgroundLoops(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	block := make(chan struct{})
	defer close(block)
	var mu sync.Mutex
	var logged []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, daemonConfig{UserHome: t.TempDir(), Home: t.TempDir(), ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: logf,
			grace: 200 * time.Millisecond, loops: []func(context.Context){func(context.Context) { <-block }},
			Ready: func(string) { close(ready) }})
	}()
	<-ready
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve waited for a stuck background loop")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 1 || logged[0] != "shutdown: background work still running after the grace period" {
		t.Fatalf("logged %q", logged)
	}
}

// An open SSE stream never goes idle, so shutdown must end it instead of waiting it out.
func TestServeShutsDownWithAnOpenEventStream(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	loopDone := make(chan struct{})
	go func() {
		done <- serve(ctx, daemonConfig{UserHome: t.TempDir(), Home: home, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf,
			grace: 2 * time.Second, Ready: func(a string) { ready <- a },
			loops: []func(context.Context){func(c context.Context) {
				<-c.Done()
				time.Sleep(100 * time.Millisecond) // work that must still finish before the db closes
				close(loopDone)
			}}})
	}()
	addr := <-ready
	tok, err := os.ReadFile(filepath.Join(home, "run", "daemon.token"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "http://"+addr+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	resp, err := (&http.Client{}).Do(req) // returns once the stream's first flush lands
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	start := time.Now()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("shutdown waited %v for an open SSE stream", d)
	}
	select {
	case <-loopDone:
	default:
		t.Fatal("the background loop lost its grace period")
	}
}

// A user who tightens ~/.swarm keeps it; only run/ (tokens) is forced to 0700.
func TestOpenDaemonKeepsTightHomePermissions(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	home := filepath.Join(t.TempDir(), "swarm")
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	dm, err := openDaemon(context.Background(), daemonConfig{UserHome: t.TempDir(), Home: home, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer dm.db.Close()
	for dir, want := range map[string]os.FileMode{home: 0o700, filepath.Join(home, "run"): 0o700} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", dir, fi.Mode().Perm(), want)
		}
	}
}

// Review round 1, Majors 1 & 3: openDaemon must sync skills into cfg.Home
// (not the real user's home, which would both break a custom --home/SWARM_HOME
// and leak into whatever machine happens to run the test), and the path is
// SkillsHome(cfg.Home) = cfg.Home/skills, not cfg.Home/.swarm/skills.
func TestOpenDaemonSyncsSkills(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	home := t.TempDir()
	dm, err := openDaemon(context.Background(), daemonConfig{UserHome: t.TempDir(), Home: home, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer dm.db.Close()
	if _, err := os.Stat(filepath.Join(home, "skills", "swarm", "SKILL.md")); err != nil {
		t.Errorf("openDaemon did not sync skills into cfg.Home: %v", err)
	}
}

// snapshotTree captures every file's mode+bytes and every symlink's target
// under root, keyed by path relative to root, so a test can assert a whole
// tree came out byte-for-byte untouched.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = fmt.Sprintf("symlink(%v) -> %s", fi.Mode().Perm(), target)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("file(%v) %s", fi.Mode().Perm(), body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// C1: a daemon opened with a temp/custom swarm Home must never reach into the
// real UserHome's own per-kind skill roots (~/.claude/skills, ...) -- only a
// daemon whose Home IS the canonical default home for that UserHome
// (filepath.Join(userHome, ".swarm")) may relink/repair them. This is the
// regression for the live-machine incident: a daemon test opened with a temp
// Home but the real UserHome relinked ~/.claude/skills into a now-deleted
// temp dir and overwrote ~/.codex/skills' content.
//
// Codex (Copy mode) is seeded with content that has drifted from what the
// binary embeds (simulating an install from an older build) and a *foreign*
// marker naming a different swarm home, then deliberately mutated again after
// WriteSkills -- this exercises the exact bug: the pre-fix ManagedMarker
// carries no record of which swarm home wrote it, so any marker at all reads
// as "owned" by ANY skillsHome, and RefreshSkillLinks recopies fresh content
// over it even though this daemon's Home has nothing to do with this
// UserHome's real install.
//
// Claude (Symlink mode) is seeded with a link pointing at a skills home that
// no longer exists on disk at all (the deleted-temp-dir shape from the
// incident) to confirm a dangling foreign link is left exactly as dangling,
// never "helpfully" touched.
func TestOpenDaemonWithACustomHomeLeavesTheRealUserHomeSkillsUntouched(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	userHome := t.TempDir()
	ic := install.Config{UserHome: userHome, Home: filepath.Join(userHome, ".swarm")}
	if _, _, err := install.WriteSkills(ic, install.KindClaude); err != nil {
		t.Fatal(err)
	}
	if _, _, err := install.WriteSkills(ic, install.KindCodex); err != nil {
		t.Fatal(err)
	}
	// Drift Codex's real, already-installed copy so a silent recopy is
	// observable.
	codexSwarmMD := filepath.Join(userHome, ".codex", "skills", "swarm", "SKILL.md")
	if err := os.WriteFile(codexSwarmMD, []byte("stale content from an older build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A dangling foreign link: this exact shape bit the live machine.
	claudeSwarmLink := filepath.Join(userHome, ".claude", "skills", "swarm")
	if err := os.RemoveAll(claudeSwarmLink); err != nil {
		t.Fatal(err)
	}
	deletedHome := filepath.Join(t.TempDir(), "already-gone")
	if err := os.Symlink(filepath.Join(deletedHome, "skills", "swarm"), claudeSwarmLink); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, filepath.Join(userHome, ".claude", "skills"))
	beforeCodex := snapshotTree(t, filepath.Join(userHome, ".codex", "skills"))

	// A different, throwaway daemon Home for the *same* real UserHome -- the
	// exact shape of `make dev` (--home ~/.swarm-dev) or any test.
	dm, err := openDaemon(context.Background(), daemonConfig{
		Home: t.TempDir(), UserHome: userHome, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer dm.db.Close()

	after := snapshotTree(t, filepath.Join(userHome, ".claude", "skills"))
	afterCodex := snapshotTree(t, filepath.Join(userHome, ".codex", "skills"))
	if !reflect.DeepEqual(before, after) {
		t.Errorf("a daemon with a different Home touched the real UserHome's claude skills:\nbefore: %v\nafter:  %v", before, after)
	}
	if !reflect.DeepEqual(beforeCodex, afterCodex) {
		t.Errorf("a daemon with a different Home touched the real UserHome's codex skills:\nbefore: %v\nafter:  %v", beforeCodex, afterCodex)
	}
}

// C1, positive case: when Home really is the canonical default for UserHome
// (filepath.Join(userHome, ".swarm")), openDaemon still repairs a broken
// per-kind link -- the gate must not turn the refresh off altogether.
func TestOpenDaemonWithTheCanonicalHomeStillRefreshesUserHomeSkillLinks(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	userHome := t.TempDir()
	swarmHome := filepath.Join(userHome, ".swarm")
	ic := install.Config{UserHome: userHome, Home: swarmHome}
	if _, _, err := install.WriteSkills(ic, install.KindClaude); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(userHome, ".claude", "skills", "swarm")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	skillsHome, err := install.SkillsHome(swarmHome)
	if err != nil {
		t.Fatal(err)
	}
	// Dangling but still swarm-owned: it resolves inside skillsHome, just at a
	// wrong (nonexistent) name.
	if err := os.Symlink(filepath.Join(skillsHome, "some-old-removed-name"), link); err != nil {
		t.Fatal(err)
	}

	dm, err := openDaemon(context.Background(), daemonConfig{
		Home: swarmHome, UserHome: userHome, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer dm.db.Close()

	got, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(skillsHome, "swarm"); got != want {
		t.Errorf("link still broken: %s, want %s", got, want)
	}
}

func TestTokenEmptyIsReplacedUnreadableFails(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	home := t.TempDir()
	path := filepath.Join(home, "run", "daemon.token")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dm, err := openDaemon(context.Background(), daemonConfig{UserHome: t.TempDir(), Home: home, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	dm.db.Close()
	b, _ := os.ReadFile(path)
	fi, _ := os.Stat(path)
	if len(strings.TrimSpace(string(b))) != 64 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token = %q mode %v", b, fi.Mode().Perm())
	}

	// a reused token is tightened to 0600 too
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	dm, err = openDaemon(context.Background(), daemonConfig{UserHome: t.TempDir(), Home: home, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	dm.db.Close()
	again, _ := os.ReadFile(path)
	if fi, _ := os.Stat(path); string(again) != string(b) || fi.Mode().Perm() != 0o600 {
		t.Fatalf("reused token = %q mode %v", again, fi.Mode().Perm())
	}

	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600)
	_, err = openDaemon(context.Background(), daemonConfig{UserHome: t.TempDir(), Home: home, ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: t.Logf})
	if err == nil || !strings.Contains(err.Error(), "Can't read the daemon token ("+path+")") {
		t.Fatalf("unreadable token = %v", err)
	}
}

func TestDaemonLoggersWired(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	var mu sync.Mutex
	var got []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, format)
	}
	dm, err := openDaemon(context.Background(), daemonConfig{UserHome: t.TempDir(), Home: t.TempDir(), ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: logf})
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
		if _, err := d.Exec(`INSERT INTO events (type, payload_json, created_at) VALUES ('x', '{}', ?)`, at); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`INSERT INTO idempotency (caller, request_id, tool, result_json, created_at) VALUES ('c', ?, 't', '{}', ?)`,
			age.String(), at); err != nil {
			t.Fatal(err)
		}
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
	if err := os.WriteFile(filepath.Join(idx.Dir, "note.md"), []byte("# Note\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "run", "daemon.token"), []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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

// The usage text is the CLI's help (§19). Every command this branch ships must be
// in it, and no command it does not ship.
func TestUsageListsTheP5Commands(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"help"}, &out, io.Discard); code != 0 {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{
		"install [--plugins] [--yes]",
		"uninstall",
		"doctor [--json] [--legacy]",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage is missing %q:\n%s", want, out.String())
		}
	}
}

func TestUnknownFlagsAreRejected(t *testing.T) {
	for _, args := range [][]string{
		{"install", "--nope"},
		{"uninstall", "--nope"},
		{"doctor", "--nope"},
	} {
		if code := run(args, io.Discard, io.Discard); code != 2 {
			t.Errorf("%v exit = %d, want 2", args, code)
		}
	}
}

// doctor --legacy prints the legacy block and nothing else.
func TestDoctorLegacyPrintsOnlyTheLegacyBlock(t *testing.T) {
	saved := doctorChecks
	savedLegacy := doctorLegacyChecks
	t.Cleanup(func() { doctorChecks, doctorLegacyChecks = saved, savedLegacy })
	doctorChecks = func(context.Context, install.Doctor) []install.Check {
		t.Fatal("plain checks must not run with --legacy")
		return nil
	}
	doctorLegacyChecks = func(context.Context, install.Doctor) []install.Check {
		return []install.Check{{Name: "Agent Swarm 1.x", OK: false, Detail: "leftover at /fake/x"}}
	}
	var out bytes.Buffer
	code := run([]string{"doctor", "--legacy", "--home", t.TempDir()}, &out, io.Discard)
	if code != 1 {
		t.Errorf("code = %d, want 1 for a failing check", code)
	}
	if !strings.Contains(out.String(), "leftover at /fake/x") {
		t.Errorf("out = %q", out.String())
	}
}

// seedLegacyDB writes a minimal Agent Swarm 1.x database at path (C5).
func seedLegacyDB(t *testing.T, path string) {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`CREATE TABLE schema_meta (version INTEGER NOT NULL,
		embed_model TEXT NOT NULL, embed_dimensions INTEGER NOT NULL,
		migrated_at TEXT NOT NULL DEFAULT (datetime('now')));
		INSERT INTO schema_meta (version, embed_model, embed_dimensions) VALUES (4, 'nomic-embed-text', 256);`); err != nil {
		t.Fatal(err)
	}
}

// swarm install refuses to run while v1 data is present (C5), with §17.3's sentence.
func TestInstallRefusesWithLegacyData(t *testing.T) {
	home := t.TempDir()
	seedLegacyDB(t, filepath.Join(home, "swarm.db"))
	var errOut bytes.Buffer
	code := run([]string{"install", "--home", home, "--dry-run"}, io.Discard, &errOut)
	if code == 0 {
		t.Fatal("want a non-zero exit")
	}
	if want := "Agent Swarm 1.x data found. Run `swarm migrate` first."; !strings.Contains(errOut.String(), want) {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

// --yes answers the release-folder confirmation without reading stdin. This is a
// real run (not --dry-run), because --dry-run stops before installAgents is called.
func TestInstallYesDoesNotReadStdin(t *testing.T) {
	savedAgents, savedLaunchd := installAgents, installLaunchd
	t.Cleanup(func() { installAgents, installLaunchd = savedAgents, savedLaunchd })
	installLaunchd = func(context.Context, install.Config, execx.Runner, bool, io.Writer) error { return nil }
	var gotConfirm bool
	installAgents = func(ctx context.Context, o install.AgentsOpts) error {
		gotConfirm = o.Confirm("prompt?")
		return nil
	}
	if code := run([]string{"install", "--yes", "--home", t.TempDir()}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !gotConfirm {
		t.Error("--yes must make Confirm return true without a prompt")
	}
}

// --plugins sets PluginsOnly and skips the launchd plist entirely.
func TestInstallPluginsOnlySkipsTheLaunchAgent(t *testing.T) {
	savedAgents, savedInstall := installAgents, installLaunchd
	t.Cleanup(func() { installAgents, installLaunchd = savedAgents, savedInstall })
	installLaunchd = func(context.Context, install.Config, execx.Runner, bool, io.Writer) error {
		t.Fatal("--plugins must not write the launch agent")
		return nil
	}
	var pluginsOnly bool
	installAgents = func(ctx context.Context, o install.AgentsOpts) error {
		pluginsOnly = o.PluginsOnly
		return nil
	}
	if code := run([]string{"install", "--plugins", "--home", t.TempDir()}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !pluginsOnly {
		t.Error("PluginsOnly was not set")
	}
}

// The four modes are mutually exclusive, and each one calls its own runner method.
func TestMigrateFlagsAreExclusiveAndDispatch(t *testing.T) {
	saved := migrateRun
	t.Cleanup(func() { migrateRun = saved })
	for _, tc := range []struct {
		args []string
		want string
		code int
	}{
		{[]string{"migrate"}, "migrate", 0},
		{[]string{"migrate", "--dry-run"}, "dry-run", 0},
		{[]string{"migrate", "--resume"}, "resume", 0},
		{[]string{"migrate", "--rollback"}, "rollback", 0},
		{[]string{"migrate", "--resume", "--rollback"}, "", 2},
		{[]string{"migrate", "--dry-run", "--resume"}, "", 2},
	} {
		var got string
		migrateRun = func(ctx context.Context, r *migrate.Runner, mode string) error { got = mode; return nil }
		code := run(append(tc.args, "--home", t.TempDir()), io.Discard, io.Discard)
		if code != tc.code {
			t.Errorf("%v exit = %d, want %d", tc.args, code, tc.code)
		}
		if got != tc.want {
			t.Errorf("%v mode = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// C1(a): Ctrl-C (SIGINT) during swarm migrate must cancel the context migrateRun
// gets, not hard-kill the process outright — a hard kill has no chance to run
// anything, including the per-action journal saves the runner relies on. This
// sends the test process itself a real SIGINT while migrateContext's
// signal.NotifyContext is armed; that is the documented way to test this pattern
// (the signal is intercepted for cancellation, not left to its default
// terminate-the-process disposition).
func TestMigrateContextCancelsOnSIGINTInsteadOfKillingTheProcess(t *testing.T) {
	ctx, stop := migrateContext()
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		// Good: the signal became a cancellation, and the test process is still here
		// to observe it — a hard kill would have ended it instead.
	case <-time.After(3 * time.Second):
		t.Fatal("context was not canceled after SIGINT")
	}
}

func TestUsageListsMigrate(t *testing.T) {
	var out bytes.Buffer
	run([]string{"help"}, &out, io.Discard)
	if !strings.Contains(out.String(), "migrate [--dry-run | --resume | --rollback]") {
		t.Errorf("usage is missing migrate:\n%s", out.String())
	}
}

func TestUninstallCallsTheInstallPackage(t *testing.T) {
	saved := installUninstall
	t.Cleanup(func() { installUninstall = saved })
	var called bool
	installUninstall = func(context.Context, install.AgentsOpts) error { called = true; return nil }
	if code := run([]string{"uninstall", "--home", t.TempDir()}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !called {
		t.Error("install.Uninstall was not called")
	}
}
