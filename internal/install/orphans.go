package install

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// Proc is one row of `ps -axEww`: a process and the SWARM_SESSION it carries
// in its environment ("" when none).
type Proc struct {
	PID, PPID int
	Session   string
	Command   string
}

// SessionRef is what the doctor needs to know about a session id.
type SessionRef struct{ Agent, State string }

var (
	psSessionRE = regexp.MustCompile(`(?:^|\s)SWARM_SESSION=(\S+)`)
	psEnvRE     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*=`)
)

// psArgs lists every process with its environment after the command: BSD ps
// takes -E on macOS; procps on Linux rejects -E ("must set personality") and
// takes BSD-style e, without the dash, for the same output.
func psArgs(m ServiceManager) []string {
	if m == Systemd {
		return []string{"axeww", "-o", "pid=,ppid=,command="}
	}
	return []string{"-axEww", "-o", "pid=,ppid=,command="}
}

// ListProcs parses `ps -axEww` (`ps axeww` on Linux). The environment follows
// the command with no delimiter, so the command is cut at the first
// NAME=value token.
func ListProcs(ctx context.Context, run execx.Runner, m ServiceManager) ([]Proc, error) {
	out, err := run(ctx, "ps", psArgs(m)...)
	if err != nil {
		return nil, err
	}
	var procs []Proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		p := Proc{PID: pid, PPID: ppid}
		if m := psSessionRE.FindStringSubmatch(line); m != nil {
			p.Session = m[1]
		}
		cmd := f[2:]
		for i, w := range cmd {
			if psEnvRE.MatchString(w) {
				cmd = cmd[:i]
				break
			}
		}
		p.Command = strings.Join(cmd, " ")
		procs = append(procs, p)
	}
	return procs, nil
}

// sharedDaemons are long-lived helpers that inherit SWARM_SESSION from
// whichever agent first started them and are reparented to launchd (or
// systemd) by design, so they are not orphans. Matched on the executable's
// base name; dockerd and containerd are Docker's on Linux.
var sharedDaemons = map[string]bool{
	"tmux": true, "gpg-agent": true, "dirmngr": true, "adb": true,
	"emulator": true, "crashpad_handler": true, "com.docker.backend": true, "Swarm": true,
	"dockerd": true, "containerd": true, "containerd-shim": true, "containerd-shim-runc-v2": true,
}

// adopters are the processes an orphan is reparented to: pid 1, and on Linux
// the user's systemd manager, a child subreaper that adopts orphans of the
// user's services before pid 1 can.
func adopters(procs []Proc) map[int]bool {
	out := map[int]bool{1: true}
	for _, p := range procs {
		if f := strings.Fields(p.Command); len(f) > 1 && filepath.Base(f[0]) == "systemd" && f[1] == "--user" {
			out[p.PID] = true
		}
	}
	return out
}

// isSwarmInfra reports whether command is the swarm tmux server, Swarm.app,
// the daemon or another shared helper from sharedDaemons.
func isSwarmInfra(command string) bool {
	f := strings.Fields(command)
	if len(f) == 0 {
		return true
	}
	if strings.Contains(command, "Swarm.app/") || strings.Contains(command, "Docker.app/") ||
		strings.Contains(filepath.Base(f[0]), "qemu-system") {
		return true
	}
	base := filepath.Base(f[0])
	return sharedDaemons[base] || (base == "swarm" && len(f) > 1 && f[1] == "daemon")
}

var terminalSession = map[string]bool{"completed": true, "failed": true, "crashed": true, "cancelled": true}

// orphans lists processes that outlived their agent: reparented to launchd
// (ppid 1) or a `systemd --user` subreaper, and carrying the SWARM_SESSION of
// a session in a terminal state. It
// only lists; it never signals (env is not proof of ownership). It warns
// rather than fails, like python3.
func (d Doctor) orphans(ctx context.Context) []Check {
	if d.Procs == nil || d.Session == nil {
		return nil
	}
	const name = "Orphan processes"
	procs, err := d.Procs(ctx)
	if err != nil {
		return []Check{{name, true, "Couldn't list processes: " + err.Error()}}
	}
	adopted := adopters(procs)
	var lines []string
	for _, p := range procs {
		if !adopted[p.PPID] || p.Session == "" || isSwarmInfra(p.Command) {
			continue
		}
		ref, ok := d.Session(ctx, p.Session)
		if !ok || !terminalSession[ref.State] {
			continue
		}
		lines = append(lines, fmt.Sprintf("pid %d (%s, %s): %s", p.PID, ref.Agent, p.Session, p.Command))
	}
	if len(lines) == 0 {
		return []Check{{name, true, "No orphan processes from finished sessions."}}
	}
	return []Check{{name, true, fmt.Sprintf("%d from finished sessions, still running: %s", len(lines), strings.Join(lines, "; "))}}
}
