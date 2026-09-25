package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readToken reads the daemon token written under home.
func readToken(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "run", "daemon.token"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// The daemon serves the P2 routes and runs the P2 loops.
func TestDaemonServesTheP2Routes(t *testing.T) {
	// Belt and braces on the two invariants this test could otherwise breach: it
	// builds a real Spawner (S-1) and a real Poller (S-4). Both already default to
	// safe — swarm-test-<pid> and no sources — so these lines assert the default
	// rather than change it, and they make every real-daemon test greppable.
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	t.Setenv("SWARM_USAGE", "")
	home := t.TempDir()
	addr := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serve(ctx, daemonConfig{UserHome: t.TempDir(), Home: home, Port: 0, Background: false,
		ScanRoot: t.TempDir(), Embedder: offlineEmb{}, Log: func(string, ...any) {},
		Ready: func(a string) { addr <- a }})
	base := "http://" + <-addr
	token := readToken(t, home)
	for _, p := range []string{"/api/state", "/api/agents", "/api/requests", "/api/usage", "/api/notifications"} {
		req, _ := http.NewRequest("GET", base+p, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s = %d", p, resp.StatusCode)
		}
	}
	// /mcp answers with the daemon token, read-only
	req, _ := http.NewRequest("POST", base+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/mcp = %d", resp.StatusCode)
	}
}

// The daemon writes ~/.swarm/tmux.conf and never touches the real one.
func TestDaemonWritesItsOwnTmuxConf(t *testing.T) {
	t.Setenv("SWARM_TMUX_SOCKET", fmt.Sprintf("swarm-test-%d", os.Getpid()))
	t.Setenv("SWARM_USAGE", "")
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addr := make(chan string, 1)
	go serve(ctx, daemonConfig{UserHome: t.TempDir(), Home: home, Port: 0, Background: false, ScanRoot: t.TempDir(),
		Embedder: offlineEmb{}, Log: func(string, ...any) {}, Ready: func(a string) { addr <- a }})
	<-addr
	b, err := os.ReadFile(filepath.Join(home, "tmux.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "set-titles-string '#W'") {
		t.Fatalf("tmux.conf = %s", b)
	}
}
