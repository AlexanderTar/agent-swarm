package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/AlexanderTar/agent-swarm/internal/migrate"
)

// pinGOOS makes newConfig pick the service manager of goos for one test.
func pinGOOS(t *testing.T, system string) {
	t.Helper()
	saved := goos
	t.Cleanup(func() { goos = saved })
	goos = system
}

func TestNewConfigPicksTheServiceManager(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	pinGOOS(t, "darwin")
	c, err := newConfig(filepath.Join(userHome, ".swarm"))
	if err != nil || c.Manager != install.Launchd || c.Service().Path() != filepath.Join(userHome, "Library", "LaunchAgents", "dev.swarm.daemon.plist") {
		t.Fatalf("darwin config = %+v %v", c, err)
	}
	pinGOOS(t, "linux")
	if c, err = newConfig(filepath.Join(userHome, ".swarm")); err != nil || c.Manager != install.Systemd ||
		c.Service().Path() != filepath.Join(userHome, ".config", "systemd", "user", "dev.swarm.daemon.service") {
		t.Fatalf("linux config = %+v %v", c, err)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if c, _ = newConfig(filepath.Join(userHome, ".swarm")); c.Service().Path() != filepath.Join(xdg, "systemd", "user", "dev.swarm.daemon.service") {
		t.Fatalf("linux config with XDG_CONFIG_HOME = %s", c.Service().Path())
	}
}

func TestInstallDryRunOnLinuxShowsTheSystemdUnit(t *testing.T) {
	pinGOOS(t, "linux")
	home, userHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	code, out, _ := swarm("install", "--dry-run", "--live", "--home", home)
	for _, want := range []string{
		"Would write " + filepath.Join(userHome, ".config", "systemd", "user", "dev.swarm.daemon.service"),
		`"daemon" "--home" "` + home + `"`,
		`"SWARM_HOME=` + home + `"`,
		"  systemctl --user restart dev.swarm.daemon.service\n",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("dry run = %d, lacks %q:\n%s", code, want, out)
		}
	}
	if strings.Contains(out, "launchctl") {
		t.Errorf("a Linux dry run mentions launchctl:\n%s", out)
	}
	for _, dir := range []string{home, userHome} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("dry run wrote into %s: %v", dir, entries)
		}
	}
}

// Agent Swarm 1.x was macOS-only: on Linux migrate says so and does nothing,
// whatever the mode or home.
func TestMigrateOnLinuxHasNothingToMigrate(t *testing.T) {
	pinGOOS(t, "linux")
	saved := migrateRun
	t.Cleanup(func() { migrateRun = saved })
	migrateRun = func(context.Context, *migrate.Runner, string) error {
		t.Error("migrate ran on Linux")
		return nil
	}
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	for _, args := range [][]string{{}, {"--dry-run"}, {"--resume"}, {"--rollback"}, {"--home", t.TempDir()}, {"--live", "--home", filepath.Join(userHome, ".swarm")}} {
		var out bytes.Buffer
		if code := run(append([]string{"migrate"}, args...), &out, io.Discard); code != 0 ||
			out.String() != "Agent Swarm 1.x only ran on macOS, so there is nothing to migrate.\n" {
			t.Errorf("migrate %v = %d %q", args, code, out.String())
		}
	}
}

func TestUninstallNamesTheServiceItRemoved(t *testing.T) {
	saved := installUninstall
	t.Cleanup(func() { installUninstall = saved })
	installUninstall = func(context.Context, install.AgentsOpts) error { return nil }
	for system, want := range map[string]string{"darwin": "Removed the launch agent and", "linux": "Removed the systemd user service and"} {
		pinGOOS(t, system)
		userHome := t.TempDir()
		t.Setenv("HOME", userHome)
		var out bytes.Buffer
		if code := run([]string{"uninstall", "--home", filepath.Join(userHome, ".swarm")}, &out, io.Discard); code != 0 || !strings.Contains(out.String(), want) {
			t.Errorf("%s: uninstall = %d %q, want %q", system, code, out.String(), want)
		}
	}
}
