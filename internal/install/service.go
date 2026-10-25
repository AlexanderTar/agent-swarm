package install

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// ServiceManager is the OS service manager that keeps the daemon running.
type ServiceManager string

const (
	// Launchd is macOS: a launch agent in the gui/<uid> domain. The zero value.
	Launchd ServiceManager = ""
	// Systemd is Linux: a systemd user unit in the user's manager.
	Systemd ServiceManager = "systemd"
)

// Service is the daemon's entry in the service manager. Install writes Render()
// to Path() and calls Start; Uninstall calls Stop, removes Path() and calls
// Removed. Every command goes through the injected runner, and execx refuses
// the real launchctl/systemctl/loginctl under `go test` (BUG-100).
type Service interface {
	Noun() string // "launch agent" or "systemd user service", for copy
	Path() string
	Render() []byte
	Commands() [][]string // what Start runs, for --dry-run
	Start(ctx context.Context, run execx.Runner, w io.Writer) error
	Stop(ctx context.Context, run execx.Runner) error
	Removed(ctx context.Context, run execx.Runner) error
}

// Service returns c's daemon entry in its service manager.
func (c Config) Service() Service {
	if c.Manager == Systemd {
		return systemdService{c}
	}
	return launchdService{c}
}

type launchdService struct{ c Config }

func (launchdService) Noun() string     { return "launch agent" }
func (s launchdService) Path() string   { return PlistPath(s.c) }
func (s launchdService) Render() []byte { return Plist(s.c) }

func (s launchdService) target() string { return fmt.Sprintf("gui/%d", s.c.UID) }

func (s launchdService) Commands() [][]string {
	return [][]string{
		{"launchctl", "bootout", s.target() + "/" + Label},
		{"launchctl", "bootstrap", s.target(), s.Path()},
	}
}

func (s launchdService) Start(ctx context.Context, run execx.Runner, _ io.Writer) error {
	cmds := s.Commands()
	bootout, bootstrap := cmds[0], cmds[1]
	if _, err := run(ctx, bootout[0], bootout[1:]...); err != nil && !notLoaded(err) {
		return fmt.Errorf("launchctl bootout: %w", err)
	} else if err == nil {
		// launchd teardown of the old service registration is asynchronous.
		// A brief pause prevents bootstrap from racing with teardown and failing with exit status 5.
		time.Sleep(300 * time.Millisecond)
	}
	if _, err := run(ctx, bootstrap[0], bootstrap[1:]...); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	return nil
}

func (s launchdService) Stop(ctx context.Context, run execx.Runner) error {
	if _, err := run(ctx, "launchctl", "bootout", s.target()+"/"+Label); err != nil && !notLoaded(err) {
		return fmt.Errorf("launchctl bootout %s: %w", Label, err)
	}
	return nil
}

func (launchdService) Removed(context.Context, execx.Runner) error { return nil }

// Unit is the systemd user unit's file name: the launchd label, so the
// isolation guards and docs keep one name for the daemon on both systems.
const Unit = Label + ".service"

type systemdService struct{ c Config }

func (systemdService) Noun() string   { return "systemd user service" }
func (s systemdService) Path() string { return filepath.Join(s.c.SystemdUserDir, Unit) }

func (s systemdService) Render() []byte { return SystemdUnit(s.c) }

func (s systemdService) Commands() [][]string {
	return [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", Unit},
		{"systemctl", "--user", "restart", Unit},
	}
}

func (s systemdService) Start(ctx context.Context, run execx.Runner, w io.Writer) error {
	for _, argv := range s.Commands() {
		if _, err := run(ctx, argv[0], argv[1:]...); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
		}
	}
	// Without linger the user manager, and every user service with it, stops
	// at logout. Turning it on is a machine setting, so it is only suggested.
	out, err := run(ctx, "loginctl", "show-user", fmt.Sprint(s.c.UID), "--property=Linger", "--value")
	if err == nil && strings.TrimSpace(string(out)) == "no" {
		fmt.Fprintln(w, "Linger is off, so systemd stops the daemon when you log out. To keep it running, run: loginctl enable-linger")
	}
	return nil
}

func (s systemdService) Stop(ctx context.Context, run execx.Runner) error {
	if _, err := run(ctx, "systemctl", "--user", "disable", "--now", Unit); err != nil && !unitMissing(err) {
		return fmt.Errorf("systemctl --user disable --now %s: %w", Unit, err)
	}
	return nil
}

func (s systemdService) Removed(ctx context.Context, run execx.Runner) error {
	if _, err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	return nil
}

// unitMissing reports whether a systemctl error only means the unit isn't
// there: "Unit file … does not exist." (disable) or "Unit … not loaded." (stop).
func unitMissing(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "does not exist") || strings.Contains(msg, "not loaded")
}

// SystemdUnit renders the user unit. Like the plist it is the only place the
// production switches SWARM_TMUX_SOCKET=swarm and SWARM_USAGE=live are set
// (S-1, S-4, R9). systemd sets HOME and USER for user units itself.
//
// KillMode=process: the daemon starts the private tmux server, which stays
// in the unit's cgroup. The default (control-group) would kill tmux and every
// agent on each restart, crash or reinstall; launchd leaves them alone because
// tmux calls setsid. Probed on systemd 255: tmux and its panes survive a
// SIGKILL of the daemon, `systemctl --user restart` and `stop` with process,
// and die on `stop` without it. Restart=on-failure with RestartSec=10 is the
// plist's KeepAlive{SuccessfulExit: false, Crashed: true} with ThrottleInterval 10.
func SystemdUnit(c Config) []byte {
	logs := filepath.Join(c.Home, "logs")
	env := []string{"PATH=" + c.Path, "SWARM_HOME=" + c.Home, "SWARM_TMUX_SOCKET=swarm", "SWARM_USAGE=live"}
	for i, kv := range env {
		env[i] = systemdQuote(kv, false)
	}
	// The program path is never variable-expanded, so its $ stays single.
	exec := []string{systemdQuote(c.Bin, false)}
	for _, arg := range []string{"daemon", "--home", c.Home} {
		exec = append(exec, systemdQuote(arg, true))
	}
	return []byte(fmt.Sprintf(`[Unit]
Description=Agent Swarm daemon

[Service]
Type=simple
ExecStart=%s
WorkingDirectory=%s
Environment=%s
Restart=on-failure
RestartSec=10
KillMode=process
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, strings.Join(exec, " "), systemdPath(c.Home), strings.Join(env, " "),
		systemdPath(filepath.Join(logs, "daemon.out.log")), systemdPath(filepath.Join(logs, "daemon.err.log"))))
}

// systemdQuote quotes one ExecStart= word or Environment= assignment
// (systemd.syntax, systemd.exec): backslash and double quote are escaped
// inside the quotes, % is a specifier everywhere, and $ is expanded only in
// ExecStart='s arguments (not its program path, nor Environment=). Checked
// against systemd 255 with a probe unit: "a$$1" ran as a$1, "q\"uo\\te" as
// q"uo\te, Environment="E1=x$y" stayed x$y, and "p%%q" became p%q.
func systemdQuote(s string, expandsDollar bool) string {
	r := []string{`\`, `\\`, `"`, `\"`, "%", "%%"}
	if expandsDollar {
		r = append(r, "$", "$$")
	}
	return `"` + strings.NewReplacer(r...).Replace(s) + `"`
}

// systemdPath escapes a path for WorkingDirectory= and append:, where
// specifiers are expanded but quotes are not parsed.
func systemdPath(p string) string { return strings.ReplaceAll(p, "%", "%%") }

// SystemdUserDir is where systemd reads user units: $XDG_CONFIG_HOME/systemd/user,
// or ~/.config/systemd/user when XDG_CONFIG_HOME is unset or not absolute.
func SystemdUserDir(userHome, xdgConfigHome string) string {
	if filepath.IsAbs(xdgConfigHome) {
		return filepath.Join(xdgConfigHome, "systemd", "user")
	}
	return filepath.Join(userHome, ".config", "systemd", "user")
}

// checkServicePaths refuses paths a unit or plist can't carry on one line.
func checkServicePaths(c Config) error {
	for _, p := range []string{c.Bin, c.Home, c.Path} {
		if strings.ContainsAny(p, "\n\r") {
			return fmt.Errorf("%q contains a line break, which the %s can't hold", p, c.Service().Noun())
		}
	}
	return nil
}

// systemdCheck is doctor's service row on Linux.
func systemdCheck(c Config) Check {
	path := c.Service().Path()
	body, err := os.ReadFile(path)
	if err != nil {
		return Check{"Service", false, "Not installed. Run swarm install."}
	}
	s := string(body)
	if !strings.Contains(s, `"daemon" "--home"`) || !strings.Contains(s, `"PATH=`) || !strings.Contains(s, "KillMode=process") {
		return Check{"Service", false, "The systemd unit is out of date. Run swarm install."}
	}
	return Check{"Service", true, path}
}
