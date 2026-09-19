package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// §11.2, §23.2 scenario 15: a hook outside swarm prints nothing, fast.
func TestHookSubcommandIsSilentWithoutASession(t *testing.T) {
	var out, errOut bytes.Buffer
	stdin := strings.NewReader(`{"session_id":"x"}`)
	start := time.Now()
	code := runWithStdin([]string{"hook", "claude", "Stop"}, stdin, &out, &errOut)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("code = %d, out = %q", code, out.String())
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("took %v", d)
	}
}

func TestHookSubcommandNeedsBothArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runWithStdin([]string{"hook", "claude"}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Fatalf("code = %d", code)
	}
}

// swarm mcp with no session and no daemon token file fails clearly.
func TestMCPSubcommandNeedsATokenSource(t *testing.T) {
	home := t.TempDir()
	var out, errOut bytes.Buffer
	code := runWithStdin([]string{"mcp", "--home", home}, strings.NewReader(""), &out, &errOut)
	if code == 0 {
		t.Fatal("with no session token and no daemon token, the shim cannot authenticate")
	}
	if !strings.Contains(errOut.String(), "token") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// The plan's later CLI batch (6b, T36-40) owns the operator verbs (new, start,
// pause, answer, …) — progress.md's split ruling scopes this batch to the
// HTTP surface and these two agent-facing subcommands only. This test only
// checks the two subcommands Task 35 actually adds; asserting the full verb
// list here would fail until 6b lands (deviation noted in the batch report).
func TestUsageMentionsBothNewSubcommands(t *testing.T) {
	var out bytes.Buffer
	run([]string{"help"}, &out, &out)
	for _, want := range []string{"mcp", "hook"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage is missing %q:\n%s", want, out.String())
		}
	}
}
