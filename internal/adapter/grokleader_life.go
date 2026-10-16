package adapter

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// One leader per agent, never shared: hooks and MCP servers run as children of
// the leader with the env of the TUI that started it, so a shared leader would
// run every agent's hooks as the first agent (TASK-1129 probe 2). The agent's
// own TUI starts its leader (--leader --leader-socket <path>), so Swarm never
// spawns one. The leader outlives the TUI and is re-parented to pid 1, so
// SweepLeaders stops it by pid once its session is gone. `grok leader kill`
// stops every leader on the machine, user-started ones included, and is never run.

const (
	leaderSockPrefix = "grok-leader-"
	leaderSockSuffix = ".sock"
)

// leaderSocket is <home>/run/grok-leader-<session id>.sock. A path never gets
// reused for a new identity because the session id is in it. ok is false when
// the path would not fit sun_path.
func (g *Grok) leaderSocket(sessionID string) (string, bool) {
	p := filepath.Join(g.d.Home, "run", leaderSockPrefix+sessionID+leaderSockSuffix)
	return p, len(p) <= codexSocketPathMax
}

// leaderArgs is the launch argv that makes the TUI start its own leader on this
// session's socket, and the prep that needs: the run dir, and no stale socket
// left by a crashed leader (the TUI would find the file and fail to connect). A
// path too long for sun_path leaves the flags off, so the agent runs without a
// leader and wake falls back to the paste.
func (g *Grok) leaderArgs(sessionID string) []string {
	sock, ok := g.leaderSocket(sessionID)
	if !ok {
		g.d.Log("grok: leader socket path %q is over %d bytes, launching without a leader (wake will paste)", sock, codexSocketPathMax)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		g.d.Log("grok: leader socket dir: %v, launching without a leader", err)
		return nil
	}
	removeIfStale(sock)
	return []string{"--leader", "--leader-socket", sock}
}

// listening reports whether something accepts connections on the socket.
func listening(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// removeIfStale deletes a socket file nothing listens on.
func removeIfStale(path string) {
	if _, err := os.Lstat(path); err == nil && !listening(path) {
		os.Remove(path)
	}
}

// SweepLeaders stops the leader of every session that is not live and removes
// the socket files nothing listens on. The runtime calls it each reconcile
// tick, so it never waits: SIGTERM makes the leader exit and remove its own
// socket, and a leader that is still up on the next tick is signalled again.
func (g *Grok) SweepLeaders(live map[string]bool) {
	socks, _ := filepath.Glob(filepath.Join(g.d.Home, "run", leaderSockPrefix+"*"+leaderSockSuffix))
	for _, sock := range socks {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(sock), leaderSockPrefix), leaderSockSuffix)
		if live[id] {
			continue
		}
		if !listening(sock) {
			os.Remove(sock)
			continue
		}
		if err := stopLeaderByPid(sock); err != nil {
			g.d.Log("grok: stop leader for %s: %v", id, err)
		}
	}
}

// stopLeaderByPid SIGTERMs the process listening on the socket. The pid is the
// socket peer's, read from the kernel, so it can never be a recycled pid
// recorded earlier.
func stopLeaderByPid(sock string) error {
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	pid, err := peerPID(c.(*net.UnixConn))
	if err != nil {
		return err
	}
	if pid <= 1 || pid == os.Getpid() {
		return errors.New("socket peer is not a leader process")
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
