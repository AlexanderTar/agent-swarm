package adapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// pane and anyMatch are shared by all four adapter test files (R2). They are
// declared here, in a file this task owns; Tasks 7, 8 and 9 use them and declare
// the dependency. They are not in adapter_test.go because Task 3 has no fixtures.
func pane(t *testing.T, agent, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", agent, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// anyMatch reports whether any of res matches s. §11.3's process-name check is
// "does any of these patterns appear", which RE2 cannot express as one pattern
// without alternation over user-supplied strings.
func anyMatch(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func claudeSpec(t *testing.T, d Deps) Spec {
	t.Helper()
	return Spec{AgentName: "login-form-coder", SessionID: "ses_01", Token: "tok",
		// Never :7777 in a fixture, even one that only asserts an argv string (F3):
		// the day someone writes a test that dials Store.DaemonURL, it must not be
		// the user's live daemon. 17778 is the e2e port, which is never up in a unit test.
		DaemonURL: "http://127.0.0.1:17778", Model: "claude-sonnet-5", Effort: "high",
		Cwd: t.TempDir(), Kickoff: "You are swarm agent login-form-coder (coder) for TASK-101: x.",
		Bin: "/usr/local/bin/swarm"}
}

// §11.1: the -- is required, or the variadic channels flag swallows the prompt.
func TestClaudeLaunchArgv(t *testing.T) {
	d := testDeps(t)
	a := newClaude(d)
	l, err := a.Launch(claudeSpec(t, d))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Argv, " ")
	for _, want := range []string{
		"claude", "--session-id", "-n login-form-coder", "--model claude-sonnet-5",
		"--effort high", "--dangerously-skip-permissions", "--mcp-config",
		"--settings", "--dangerously-load-development-channels server:swarm",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q:\n%s", want, joined)
		}
	}
	if l.Argv[len(l.Argv)-2] != "--" {
		t.Fatalf("-- must come right before the kickoff text: %v", l.Argv[len(l.Argv)-3:])
	}
	if l.Argv[len(l.Argv)-1] != claudeSpec(t, d).Kickoff {
		t.Fatalf("last argument = %q", l.Argv[len(l.Argv)-1])
	}
	// the session token never appears in argv (§6.4)
	if strings.Contains(joined, "tok") {
		t.Fatalf("the token leaked into argv: %s", joined)
	}
	// a valid UUID is preassigned
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for i, v := range l.Argv {
		if v == "--session-id" && !uuid.MatchString(l.Argv[i+1]) {
			t.Fatalf("--session-id %q is not a UUIDv4", l.Argv[i+1])
		}
	}
}

func TestClaudeLaunchOmitsEffortWhenUnset(t *testing.T) {
	d := testDeps(t)
	s := claudeSpec(t, d)
	s.Effort = ""
	l, err := newClaude(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(l.Argv, " "), "--effort") {
		t.Fatal("no --effort flag when the level is the agent default (L27)")
	}
}

func TestClaudeSettingsJSONTurnsAttributionOffAndListsEveryHook(t *testing.T) {
	d := testDeps(t)
	s := claudeSpec(t, d)
	s.AdvisorModel = "fable"
	l, err := newClaude(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for i, v := range l.Argv {
		if v == "--settings" {
			path = l.Argv[i+1]
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Attribution struct {
			Commit string `json:"commit"`
			PR     string `json:"pr"`
		} `json:"attribution"`
		IncludeCoAuthoredBy *bool          `json:"includeCoAuthoredBy"`
		AdvisorModel        string         `json:"advisorModel"`
		Hooks               map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("settings JSON does not parse: %v\n%s", err, b)
	}
	if cfg.Attribution.Commit != "" || cfg.Attribution.PR != "" {
		t.Errorf("attribution must be empty strings (L23): %s", b)
	}
	if cfg.IncludeCoAuthoredBy == nil || *cfg.IncludeCoAuthoredBy {
		t.Errorf("includeCoAuthoredBy must be false: %s", b)
	}
	if cfg.AdvisorModel != "fable" {
		t.Errorf("advisorModel = %q", cfg.AdvisorModel)
	}
	for _, e := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact", "Stop"} {
		if _, ok := cfg.Hooks[e]; !ok {
			t.Errorf("hook %s missing: %s", e, b)
		}
	}
	if strings.Contains(string(b), "advisorModel\": \"\"") {
		t.Error("advisorModel must be left out when there is no native advisor")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings file mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestClaudeSettingsJSONBlanksTheUserStatusLine(t *testing.T) {
	d := testDeps(t)
	s := claudeSpec(t, d)
	l, err := newClaude(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for i, v := range l.Argv {
		if v == "--settings" {
			path = l.Argv[i+1]
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		StatusLine *struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("settings JSON does not parse: %v\n%s", err, b)
	}
	// An object, never null: Claude Code rejects "statusLine": null with a blocking settings-error dialog.
	if cfg.StatusLine == nil || cfg.StatusLine.Type != "command" || cfg.StatusLine.Command != "true" {
		t.Errorf("statusLine must be {type:command, command:true} to blank the user's status line: %s", b)
	}
}

func TestClaudeMCPConfigJSON(t *testing.T) {
	d := testDeps(t)
	l, err := newClaude(d).Launch(claudeSpec(t, d))
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for i, v := range l.Argv {
		if v == "--mcp-config" {
			path = l.Argv[i+1]
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	sw := cfg.Servers["swarm"]
	if sw.Type != "stdio" || sw.Command != "/usr/local/bin/swarm" || strings.Join(sw.Args, " ") != "mcp" {
		t.Fatalf("mcp config = %s", b)
	}
	if len(sw.Env) != 0 {
		t.Fatalf("claude inherits the environment; no env block belongs here (P0-1): %s", b)
	}
}

func TestClaudeMCPConfigIsIsolated(t *testing.T) {
	d := testDeps(t)
	// Even if user has .claude.json with other servers, claude-mcp.json must only contain swarm
	userJSON := []byte(`{"mcpServers":{"neon":{"type":"http","url":"https://mcp.neon.tech"}}}`)
	if err := os.WriteFile(filepath.Join(d.UserHome, ".claude.json"), userJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := claudeSpec(t, d)
	spec.Instructions = "# Custom Instructions\nFollow TDD strictly."
	l, err := newClaude(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}

	var mcpPath, promptFilePath string
	var hasStrictMCP, hasSettingSources bool
	for i, v := range l.Argv {
		if v == "--mcp-config" {
			mcpPath = l.Argv[i+1]
		}
		if v == "--append-system-prompt-file" {
			promptFilePath = l.Argv[i+1]
		}
		if v == "--strict-mcp-config" {
			hasStrictMCP = true
		}
		if v == "--setting-sources" && i+1 < len(l.Argv) && l.Argv[i+1] == "project,local" {
			hasSettingSources = true
		}
	}

	if !hasStrictMCP {
		t.Errorf("expected --strict-mcp-config flag in Claude argv")
	}
	if !hasSettingSources {
		t.Errorf("expected --setting-sources project,local in Claude argv")
	}
	if promptFilePath == "" {
		t.Fatalf("expected --append-system-prompt-file in Claude argv")
	}
	content, err := os.ReadFile(promptFilePath)
	if err != nil || string(content) != spec.Instructions {
		t.Fatalf("prompt file content = %q, want %q", string(content), spec.Instructions)
	}

	b, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Servers["neon"] != nil {
		t.Errorf("expected neon to be excluded from isolated mcp config")
	}
	if cfg.Servers["swarm"] == nil {
		t.Errorf("expected swarm to be present in mcpServers")
	}
}

// 2026-09-23, root-caused live: --setting-sources project,local (added
// yesterday to keep ~/.claude/CLAUDE.md out) also hides ~/.claude/skills and
// whatever registry the channels feature resolves "server:swarm" against --
// both confirmed by direct reproduction, not a timing race (see the spec).
// A project-scope .mcp.json in the session's own scratch cwd fixes the
// channels side deterministically.
func TestClaudeLaunchWritesProjectScopeMCPConfig(t *testing.T) {
	d := testDeps(t)
	s := claudeSpec(t, d)
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Cwd, ".mcp.json"))
	if err != nil {
		t.Fatalf("expected .mcp.json in the session cwd: %v", err)
	}
	var cfg struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("project .mcp.json does not parse: %v\n%s", err, b)
	}
	sw := cfg.Servers["swarm"]
	if sw.Type != "stdio" || sw.Command != s.Bin || strings.Join(sw.Args, " ") != "mcp" {
		t.Fatalf("project .mcp.json swarm entry = %+v", sw)
	}
}

// seedSkillsHome stands in for the daemon's own SyncSkills, which in
// production has already populated Deps.Home/skills/<name> before any agent
// is ever spawned (unit 1.1). testDeps gives Home and UserHome unrelated temp
// dirs, so tests that exercise the per-spawn skill link must seed this
// themselves.
func seedSkillsHome(t *testing.T, home string) {
	t.Helper()
	for _, name := range install.SkillNames() {
		p := filepath.Join(home, "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, install.SkillBody(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Same bug, skill side: Skill(swarm) was "Unknown skill: swarm" 100% of the
// time under --setting-sources project,local (verified live, zero-delay
// repro) because ~/.claude/skills is "user" scope. Project-scope copies in
// the session's own cwd fix it without reopening the excluded user scope.
func TestClaudeLaunchWritesProjectScopeSkills(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	s := claudeSpec(t, d)
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatal(err)
	}
	for _, name := range install.SkillNames() {
		got, err := os.ReadFile(filepath.Join(s.Cwd, ".claude", "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatalf("expected %s skill reachable in the session cwd: %v", name, err)
		}
		if string(got) != string(install.SkillBody(name)) {
			t.Errorf("%s skill body does not match the embedded source", name)
		}
	}
}

// Resume() shares flags() with Launch(); this must not regress on resume.
func TestClaudeResumeWritesProjectScopeSkillsAndMCP(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	s := claudeSpec(t, d)
	s.ProviderSessionID = "11111111-2222-4333-8444-555555555555"
	if _, err := newClaude(d).Resume(s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Cwd, ".mcp.json")); err != nil {
		t.Errorf("resume: expected .mcp.json in the session cwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Cwd, ".claude", "skills", "swarm", "SKILL.md")); err != nil {
		t.Errorf("resume: expected the swarm skill in the session cwd: %v", err)
	}
}

// A1, unit 1.3: the per-spawn skill exposure is a real symlink into
// ~/.swarm/skills (Deps.Home/skills here), matching swarm install's own
// choice for Claude (Symlink mode), not a copy of every file — the
// ui-ux-pro-max data alone is 3.1 MB.
func TestClaudeProjectConfigLinksSkills(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	s := claudeSpec(t, d)
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatal(err)
	}
	skillsHome := filepath.Join(d.Home, "skills")
	for _, name := range install.SkillNames() {
		link := filepath.Join(s.Cwd, ".claude", "skills", name)
		fi, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is not a symlink", link)
		}
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(skillsHome, name); target != want {
			t.Errorf("%s -> %s, want %s", link, target, want)
		}
	}
}

// Review round 1, Major 2: a pre-existing real (non-symlink) skill directory
// in the spawn's own scratch cwd -- left by an older, copy-based
// writeProjectSwarmConfig, or simply stale from a previous launch this same
// cwd was somehow reused for -- is swarm-owned by construction: every file
// under this cwd is the daemon's own (internal/runtime/agents.go creates it
// fresh right before Launch/Resume, never a real git worktree the user
// touches). It must be replaced with v2's own symlink, not left alone as if
// it were the user's.
func TestClaudeProjectConfigAdoptsAPreExistingRealSkillDir(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	s := claudeSpec(t, d)
	old := filepath.Join(s.Cwd, ".claude", "skills", "swarm")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "SKILL.md"), []byte("stale copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(old)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the pre-existing real skill dir in the spawn cwd was not replaced with v2's symlink")
	}
}

func TestClaudeOmitsInstructionsWhenUnset(t *testing.T) {
	d := testDeps(t)
	spec := claudeSpec(t, d)
	spec.Instructions = ""
	l, err := newClaude(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range l.Argv {
		if v == "--append-system-prompt-file" {
			t.Errorf("expected no --append-system-prompt-file flag when instructions are empty")
		}
	}
}

// §11.1 resume: the same flags, --resume <uuid>, and no --session-id.
// UNVERIFIED: no probe ran a claude resume (see the Phase 0 note in the header).
func TestClaudeResumeUsesTheProviderID(t *testing.T) {
	d := testDeps(t)
	s := claudeSpec(t, d)
	s.ProviderSessionID = "11111111-2222-4333-8444-555555555555"
	s.Kickoff = "resuming"
	l, err := newClaude(d).Resume(s)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Argv, " ")
	if !strings.Contains(joined, "--resume "+s.ProviderSessionID) {
		t.Fatalf("argv = %s", joined)
	}
	if strings.Contains(joined, "--session-id") {
		t.Fatal("--resume and --session-id are mutually exclusive")
	}
	if l.Argv[len(l.Argv)-2] != "--" || l.Argv[len(l.Argv)-1] != "resuming" {
		t.Fatalf("tail = %v", l.Argv[len(l.Argv)-2:])
	}
}

// P0-4: the native binary's pane command is its version; a shell is rejected (I2).
func TestClaudeProcessNames(t *testing.T) {
	a := newClaude(testDeps(t))
	accept := []string{"2.1.274", "claude"}
	reject := []string{"zsh", "bash", "node", "", "claude-code"}
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

// P0-4: claude keeps drawing the empty prompt while busy, so the spinner decides.
func TestClaudeIdleAgainstTheCapturedPanes(t *testing.T) {
	a := newClaude(testDeps(t))
	if !a.Idle(pane(t, "claude", "pane-idle.txt")) {
		t.Error("pane-idle.txt should be idle")
	}
	if a.Idle(pane(t, "claude", "pane-busy.txt")) {
		t.Error("pane-busy.txt draws the empty prompt but must not count as idle")
	}
	// Live incident (2026-09-19): the spinner line renders with a leading SGR
	// color escape (`\x1b[38;5;174m·...`), so the plain, unanchored-for-ANSI
	// Busy regex never matched it and a genuinely busy session got reported
	// idle/waiting.
	if a.Idle(pane(t, "claude", "pane-busy-ansi.txt")) {
		t.Error("pane-busy-ansi.txt's spinner is ANSI-prefixed but must still count as busy")
	}
	if a.Idle(pane(t, "claude", "pane-input-nonempty.txt")) {
		t.Error("typed input is not idle")
	}
	// 2026-09-19, live incident: Claude 2.1.278 draws a dim "next action"
	// suggestion after the prompt when idle with an empty input box (a real
	// captured pane showed "\x1b[39m❯ \x1b[2mcheck on s0.1 progress\x1b[0m").
	// The old regex required nothing but the bare prompt, so this was never
	// detected as idle: watchStartup's Idle check never fired and wake.go's
	// idle-paste never fired either, leaving a real pending message
	// undelivered forever.
	if !a.Idle(pane(t, "claude", "pane-idle-with-suggestion.txt")) {
		t.Error("a dim next-action suggestion after the prompt is still idle")
	}
}

func TestClaudeIdle(t *testing.T) {
	promptWithTrailingSpaces := "\x1b[39m❯\u00a0                                        "
	if !claudeIdle.MatchString(promptWithTrailingSpaces) {
		t.Errorf("expected prompt with trailing spaces to match claudeIdle: %q", promptWithTrailingSpaces)
	}

	promptWithSuggestionAndTrailing := "\x1b[39m❯\u00a0\x1b[2mcheck on s0.1 progress\x1b[0m   "
	if !claudeIdle.MatchString(promptWithSuggestionAndTrailing) {
		t.Errorf("expected prompt with dim suggestion and trailing spaces to match claudeIdle: %q", promptWithSuggestionAndTrailing)
	}

	promptWithHumanInput := "❯\u00a0half typed by human"
	if claudeIdle.MatchString(promptWithHumanInput) {
		t.Errorf("expected prompt with human input to fail matching claudeIdle: %q", promptWithHumanInput)
	}
}

// The scrape-side trust matcher carries the same guard as the startup dialog: the keys
// (Down+Enter, which would pick "No, exit" if the list order differed) are only pressed
// when the "Yes, I trust this folder" line is on screen. The dev-channels one has none.
func TestClaudePromptPatternsGuardTrustWithYesLine(t *testing.T) {
	ps := newClaude(testDeps(t)).PromptPatterns()
	if len(ps) != 2 {
		t.Fatalf("want 2 prompt patterns, got %d", len(ps))
	}
	trust, dev := ps[0], ps[1]
	screen := pane(t, "claude", "pane-dialog-trust.txt")
	if !trust.Match.MatchString(screen) {
		t.Error("the trust matcher does not match its screen")
	}
	if trust.Require == nil || !trust.Require.MatchString(screen) {
		t.Error("the trust matcher must require the 'Yes, I trust this folder' line")
	}
	if trust.Action != "Down+Enter" {
		t.Errorf("trust action = %q", trust.Action)
	}
	if dev.Require != nil {
		t.Error("the dev-channels matcher has no Require, like its startup dialog")
	}
}

// §11.1, P0-4: trust defaults to "No, exit", so the keys are Down then Enter, and
// they are only sent when the trust line is on screen.
func TestClaudeStartupDialogs(t *testing.T) {
	a := newClaude(testDeps(t))
	ds := a.StartupDialogs()
	if len(ds) != 2 {
		t.Fatalf("want 2 dialogs, got %d", len(ds))
	}
	trust, dev := ds[0], ds[1]
	screen := pane(t, "claude", "pane-dialog-trust.txt")
	if !trust.Match.MatchString(screen) {
		t.Error("the trust dialog pattern does not match its screen")
	}
	if trust.Require == nil || !trust.Require.MatchString(screen) {
		t.Error("the trust dialog must require the 'Yes, I trust this folder' line")
	}
	if strings.Join(trust.Keys, ",") != "Down,Enter" {
		t.Fatalf("trust keys = %v", trust.Keys)
	}
	if trust.Fail {
		t.Error("the trust dialog is answered, not failed")
	}
	devScreen := pane(t, "claude", "pane-dialog-devchannels.txt")
	if !dev.Match.MatchString(devScreen) || strings.Join(dev.Keys, ",") != "Enter" {
		t.Fatalf("dev-channels dialog = %+v", dev)
	}
	// neither pattern fires on an idle screen or on a child of a trusted folder
	for _, f := range []string{"pane-idle.txt", "pane-child-of-trusted-no-dialog.txt"} {
		s := pane(t, "claude", f)
		if trust.Match.MatchString(s) || dev.Match.MatchString(s) {
			t.Errorf("a dialog pattern matched %s", f)
		}
	}
}

func TestClaudeHookOutputShapes(t *testing.T) {
	a := newClaude(testDeps(t))
	ctxOut, _ := a.HookOutput("SessionStart", HookDecision{Context: "T"})
	if string(ctxOut) != `{"hookSpecificOutput":{"additionalContext":"T","hookEventName":"SessionStart"}}` {
		t.Errorf("context output = %s", ctxOut)
	}
	deny, _ := a.HookOutput("PreToolUse", HookDecision{Block: true, Reason: "R"})
	var m map[string]map[string]string
	json.Unmarshal(deny, &m)
	if m["hookSpecificOutput"]["permissionDecision"] != "deny" ||
		m["hookSpecificOutput"]["permissionDecisionReason"] != "R" ||
		m["hookSpecificOutput"]["hookEventName"] != "PreToolUse" {
		t.Errorf("deny output = %s", deny)
	}
	stop, _ := a.HookOutput("Stop", HookDecision{Block: true, Reason: "R"})
	if string(stop) != `{"decision":"block","reason":"R"}` {
		t.Errorf("stop output = %s", stop)
	}
	empty, _ := a.HookOutput("PostToolUse", HookDecision{})
	if len(empty) != 0 {
		t.Errorf("an empty decision prints nothing, got %s", empty)
	}
}

func TestClaudeParseHook(t *testing.T) {
	a := newClaude(testDeps(t))
	in, err := a.ParseHook("SessionStart", []byte(`{"session_id":"abc","source":"compact","cwd":"/w","transcript_path":"/t.jsonl","hook_event_name":"SessionStart"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.ProviderSessionID != "abc" || in.Source != "compact" || in.Cwd != "/w" || in.TranscriptPath != "/t.jsonl" {
		t.Fatalf("parsed = %+v", in)
	}
	tool, _ := a.ParseHook("PreToolUse", []byte(`{"session_id":"abc","tool_name":"mcp__swarm__swarm_sync"}`))
	if !tool.IsSwarmTool {
		t.Error("mcp__swarm__* is a swarm tool")
	}
	sh, _ := a.ParseHook("PreToolUse", []byte(`{"session_id":"abc","tool_name":"Bash","tool_input":{"command":"git commit -m x"}}`))
	if sh.IsSwarmTool || sh.Command != "git commit -m x" || sh.ToolName != "Bash" {
		t.Fatalf("shell parse = %+v", sh)
	}
}

// The native wake goes through the channel bridge: the daemon publishes, the shim delivers.
func TestClaudeWakePublishes(t *testing.T) {
	d := testDeps(t)
	var got string
	d.PublishWake = func(ctx context.Context, sessionID, notice string) (bool, error) {
		got = sessionID + "|" + notice
		return true, nil
	}
	ok, err := newClaude(d).Wake(context.Background(), WakeTarget{SessionID: "ses_1", Notice: "N"})
	if err != nil || !ok {
		t.Fatalf("Wake = %v, %v", ok, err)
	}
	if got != "ses_1|N" {
		t.Fatalf("published %q", got)
	}
}

// Publishing into the void is not a delivery. The claude bridge fans a notice
// out to whatever SubscribeWake handed out for the session; with the mcpshim
// not connected there is nobody subscribed, and reporting delivered=true then
// makes WakeDue record a wake that never reached anyone -- so the session sits
// silent until the cooldown expires instead of falling to the paste at once.
func TestClaudeWakeIsNotDeliveredWithoutASubscriber(t *testing.T) {
	d := testDeps(t)
	d.PublishWake = func(ctx context.Context, sessionID, notice string) (bool, error) {
		return false, nil // published, but nobody is listening
	}
	ok, err := newClaude(d).Wake(context.Background(), WakeTarget{SessionID: "ses_1", Notice: "N"})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Wake reported delivered with no subscriber on the channel bridge")
	}
}

// UNVERIFIED: §11.1 states `claude auth status --json` with `.loggedIn`; no probe
// ran it. Preflight treats an unparseable result as "not signed in" (fail safe).
func TestClaudeChecks(t *testing.T) {
	d := testDeps(t)
	fake := &execx.Fake{Responses: map[string]execx.Result{
		"claude --version":          {Out: "2.1.274 (Claude Code)\n"},
		"claude auth status --json": {Out: `{"loggedIn":true}`},
	}}
	d.Run = fake.Runner()
	a := newClaude(d)
	if v, ok := a.Installed(context.Background()); !ok || v != "2.1.274" {
		t.Fatalf("Installed = %q, %v", v, ok)
	}
	if err := a.AuthOK(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.Responses["claude auth status --json"] = execx.Result{Out: `{"loggedIn":false}`}
	err := a.AuthOK(context.Background())
	if err == nil || err.Error() != "Claude isn't signed in. Run `claude /login` in a terminal." {
		t.Fatalf("err = %v", err)
	}
}

func TestClaudeSuperpowersCheckNeedsBrainstormingAndTDD(t *testing.T) {
	d := testDeps(t)
	base := filepath.Join(d.UserHome, ".claude", "plugins", "cache", "obra", "superpowers", "6.3.0", "skills")
	if err := os.MkdirAll(filepath.Join(base, "brainstorming"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(base, "brainstorming", "SKILL.md"), []byte("x"), 0o644)
	a := newClaude(d)
	if a.SuperpowersInstalled() {
		t.Fatal("the TDD skill is also required (§23.3 install coverage)")
	}
	os.MkdirAll(filepath.Join(base, "test-driven-development"), 0o755)
	os.WriteFile(filepath.Join(base, "test-driven-development", "SKILL.md"), []byte("x"), 0o644)
	if !a.SuperpowersInstalled() {
		t.Fatal("both skills present should pass")
	}
}

// 2026-09-21, live: Claude 2.1.278 draws an idle, empty, suggestion-less
// prompt as "❯ NBSP + reverse-video cursor cell". claudeIdle only allowed an
// SGR-2 suggestion after the prompt, so idle agents were never pasted and a
// message sat pending for 11+ minutes (msg_01M31G7G6PT7G7DKKPH91EZK5H). The
// three captures come from three live agents via
// `tmux capture-pane -p -e -J`.
func TestClaudeIdleAcceptsTheReverseVideoCursorCell(t *testing.T) {
	a := newClaude(testDeps(t))
	for _, f := range []string{
		"pane-idle-cursor-default-fg.txt",
		"pane-idle-cursor-grey-prompt.txt",
		"pane-idle-cursor-reset-fg.txt",
	} {
		if !a.Idle(pane(t, "claude", f)) {
			t.Errorf("%s: idle prompt with a cursor cell must count as idle", f)
		}
	}
	for _, s := range []string{
		"\x1b[39m❯ \x1b[7m \x1b[0m",        // bare cursor cell
		"\x1b[39m❯ \x1b[7m \x1b[0m   ",     // padded by tmux
		"\x1b[38;5;246m❯ \x1b[7m\x1b[39m ", // SGR between the cell attribute and the space
	} {
		if !claudeIdle.MatchString(s) {
			t.Errorf("expected idle: %q", s)
		}
	}
	for _, s := range []string{
		"\x1b[39m❯ half typed",                // draft
		"\x1b[39m❯ \x1b[7mh\x1b[0malf typed",  // cursor on the first character
		"\x1b[39m❯ half typed\x1b[7m \x1b[0m", // cursor after the draft
	} {
		if claudeIdle.MatchString(s) {
			t.Errorf("a human draft must not count as idle: %q", s)
		}
	}
	if a.Idle(pane(t, "claude", "pane-input-nonempty.txt")) {
		t.Error("pane-input-nonempty.txt must stay non-idle")
	}
}
