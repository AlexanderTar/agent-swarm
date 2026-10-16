package adapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
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
	t.Helper()
	if os.Getenv("GROK_LIVE_PROBE") != "1" {
		t.Skip("live probe against the real grok CLI; set GROK_LIVE_PROBE=1 to run")
	}
	if _, err := exec.LookPath(grokhome.Bin); err != nil {
		t.Skipf("no %s on PATH", grokhome.Bin)
	}
	home := t.TempDir()
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
	return append(env, grokLiveDump+"="+dump), dump, preReply
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
