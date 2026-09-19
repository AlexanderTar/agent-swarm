// Package spawn wraps the tmux server Swarm runs its agents on (spec §6.4, L8).
package spawn

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

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
// remain-on-exit keeps a dead pane readable for the reconciler (§10.6).
func TmuxConf() []byte {
	return []byte(strings.Join([]string{
		"# Written by `swarm install`. Swarm's tmux server only.",
		"set -g set-titles on",
		"set -g set-titles-string 'swarm:#S'",
		"set -g focus-events on",
		"set -g remain-on-exit on",
		"set -g history-limit 20000",
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

const paneFormat = "#{session_name}\t#{pane_dead}\t#{pane_dead_status}\t#{session_attached}\t#{pane_current_command}"

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
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		st, _ := strconv.Atoi(f[2])
		ps = append(ps, runtime.Pane{Session: f[0], Dead: f[1] == "1", DeadStatus: st,
			Attached: f[3] != "0", Command: f[4]})
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
func (s *Spawner) PasteLine(ctx context.Context, name, line string) error {
	return s.pasteViaTempFile(ctx, name, line)
}

func (s *Spawner) Keys(ctx context.Context, name string, keys ...string) error {
	_, err := s.run(ctx, append([]string{"send-keys", "-t", name}, keys...)...)
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

// pasteViaTempFile is how the line reaches tmux: execx.Runner has no stdin, and
// send-keys -l would interpret some characters.
func (s *Spawner) pasteViaTempFile(ctx context.Context, name, line string) error {
	f, err := os.CreateTemp("", "swarm-paste-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if _, err := s.run(ctx, "load-buffer", "-b", "swarmwake", f.Name()); err != nil {
		return err
	}
	if _, err := s.run(ctx, "paste-buffer", "-d", "-b", "swarmwake", "-t", name); err != nil {
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
