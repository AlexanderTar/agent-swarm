package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// devServer is an in-process daemon on a temp home and a port the OS chose.
type devServer struct {
	URL  string
	Stop func()
}

// devDaemon is devDaemonWithout with --dev, which is what enables the seed route.
func devDaemon(t *testing.T) (*devServer, string) {
	t.Helper()
	return devDaemonWithout(t) // no flags withheld
}

// devDaemonWithout starts a daemon with the standard dev flags MINUS the ones
// named, so a test can prove a flag is load-bearing. Background is false:
// nothing in this task needs a loop, and a background daemon would run the
// reconciler and the wake loop against a fake-less store.
//
// The two t.Setenv calls are the invariants, not decoration. SWARM_TMUX_SOCKET
// keeps openDaemon's Spawner off the live server (S-1) and SWARM_USAGE=""
// keeps usage.SourcesFromEnv returning nil, so this daemon reads no keychain
// and calls no vendor endpoint (S-4). Both already default to safe; a test
// that builds a real daemon states them anyway, so grep can find every such
// test.
func devDaemonWithout(t *testing.T, without ...string) (*devServer, string) {
	t.Helper()
	t.Setenv("SWARM_TMUX_SOCKET", "swarm-test-"+strconv.Itoa(os.Getpid()))
	t.Setenv("SWARM_USAGE", "")
	home := t.TempDir()
	cfg := daemonConfig{Home: home, Port: 0, Background: false, ScanRoot: t.TempDir(),
		Embedder: offlineEmb{}, Log: func(string, ...any) {},
		Dev: !slices.Contains(without, "--dev")}
	addr := make(chan string, 1)
	cfg.Ready = func(a string) { addr <- a }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := serve(ctx, cfg); err != nil {
			t.Logf("daemon exited: %v", err)
		}
	}()
	var url string
	select {
	case a := <-addr:
		url = "http://" + a
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("the daemon did not come up")
	}
	stop := func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return &devServer{URL: url, Stop: stop}, home
}

func devToken(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "run", "daemon.token"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func devGet(t *testing.T, srv *devServer, home, path string, out any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+devToken(t, home))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("GET %s: decode %s: %v", path, raw, err)
	}
}

func listKeys(t *testing.T, srv *devServer, home string) []string {
	t.Helper()
	var list struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	devGet(t, srv, home, "/api/items?view=flat", &list)
	var keys []string
	for _, it := range list.Items {
		keys = append(keys, it.Key)
	}
	slices.Sort(keys)
	return keys
}

func keyStatuses(t *testing.T, srv *devServer, home string) map[string]string {
	t.Helper()
	var list struct {
		Items []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
		} `json:"items"`
	}
	devGet(t, srv, home, "/api/items?view=flat", &list)
	out := map[string]string{}
	for _, it := range list.Items {
		out[it.Key] = it.Status
	}
	return out
}

func listRequests(t *testing.T, srv *devServer, home string) []map[string]any {
	t.Helper()
	var list []map[string]any
	devGet(t, srv, home, "/api/requests", &list)
	return list
}

func TestDevSeedRefusesTheLiveDaemon(t *testing.T) {
	var out, errOut bytes.Buffer
	home, _ := os.UserHomeDir()
	code := run([]string{"dev-seed", "--home", filepath.Join(home, ".swarm"),
		"--url", "http://127.0.0.1:17777"}, &out, &errOut)
	if code == 0 {
		t.Fatal("dev-seed must refuse ~/.swarm")
	}
	code = run([]string{"dev-seed", "--home", t.TempDir(), "--url", "http://127.0.0.1:7777"}, &out, &errOut)
	if code == 0 {
		t.Fatal("dev-seed must refuse port 7777")
	}
}

func TestDevSeedCreatesTheContractKeysAndIsIdempotent(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", "swarm-test-"+strconv.Itoa(os.Getpid()))
	t.Setenv("SWARM_USAGE", "")
	srv, home := devDaemon(t) // a real daemon on a temp home and a random port
	defer srv.Stop()
	var out bytes.Buffer
	if code := run([]string{"dev-seed", "--home", home, "--url", srv.URL}, &out, &out); code != 0 {
		t.Fatalf("code = %d: %s", code, out.String())
	}
	// a daemon started without --dev has no seed route
	plain, plainHome := devDaemonWithout(t, "--dev")
	defer plain.Stop()
	var perr bytes.Buffer
	if code := run([]string{"dev-seed", "--home", plainHome, "--url", plain.URL}, &perr, &perr); code == 0 {
		t.Fatal("dev-seed must fail against a daemon without --dev")
	}
	if !strings.Contains(perr.String(), "Unknown API route.") {
		t.Fatalf("stderr = %q", perr.String())
	}
	keys := listKeys(t, srv, home)
	for _, want := range []string{"EPIC-12", "STORY-40", "STORY-41", "TASK-98", "TASK-101",
		"TASK-102", "TASK-103", "TASK-104", "TASK-110", "BUG-7", "BUG-8",
		"SPIKE-3", "SPIKE-4", "SPIKE-5", "EPIC-20", "EPIC-30"} {
		if !slices.Contains(keys, want) {
			t.Errorf("missing %s", want)
		}
	}
	if slices.Contains(keys, "TASK-999") {
		t.Error("the orphan task belongs to the client fixtures only (P3 ruling F15)")
	}
	reqs := listRequests(t, srv, home)
	if len(reqs) != 9 {
		t.Fatalf("seeded %d requests, want 9", len(reqs))
	}
	byKind := map[string]int{}
	for _, r := range reqs {
		byKind[r["kind"].(string)]++
	}
	// Counts match the real, landed web/src/mock/fixtures.ts (P3 Task 13),
	// not the brief's own Task 39 prose written before it landed: fixtures.ts
	// has two `question` requests (SPIKE-3 and TASK-104) and one
	// `approve_section`, not the reverse. See internal/httpapi/dev.go's
	// seedRequests doc comment for the full reconciliation.
	for kind, want := range map[string]int{"question": 2, "confirm_repos": 1, "approve_section": 1,
		"approve_plan": 1, "approve_report": 1, "accept_epic": 1, "accept_fix": 1, "close_spike": 1} {
		if byKind[kind] != want {
			t.Errorf("%s = %d, want %d", kind, byKind[kind], want)
		}
	}

	// idempotent
	before := listKeys(t, srv, home)
	out.Reset()
	if code := run([]string{"dev-seed", "--home", home, "--url", srv.URL}, &out, &out); code != 0 {
		t.Fatalf("second run: %s", out.String())
	}
	after := listKeys(t, srv, home)
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Fatalf("a second run changed the data:\n%v\n%v", before, after)
	}
}

// The seeded statuses match the fixtures the board renders against.
func TestDevSeedStatuses(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", "swarm-test-"+strconv.Itoa(os.Getpid()))
	t.Setenv("SWARM_USAGE", "")
	srv, home := devDaemon(t)
	defer srv.Stop()
	run([]string{"dev-seed", "--home", home, "--url", srv.URL}, io.Discard, io.Discard)
	statuses := keyStatuses(t, srv, home)
	// BUG-7 is "blocked" and BUG-8 is "in_review" in fixtures.ts — the
	// reverse of the brief's own Task 39 prose (which also put accept_fix on
	// BUG-7; fixtures.ts puts it on BUG-8). See dev.go's seedRequests comment.
	for key, want := range map[string]string{
		"EPIC-12": "in_progress", "STORY-40": "in_progress", "TASK-98": "done",
		"TASK-101": "in_progress", "BUG-7": "blocked", "BUG-8": "in_review", "SPIKE-3": "awaiting_approval",
		"EPIC-20": "draft", "EPIC-30": "done",
	} {
		if statuses[key] != want {
			t.Errorf("%s = %s, want %s", key, statuses[key], want)
		}
	}
}
