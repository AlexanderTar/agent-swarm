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
	for _, flag := range []string{"-i", "--model", "--reasoning-effort", "--yolo", "--trust-workspace"} {
		if !strings.Contains(string(out), flag) {
			t.Errorf("muse --help lacks %q: Launch argv invents a flag", flag)
		}
	}
	resume, err := exec.Command("muse", "resume", "--help").Output()
	if err != nil {
		t.Fatalf("muse resume --help: %v", err)
	}
	if !strings.Contains(string(resume), "session-ref") && !strings.Contains(string(resume), "session") {
		t.Errorf("muse resume takes no session target: Resume argv is wrong:\n%s", resume)
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
