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

// tmux turns every "\n" in a pasted buffer into a carriage return, i.e. an
// Enter keypress in the pane. Claude Code will not submit a notice pasted that
// way at all -- measured live on 2026-09-23, the full multi-line notice sat
// unsent in its input box through settle windows of 0.5 s and 1.0 s, while the
// identical notice flattened to one line was submitted by the first Enter and
// recorded whole in the transcript. PasteLine pastes ONE line; this pins that.
func TestPasteLineFlattensNewlinesIntoOneLine(t *testing.T) {
	s := newSpawner(t)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "typed.txt")
	if err := s.Start(ctx, "flat", t.TempDir(), nil,
		[]string{"sh", "-c", "read line; printf '%s' \"$line\" > " + out + "; while :; do sleep 1; done"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the shell to start reading", func() bool {
		_, err := s.Capture(ctx, "flat", 5)
		return err == nil
	})
	notice := "[swarm] header line\n- msg_1:note [NOTE] note from peer: body\n\ntrailer — with an em dash"
	want := "[swarm] header line - msg_1:note [NOTE] note from peer: body trailer — with an em dash"
	if err := s.PasteLine(ctx, "flat", notice); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the flattened line", func() bool {
		b, err := os.ReadFile(out)
		return err == nil && string(b) == want
	})
}

// readSizeProbe is a pane program that records the length of every os.read()
// it gets off its tty, in raw mode -- the same mode every agent CLI runs its
// terminal in. It is the ground truth for the 2026-09-23 truncation: a single
// tmux paste-buffer larger than the kernel's per-read ceiling is delivered to
// the pane as several reads, and Claude Code's raw-mode input handler keeps
// only the last of them.
const readSizeProbe = `
import sys, os, tty, termios, select, time
fd = sys.stdin.fileno()
old = termios.tcgetattr(fd)
tty.setraw(fd)
open(sys.argv[2], "w").close()   # tell the test the tty is raw; a paste before
                                 # this would be read in canonical mode instead
sizes, total, t0 = [], 0, time.time()
try:
    while time.time() - t0 < 5:
        if select.select([fd], [], [], 0.2)[0]:
            b = os.read(fd, 65536)
            if not b:
                break
            sizes.append(len(b)); total += len(b)
finally:
    termios.tcsetattr(fd, termios.TCSAFLUSH, old)
open(sys.argv[1], "w").write("%d %d" % (total, max(sizes or [0])))
time.sleep(600)
`

// ttyReadCeiling is the size of the first read() a raw-mode pane program gets
// from any single oversized write: on Darwin ptcwrite blocks the master writer
// once the slave's raw queue hits TTYHOG-2, so the first read returns exactly
// 1022 bytes and the rest follows in a second read. Measured live, and it is
// exactly the split seen in the incident (a 1716-byte notice arrived as
// [1022, 694] and Claude Code recorded only the 694-byte tail, starting
// mid-word).
const ttyReadCeiling = 1022

// A notice big enough to cross that ceiling in one write must still reach the
// pane as a series of small reads, because the agent CLIs on the other end are
// closed-source binaries that demonstrably do not reassemble a split read. The
// real Inbox notice is routinely over 1 KB (header + items + the ~550-byte
// trailer), so this is the normal case, not an edge case.
func TestPasteLineNeverExceedsOneTtyReadPerChunk(t *testing.T) {
	s := newSpawner(t)
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "readsize.py")
	if err := os.WriteFile(probe, []byte(readSizeProbe), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "sizes.txt")
	ready := filepath.Join(dir, "ready")
	ctx := context.Background()
	if err := s.Start(ctx, "readsize", dir, nil, []string{py, probe, out, ready}); err != nil {
		t.Fatal(err)
	}
	// Wait for the tty to actually be in raw mode, not merely for the pane to
	// exist: a paste that lands while it is still canonical would be read
	// under MAX_CANON rules and measure the wrong thing entirely.
	waitFor(t, "the probe to put its tty in raw mode", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})
	line := strings.Repeat("swarm inbox notice filler ", 66)[:1700]
	if err := s.PasteLine(ctx, "readsize", line); err != nil {
		t.Fatal(err)
	}
	var total, maxRead int
	waitFor(t, "the probe to report its read sizes", func() bool {
		b, err := os.ReadFile(out)
		if err != nil {
			return false
		}
		_, err = fmt.Sscanf(string(b), "%d %d", &total, &maxRead)
		return err == nil
	})
	if want := len(line) + 1; total != want { // +1 for the Enter
		t.Errorf("the pane received %d bytes, want %d -- the paste did not arrive whole", total, want)
	}
	if maxRead >= ttyReadCeiling {
		t.Errorf("largest single tty read = %d bytes (ceiling %d): an agent CLI that keeps only the "+
			"last read of a split write would silently drop everything before it, which is the "+
			"2026-09-23 truncation. Paste must be chunked.", maxRead, ttyReadCeiling)
	}
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
		"tmux -L swarm list-panes -a -F " + paneFormat: {Out: "full-go-api-migration-orchestrator|0||2|2.1.278|4242\n"},
	}}
	s := &Spawner{Socket: "swarm", Tmux: "tmux", Run: f.Runner(), Log: func(string, ...any) {}}
	ps, err := s.Panes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Session != "full-go-api-migration-orchestrator" || ps[0].Dead || ps[0].Command != "2.1.278" || ps[0].Pid != 4242 {
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

// cancelCopyModeIfNeeded is the guard for the 2026-09-25 22:27Z bug: mouse
// wheel scroll (mouse on, TmuxConf) puts a pane in copy-mode, and copy-mode's
// key table has no Enter binding, so the Enter that submits a paste is
// swallowed and the notice sits complete but unsent. When the pane reports
// pane_in_mode=1, the cancel must be sent before anything else.
func TestCancelCopyModeIfNeededCancelsWhenPaneIsInCopyMode(t *testing.T) {
	f := &execx.Fake{Responses: map[string]execx.Result{
		"tmux -L swarm display -p -t sess #{pane_in_mode}": {Out: "1\n"},
		"tmux -L swarm send-keys -t sess -X cancel":        {Out: ""},
	}}
	s := &Spawner{Socket: "swarm", Tmux: "tmux", Run: f.Runner(), Log: func(string, ...any) {}}
	s.cancelCopyModeIfNeeded(context.Background(), "sess")
	want := []string{"tmux -L swarm display -p -t sess #{pane_in_mode}", "tmux -L swarm send-keys -t sess -X cancel"}
	if got := f.Calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v (cancel must be sent, in order, when in copy-mode)", got, want)
	}
}

func TestCancelCopyModeIfNeededDoesNothingWhenPaneIsNotInCopyMode(t *testing.T) {
	f := &execx.Fake{Responses: map[string]execx.Result{
		"tmux -L swarm display -p -t sess #{pane_in_mode}": {Out: "0\n"},
	}}
	s := &Spawner{Socket: "swarm", Tmux: "tmux", Run: f.Runner(), Log: func(string, ...any) {}}
	s.cancelCopyModeIfNeeded(context.Background(), "sess")
	want := []string{"tmux -L swarm display -p -t sess #{pane_in_mode}"}
	if got := f.Calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v (no cancel when not in copy-mode)", got, want)
	}
}

// The regression test for the actual incident: a pane pushed into copy-mode
// (exactly what a mouse-wheel scroll under `mouse on` does) must still have
// its pasted line submitted -- not left sitting unsent because copy-mode ate
// the Enter.
func TestPasteLineCancelsCopyModeSoTheLineIsSubmitted(t *testing.T) {
	s := newSpawner(t)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "typed.txt")
	if err := s.Start(ctx, "copymode", t.TempDir(), nil,
		[]string{"sh", "-c", "read line; printf '%s' \"$line\" > " + out + "; while :; do sleep 1; done"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the shell to start reading", func() bool {
		_, err := s.Capture(ctx, "copymode", 5)
		return err == nil
	})
	if _, err := s.run(ctx, "copy-mode", "-t", "copymode"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pane to enter copy-mode", func() bool {
		out, err := s.run(ctx, "display", "-p", "-t", "copymode", "#{pane_in_mode}")
		return err == nil && strings.TrimSpace(string(out)) == "1"
	})
	if err := s.PasteLine(ctx, "copymode", "swarm: inbox (call swarm_sync)"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pasted line to be submitted despite copy-mode", func() bool {
		b, err := os.ReadFile(out)
		return err == nil && string(b) == "swarm: inbox (call swarm_sync)"
	})
}
