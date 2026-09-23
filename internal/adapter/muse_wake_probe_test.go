package adapter

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMuseLaunchFlagsProbe validates every flag muse.go's argv uses against the
// real CLI's help. Offline and free, but gated like all live probes: CI
// machines have no muse binary, and a flag set is version-dependent.
//
//	MUSE_LIVE_PROBE=1 go test ./internal/adapter/ -run TestMuseLaunchFlagsProbe -v
func TestMuseLaunchFlagsProbe(t *testing.T) {
	if os.Getenv("MUSE_LIVE_PROBE") != "1" {
		t.Skip("live probe against real muse CLI; set MUSE_LIVE_PROBE=1 to run")
	}
	out, err := exec.Command("muse", "--help").Output()
	if err != nil {
		t.Fatalf("muse --help: %v", err)
	}
	help := string(out)
	// The kickoff is a bare positional, never a flag. Assert the documented
	// usage grammar exactly -- a loose `strings.Contains(help, "-i")` (the
	// prior form of this check) is satisfied by "--image" too, so it never
	// would have caught argv passing a nonexistent -i flag (see the fix in
	// muse.go: Launch/argv no longer emits -i).
	if !strings.Contains(help, "Usage: muse [OPTIONS] [PROMPT]") {
		t.Errorf("muse --help no longer documents the kickoff as a bare positional:\n%s", help)
	}
	for _, flag := range []string{"--model", "--reasoning-effort", "--yolo", "--trust-workspace"} {
		if !strings.Contains(help, flag) {
			t.Errorf("muse --help lacks %q: Launch argv invents a flag", flag)
		}
	}
	resume, err := exec.Command("muse", "resume", "--help").Output()
	if err != nil {
		t.Fatalf("muse resume --help: %v", err)
	}
	resumeHelp := string(resume)
	// `strings.Contains(resumeHelp, "session")` (the prior check) is satisfied
	// by nearly any resume help text, correct or not. Assert the specific
	// positional token instead, and that resume takes no prompt argument.
	if !strings.Contains(resumeHelp, "<session-ref>") {
		t.Errorf("muse resume --help dropped <session-ref>: Resume argv is wrong:\n%s", resumeHelp)
	}
	if strings.Contains(resumeHelp, "[PROMPT]") {
		t.Errorf("muse resume --help now documents a prompt argument; Resume's no-kickoff assumption is stale:\n%s", resumeHelp)
	}
}

// TestMuseWakeProbe tries a native wake: send a marker to the session named by
// MUSE_WAKE_TARGET and assert the send is acknowledged.
//
// Probed 2026-09-23: `session-message send` to a live, message_capable TUI
// session fails with external_agent_ingress_closed, and no CLI flag, settings
// key, or schema surface enables ingress. Native wake is therefore unavailable
// and Wake stays on the tmux-paste fallback. Re-run this probe if a newer muse
// documents an ingress opt-in; until it passes, do not flip Wake to native.
func TestMuseWakeProbe(t *testing.T) {
	if os.Getenv("MUSE_LIVE_PROBE") != "1" {
		t.Skip("live probe against real muse CLI; set MUSE_LIVE_PROBE=1 to run")
	}
	target := os.Getenv("MUSE_WAKE_TARGET")
	if target == "" {
		t.Skip("set MUSE_WAKE_TARGET=<session-uuid-or-name> to a live muse TUI session")
	}
	cmd := exec.Command("muse", "session-message", "send",
		"--target", target, "--json")
	cmd.Stdin = strings.NewReader("SWARM-PROBE-MARKER reply with the exact single word PROBE_M and nothing else")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("session-message send to %q: %v\n%s", target, err, out)
	}
	t.Logf("send acknowledged; verify the marker turn appears in %q's pane", target)
}
