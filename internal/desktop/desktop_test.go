package desktop

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

var bg = context.Background()

func have(names ...string) func(string) (string, error) {
	return func(n string) (string, error) {
		if slices.Contains(names, n) {
			return "/usr/bin/" + n, nil
		}
		return "", errors.New("not found")
	}
}

var attach = []string{"/opt/tmux", "-L", "swarm", "attach", "-t", "builder"}

func TestTerminalArgvPicksTheFirstInstalledTerminal(t *testing.T) {
	for _, tc := range []struct {
		installed []string
		want      []string
	}{
		{[]string{"xterm", "ghostty"}, []string{"ghostty", "-e"}},
		{[]string{"xterm", "x-terminal-emulator", "xdg-terminal-exec"}, []string{"xdg-terminal-exec"}},
		{[]string{"xterm", "x-terminal-emulator"}, []string{"x-terminal-emulator", "-e"}},
		{[]string{"gnome-terminal", "kitty"}, []string{"gnome-terminal", "--"}},
		{[]string{"konsole"}, []string{"konsole", "-e"}},
		{[]string{"kitty"}, []string{"kitty"}},
		{[]string{"alacritty"}, []string{"alacritty", "-e"}},
		{[]string{"wezterm"}, []string{"wezterm", "start", "--"}},
		{[]string{"foot"}, []string{"foot"}},
		{[]string{"xterm"}, []string{"xterm", "-e"}},
	} {
		got, err := TerminalArgv(have(tc.installed...), attach)
		if want := append(tc.want, attach...); err != nil || !slices.Equal(got, want) {
			t.Errorf("%v: argv = %q %v, want %q", tc.installed, got, err, want)
		}
	}
}

func TestTerminalArgvNamesTheCandidatesWhenNoneIsInstalled(t *testing.T) {
	_, err := TerminalArgv(have(), attach)
	if !errors.Is(err, ErrNoTerminal) || !strings.Contains(err.Error(), "ghostty, xdg-terminal-exec, x-terminal-emulator") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseEnvironment(t *testing.T) {
	out := "HOME=/home/a\n" +
		"DISPLAY=:1\n" +
		`PS1=$'a b\'c\\d\n\te\033[0m'` + "\n" +
		`ODD=$'x\x41y'` + "\n" +
		"EMPTY=\n" +
		"EQ=a=b\n" +
		"=bad\n" +
		"noequals\n"
	env := ParseEnvironment(out)
	want := map[string]string{"HOME": "/home/a", "DISPLAY": ":1", "PS1": "a b'c\\d\n\te\x1b[0m",
		"ODD": "xAy", "EMPTY": "", "EQ": "a=b"}
	if len(env) != len(want) {
		t.Errorf("env = %q", env)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

// A lone or unknown escape is kept rather than dropped.
func TestUnescapeCKeepsWhatItCannotDecode(t *testing.T) {
	for in, want := range map[string]string{`a\`: `a\`, `\q`: "q", `\xZZ`: `\xZZ`, `\9`: "9", `\07`: "07"} {
		if got := unescapeC(in); got != want {
			t.Errorf("unescapeC(%q) = %q, want %q", in, got, want)
		}
	}
}

type launcherEnv struct {
	showEnv execx.Result
	getenv  map[string]string
	exists  map[string]bool
	started [][]string
	envs    [][]string
	exit    error // nil: still running; set: exits at once with this error
	exitOK  bool  // exits at once with status 0
}

func (e *launcherEnv) launcher() Launcher {
	f := &execx.Fake{Responses: map[string]execx.Result{"systemctl --user show-environment": e.showEnv}}
	return Launcher{
		Run: f.Runner(),
		Start: func(env []string, name string, args ...string) (*execx.Detached, error) {
			e.started = append(e.started, append([]string{name}, args...))
			e.envs = append(e.envs, env)
			done := make(chan struct{})
			if e.exit != nil || e.exitOK {
				close(done)
			}
			return &execx.Detached{Done: done, Err: func() error { return e.exit }}, nil
		},
		LookPath: have("kitty"),
		Getenv:   func(k string) string { return e.getenv[k] },
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=/home/a", "WAYLAND_DISPLAY=wayland-stale", "XAUTHORITY=/run/stale/Xauthority"}
		},
		Exists: func(p string) bool { return e.exists[p] },
		Settle: 20 * time.Millisecond,
	}
}

const liveSession = "XDG_RUNTIME_DIR=/run/user/1000\nWAYLAND_DISPLAY=wayland-1\nDISPLAY=:1\nDBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus\nHOME=/home/a\n"

func TestOpenStartsTheTerminalOnTheCurrentSession(t *testing.T) {
	e := &launcherEnv{showEnv: execx.Result{Out: liveSession},
		exists: map[string]bool{"/run/user/1000/wayland-1": true, "/tmp/.X11-unix/X1": true}}
	if err := e.launcher().Open(bg, attach); err != nil {
		t.Fatal(err)
	}
	if len(e.started) != 1 || !slices.Equal(e.started[0], append([]string{"kitty"}, attach...)) {
		t.Fatalf("started = %q", e.started)
	}
	// The daemon's environment, with every graphical key from the live session.
	want := []string{"PATH=/usr/bin", "HOME=/home/a", "WAYLAND_DISPLAY=wayland-1", "DISPLAY=:1",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "XDG_RUNTIME_DIR=/run/user/1000"}
	if got := e.envs[0]; !slices.Equal(got, want) {
		t.Errorf("env = %q, want %q", got, want)
	}
}

// Stale values left by a desktop at logout are dropped, display by display.
func TestSessionEnvDropsADeadDisplay(t *testing.T) {
	e := &launcherEnv{showEnv: execx.Result{Out: liveSession}, exists: map[string]bool{"/tmp/.X11-unix/X1": true}}
	env, err := e.launcher().SessionEnv(bg)
	if err != nil || env["WAYLAND_DISPLAY"] != "" || env["DISPLAY"] != ":1" {
		t.Fatalf("env = %q %v, want X11 only", env, err)
	}
	e.exists = map[string]bool{}
	if _, err := e.launcher().SessionEnv(bg); !errors.Is(err, ErrNoSession) {
		t.Fatalf("no live display: err = %v", err)
	}
	if err := e.launcher().Open(bg, attach); !errors.Is(err, ErrNoSession) || len(e.started) != 0 {
		t.Fatalf("Open without a session = %v, started %q", err, e.started)
	}
}

// When the user manager can't be asked, the daemon's own environment is used.
func TestSessionEnvFallsBackToTheDaemonEnvironment(t *testing.T) {
	e := &launcherEnv{showEnv: execx.Result{Err: errors.New("Failed to connect to bus")},
		getenv: map[string]string{"WAYLAND_DISPLAY": "/run/w-0", "HOME": "/home/a"},
		exists: map[string]bool{"/run/w-0": true}}
	env, err := e.launcher().SessionEnv(bg)
	if err != nil || env["WAYLAND_DISPLAY"] != "/run/w-0" || env["HOME"] != "" {
		t.Fatalf("env = %q %v", env, err)
	}
}

func TestX11Socket(t *testing.T) {
	for display, want := range map[string]string{":0": "/tmp/.X11-unix/X0", ":12.1": "/tmp/.X11-unix/X12",
		"unix:3": "/tmp/.X11-unix/X3", ":x": "", "remote:10.0": "/"} {
		if got := x11Socket(display); got != want {
			t.Errorf("x11Socket(%q) = %q, want %q", display, got, want)
		}
	}
	if waylandSocket("wayland-0", "") != "" {
		t.Error("a relative WAYLAND_DISPLAY with no runtime dir has no socket")
	}
}

func TestOpenReportsATerminalThatFailsAtOnce(t *testing.T) {
	e := &launcherEnv{showEnv: execx.Result{Out: liveSession}, exists: map[string]bool{"/tmp/.X11-unix/X1": true},
		exit: errors.New("exit status 1: cannot open display")}
	err := e.launcher().Open(bg, attach)
	if err == nil || !strings.Contains(err.Error(), "kitty exited at once") || !strings.Contains(err.Error(), "cannot open display") {
		t.Fatalf("err = %v", err)
	}
}

// gnome-terminal and friends hand the window to a server and exit 0.
func TestOpenAcceptsATerminalThatHandsOffAndExitsZero(t *testing.T) {
	e := &launcherEnv{showEnv: execx.Result{Out: liveSession}, exists: map[string]bool{"/tmp/.X11-unix/X1": true}, exitOK: true}
	if err := e.launcher().Open(bg, attach); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReportsAStartFailureAndAMissingTerminal(t *testing.T) {
	e := &launcherEnv{showEnv: execx.Result{Out: liveSession}, exists: map[string]bool{"/tmp/.X11-unix/X1": true}}
	l := e.launcher()
	l.Start = func([]string, string, ...string) (*execx.Detached, error) {
		return nil, errors.New("permission denied")
	}
	if err := l.Open(bg, attach); err == nil || !strings.Contains(err.Error(), "Couldn't start kitty: permission denied") {
		t.Fatalf("start failure = %v", err)
	}
	l.LookPath = have()
	if err := l.Open(bg, attach); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("no terminal = %v", err)
	}
}

// A display variable the live session lacks never reaches the terminal at the
// daemon's stale value: it is removed, not inherited.
func TestOpenDropsTheDaemonsStaleDisplayVariables(t *testing.T) {
	e := &launcherEnv{showEnv: execx.Result{Out: "DISPLAY=:1\n"}, exists: map[string]bool{"/tmp/.X11-unix/X1": true}}
	if err := e.launcher().Open(bg, attach); err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/usr/bin", "HOME=/home/a", "DISPLAY=:1"}
	if got := e.envs[0]; !slices.Equal(got, want) {
		t.Fatalf("env = %q, want %q (no stale WAYLAND_DISPLAY or XAUTHORITY)", got, want)
	}
}
