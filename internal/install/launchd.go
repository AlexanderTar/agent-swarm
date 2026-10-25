// Package install writes the daemon's service (a launch agent on macOS, a
// systemd user unit on Linux) and checks prerequisites (§19, §21).
package install

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	_ "modernc.org/sqlite"
)

const Label = "dev.swarm.daemon"

type Config struct {
	Bin             string // absolute path of the swarm binary
	Home            string // swarm home (~/.swarm)
	LaunchAgentsDir string // ~/Library/LaunchAgents
	SystemdUserDir  string // ~/.config/systemd/user (see SystemdUserDir)
	Manager         ServiceManager
	Path            string // PATH for the daemon (see LaunchPath)
	UID             int
	User            string // USER for the daemon; omitted when empty
	UserHome        string // HOME for the daemon; omitted when empty
	// Isolated is set when Home is not the default ~/.swarm and the caller did
	// not ask for --live. Install and Uninstall then leave the live service
	// (launchd's dev.swarm.daemon and dev.swarm.updater, systemd's
	// dev.swarm.daemon.service) and the user-level agent configuration alone:
	// both are fixed, machine-wide names that a scratch home must not be able
	// to stop or rewrite.
	Isolated bool
}

func PlistPath(c Config) string { return filepath.Join(c.LaunchAgentsDir, Label+".plist") }

var systemPath = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// LaunchPath cleans the caller's PATH for launchd and appends the system folders.
func LaunchPath(callerPath, userHome string) string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "~" {
			p = userHome
		} else if rest, ok := strings.CutPrefix(p, "~/"); ok {
			p = filepath.Join(userHome, rest)
		}
		if !filepath.IsAbs(p) {
			return
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range strings.Split(callerPath, ":") {
		add(p)
	}
	for _, p := range systemPath {
		add(p)
	}
	return strings.Join(out, ":")
}

func esc(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func Plist(c Config) []byte {
	logs := filepath.Join(c.Home, "logs")
	env := ""
	for _, kv := range [][2]string{{"USER", c.User}, {"HOME", c.UserHome}} {
		if kv[1] != "" {
			env += fmt.Sprintf("    <key>%s</key><string>%s</string>\n", kv[0], esc(kv[1]))
		}
	}
	// SWARM_TMUX_SOCKET=swarm and SWARM_USAGE=live are the only place the
	// production tmux socket name and the real usage sources are enabled
	// (safety invariants S-1, S-4, R9). Every other daemon — a test's, a `go
	// run`, `make dev`, `make e2e`, one started by hand in a worktree — never
	// sets either and stays on its own socket with no usage sources.
	for _, kv := range [][2]string{{"SWARM_TMUX_SOCKET", "swarm"}, {"SWARM_USAGE", "live"}} {
		env += fmt.Sprintf("    <key>%s</key><string>%s</string>\n", kv[0], kv[1])
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>daemon</string><string>--home</string><string>%s</string></array>
  <key>WorkingDirectory</key><string>%s</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key><false/>
    <key>Crashed</key><true/>
  </dict>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>%s</string>
    <key>SWARM_HOME</key><string>%s</string>
%s  </dict>
</dict>
</plist>
`, Label, esc(c.Bin), esc(c.Home), esc(c.Home), esc(filepath.Join(logs, "daemon.out.log")),
		esc(filepath.Join(logs, "daemon.err.log")), esc(c.Path), esc(c.Home), env))
}

// HasLegacyData reports whether dbPath is an Agent Swarm 1.x database (C5).
func HasLegacyData(dbPath string) bool {
	if _, err := os.Stat(dbPath); err != nil {
		return false
	}
	d, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return false
	}
	defer d.Close()
	var n int
	err = d.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_meta'`).Scan(&n)
	return err == nil && n > 0
}

// Install writes the service file and (re)starts the daemon. dryRun prints what it would do.
func Install(ctx context.Context, c Config, run execx.Runner, dryRun bool, w io.Writer) error {
	if HasLegacyData(filepath.Join(c.Home, "swarm.db")) {
		return db.ErrLegacy
	}
	svc := c.Service()
	if c.Isolated {
		fmt.Fprintf(w, "%s is not the default swarm home, so the %s %s was left alone. Pass --live to install it anyway.\n", c.Home, Label, svc.Noun())
		return nil
	}
	if err := checkServicePaths(c); err != nil {
		return err
	}
	path := svc.Path()
	if dryRun {
		fmt.Fprintf(w, "Would write %s:\n%s\nWould run:\n", path, svc.Render())
		for _, argv := range svc.Commands() {
			fmt.Fprintf(w, "  %s\n", strings.Join(argv, " "))
		}
		return nil
	}
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Dir(path), 0o755}, {filepath.Join(c.Home, "logs"), 0o755}, {filepath.Join(c.Home, "run"), 0o700}} {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return err
		}
	}
	// run holds session tokens: tighten it even when it already existed.
	if err := os.Chmod(filepath.Join(c.Home, "run"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, svc.Render(), 0o644); err != nil {
		return err
	}
	if err := svc.Start(ctx, run, w); err != nil {
		return err
	}
	fmt.Fprintf(w, "Installed %s and started the daemon.\n", path)
	return nil
}

// NotLoaded reports whether a launchctl bootout error only means the job was not
// loaded. internal/migrate needs the same judgement (§20 step 2).
func NotLoaded(err error) bool { return notLoaded(err) }

// notLoaded reports whether a bootout error only means the job wasn't loaded
// (launchctl exits 3 "No such process", or 113 "Could not find service").
func notLoaded(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 3 {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such process") || strings.Contains(msg, "could not find service")
}
