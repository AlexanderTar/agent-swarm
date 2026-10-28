package adapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/grokhome"
)

// Live probes against the real grok CLI. Each run is a paid headless turn on
// the signed-in account (about 27k input tokens), so they are gated:
//
//	GROK_LIVE_PROBE=1 go test ./internal/adapter/ -run 'TestGrokLive' -v
//
// They run grok under a throwaway Swarm home (grokhome.Env) whose config.toml
// registers dump hooks, sharing the user's ~/.grok auth through links like
// `swarm install` does.

const grokLiveDump = "GROK_PROBE_DUMP"

// grokLiveSetup builds the throwaway home and returns the env grok runs with,
// the dump dir its hooks write to, and a file whose content a PreToolUse hook
// prints (empty: the hook prints nothing).
func grokLiveSetup(t *testing.T) (env []string, dump, preReply string) {
	env, _, dump, preReply = grokLiveSetupHome(t)
	return env, dump, preReply
}

// grokLiveSetupHome is grokLiveSetup that also returns the Swarm home.
func grokLiveSetupHome(t *testing.T) (env []string, home, dump, preReply string) {
	t.Helper()
	if os.Getenv("GROK_LIVE_PROBE") != "1" {
		t.Skip("live probe against the real grok CLI; set GROK_LIVE_PROBE=1 to run")
	}
	if _, err := exec.LookPath(grokhome.Bin); err != nil {
		t.Skipf("no %s on PATH", grokhome.Bin)
	}
	home = shortTempDir(t) // the leader socket lives under it: sun_path is 104 bytes
	gh := grokhome.Dir(home)
	if err := os.MkdirAll(gh, 0o700); err != nil {
		t.Fatal(err)
	}
	user, _ := os.UserHomeDir()
	for _, f := range []string{"auth.json", "auth.json.lock"} {
		if _, err := os.Stat(filepath.Join(user, ".grok", f)); err == nil {
			if err := os.Symlink(filepath.Join(user, ".grok", f), filepath.Join(gh, f)); err != nil {
				t.Fatal(err)
			}
		}
	}
	dump = t.TempDir()
	preReply = filepath.Join(t.TempDir(), "pre-reply.json")
	script := filepath.Join(t.TempDir(), "hook.sh")
	body := "#!/bin/sh\ncat > \"$" + grokLiveDump + "/$1.$$.json\"\n" +
		"if [ \"$1\" = PreToolUse ] && [ -s \"" + preReply + "\" ]; then cat \"" + preReply + "\"; fi\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	var cfg strings.Builder
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "SessionEnd"} {
		cfg.WriteString("[[hooks." + ev + "]]\n\n[[hooks." + ev + ".hooks]]\ntype = \"command\"\ncommand = \"sh " + script + " " + ev + "\"\n\n")
	}
	if err := os.WriteFile(filepath.Join(gh, "config.toml"), []byte(cfg.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	env = os.Environ()
	for k, v := range grokhome.Env(home) {
		env = append(env, k+"="+v)
	}
	return append(env, grokLiveDump+"="+dump), home, dump, preReply
}

// grokLiveRun runs one headless turn and returns its combined output.
func grokLiveRun(t *testing.T, env []string, prompt string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, grokhome.Bin, "-p", prompt, "-m", "grok-4.7", "--reasoning-effort", "low",
		"--always-approve", "--trust", "--no-subagents", "--output-format", "json", "--max-turns", "4")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("grok: %v\n%s", err, out)
	}
	return string(out)
}

// grokLiveHooks parses every dumped payload with the adapter, keyed by event.
func grokLiveHooks(t *testing.T, dump string) map[string][]HookInput {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dump, "*.json"))
	g := newGrok(Deps{})
	got := map[string][]HookInput{}
	for _, f := range files {
		ev := strings.SplitN(filepath.Base(f), ".", 2)[0]
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		in, err := g.ParseHook(ev, raw)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		got[ev] = append(got[ev], in)
	}
	return got
}

// TestGrokLiveHookPayloadsParse proves the adapter reads what a real grok
// session sends for every hook a plain turn fires.
func TestGrokLiveHookPayloadsParse(t *testing.T) {
	env, dump, _ := grokLiveSetup(t)
	grokLiveRun(t, env, "Run the shell command `echo hi` with your terminal tool, then reply with the single word done.")
	hooks := grokLiveHooks(t, dump)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		if len(hooks[ev]) == 0 {
			t.Fatalf("no %s payload dumped; got events %v", ev, hooks)
		}
		for _, in := range hooks[ev] {
			if in.ProviderSessionID == "" || in.Event != ev {
				t.Errorf("%s payload parsed to %+v", ev, in)
			}
		}
	}
	var sawShell bool
	for _, in := range hooks["PreToolUse"] {
		if in.Command == "echo hi" {
			sawShell = true
		}
	}
	if !sawShell {
		t.Errorf("no PreToolUse carried the shell command: %+v", hooks["PreToolUse"])
	}
	var ended bool
	for _, in := range hooks["Stop"] {
		if in.Reason == "end_turn" && !in.StopHookActive {
			ended = true
		}
	}
	if !ended {
		t.Errorf("no end_turn Stop: %+v", hooks["Stop"])
	}
}

// TestGrokLiveAskUserQuestionDeny proves a PreToolUse deny built by
// Grok.HookOutput suppresses the ask_user_question card and its reason reaches
// the model.
func TestGrokLiveAskUserQuestionDeny(t *testing.T) {
	env, dump, preReply := grokLiveSetup(t)
	const sentinel = "SWARM-LIVE-DENY-7f3a"
	deny, err := newGrok(Deps{}).HookOutput("PreToolUse", HookDecision{Block: true,
		Reason: "[swarm] Your question went to the user as request " + sentinel + ". End your turn now."})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preReply, deny, 0o600); err != nil {
		t.Fatal(err)
	}
	out := grokLiveRun(t, env, "Call the ask_user_question tool once with the question \"Tea or coffee?\" and the options Tea and Coffee. "+
		"Then reply with the exact denial reason you received, nothing else.")
	if !strings.Contains(out, sentinel) {
		t.Errorf("the deny reason did not reach the model:\n%s", out)
	}
	var asked bool
	for _, in := range grokLiveHooks(t, dump)["PreToolUse"] {
		if in.ToolName == "ask_user_question" && strings.Contains(string(in.RawToolInput), "Tea or coffee?") {
			asked = true
		}
	}
	if !asked {
		t.Error("no PreToolUse payload for ask_user_question")
	}
}

// TestGrokLeaderLive proves the whole leader path on the real CLI, in a private
// tmux server (never the daemon's): the TUI starts its own leader from the
// launch flags, Wake submits a prompt through the leader socket to the idle
// session, the leader's pushes tell the watcher when a turn runs (so a Wake
// mid-turn is held back and one after it delivers), and the sweep stops the
// leader by pid afterwards. It spends three short turns:
//
//	GROK_LIVE_PROBE=1 go test ./internal/adapter/ -run TestGrokLeaderLive -v
func TestGrokLeaderLive(t *testing.T) {
	env, home, _, _ := grokLiveSetupHome(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux on PATH")
	}
	g := newGrok(Deps{Home: home, Log: t.Logf})
	const sessionID, providerID = "ses_live", "7d1c0f3e-5b1a-4c8e-9a52-0c3f9e1b2a64"
	sock, ok := g.leaderSocket(sessionID)
	if !ok {
		t.Fatalf("leader socket path %q does not fit sun_path", sock)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	tmuxSock := filepath.Join(shortTempDir(t), "t.sock")
	tmux := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-S", tmuxSock}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	argv := append([]string{"env"}, env...)
	argv = append(argv, grokhome.Bin, "-m", "grok-4.7", "--reasoning-effort", "low", "--always-approve", "--trust",
		"--no-subagents", "--session-id", providerID, "--leader", "--leader-socket", sock)
	tmux(append([]string{"new-session", "-d", "-s", "w", "-x", "200", "-y", "50", "-c", cwd}, argv...)...)
	t.Cleanup(func() {
		g.SweepLeaders(map[string]bool{}) // the session is over: stop its leader by pid
		exec.Command("tmux", "-S", tmuxSock, "kill-server").Run()
	})
	pane := func() string { return StripANSI(tmux("capture-pane", "-p", "-t", "w")) }
	waitFor := func(what string, timeout time.Duration, cond func(string) bool) string {
		t.Helper()
		for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(time.Second) {
			b, _ := exec.Command("tmux", "-S", tmuxSock, "capture-pane", "-p", "-t", "w").Output()
			if p := StripANSI(string(b)); cond(p) {
				return p
			}
		}
		t.Fatalf("timed out waiting for %s; pane:\n%s", what, pane())
		return ""
	}
	waitFor("an idle grok", 90*time.Second, func(p string) bool { return g.Idle(p) })
	waitFor("the leader socket", 30*time.Second, func(string) bool { return listening(sock) })
	g.SweepLeaders(map[string]bool{sessionID: true}) // starts the register-only watcher the busy gate reads

	w := WakeTarget{SessionID: sessionID, ProviderSessionID: providerID, Notice: "Reply with the single word: IDLEWAKE"}
	// The TUI is idle a moment before the leader knows its session (-32602
	// Invalid params, logged by the Wake goroutine), so the first wake may need
	// a second try, as the runtime's next tick would give it.
	answered := false
	for try := 0; try < 4 && !answered; try++ {
		if delivered, err := g.Wake(context.Background(), w); err != nil || !delivered {
			t.Fatalf("idle Wake = %v, %v", delivered, err)
		}
		for end := time.Now().Add(30 * time.Second); time.Now().Before(end) && !answered; time.Sleep(time.Second) {
			b, _ := exec.Command("tmux", "-S", tmuxSock, "capture-pane", "-p", "-t", "w").Output()
			// The prompt echo is one occurrence, the reply the second.
			answered = strings.Count(StripANSI(string(b)), "IDLEWAKE") >= 2 && g.Idle(StripANSI(string(b)))
		}
	}
	if !answered {
		t.Fatalf("the idle wake was never answered; pane:\n%s", pane())
	}
	waitFor("the leader to report the idle wake's turn over", 30*time.Second, func(string) bool { return !g.LeaderBusy(sessionID) })

	w.Notice = "Run the shell command `sleep 12`, then reply with the single word: FIRSTDONE"
	if delivered, err := g.Wake(context.Background(), w); err != nil || !delivered {
		t.Fatalf("Wake = %v, %v", delivered, err)
	}
	waitFor("the first turn to start", time.Minute, func(p string) bool { return g.Busy().MatchString(p) })
	// The probe: _x.ai/sessions/changed must reach a register-only client, or
	// LeaderBusy (and the busy gate in Wake) never sees a turn.
	waitFor("the leader to push that a turn is running", 30*time.Second, func(string) bool { return g.LeaderBusy(sessionID) })
	w.Notice = "Reply with the single word: QUEUEDWAKE"
	if delivered, err := g.Wake(context.Background(), w); err != nil || delivered {
		t.Fatalf("mid-turn Wake = %v, %v, want false: the hooks carry the mail to a working agent", delivered, err)
	}
	waitFor("the first turn to finish", 3*time.Minute, func(p string) bool {
		return strings.Count(p, "FIRSTDONE") >= 1 && g.Idle(p)
	})
	waitFor("the leader to push that the turn is over", 30*time.Second, func(string) bool { return !g.LeaderBusy(sessionID) })
	if delivered, err := g.Wake(context.Background(), w); err != nil || !delivered {
		t.Fatalf("Wake after the turn = %v, %v", delivered, err)
	}
	final := waitFor("the queued wake to be answered", 2*time.Minute, func(p string) bool {
		return strings.Count(p, "QUEUEDWAKE") >= 2 && g.Idle(p)
	})
	t.Logf("final pane:\n%s", final)

	// The session ends first: the leader outlives its pane, and one whose TUI is
	// still attached ignores the SIGTERM. The runtime sweeps every tick, so this
	// does too.
	tmux("kill-session", "-t", "w")
	for end := time.Now().Add(30 * time.Second); listening(sock); time.Sleep(time.Second) {
		if time.Now().After(end) {
			t.Fatal("timed out waiting for the leader to stop")
		}
		g.SweepLeaders(map[string]bool{})
	}
}
