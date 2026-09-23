package adapter

import (
	"os"
	"os/exec"
	"path/filepath"
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

// TestMuseSetupEnvReachesRealMCPSubprocess is the P0-1 fix's live proof: it
// runs setupEnv exactly as Launch/Resume do, then actually starts the real
// muse CLI against the isolated XDG_CONFIG_HOME it produced and confirms a
// spawned stdio "MCP server" (standing in for `swarm mcp`, so the probe stays
// free -- no `meta` provider call, no real swarm daemon) sees the four SWARM_*
// vars in its own process environment. `muse exec --provider echo` is
// headless and loads mcpServers at startup (confirmed live 2026-09-23; see
// museMCPEnv's doc), so no paid model turn is needed. The ambient shell's
// SWARM_* vars are stripped from the child's env first, so a pass here can
// only mean the isolated settings.json's literal `env` block was what
// delivered them -- the exact mechanism the fix relies on.
//
//	MUSE_LIVE_PROBE=1 go test ./internal/adapter/ -run TestMuseSetupEnvReachesRealMCPSubprocess -v
func TestMuseSetupEnvReachesRealMCPSubprocess(t *testing.T) {
	if os.Getenv("MUSE_LIVE_PROBE") != "1" {
		t.Skip("live probe against real muse CLI; set MUSE_LIVE_PROBE=1 to run")
	}
	if _, err := exec.LookPath("muse"); err != nil {
		t.Skip("muse not installed")
	}

	dir := t.TempDir()
	outFile := filepath.Join(dir, "envecho-output.txt")
	script := filepath.Join(dir, "envecho.sh")
	// A stand-in stdio MCP server: dumps what it sees on start (before any
	// handshake), then just echoes empty JSON-RPC replies forever so muse
	// doesn't consider it crashed mid-init.
	body := "#!/usr/bin/env bash\n" +
		"{\n" +
		"  echo \"SWARM_URL=${SWARM_URL:-<unset>}\"\n" +
		"  echo \"SWARM_SESSION=${SWARM_SESSION:-<unset>}\"\n" +
		"  echo \"SWARM_TOKEN_FILE=${SWARM_TOKEN_FILE:-<unset>}\"\n" +
		"  echo \"SWARM_AGENT_KIND=${SWARM_AGENT_KIND:-<unset>}\"\n" +
		"} > " + outFile + "\n" +
		"while IFS= read -r line; do echo '{\"jsonrpc\":\"2.0\",\"id\":null,\"result\":{}}'; done\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	d := Deps{Home: t.TempDir(), UserHome: t.TempDir(), Bin: script, Now: nowStub, Log: func(string, ...any) {}}
	s := Spec{SessionID: "live-probe-session", DaemonURL: "http://127.0.0.1:9-probe-url", Bin: script}
	env, err := newMuse(d).setupEnv(s)
	if err != nil {
		t.Fatal(err)
	}
	xdgConfigHome, ok := env["XDG_CONFIG_HOME"]
	if !ok || xdgConfigHome == "" {
		t.Fatalf("setupEnv did not return XDG_CONFIG_HOME: %v", env)
	}

	var childEnv []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SWARM_") {
			continue // strip ambient SWARM_* so a pass can only come from settings.json
		}
		childEnv = append(childEnv, kv)
	}
	childEnv = append(childEnv, "XDG_CONFIG_HOME="+xdgConfigHome)

	cmd := exec.Command("muse", "exec", "--provider", "echo", "--yolo", "--trust-workspace",
		"--no-session-log", "hello")
	cmd.Dir = t.TempDir()
	cmd.Env = childEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("muse exec: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("swarm MCP stand-in never started (no output file written): %v\nmuse output:\n%s", err, out)
	}
	got := string(raw)
	wantTokenFile := filepath.Join(d.Home, "run", "tokens", s.SessionID)
	for _, want := range []string{
		"SWARM_URL=" + s.DaemonURL,
		"SWARM_SESSION=" + s.SessionID,
		"SWARM_TOKEN_FILE=" + wantTokenFile,
		"SWARM_AGENT_KIND=muse",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("MCP subprocess env missing %q; got:\n%s", want, got)
		}
	}
}
