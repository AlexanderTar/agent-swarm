// Command swarm-fake-agent is the process adapter.Fake launches. It reads a
// JSON scenario and drives it against the daemon's /mcp endpoint, drawing a
// claude-style pane so the idle-wake rules (§11.3) can be exercised. It never
// calls a model (P2 Task 37).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

// resolveScenarioPath is --scenario, or $SWARM_FAKE_SCENARIO. A value that
// already looks like a path (has a "/" or ends ".json") is used as-is; a bare
// name is resolved against $SWARM_E2E_SCENARIOS_DIR (scripts/e2e.sh sets it)
// so the tmux pane's own cwd — under $SWARM_HOME/work/<agent>, not the repo —
// never matters.
//
// Each daemon spawns many fake agents over its lifetime with different
// scripts, so the scenario can't be a single value fixed at daemon startup;
// adapter.Fake.Launch sets $SWARM_FAKE_SCENARIO per spawn, to the agent's own
// kebab name (Task 37 design note, see the batch report). A harness that
// wants a specific scenario for a specific spawn names its item/agent to
// match. When the daemon picked that name itself (a collision suffix, or a
// test that only needs a live, addressable session and doesn't care what the
// pane does), there is no file to match: $SWARM_FAKE_SCENARIO then falls back
// to _default.json rather than failing, so the harness never has to predict
// every name the daemon might produce. An explicit --scenario is never
// substituted this way — a deliberate, wrong path should fail loudly.
func resolveScenarioPath(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	cand := os.Getenv("SWARM_FAKE_SCENARIO")
	if cand == "" {
		return "", errors.New("no scenario given: pass --scenario or set SWARM_FAKE_SCENARIO")
	}
	if strings.HasSuffix(cand, ".json") || strings.Contains(cand, "/") {
		return cand, nil
	}
	dir := os.Getenv("SWARM_E2E_SCENARIOS_DIR")
	if dir == "" {
		dir = "scripts/e2e/scenarios"
	}
	path := filepath.Join(dir, cand+".json")
	if _, err := os.Stat(path); err != nil {
		return filepath.Join(dir, "_default.json"), nil
	}
	return path, nil
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("swarm-fake-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "daemon base url")
	scenarioFlag := fs.String("scenario", "", "scenario file (or bare name under SWARM_E2E_SCENARIOS_DIR)")
	fs.String("session", "", "session id (unused: the token already identifies the session)")
	fs.String("name", "", "agent name (unused: read from SWARM_FAKE_SCENARIO instead)")
	fs.String("kickoff", "", "kickoff text (unused: the scenario script is authoritative)")
	fs.String("resume", "", "provider session id to resume (unused: the fake agent is stateless)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	tokPath := os.Getenv("SWARM_TOKEN_FILE")
	tokBytes, err := os.ReadFile(tokPath)
	if err != nil {
		fmt.Fprintln(stderr, "swarm-fake-agent:", err)
		return 1
	}

	scPath, err := resolveScenarioPath(*scenarioFlag)
	if err != nil {
		fmt.Fprintln(stderr, "swarm-fake-agent:", err)
		return 1
	}
	raw, err := os.ReadFile(scPath)
	if err != nil {
		fmt.Fprintln(stderr, "swarm-fake-agent: reading scenario", scPath, ":", err)
		return 1
	}
	var sc Scenario
	if err := json.Unmarshal(raw, &sc); err != nil {
		fmt.Fprintln(stderr, "swarm-fake-agent: parsing scenario", scPath, ":", err)
		return 1
	}

	r := &Runner{URL: *url, Token: strings.TrimSpace(string(tokBytes)), Out: stdout,
		BodiesDir: filepath.Join(filepath.Dir(filepath.Dir(scPath)), "bodies")}
	r.setMode("idle")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigc)
	go func() {
		select {
		case <-sigc:
			cancel()
		case <-ctx.Done():
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); drawLoop(ctx, stdout, r) }()
	go func() { defer wg.Done(); watchStdin(ctx, stdin, stdout, r) }()

	runErr := r.Run(ctx, sc)
	cancel()
	wg.Wait()
	if runErr != nil {
		fmt.Fprintln(stderr, "swarm-fake-agent:", runErr)
		return 1
	}
	if code := sc.exitCode(); code != nil {
		return *code
	}
	return 0
}

// drawLoop redraws the current pane mode every tick, so a `tmux capture-pane`
// taken at any moment finds it. Old frames scroll off the pane's visible
// screen naturally as new ones are printed (ponytail: relies on scroll-off
// rather than an explicit clear; fine at the tick rate and pane sizes this
// harness uses — clear with "\x1b[2J\x1b[H" first if a scenario ever needs a
// guarantee stronger than that).
func drawLoop(ctx context.Context, w io.Writer, r *Runner) {
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		drawPane(w, r.Mode())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// watchStdin re-draws the instant a line lands on stdin: that is how a real
// terminal app reacts to a paste, and scenario 13 (idle wake) checks that the
// pane reflects it without waiting for the next tick.
func watchStdin(ctx context.Context, in io.Reader, out io.Writer, r *Runner) {
	sc := bufio.NewScanner(in)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sc.Scan() {
			drawPane(out, r.Mode())
		}
	}()
	select {
	case <-ctx.Done():
	case <-done:
	}
}
