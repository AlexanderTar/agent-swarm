package execx

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesStdout(t *testing.T) {
	out, err := Run(context.Background(), "printf", "hello")
	if err != nil || string(out) != "hello" {
		t.Fatalf("Run = %q, %v", out, err)
	}
}

func TestRunReportsStderrOnFailure(t *testing.T) {
	_, err := Run(context.Background(), "sh", "-c", "echo boom >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want stderr in message", err)
	}
}

func TestStartKeepsStdinOpenBetweenWrites(t *testing.T) {
	p, err := Start(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	buf := make([]byte, 3)
	for _, word := range []string{"one", "two"} {
		if _, err := io.WriteString(p.Stdin, word); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(p.Stdout, buf); err != nil || string(buf) != word {
			t.Fatalf("read %q, %v; want %q", buf, err, word)
		}
	}
}

// RunEnv exists alongside Run for callers that need an isolated env var on a
// blocking, output-returning call (e.g. Codex.Wake, which must target a
// session's isolated CODEX_HOME instead of the daemon's ambient ~/.codex).
func TestRunEnvMergesExtraVariablesOverTheAmbientEnvironment(t *testing.T) {
	out, err := RunEnv(context.Background(), map[string]string{"SWARM_PROBE": "isolated-value"}, "sh", "-c", "echo $SWARM_PROBE")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "isolated-value" {
		t.Fatalf("got %q, want %q", got, "isolated-value")
	}
}

func TestRunEnvKeepsTheAmbientEnvironmentOtherwiseIntact(t *testing.T) {
	t.Setenv("SWARM_PROBE_AMBIENT", "from-parent")
	out, err := RunEnv(context.Background(), map[string]string{"SWARM_PROBE_OVERRIDE": "x"}, "sh", "-c", "echo $SWARM_PROBE_AMBIENT")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "from-parent" {
		t.Fatalf("got %q, want %q (RunEnv must not drop the ambient environment)", got, "from-parent")
	}
}

func TestRunEnvReportsStderrOnFailure(t *testing.T) {
	_, err := RunEnv(context.Background(), nil, "sh", "-c", "echo boom >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want stderr in message", err)
	}
}

// StartEnv exists alongside Start for callers that need an isolated HOME
// (e.g. agy.Wake targeting a session's isolated agy-home) without changing
// Start's signature for every other caller (catalog/usage app-server probes,
// which need no env override).
func TestStartEnvMergesExtraVariablesOverTheAmbientEnvironment(t *testing.T) {
	p, err := StartEnv(context.Background(), map[string]string{"SWARM_PROBE": "isolated-value"}, "sh", "-c", "echo $SWARM_PROBE")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	out, err := io.ReadAll(p.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "isolated-value" {
		t.Fatalf("got %q, want %q", got, "isolated-value")
	}
}

func TestStartEnvKeepsTheAmbientEnvironmentOtherwiseIntact(t *testing.T) {
	t.Setenv("SWARM_PROBE_AMBIENT", "from-parent")
	p, err := StartEnv(context.Background(), map[string]string{"SWARM_PROBE_OVERRIDE": "x"}, "sh", "-c", "echo $SWARM_PROBE_AMBIENT")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	out, err := io.ReadAll(p.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "from-parent" {
		t.Fatalf("got %q, want %q (StartEnv must not drop the ambient environment)", got, "from-parent")
	}
}

func TestFakeRecordsCallsAndReturnsResponses(t *testing.T) {
	f := &Fake{Responses: map[string]Result{
		"git -C /r config --get remote.origin.url": {Out: "git@github.com:o/r.git\n"},
		"git -C /r status --porcelain":             {Err: errors.New("exit 128")},
	}}
	run := f.Runner()
	out, err := run(context.Background(), "git", "-C", "/r", "config", "--get", "remote.origin.url")
	if err != nil || string(out) != "git@github.com:o/r.git\n" {
		t.Fatalf("got %q, %v", out, err)
	}
	if _, err := run(context.Background(), "git", "-C", "/r", "status", "--porcelain"); err == nil {
		t.Fatal("want configured error")
	}
	if _, err := run(context.Background(), "rm", "-rf", "/"); err == nil || !strings.Contains(err.Error(), "unexpected command: rm -rf /") {
		t.Fatalf("unknown command err = %v", err)
	}
	want := []string{"git -C /r config --get remote.origin.url", "git -C /r status --porcelain", "rm -rf /"}
	if got := f.Calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Calls = %v", got)
	}
}

func TestFakeBlockWaitsForContext(t *testing.T) {
	f := &Fake{Responses: map[string]Result{"git status": {Block: true}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := f.Runner()(ctx, "git", "status"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Block ignored the context")
	}
}

func TestRunWithLongerCallerDeadlineDoesNotTruncate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, "sleep", "1")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	dur := time.Since(start)
	if dur > 500*time.Millisecond {
		t.Fatalf("expected command to abort near caller deadline ~200ms, took %v", dur)
	}
}
