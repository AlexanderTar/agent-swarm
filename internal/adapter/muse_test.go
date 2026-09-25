package adapter

import (
	"context"
	"encoding/json"
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
		TokenFile: "/swarm-home/run/tokens/ses_01",
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
	want := []string{"muse",
		"--model", "muse-spark-1.3-contributor", "--reasoning-effort", "high",
		"--yolo", "--trust-workspace",
		"You are swarm agent login-form-coder (coder) for TASK-101: x."}
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

// TestMuseResumeArgv pins that Resume never appends s.Kickoff: `muse resume
// <sid>` takes exactly one positional, the session ref, confirmed live
// 2026-09-23 (a second positional is parsed as an invalid session name, not
// a prompt). The reminder to call swarm_sync reaches the agent a different
// way -- runtime/pause.go's Resume queues it a message that WakeDue's
// idle-paste fallback delivers once the reattached pane goes idle.
func TestMuseResumeArgv(t *testing.T) {
	d := testDeps(t)
	a := newMuse(d)
	s := museSpec(t)
	s.ProviderSessionID = "uuid-123"
	l, err := a.Resume(s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"muse", "--model", "muse-spark-1.3-contributor", "--reasoning-effort", "high",
		"--yolo", "--trust-workspace", "resume", "uuid-123"}
	if len(l.Argv) != len(want) {
		t.Fatalf("argv = %q, want %q", l.Argv, want)
	}
	for i := range want {
		if l.Argv[i] != want[i] {
			t.Fatalf("argv = %q, want %q", l.Argv, want)
		}
	}
	for _, arg := range l.Argv {
		if strings.Contains(arg, s.Kickoff) {
			t.Fatalf("argv must not carry the kickoff, muse resume takes no prompt argument: %q", l.Argv)
		}
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

// TestMuseLaunchIsolatesXDGConfigHomeWithLiteralSwarmEnv is the P0-1 fix's
// non-live proof: muse only forwards a fixed allowlist (HOME, PATH, USER,
// LANG, TERM and similar) plus a settings.json server's literal `env` map to
// its spawned stdio MCP subprocesses -- it does not expand ${VAR} and does
// not auto-forward its own process env by name (confirmed live 2026-09-23,
// see museMCPEnv's doc). So the swarm MCP server can only authenticate if
// Launch bakes this launch's concrete SWARM_* values into an isolated
// settings.json and points muse at it via XDG_CONFIG_HOME, which is what
// this test asserts byte-for-byte. TestMuseSetupEnvReachesRealMCPSubprocess
// (muse_wake_probe_test.go, MUSE_LIVE_PROBE=1) proves the real binary honors
// that shape end to end.
func TestMuseLaunchIsolatesXDGConfigHomeWithLiteralSwarmEnv(t *testing.T) {
	d := testDeps(t)
	// The operator's real, pre-existing settings.json: another MCP server and
	// unrelated settings that must survive the isolated clone unchanged.
	realDir := filepath.Join(d.UserHome, ".config", "muse")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	realSettings := `{"schema_version":1,"provider":"meta","model":"muse-spark-1.3",` +
		`"mcpServers":{"notion":{"mode":"optional","url":"https://mcp.notion.com/mcp"}}}`
	if err := os.WriteFile(filepath.Join(realDir, "settings.json"), []byte(realSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "auth.json"),
		[]byte(`{"schema_version":1,"providers":{"meta":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(realDir, "skills", "swarm"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A sibling XDG_CONFIG_HOME-rooted tool's config (e.g. gh, git): the
	// isolation must not blind it, since XDG_CONFIG_HOME lands on the whole
	// tmux pane, not just muse.
	if err := os.MkdirAll(filepath.Join(d.UserHome, ".config", "gh"), 0o700); err != nil {
		t.Fatal(err)
	}

	s := museSpec(t)
	l, err := newMuse(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	xdgConfigHome := l.Env["XDG_CONFIG_HOME"]
	if xdgConfigHome == "" {
		t.Fatal("Launch must set XDG_CONFIG_HOME so muse reads the isolated settings.json")
	}

	raw, err := os.ReadFile(filepath.Join(xdgConfigHome, "muse", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["schema_version"] != float64(1) {
		t.Errorf("schema_version = %v, want 1 (muse's loader rejects a file without it)", m["schema_version"])
	}
	if m["provider"] != "meta" || m["model"] != "muse-spark-1.3" {
		t.Errorf("unrelated real settings keys were not cloned: %v", m)
	}
	servers, _ := m["mcpServers"].(map[string]any)
	if _, ok := servers["notion"]; !ok {
		t.Errorf("unrelated real MCP server was dropped: %v", servers)
	}
	swarm, _ := servers["swarm"].(map[string]any)
	if swarm["command"] != s.Bin {
		t.Errorf("swarm.command = %v, want %q", swarm["command"], s.Bin)
	}
	if args, _ := swarm["args"].([]any); len(args) != 1 || args[0] != "mcp" {
		t.Errorf("swarm.args = %v, want [mcp]", args)
	}
	if swarm["mode"] != "optional" {
		t.Errorf("swarm.mode = %v, want optional", swarm["mode"])
	}
	env, _ := swarm["env"].(map[string]any)
	want := map[string]any{
		"SWARM_URL":        s.DaemonURL,
		"SWARM_SESSION":    s.SessionID,
		"SWARM_TOKEN_FILE": s.TokenFile,
		"SWARM_AGENT_KIND": "muse",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("swarm.env[%s] = %v, want %v", k, env[k], v)
		}
	}
	// Literal values, never a template: muse does not expand ${VAR}.
	for k, v := range env {
		if s, ok := v.(string); ok && strings.Contains(s, "${") {
			t.Errorf("swarm.env[%s] = %q must be a literal value, not a template", k, s)
		}
	}

	// auth.json and the user skills dir are symlinked in so provider login
	// and installed skills still work from the isolated config dir -- and
	// must point AT the real files, not just be a symlink of some kind.
	for _, tc := range []struct{ name, wantTarget string }{
		{"auth.json", filepath.Join(realDir, "auth.json")},
		{"skills", filepath.Join(realDir, "skills")},
	} {
		p := filepath.Join(xdgConfigHome, "muse", tc.name)
		fi, err := os.Lstat(p)
		if err != nil {
			t.Errorf("%s not present in isolated config dir: %v", tc.name, err)
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s should be a symlink to the real one", tc.name)
			continue
		}
		if target, err := os.Readlink(p); err != nil || target != tc.wantTarget {
			t.Errorf("%s symlink target = %q, %v; want %q", tc.name, target, err, tc.wantTarget)
		}
	}
	// A sibling ~/.config dir (gh, git, ...) must also be symlinked straight
	// into the isolated XDG_CONFIG_HOME, or every tool but muse goes blind.
	ghLink := filepath.Join(xdgConfigHome, "gh")
	wantGhTarget := filepath.Join(d.UserHome, ".config", "gh")
	if fi, err := os.Lstat(ghLink); err != nil {
		t.Errorf("sibling .config/gh dir not carried into the isolated XDG_CONFIG_HOME: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Error(".config/gh should be a symlink to the real one")
	} else if target, err := os.Readlink(ghLink); err != nil || target != wantGhTarget {
		t.Errorf("gh symlink target = %q, %v; want %q", target, err, wantGhTarget)
	}

	// The whole point of the isolation: the operator's real settings.json
	// must never be mutated by a launch.
	afterReal, err := os.ReadFile(filepath.Join(realDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterReal) != realSettings {
		t.Errorf("Launch must not touch the real settings.json; got %q, want %q", afterReal, realSettings)
	}

	// Resume must isolate the same way.
	s.ProviderSessionID = "prov-abc"
	rl, err := newMuse(d).Resume(s)
	if err != nil {
		t.Fatal(err)
	}
	if rl.Env["XDG_CONFIG_HOME"] == "" {
		t.Error("Resume must also set XDG_CONFIG_HOME")
	}
}

// A missing real settings.json (fresh machine, muse never run) must not fail
// the launch: setupEnv starts from an empty object plus schema_version.
func TestMuseLaunchIsolatesConfigWithNoRealSettingsFile(t *testing.T) {
	d := testDeps(t)
	l, err := newMuse(d).Launch(museSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(l.Env["XDG_CONFIG_HOME"], "muse", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	servers, _ := m["mcpServers"].(map[string]any)
	if _, ok := servers["swarm"].(map[string]any); !ok {
		t.Errorf("swarm entry missing: %v", m)
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

// TestMuseSetupEnvIsolatesHOMEExceptOtherAgentsPersonalRoots is the fix for
// the user's report (PR #20): a spawned muse independently scans
// $HOME/.claude/skills, $HOME/.codex/skills and $HOME/.agents/skills (and
// loads $HOME/.claude/CLAUDE.md) regardless of any XDG_CONFIG_HOME
// isolation -- proven live (docs/plans/2026-09-25-muse-isolation-probe.md,
// Finding 2/2b): isolating XDG_CONFIG_HOME alone removed zero of 13 foreign
// skills. HOME must be isolated too, using a denylist (mirrors the existing
// ~/.config sibling loop's shape) rather than an allowlist: a shell-tool
// call still needs the real .gitconfig, .ssh, toolchains, etc., which an
// unprobed allowlist would silently break.
func TestMuseSetupEnvIsolatesHOMEExceptOtherAgentsPersonalRoots(t *testing.T) {
	d := testDeps(t)
	for _, dir := range []string{".claude", ".codex", ".cursor", ".agents", ".gemini", ".muse"} {
		if err := os.MkdirAll(filepath.Join(d.UserHome, dir, "skills"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Ordinary things a shell-tool call needs: must survive the isolation.
	if err := os.WriteFile(filepath.Join(d.UserHome, ".gitconfig"), []byte("[user]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d.UserHome, "go", "bin"), 0o700); err != nil {
		t.Fatal(err)
	}

	l, err := newMuse(d).Launch(museSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	home := l.Env["HOME"]
	if home == "" {
		t.Fatal("Launch must set HOME to an isolated per-launch dir")
	}
	if home == d.UserHome {
		t.Fatal("HOME must not be the real UserHome")
	}
	for _, excluded := range []string{".claude", ".codex", ".cursor", ".agents", ".gemini", ".config", ".muse"} {
		if _, err := os.Lstat(filepath.Join(home, excluded)); !os.IsNotExist(err) {
			t.Errorf("isolated HOME must not carry %s through, got err=%v", excluded, err)
		}
	}
	for _, name := range []string{".gitconfig", "go"} {
		p := filepath.Join(home, name)
		fi, err := os.Lstat(p)
		if err != nil {
			t.Errorf("isolated HOME missing ordinary entry %s: %v", name, err)
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s should be a symlink to the real one", name)
			continue
		}
		if target, err := os.Readlink(p); err != nil || target != filepath.Join(d.UserHome, name) {
			t.Errorf("%s symlink target = %q, %v; want %q", name, target, err, filepath.Join(d.UserHome, name))
		}
	}
}

// TestMuseSetupEnvPinsDataStateCacheToRealHome: once HOME is isolated, an
// unset XDG_DATA_HOME/XDG_STATE_HOME/XDG_CACHE_HOME would fall through the
// *isolated* HOME (muse's own fallback is $HOME/.local/share etc., confirmed
// from the binary's embedded docs strings), silently moving muse's plugin
// store and session registry away from the real one. The plugin store's own
// integrity check rejects every partial reconstruction we tried (probe
// Finding 5), so these must be pinned explicitly to the real UserHome-rooted
// paths, not left unset.
func TestMuseSetupEnvPinsDataStateCacheToRealHome(t *testing.T) {
	d := testDeps(t)
	l, err := newMuse(d).Launch(museSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"XDG_DATA_HOME":  filepath.Join(d.UserHome, ".local", "share"),
		"XDG_STATE_HOME": filepath.Join(d.UserHome, ".local", "state"),
		"XDG_CACHE_HOME": filepath.Join(d.UserHome, ".cache"),
	}
	for k, v := range want {
		if l.Env[k] != v {
			t.Errorf("%s = %q, want %q", k, l.Env[k], v)
		}
	}
}

// TestMuseResumeUsesSameIsolation: Resume must isolate identically to Launch
// so a relaunch never regains access to the operator's real HOME.
func TestMuseResumeUsesSameIsolation(t *testing.T) {
	d := testDeps(t)
	if err := os.MkdirAll(filepath.Join(d.UserHome, ".claude", "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := museSpec(t)
	s.ProviderSessionID = "prov-xyz"
	l, err := newMuse(d).Resume(s)
	if err != nil {
		t.Fatal(err)
	}
	home := l.Env["HOME"]
	if home == "" || home == d.UserHome {
		t.Fatalf("Resume must also isolate HOME, got %q", home)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Errorf("Resume's isolated HOME must not carry .claude through, got err=%v", err)
	}
	for _, k := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if l.Env[k] == "" {
			t.Errorf("Resume must also pin %s", k)
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
