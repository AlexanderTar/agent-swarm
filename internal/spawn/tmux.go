// Package spawn wraps the tmux server Swarm runs its agents on (spec §6.4, L8).
package spawn

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// Spawner implements runtime.Tmux against one tmux server, addressed by
// socket name. Tests use "swarm-test-<pid>"; production uses "swarm" (L8),
// chosen once by SocketFromEnv and never hardcoded elsewhere.
type Spawner struct {
	Socket string // "swarm"; tests use "swarm-test-<pid>"
	Conf   string // path to tmux.conf
	Tmux   string // absolute tmux path
	Run    execx.Runner
	Log    func(format string, args ...any)
}

// TmuxConf is ~/.swarm/tmux.conf. set-titles lets the menubar find a terminal by
// title (L17); focus-events keeps Claude Code from printing a hint (P0-6);
// remain-on-exit keeps a dead pane readable for the reconciler (§10.6);
// mouse on enables wheel scroll to enter copy-mode and scroll conversation history
// rather than sending Up/Down arrow keys that cycle prompt history in Codex/agy/Cursor.
// set-titles-string is '#W' (the window name), not a literal: the reconciler
// renames each session's window every tick to the fun title (status/role/tree
// emoji + name, see runtime.sessionTitle), and automatic-rename off stops tmux
// fighting that by renaming the window back to the foreground command itself.
func TmuxConf() []byte {
	return []byte(strings.Join([]string{
		"# Written by `swarm install`. Swarm's tmux server only.",
		"set -g set-titles on",
		"set -g set-titles-string '#W'",
		"set -g automatic-rename off",
		"set -g focus-events on",
		"set -g remain-on-exit on",
		"set -g history-limit 20000",
		"set -g mouse on",
		"",
	}, "\n"))
}

// BaseEnv is the environment every spawn copies (L9). USER is what the keychain
// lookup needs, so the six L9 keys are always present even when the value is
// empty. GNUPGHOME is different: it is copied only when the daemon has one, and
// it is not one of L9's six. It is here for safety, not for the feature (R10) —
// without it, an e2e run that sets up a throwaway keyring in its own process
// would have the agent sign with the user's real secret keys, because the agent
// runs in a tmux pane whose whole environment is this map.
func BaseEnv(osEnv func(string) string) map[string]string {
	env := map[string]string{}
	for _, k := range []string{"USER", "HOME", "LOGNAME", "SHELL", "TMPDIR", "PATH"} {
		env[k] = osEnv(k)
	}
	env["TERM"] = "xterm-256color"
	env["LANG"] = "en_US.UTF-8"
	if v := osEnv("GNUPGHOME"); v != "" {
		env["GNUPGHOME"] = v // empty means "the user's default", which we must not pass on as a value
	}
	return env
}

// SocketFromEnv is the one place the tmux socket name is decided (R9, safety
// invariant S-1). The default is a per-process test socket, NOT "swarm": a
// default of "swarm" means any test that forgets to set the variable talks to
// the user's live daemon's tmux server, and "it is latent because no loop runs
// yet" is not a property a plan can rely on staying true. Production is opt-in:
// the launchd plist Task 35 writes sets SWARM_TMUX_SOCKET=swarm explicitly.
func SocketFromEnv(osEnv func(string) string) string {
	if v := osEnv("SWARM_TMUX_SOCKET"); v != "" {
		return v
	}
	return fmt.Sprintf("swarm-test-%d", os.Getpid())
}

func (s *Spawner) args(rest ...string) []string {
	out := []string{"-L", s.Socket}
	if s.Conf != "" {
		out = append(out, "-f", s.Conf)
	}
	return append(out, rest...)
}

func (s *Spawner) run(ctx context.Context, rest ...string) ([]byte, error) {
	return s.Run(ctx, s.Tmux, s.args(rest...)...)
}

// Start creates the detached session. The token never appears in argv (§6.4), so
// callers pass it through env as SWARM_TOKEN_FILE.
func (s *Spawner) Start(ctx context.Context, name, cwd string, env map[string]string, argv []string) error {
	a := []string{"new-session", "-d", "-s", name, "-c", cwd, "-x", "220", "-y", "60"}
	for _, k := range sortedKeys(env) {
		a = append(a, "-e", k+"="+env[k])
	}
	a = append(a, "--")
	a = append(a, argv...)
	if _, err := s.run(ctx, a...); err != nil {
		return fmt.Errorf("tmux new-session %s: %w", name, err)
	}
	return nil
}

// paneFormat's fields are "|"-delimited, not tab-delimited: confirmed live
// (P0-crash-4, 2026-09-19) that tmux 3.7c replaces literal tab bytes in a
// -F format string's OUTPUT with "_" whenever the calling process has no
// locale set (no LANG/LC_ALL/LC_CTYPE) -- exactly launchd's environment for
// this daemon, never an interactive shell's. That silently broke every
// field split below (no real tab left to split on), so Panes() always
// returned zero real panes to a daemon that had launched normally from
// launchd, while the exact same command run from a terminal (which always
// has a locale) parsed fine -- the daemon then treated every one of its own
// live sessions as gone and crashed them. "|" is not sanitized under any
// locale and cannot appear in a session name (ids.Kebab-sanitized) or a
// process name, so it can't collide with real field content.
const paneFormat = "#{session_name}|#{pane_dead}|#{pane_dead_status}|#{session_attached}|#{pane_current_command}|#{pane_pid}"

// noServer reports whether a tmux failure just means there is no server on
// this socket yet, not a real error. A socket that already had a server but
// lost its last session says "no server running"; a socket that has never
// had a session on it at all (a fresh daemon restart, before the first spawn)
// says "error connecting to <path> (No such file or directory)" instead.
// Every caller here treats both the same: zero panes / already gone. This
// must NOT match every "error connecting to" failure (a permission error or
// a genuinely hung server also says that) -- only the specific "the socket
// file isn't there" case, or Kill would silently swallow a real failure and
// let startSession's Kill-before-Start move straight to a misleading
// "duplicate session" from Start instead.
func noServer(out []byte, err error) bool {
	for _, msg := range []string{"no server running", "No such file or directory"} {
		if strings.Contains(string(out), msg) || strings.Contains(err.Error(), msg) {
			return true
		}
	}
	return false
}

func (s *Spawner) Panes(ctx context.Context) ([]runtime.Pane, error) {
	out, err := s.run(ctx, "list-panes", "-a", "-F", paneFormat)
	if err != nil {
		if noServer(out, err) {
			return nil, nil
		}
		return nil, err
	}
	var ps []runtime.Pane
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 6 {
			continue
		}
		st, _ := strconv.Atoi(f[2])
		pid, _ := strconv.Atoi(f[5])
		ps = append(ps, runtime.Pane{Session: f[0], Dead: f[1] == "1", DeadStatus: st,
			Attached: f[3] != "0", Command: f[4], Pid: pid})
	}
	return ps, nil
}

// Capture keeps escape codes (-e): the dim attribute is how a placeholder is told
// from typed text (§11.3, P0-4).
func (s *Spawner) Capture(ctx context.Context, name string, lines int) (string, error) {
	out, err := s.run(ctx, "capture-pane", "-p", "-e", "-J", "-t", name, "-S", "-"+strconv.Itoa(lines))
	return string(out), err
}

// PasteLine loads the text into a buffer and pastes it, so bracketed paste and
// long lines survive; -d deletes the buffer afterwards. pasteViaTempFile is
// defined below and is the whole implementation.
//
// The newlines are flattened first, because this pastes ONE line and tmux
// turns every "\n" in a buffer into a carriage return -- i.e. an Enter
// keypress -- on its way into the pane. Claude Code then will not submit the
// result at all: a multi-line paste left the full notice sitting in its input
// box, unsent, through a settle of half a second (measured live 2026-09-23);
// the same notice flattened to one line was submitted by the very first Enter
// and recorded whole in its transcript. Nothing is lost by flattening:
// Inbox()'s newlines only separate the header, items and trailer, and every
// value inside an item has already been through runtime.sanitizeOneLine.
func (s *Spawner) PasteLine(ctx context.Context, name, line string) error {
	return s.pasteViaTempFile(ctx, name, strings.Join(strings.Fields(line), " "))
}

// Keys is the one place every send-keys call goes through -- prompt
// auto-answers (reconcile.go), interrupts (pause.go, agents.go,
// checkpoint.go), startup dialogs (agents.go), and pasteViaTempFile's own
// trailing Enter. cancelCopyModeIfNeeded guards all of them here, not just
// the paste path: an Escape/Enter/dialog answer sent while the pane is in
// copy-mode is swallowed exactly the way the paste's Enter was.
func (s *Spawner) Keys(ctx context.Context, name string, keys ...string) error {
	s.cancelCopyModeIfNeeded(ctx, name)
	_, err := s.run(ctx, append([]string{"send-keys", "-t", name}, keys...)...)
	return err
}

// RenameWindow sets the session's window name (#W), which set-titles-string
// then puts in the terminal's title. name resolves the same way every other
// per-session call here does (Capture, Keys): a plain "-t name", not the
// exact-match "=name" form Terminals.swift needs for tmux attach.
func (s *Spawner) RenameWindow(ctx context.Context, name, title string) error {
	_, err := s.run(ctx, "rename-window", "-t", name, title)
	return err
}

// Env reads one variable from the session environment. §10.5 uses it to confirm a
// pane's SWARM_SESSION before killing it, so a newer generation is never killed.
func (s *Spawner) Env(ctx context.Context, name, key string) (string, error) {
	out, err := s.run(ctx, "show-environment", "-t", name, key)
	if err != nil {
		return "", err
	}
	line := strings.TrimRight(string(out), "\n")
	_, v, ok := strings.Cut(line, "=")
	if !ok {
		return "", fmt.Errorf("tmux show-environment %s %s: %q", name, key, line)
	}
	return v, nil
}

// Kill is idempotent: a missing session, or a server that has already exited
// because that was its last session, is not an error.
func (s *Spawner) Kill(ctx context.Context, name string) error {
	out, err := s.run(ctx, "kill-session", "-t", name)
	if err == nil {
		return nil
	}
	if strings.Contains(string(out), "can't find session") || strings.Contains(err.Error(), "can't find session") || noServer(out, err) {
		return nil
	}
	return err
}

// Paste pacing. These are calibration knobs, not constants of nature: 1022 is
// a Darwin kernel figure and the settle window is an observed property of two
// closed-source TUIs, so both are expected to need tuning on other platforms
// or after an agent CLI updates.
//
// pasteChunkSize is half the measured 1022-byte first-read ceiling (see
// ttyReadCeiling in tmux_test.go). A single write larger than that ceiling is
// split by the kernel into several reads on the pane's side, and the agent
// CLIs on the other end do not reassemble them: Claude Code keeps only the
// last read and silently discards everything before it. That is the whole of
// the 2026-09-23 truncation -- a 1716-byte notice arrived as [1022, 694] and
// the model was handed the 694-byte tail, starting mid-word. Pacing the write
// so every read stays small is the fix; it is not a "wait and hope" delay,
// because the bytes are lost during the write itself and no delay placed
// before Enter can bring them back.
//
// pasteSettle is separate and is about Enter, not about the bytes. Both
// Claude Code and cursor-agent coalesce input that arrives in a burst and
// treat it as one paste, and an Enter landing inside that window is absorbed
// into the pasted text instead of submitting it -- observed live as a notice
// sitting complete but unsent in cursor's input box (its own chat store showed
// hasConversation:false). One settle before Enter, always, including for a
// single-chunk notice like IdleToken.
var (
	pasteChunkSize = 512
	pasteChunkGap  = 50 * time.Millisecond
	pasteSettle    = 500 * time.Millisecond
)

// chunkRunes splits s into pieces of at most max bytes, never cutting a rune
// in half. TUIs decode each stdin read as UTF-8 on its own, so a split
// mid-rune shows up as U+FFFD -- and the notice really does carry multi-byte
// characters (inboxTrailer's "—", and the "(+N more pending —" tail).
func chunkRunes(s string, max int) []string {
	var out []string
	for len(s) > 0 {
		n := max
		if n >= len(s) {
			out = append(out, s)
			break
		}
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		if n == 0 { // one rune longer than max: emit it whole rather than corrupt it
			_, n = utf8.DecodeRuneInString(s)
		}
		out, s = append(out, s[:n]), s[n:]
	}
	return out
}

// sleep waits d, or gives up early if the caller's context is done. Wake is a
// best-effort path; a cancelled daemon must not sit here.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// cancelCopyModeIfNeeded exits copy-mode before sending keys, so a keypress
// that matters -- a paste's Enter, an interrupt's Escape/C-c, a prompt
// auto-answer -- isn't swallowed. mouse on (TmuxConf) lets a mouse-wheel
// scroll put a pane into copy-mode, and copy-mode's key table (mode-keys
// emacs) has no Enter binding -- Enter there is simply dropped, not passed
// through to the program underneath. That silently left a relay notice for
// go-migration-agent-debug sitting complete but unsent in its input box
// (2026-09-25 22:27Z), which then made every idle check fail and every
// quota-reset wake log "pane not idle" until the pane was touched by hand.
// Best-effort: a failure here must not block the keys that follow.
func (s *Spawner) cancelCopyModeIfNeeded(ctx context.Context, name string) {
	out, err := s.run(ctx, "display", "-p", "-t", name, "#{pane_in_mode}")
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		return
	}
	if _, err := s.run(ctx, "send-keys", "-t", name, "-X", "cancel"); err != nil {
		s.Log("tmux: cancel copy-mode for %s: %v", name, err)
	}
}

// pasteViaTempFile is how the line reaches tmux: execx.Runner has no stdin, and
// send-keys -l would interpret some characters. The buffer is reused across
// chunks, so at most one temp file exists per paste. The trailing Keys(Enter)
// call already runs the copy-mode guard, so there is no separate guard here.
func (s *Spawner) pasteViaTempFile(ctx context.Context, name, line string) error {
	f, err := os.CreateTemp("", "swarm-paste-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	f.Close()
	for _, chunk := range chunkRunes(line, pasteChunkSize) {
		if err := os.WriteFile(f.Name(), []byte(chunk), 0o600); err != nil {
			return err
		}
		if _, err := s.run(ctx, "load-buffer", "-b", "swarmwake", f.Name()); err != nil {
			return err
		}
		if _, err := s.run(ctx, "paste-buffer", "-d", "-b", "swarmwake", "-t", name); err != nil {
			return err
		}
		if err := sleep(ctx, pasteChunkGap); err != nil {
			return err
		}
	}
	if err := sleep(ctx, pasteSettle); err != nil {
		return err
	}
	return s.Keys(ctx, name, "Enter")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out) // a stable order makes the argv assertable in tests
	return out
}
