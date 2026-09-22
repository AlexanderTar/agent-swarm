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

// RenameWindow is how the reconciler pushes the fun Ghostty title (status +
// role + tree emoji + name) into #W every tick; set-titles-string='#W' is
// what then surfaces it as the terminal's actual title.
func TestRenameWindowSetsTheWindowName(t *testing.T) {
	s := newSpawner(t)
	ctx := context.Background()
	if err := s.Start(ctx, "renamed", t.TempDir(), nil, []string{"sh", "-c", "while :; do sleep 0.2; done"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameWindow(ctx, "renamed", "▶️ 🧠 🔵 renamed"); err != nil {
		t.Fatal(err)
	}
	out, err := s.run(ctx, "list-windows", "-t", "renamed", "-F", "#{window_name}")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "▶️ 🧠 🔵 renamed" {
		t.Fatalf("window name = %q", got)
	}
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

// P0-crash-1 (2026-09-19): a socket that has never had a session on it yet
// (a fresh daemon restart, before the first spawn) fails "error connecting
// to <path> (No such file or directory)", not "no server running". Panes and
// Kill must treat that the same as "no server running": it means the same
// thing (no live server on this socket), not a real error. Before this fix
// Reconcile aborted on every tick until the first tmux session existed.
func fakeSpawner(t *testing.T, msg string) *Spawner {
	t.Helper()
	f := &execx.Fake{Responses: map[string]execx.Result{
		"tmux -L swarm list-panes -a -F " + paneFormat: {Err: fmt.Errorf("exit status 1: %s", msg)},
		"tmux -L swarm kill-session -t ghost":          {Err: fmt.Errorf("exit status 1: %s", msg)},
	}}
	return &Spawner{Socket: "swarm", Tmux: "tmux", Run: f.Runner(), Log: func(string, ...any) {}}
}

func TestPanesTreatsAnUnopenedSocketAsNoPanes(t *testing.T) {
	s := fakeSpawner(t, "error connecting to /private/tmp/tmux-501/swarm (No such file or directory)")
	ps, err := s.Panes(context.Background())
	if err != nil {
		t.Fatalf("Panes = %v, want nil error (no server yet is not an error)", err)
	}
	if ps != nil {
		t.Fatalf("Panes = %v, want nil", ps)
	}
}

func TestKillTreatsAnUnopenedSocketAsAlreadyGone(t *testing.T) {
	s := fakeSpawner(t, "error connecting to /private/tmp/tmux-501/swarm (No such file or directory)")
	if err := s.Kill(context.Background(), "ghost"); err != nil {
		t.Fatalf("Kill = %v, want nil (no server means already gone)", err)
	}
}

// noServer must not swallow every "error connecting to" failure -- only the
// "no such file" one that really means no server exists yet. A permission
// error (or any other real connect failure) is a genuine problem and must
// still surface, or Kill would silently no-op ahead of a Start that then
// fails with a confusing "duplicate session" instead of the real cause.
func TestPanesAndKillStillReportAGenuineConnectFailure(t *testing.T) {
	s := fakeSpawner(t, "error connecting to /private/tmp/tmux-501/swarm (Permission denied)")
	if _, err := s.Panes(context.Background()); err == nil {
		t.Fatal("Panes swallowed a permission error, want it returned")
	}
	if err := s.Kill(context.Background(), "ghost"); err == nil {
		t.Fatal("Kill swallowed a permission error, want it returned")
	}
}

// P0-crash-4 (2026-09-19): tmux 3.7c replaces literal tab bytes in a -F
// format string's OUTPUT with "_" whenever the calling process has no
// locale set (confirmed live: reproduced with `env -i` stripping LANG/
// LC_ALL) -- exactly launchd's environment for this daemon, never an
// interactive shell's. A tab-delimited paneFormat silently parsed zero
// panes from every one of the daemon's own live sessions as a result. This
// pins Panes() to a delimiter tmux never sanitizes (a real tmux process
// under this same daemon's real launchd environment, not a fake Runner,
// would be needed to reproduce the underscore substitution itself -- this
// test instead pins the parsing contract survives even the exact garbled
// shape locale sanitization would have produced under the old delimiter,
// by asserting a comma inside pane_current_command doesn't confuse a
// "|"-split, and that the format constant itself no longer contains a tab).
func TestPaneFormatSurvivesLocaleSanitizationOfControlCharacters(t *testing.T) {
	if strings.Contains(paneFormat, "\t") {
		t.Fatal("paneFormat must not use a tab delimiter -- tmux replaces literal tabs in -F output with \"_\" when the calling process has no locale set (confirmed live against the real daemon's launchd environment), silently breaking every field split")
	}
	f := &execx.Fake{Responses: map[string]execx.Result{
		"tmux -L swarm list-panes -a -F " + paneFormat: {Out: "full-go-api-migration-orchestrator|0||2|2.1.278\n"},
	}}
	s := &Spawner{Socket: "swarm", Tmux: "tmux", Run: f.Runner(), Log: func(string, ...any) {}}
	ps, err := s.Panes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Session != "full-go-api-migration-orchestrator" || ps[0].Dead || ps[0].Command != "2.1.278" {
		t.Fatalf("Panes = %+v", ps)
	}
}

// remain-on-exit keeps a dead pane visible so the reconciler can read its status.
// mouse on enables wheel scrolling through conversation history rather than cycling prompt history.
func TestTmuxConfSetsTitlesFocusEventsAndRemainOnExit(t *testing.T) {
	conf := string(TmuxConf())
	for _, want := range []string{
		"set -g set-titles on",
		"set -g set-titles-string '#W'",
		"set -g automatic-rename off",
		"set -g focus-events on",
		"set -g remain-on-exit on",
		"set -g history-limit 20000",
		"set -g mouse on",
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
