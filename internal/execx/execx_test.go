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

