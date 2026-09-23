package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

func museSpec(t *testing.T) Spec {
	t.Helper()
	return Spec{AgentName: "login-form-coder", SessionID: "ses_01", Token: "tok",
		DaemonURL: "http://127.0.0.1:17778", Model: "muse-spark-1.3-contributor", Effort: "high",
		Cwd: t.TempDir(), Kickoff: "You are swarm agent login-form-coder (coder) for TASK-101: x.",
		Bin: "/usr/local/bin/swarm"}
}

func TestMuseLaunchArgv(t *testing.T) {
	d := testDeps(t)
	a := newMuse(d)
	l, err := a.Launch(museSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"muse", "-i", "You are swarm agent login-form-coder (coder) for TASK-101: x.",
		"--model", "muse-spark-1.3-contributor", "--reasoning-effort", "high",
		"--yolo", "--trust-workspace"}
	if len(l.Argv) != len(want) {
		t.Fatalf("argv = %q, want %q", l.Argv, want)
	}
	for i := range want {
		if l.Argv[i] != want[i] {
			t.Fatalf("argv = %q, want %q", l.Argv, want)
		}
	}
	if a.Kind() != kinds.Muse {
		t.Errorf("Kind() = %s", a.Kind())
	}
}

// Instructions ride the workspace AGENTS.md (cursor pattern): only when the
// operator configured custom instructions; empty means no file is touched.
func TestMuseLaunchWritesInstructionsToWorkspaceAgentsMd(t *testing.T) {
	d := testDeps(t)
	s := museSpec(t)
	s.Instructions = "Obey the fleet."
	l, err := newMuse(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	_ = l
	raw, err := os.ReadFile(filepath.Join(s.Cwd, "AGENTS.md"))
	if err != nil || string(raw) != "Obey the fleet." {
		t.Errorf("AGENTS.md = %q, %v", raw, err)
	}
	s2 := museSpec(t)
	if _, err := newMuse(d).Launch(s2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s2.Cwd, "AGENTS.md")); !os.IsNotExist(err) {
		t.Error("empty instructions must not touch the workspace")
	}
}

func TestMuseLaunchDefaultsEmptyEffort(t *testing.T) {
	d := testDeps(t)
	s := museSpec(t)
	s.Effort = ""
	l, err := newMuse(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Argv, " ")
	if !strings.Contains(joined, "--reasoning-effort high") {
		t.Errorf("empty effort must default to high: %q", joined)
	}
}

func writeMuseAuth(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "muse")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMuseAuthOK(t *testing.T) {
	d := testDeps(t)
	writeMuseAuth(t, d.UserHome, `{"schema_version":1,"providers":{"meta":{}}}`)
	if err := newMuse(d).AuthOK(context.Background()); err != nil {
		t.Errorf("signed-in auth.json: %v", err)
	}
	d2 := testDeps(t)
	writeMuseAuth(t, d2.UserHome, `{"schema_version":1,"providers":{}}`)
	if err := newMuse(d2).AuthOK(context.Background()); err == nil {
		t.Error("empty providers must fail AuthOK")
	}
	d3 := testDeps(t)
	if err := newMuse(d3).AuthOK(context.Background()); err == nil {
		t.Error("missing auth.json must fail AuthOK")
	}
}

// Live captures 2026-09-23 (tmux pane, v1.3.0): idle is a bare ❯ prompt with
// the model/effort/YOLO status line; busy adds "◈ Thinking (Ns · esc to
// interrupt)" (also seen as "◇ Double checking"). The ansi busy fixture keeps
// the real per-letter truecolor escapes, which Busy must survive via StripANSI.
func TestMuseIdleAndBusy(t *testing.T) {
	a := newMuse(testDeps(t))
	if !a.Idle(pane(t, "muse", "pane-idle.txt")) {
		t.Error("bare ❯ prompt with status line should be idle")
	}
	if a.Idle(pane(t, "muse", "pane-busy-ansi.txt")) {
		t.Error("spinner line means busy even with the ❯ prompt drawn")
	}
	if a.Idle("$ \n") {
		t.Error("a shell prompt is not idle")
	}
}

// TestMuseDiscoverSession pins the pid-match fallback (no hook surface):
// fixture shape confirmed live 2026-09-23 against
// ~/.local/share/muse/runtime/muse/sessions/<uuid>.json.
func TestMuseDiscoverSession(t *testing.T) {
	d := testDeps(t)
	dir := filepath.Join(d.UserHome, ".local", "share", "muse", "runtime", "muse", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"schema_version":1,"session_id":"01a0cd08-3c06-7a12-a7dc-c56be5066bc8",` +
		`"session_name":null,"endpoint_hint":"ms-7772700b22e7.sock",` +
		`"workspace_label":"agent-swarm","target_eligibility":"message_capable",` +
		`"process_generation_hint":"pid=96988"}`
	if err := os.WriteFile(filepath.Join(dir, "01a0cd08.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newMuse(d)
	id, ok := a.DiscoverSession(context.Background(), 96988, "/some/workspace")
	if !ok || id != "01a0cd08-3c06-7a12-a7dc-c56be5066bc8" {
		t.Fatalf("DiscoverSession(96988) = %q, %v, want the matching session id", id, ok)
	}
	if _, ok := a.DiscoverSession(context.Background(), 1, "/some/workspace"); ok {
		t.Error("DiscoverSession(1) matched no file, want false")
	}
}

// A malformed registry file (a session mid-write) must be skipped, not
// treated as an error that fails the whole scan.
func TestMuseDiscoverSessionSkipsUnparsableFiles(t *testing.T) {
	d := testDeps(t)
	dir := filepath.Join(d.UserHome, ".local", "share", "muse", "runtime", "muse", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"session_id":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := newMuse(d).DiscoverSession(context.Background(), 96988, ""); ok {
		t.Error("an unparsable fixture must not be reported as a match")
	}
}

// No registry directory at all (called before muse ever writes one) is the
// normal early-startup case, not an error.
func TestMuseDiscoverSessionNoRegistryDir(t *testing.T) {
	d := testDeps(t)
	if _, ok := newMuse(d).DiscoverSession(context.Background(), 1, ""); ok {
		t.Error("an absent registry dir must not be reported as a match")
	}
}

// P0-4: `muse` is a launcher script that `exec`s the real binary
// (muse-bin-<version>), so the pane command becomes the binary's name, seen
// truncated to MAXCOMLEN on macOS (confirmed live 2026-09-23).
func TestMuseProcessNames(t *testing.T) {
	a := newMuse(testDeps(t))
	accept := []string{"muse", "muse-bin-1.3.0-R3401.1", "muse-bin-1.3.0-"}
	reject := []string{"zsh", "node"}
	for _, s := range accept {
		if !anyMatch(a.ProcessNames(), s) {
			t.Errorf("%q should be accepted", s)
		}
	}
	for _, s := range reject {
		if anyMatch(a.ProcessNames(), s) {
			t.Errorf("%q should be rejected", s)
		}
	}
}

func TestMuseInstalled(t *testing.T) {
	d := testDeps(t)
	d.Run = (&execx.Fake{Responses: map[string]execx.Result{
		"muse --version": {Out: "Muse Code 1.3.0 (1.3.0-R3401.1)\n"},
	}}).Runner()
	v, ok := newMuse(d).Installed(context.Background())
	if !ok || v == "" {
		t.Errorf("Installed = %q, %v", v, ok)
	}
	d.Run = (&execx.Fake{}).Runner()
	if _, ok := newMuse(d).Installed(context.Background()); ok {
		t.Error("missing binary must not report installed")
	}
}
