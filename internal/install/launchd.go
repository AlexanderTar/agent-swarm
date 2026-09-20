// Package install writes the launch agent and checks prerequisites (§19, §21).
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
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	_ "modernc.org/sqlite"
)

const Label = "dev.swarm.daemon"

type Config struct {
	Bin             string // absolute path of the swarm binary
	Home            string // swarm home (~/.swarm)
	LaunchAgentsDir string // ~/Library/LaunchAgents
	Path            string // PATH for the daemon (see LaunchPath)
	UID             int
	User            string // USER for the daemon; omitted when empty
	UserHome        string // HOME for the daemon; omitted when empty
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

// Install writes the plist and (re)starts the job. dryRun prints what it would do.
func Install(ctx context.Context, c Config, run execx.Runner, dryRun bool, w io.Writer) error {
	if HasLegacyData(filepath.Join(c.Home, "swarm.db")) {
		return db.ErrLegacy
	}
	path := PlistPath(c)
	target := fmt.Sprintf("gui/%d", c.UID)
	bootout := []string{"launchctl", "bootout", target + "/" + Label}
	bootstrap := []string{"launchctl", "bootstrap", target, path}
	if dryRun {
		fmt.Fprintf(w, "Would write %s:\n%s\nWould run:\n  %s\n  %s\n", path, Plist(c),
			strings.Join(bootout, " "), strings.Join(bootstrap, " "))
		return nil
	}
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{c.LaunchAgentsDir, 0o755}, {filepath.Join(c.Home, "logs"), 0o755}, {filepath.Join(c.Home, "run"), 0o700}} {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return err
		}
	}
	// run holds session tokens: tighten it even when it already existed.
	if err := os.Chmod(filepath.Join(c.Home, "run"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, Plist(c), 0o644); err != nil {
		return err
	}
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
