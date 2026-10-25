// Package desktop opens an agent's tmux session in a terminal window and
// reads the graphical session's environment on Linux, where no menu bar app
// does it (the macOS flow lives in the Swift app and httpapi's Ghostty
// fallback).
package desktop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// ErrNoTerminal: none of the known terminal emulators is installed.
var ErrNoTerminal = errors.New("No terminal emulator found")

// terminals are tried in order: Ghostty first (what macOS opens), then the
// user's own choice where the system records one (xdg-terminal-exec, then
// Debian's x-terminal-emulator alternative), then common emulators. Each
// prefix is followed by the command and its arguments, as that terminal's
// documentation gives it.
var terminals = [][]string{
	{"ghostty", "-e"},
	{"xdg-terminal-exec"},
	{"x-terminal-emulator", "-e"},
	{"gnome-terminal", "--"},
	{"konsole", "-e"},
	{"kitty"},
	{"alacritty", "-e"},
	{"wezterm", "start", "--"},
	{"foot"},
	{"xterm", "-e"},
}

// Terminals lists the emulators TerminalArgv looks for, for messages.
func Terminals() string {
	names := make([]string, len(terminals))
	for i, t := range terminals {
		names[i] = t[0]
	}
	return strings.Join(names, ", ")
}

// TerminalArgv is the argv that opens command in the first installed terminal.
func TerminalArgv(lookPath func(string) (string, error), command []string) ([]string, error) {
	for _, t := range terminals {
		if _, err := lookPath(t[0]); err == nil {
			return append(append([]string(nil), t...), command...), nil
		}
	}
	return nil, fmt.Errorf("%w. Install one of %s.", ErrNoTerminal, Terminals())
}

// Launcher opens terminals for the daemon on Linux. Every field is required.
type Launcher struct {
	Run      execx.Runner   // for `systemctl --user show-environment`
	Start    execx.Detacher // starts the terminal in its own session
	LookPath func(string) (string, error)
	Getenv   func(string) string
	Exists   func(path string) bool
	// Settle is how long Open watches a started terminal for an early failure
	// (bad arguments, a display that refuses it).
	Settle time.Duration
}

// Open starts a terminal running command on the current graphical session.
// A terminal that exits non-zero within Settle is an error carrying its
// stderr; one that exits 0 (it handed the window to a running server, like
// gnome-terminal) or is still running is a success.
func (l Launcher) Open(ctx context.Context, command []string) error {
	argv, err := TerminalArgv(l.LookPath, command)
	if err != nil {
		return err
	}
	env, err := l.SessionEnv(ctx)
	if err != nil {
		return err
	}
	p, err := l.Start(env, argv[0], argv[1:]...)
	if err != nil {
		return fmt.Errorf("Couldn't start %s: %w", argv[0], err)
	}
	select {
	case <-p.Done:
		if err := p.Err(); err != nil {
			return fmt.Errorf("%s exited at once: %w", argv[0], err)
		}
	case <-time.After(l.Settle):
	case <-ctx.Done():
	}
	return nil
}
