package spawn

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// newSpawner gives each test its own tmux server on a private socket.
func newSpawner(t *testing.T) *Spawner {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(conf, TmuxConf(), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Spawner{Socket: fmt.Sprintf("swarm-test-%d-%s", os.Getpid(), t.Name()),
		Conf: conf, Tmux: bin, Run: execx.Run, Log: func(string, ...any) {}}
	t.Cleanup(func() { s.Run(context.Background(), s.Tmux, "-L", s.Socket, "kill-server") })
	return s
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStartPassesTheEnvironmentIntoThePane(t *testing.T) {
	s := newSpawner(t)
	ctx := context.Background()
	env := map[string]string{"USER": "someone", "HOME": "/tmp/home", "PATH": "/usr/bin",
		"SWARM_SESSION": "ses_1", "SWARM_AGENT_KIND": "fake"}
	if err := s.Start(ctx, "envtest", t.TempDir(), env, []string{"sh", "-c", "while :; do sleep 1; done"}); err != nil {
		t.Fatal(err)
	}
	for k, want := range env {
		got, err := s.Env(ctx, "envtest", k)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestPanesReportsCommandDeadAndExitStatus(t *testing.T) {
	s := newSpawner(t)
	ctx := context.Background()
	if err := s.Start(ctx, "alive", t.TempDir(), nil, []string{"sh", "-c", "while :; do sleep 1; done"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx, "gone", t.TempDir(), nil, []string{"sh", "-c", "exit 3"}); err != nil {
		t.Fatal(err)
	}
	var alive, dead bool
	waitFor(t, "the dead pane", func() bool {
		ps, err := s.Panes(ctx)
		if err != nil {
			return false
		}
		alive, dead = false, false
		for _, p := range ps {
			switch p.Session {
			case "alive":
				alive = !p.Dead && p.Command != ""
			case "gone":
				dead = p.Dead && p.DeadStatus == 3
			}
		}
		return alive && dead
	})
}

func TestCaptureReadsTheLastLines(t *testing.T) {
	s := newSpawner(t)
	ctx := context.Background()
	if err := s.Start(ctx, "cap", t.TempDir(), nil,
		[]string{"sh", "-c", "printf 'one\\ntwo\\nthree\\n'; while :; do sleep 1; done"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the captured output", func() bool {
		out, err := s.Capture(ctx, "cap", 15)
		return err == nil && strings.Contains(out, "three")
	})
}

func TestPasteLineDeliversExactlyOneLine(t *testing.T) {
	s := newSpawner(t)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "typed.txt")
	if err := s.Start(ctx, "paste", t.TempDir(), nil,
		[]string{"sh", "-c", "read line; printf '%s' \"$line\" > " + out + "; while :; do sleep 1; done"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the shell to start reading", func() bool {
		_, err := s.Capture(ctx, "paste", 5)
		return err == nil
	})
	if err := s.PasteLine(ctx, "paste", "swarm: inbox (call swarm_sync)"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pasted line", func() bool {
		b, err := os.ReadFile(out)
		return err == nil && string(b) == "swarm: inbox (call swarm_sync)"
	})
}

// §10.5 interrupts a quiescing agent with Escape then C-c. A test that only
// checks err == nil would pass even if Keys sent nothing at all, so the pane
// records the interrupt it receives.
func TestKeysSendsEscapeAndCtrlC(t *testing.T) {
	s := newSpawner(t)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "sig.txt")
	if err := s.Start(ctx, "keys", t.TempDir(), nil, []string{"sh", "-c",
		"trap 'printf interrupted > " + out + "; exit 0' INT; while :; do sleep 0.2; done"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pane to install its trap", func() bool {
		_, err := s.Capture(ctx, "keys", 5)
		return err == nil
	})
	if err := s.Keys(ctx, "keys", "Escape"); err != nil {
		t.Fatal(err)
	}
	if err := s.Keys(ctx, "keys", "C-c"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pane to see SIGINT", func() bool {
		b, err := os.ReadFile(out)
		return err == nil && string(b) == "interrupted"
	})
}

func TestKillIsIdempotent(t *testing.T) {
	s := newSpawner(t)
	ctx := context.Background()
	if err := s.Start(ctx, "bye", t.TempDir(), nil, []string{"sh", "-c", "while :; do sleep 1; done"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Kill(ctx, "bye"); err != nil {
		t.Fatal(err)
	}
	if err := s.Kill(ctx, "bye"); err != nil {
		t.Fatalf("second Kill should be a no-op, got %v", err)
	}
}

// remain-on-exit keeps a dead pane visible so the reconciler can read its status.
func TestTmuxConfSetsTitlesFocusEventsAndRemainOnExit(t *testing.T) {
	conf := string(TmuxConf())
	for _, want := range []string{
		"set -g set-titles on",
		"set -g set-titles-string 'swarm:#S'",
		"set -g focus-events on",
		"set -g remain-on-exit on",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("tmux.conf is missing %q:\n%s", want, conf)
		}
	}
}

func TestBaseEnvCopiesTheLaunchdCriticalVariables(t *testing.T) {
	env := BaseEnv(func(k string) string {
		return map[string]string{"USER": "u", "HOME": "/h", "LOGNAME": "u",
			"SHELL": "/bin/zsh", "TMPDIR": "/tmp/", "PATH": "/opt/homebrew/bin"}[k]
	})
	for k, want := range map[string]string{"USER": "u", "HOME": "/h", "LOGNAME": "u",
		"SHELL": "/bin/zsh", "TMPDIR": "/tmp/", "PATH": "/opt/homebrew/bin",
		"TERM": "xterm-256color", "LANG": "en_US.UTF-8"} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	// A missing USER is what breaks the keychain lookup (L9), so it is never dropped silently.
	if _, ok := BaseEnv(func(string) string { return "" })["USER"]; !ok {
		t.Error("USER must always be present, even when empty")
	}
}

// R10: an e2e run that points GNUPGHOME at a throwaway keyring needs the agent
// in the pane to see it. Without this the agent signs with the user's real keys.
func TestBaseEnvCarriesGNUPGHOMEWhenSetAndOmitsItWhenNot(t *testing.T) {
	with := BaseEnv(func(k string) string {
		if k == "GNUPGHOME" {
			return "/tmp/throwaway-gnupg"
		}
		return ""
	})
	if with["GNUPGHOME"] != "/tmp/throwaway-gnupg" {
		t.Fatalf("GNUPGHOME = %q, want the throwaway path", with["GNUPGHOME"])
	}
	if _, ok := BaseEnv(func(string) string { return "" })["GNUPGHOME"]; ok {
		t.Error("an unset GNUPGHOME must not be passed through as an empty value")
	}
}

// R9 / safety invariant S-1: the default must never be the live server.
func TestSocketFromEnvDefaultsToATestSocket(t *testing.T) {
	got := SocketFromEnv(func(string) string { return "" })
	if got == "swarm" || got == "swarm-dev" {
		t.Fatalf("SocketFromEnv() default = %q — a test that forgets to set the variable would reach the live server", got)
	}
	if want := fmt.Sprintf("swarm-test-%d", os.Getpid()); got != want {
		t.Fatalf("SocketFromEnv() = %q, want %q", got, want)
	}
	if got := SocketFromEnv(func(string) string { return "swarm-e2e" }); got != "swarm-e2e" {
		t.Fatalf("SocketFromEnv() with the variable set = %q, want swarm-e2e", got)
	}
}
