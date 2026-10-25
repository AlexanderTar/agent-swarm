package install

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

func systemdConfig(t *testing.T) Config {
	c := testConfig(t)
	c.Manager = Systemd
	c.SystemdUserDir = filepath.Join(filepath.Dir(c.Home), "systemd", "user")
	return c
}

// systemdStart is every command a systemd install runs, with linger off.
func systemdStart(linger string) map[string]execx.Result {
	return map[string]execx.Result{
		"systemctl --user daemon-reload":                          {},
		"systemctl --user enable dev.swarm.daemon.service":        {},
		"systemctl --user restart dev.swarm.daemon.service":       {},
		"loginctl show-user 501 --property=Linger --value":        {Out: linger + "\n"},
		"systemctl --user disable --now dev.swarm.daemon.service": {},
	}
}

func TestServiceFollowsTheManager(t *testing.T) {
	c := testConfig(t)
	if s := c.Service(); s.Noun() != "launch agent" || s.Path() != PlistPath(c) || !bytes.Equal(s.Render(), Plist(c)) {
		t.Errorf("launchd service = %s %s", s.Noun(), s.Path())
	}
	c = systemdConfig(t)
	if s := c.Service(); s.Noun() != "systemd user service" || s.Path() != filepath.Join(c.SystemdUserDir, "dev.swarm.daemon.service") || !bytes.Equal(s.Render(), SystemdUnit(c)) {
		t.Errorf("systemd service = %s %s", s.Noun(), s.Path())
	}
}

func TestSystemdUnitContent(t *testing.T) {
	c := systemdConfig(t)
	u := string(SystemdUnit(c))
	for _, want := range []string{
		"\n[Service]\nType=simple\n",
		`ExecStart="/Users/a/.swarm/bin/swarm" "daemon" "--home" "` + c.Home + `"` + "\n",
		"WorkingDirectory=" + c.Home + "\n",
		// The unit is the only place the production switches are set (S-1, S-4, R9).
		`Environment="PATH=/Users/a/bin:/usr/bin&x" "SWARM_HOME=` + c.Home + `" "SWARM_TMUX_SOCKET=swarm" "SWARM_USAGE=live" "SWARM_NOTIFY=desktop"` + "\n",
		"\nRestart=on-failure\nRestartSec=10\n",
		// The tmux server and every agent must survive a restart (see SystemdUnit).
		"\nKillMode=process\n",
		"StandardOutput=append:" + c.Home + "/logs/daemon.out.log\n",
		"StandardError=append:" + c.Home + "/logs/daemon.err.log\n",
		"\n[Install]\nWantedBy=default.target\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q\n%s", want, u)
		}
	}
	// systemd sets USER and HOME for user units; the plist's keychain account is not needed.
	if strings.Contains(u, `"USER=`) || strings.Contains(u, `"HOME=`) {
		t.Errorf("unit sets USER or HOME\n%s", u)
	}
}

// Each directive has its own escaping rules (systemd.syntax, systemd.exec).
func TestSystemdUnitEscaping(t *testing.T) {
	c := systemdConfig(t)
	c.Bin = `/opt/my apps/sw"arm\%x$y`
	c.Home = `/home/a b/.swarm%h$H`
	c.Path = `/home/a b/bin:/x%y$z`
	u := string(SystemdUnit(c))
	for _, want := range []string{
		// ExecStart: quoted words, \ and " escaped, % doubled; $ doubled in the
		// arguments only, since the program path is never variable-expanded.
		`ExecStart="/opt/my apps/sw\"arm\\%%x$y" "daemon" "--home" "/home/a b/.swarm%%h$$H"` + "\n",
		// Environment: quoted assignments, % doubled, $ left alone (never expanded there).
		`Environment="PATH=/home/a b/bin:/x%%y$z" "SWARM_HOME=/home/a b/.swarm%%h$H"`,
		// WorkingDirectory and append: take the raw path, only % is special.
		"WorkingDirectory=/home/a b/.swarm%%h$H\n",
		"StandardOutput=append:/home/a b/.swarm%%h$H/logs/daemon.out.log\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %s\n%s", want, u)
		}
	}
}

// systemd itself accepts the rendered unit, odd characters included. Skipped
// where systemd-analyze is missing (macOS); it only parses a file, it never
// talks to a service manager.
func TestSystemdAnalyzeAcceptsTheUnit(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not installed")
	}
	root := t.TempDir()
	c := systemdConfig(t)
	c.Bin = filepath.Join(root, "my apps", `sw%a$rm`)
	c.Home = filepath.Join(root, "a b", ".swarm")
	for _, dir := range []string{filepath.Dir(c.Bin), filepath.Join(c.Home, "logs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(c.Bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(root, Unit)
	if err := os.WriteFile(unit, SystemdUnit(c), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(analyze, "verify", "--user", unit).CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) > 0 {
		t.Fatalf("systemd-analyze verify: %v\n%s\n%s", err, out, SystemdUnit(c))
	}
}

func TestSystemdInstallWritesTheUnitAndRestartsTheDaemon(t *testing.T) {
	c := systemdConfig(t)
	f := &execx.Fake{Responses: systemdStart("no")}
	var out bytes.Buffer
	if err := Install(bg, c, f.Runner(), false, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable dev.swarm.daemon.service",
		"systemctl --user restart dev.swarm.daemon.service",
		"loginctl show-user 501 --property=Linger --value",
	}
	if got := f.Calls(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
	path := filepath.Join(c.SystemdUserDir, Unit)
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, SystemdUnit(c)) {
		t.Fatalf("unit = %q %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(c.Home, "run")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("run dir = %v %v", fi, err)
	}
	for _, want := range []string{"loginctl enable-linger", "Installed " + path + " and started the daemon."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if launchctl := filepath.Join(c.LaunchAgentsDir); dirExists(launchctl) {
		t.Errorf("a systemd install created %s", launchctl)
	}
}

func dirExists(p string) bool { _, err := os.Stat(p); return err == nil }

// Linger is only suggested when loginctl says it is off.
func TestSystemdInstallLingerNote(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  execx.Result
		note bool
	}{
		{"off", execx.Result{Out: "no\n"}, true},
		{"on", execx.Result{Out: "yes\n"}, false},
		{"loginctl fails", execx.Result{Err: errors.New("loginctl: not found")}, false},
	} {
		c := systemdConfig(t)
		r := systemdStart("no")
		r["loginctl show-user 501 --property=Linger --value"] = tc.res
		var out bytes.Buffer
		if err := Install(bg, c, (&execx.Fake{Responses: r}).Runner(), false, &out); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := strings.Contains(out.String(), "enable-linger"); got != tc.note {
			t.Errorf("%s: linger note = %v, want %v:\n%s", tc.name, got, tc.note, out.String())
		}
	}
}

func TestSystemdInstallReportsAFailingSystemctl(t *testing.T) {
	c := systemdConfig(t)
	r := systemdStart("yes")
	r["systemctl --user enable dev.swarm.daemon.service"] = execx.Result{Err: errors.New("Failed to connect to bus: No medium found")}
	err := Install(bg, c, (&execx.Fake{Responses: r}).Runner(), false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "systemctl --user enable dev.swarm.daemon.service") || !strings.Contains(err.Error(), "No medium found") {
		t.Fatalf("err = %v", err)
	}
}

func TestSystemdDryRunPrintsTheUnitAndCommandsOnly(t *testing.T) {
	c := systemdConfig(t)
	f := &execx.Fake{}
	var out bytes.Buffer
	if err := Install(bg, c, f.Runner(), true, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("dry run ran %v", f.Calls())
	}
	if dirExists(c.SystemdUserDir) || dirExists(c.Home) {
		t.Error("dry run created files")
	}
	for _, want := range []string{"Would write " + filepath.Join(c.SystemdUserDir, Unit), "KillMode=process",
		"  systemctl --user daemon-reload\n", "  systemctl --user enable dev.swarm.daemon.service\n", "  systemctl --user restart dev.swarm.daemon.service\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run lacks %q:\n%s", want, out.String())
		}
	}
}

// The launchd dry run keeps today's output.
func TestLaunchdDryRunIsUnchanged(t *testing.T) {
	c := testConfig(t)
	var out bytes.Buffer
	if err := Install(bg, c, (&execx.Fake{}).Runner(), true, &out); err != nil {
		t.Fatal(err)
	}
	want := "Would write " + PlistPath(c) + ":\n" + string(Plist(c)) + "\nWould run:\n  launchctl bootout gui/501/dev.swarm.daemon\n  launchctl bootstrap gui/501 " + PlistPath(c) + "\n"
	if out.String() != want {
		t.Errorf("dry run =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestInstallRefusesALineBreakInAPath(t *testing.T) {
	for _, m := range []ServiceManager{Launchd, Systemd} {
		c := systemdConfig(t)
		c.Manager = m
		c.Home += "\nExecStartPre=/bin/evil"
		f := &execx.Fake{}
		err := Install(bg, c, f.Runner(), false, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "line break") {
			t.Errorf("%q: err = %v", m, err)
		}
		if len(f.Calls()) != 0 || dirExists(c.SystemdUserDir) || dirExists(c.LaunchAgentsDir) {
			t.Errorf("%q: install went ahead: %v", m, f.Calls())
		}
	}
}

func TestSystemdStopToleratesAMissingUnit(t *testing.T) {
	for _, msg := range []string{
		"Failed to disable unit: Unit file dev.swarm.daemon.service does not exist.",
		"Failed to stop dev.swarm.daemon.service: Unit dev.swarm.daemon.service not loaded.",
	} {
		r := map[string]execx.Result{"systemctl --user disable --now dev.swarm.daemon.service": {Err: errors.New(msg)}}
		if err := systemdConfig(t).Service().Stop(bg, (&execx.Fake{Responses: r}).Runner()); err != nil {
			t.Errorf("%s: %v", msg, err)
		}
	}
	r := map[string]execx.Result{"systemctl --user disable --now dev.swarm.daemon.service": {Err: errors.New("Failed to connect to bus")}}
	if err := systemdConfig(t).Service().Stop(bg, (&execx.Fake{Responses: r}).Runner()); err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Errorf("a real failure was swallowed: %v", err)
	}
}

func TestSystemdUninstallStopsRemovesAndReloads(t *testing.T) {
	c := systemdConfig(t)
	path := filepath.Join(c.SystemdUserDir, Unit)
	if err := os.MkdirAll(c.SystemdUserDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, SystemdUnit(c), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{
		"systemctl --user disable --now dev.swarm.daemon.service": {},
		"systemctl --user daemon-reload":                          {},
	}}
	o := AgentsOpts{Cfg: c, Run: f.Runner(), Out: &bytes.Buffer{}}
	if err := Uninstall(bg, o); err != nil {
		t.Fatal(err)
	}
	if dirExists(path) {
		t.Error("the unit is still there")
	}
	calls := f.Calls()
	if len(calls) < 2 || calls[0] != "systemctl --user disable --now dev.swarm.daemon.service" || calls[1] != "systemctl --user daemon-reload" {
		t.Errorf("calls = %q", calls)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "launchctl") {
			t.Errorf("a systemd uninstall ran %s", call)
		}
	}
}

// Agent Swarm 1.x only ran under launchd: a systemd install never asks for its updater.
func TestSystemdSkipsTheV1UpdaterBootout(t *testing.T) {
	c := systemdConfig(t)
	f := &execx.Fake{}
	if _, err := RemoveLegacyShared(bg, c, f.Runner(), func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("calls = %v", f.Calls())
	}
}

func TestSystemdUserDir(t *testing.T) {
	for _, tc := range []struct{ xdg, want string }{
		{"", "/home/a/.config/systemd/user"},
		{"/xdg", "/xdg/systemd/user"},
		{"relative/cfg", "/home/a/.config/systemd/user"}, // XDG says relative values are ignored
	} {
		if got := SystemdUserDir("/home/a", tc.xdg); got != tc.want {
			t.Errorf("SystemdUserDir(%q) = %s, want %s", tc.xdg, got, tc.want)
		}
	}
}

func TestDoctorServiceRowUnderSystemd(t *testing.T) {
	e := newDoctorEnv(t)
	c := systemdConfig(t)
	e.d.Cfg = c
	row := func() Check {
		for _, ch := range e.d.Checks(bg) {
			if ch.Name == "Service" {
				return ch
			}
		}
		t.Fatal("no Service row")
		return Check{}
	}
	if ch := row(); ch.OK || ch.Detail != "Not installed. Run swarm install." {
		t.Errorf("missing unit = %+v", ch)
	}
	path := filepath.Join(c.SystemdUserDir, Unit)
	os.MkdirAll(c.SystemdUserDir, 0o755)
	os.WriteFile(path, []byte("[Service]\nExecStart=/old/swarm daemon\n"), 0o644)
	if ch := row(); ch.OK || !strings.Contains(ch.Detail, "out of date") {
		t.Errorf("stale unit = %+v", ch)
	}
	os.WriteFile(path, SystemdUnit(c), 0o644)
	if ch := row(); !ch.OK || ch.Detail != path {
		t.Errorf("current unit = %+v", ch)
	}
	for _, ch := range e.d.Checks(bg) {
		if ch.Name == "Launch agent" {
			t.Errorf("systemd doctor still has a launch agent row: %+v", ch)
		}
	}
}
