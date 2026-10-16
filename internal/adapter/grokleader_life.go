package adapter

import (
	"context"
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

// SweepLeaders is the per-tick upkeep of every session's leader: a live
// session's leader gets a watcher (grokleader_state.go), and for a session that
// is not live the watcher goes, its leader is stopped, and a socket file nothing
// listens on is removed. The runtime calls it each reconcile tick, so it never
// waits: SIGTERM makes the leader exit and remove its own socket, and a leader
// still up on the next tick is signalled again.
func (g *Grok) SweepLeaders(live map[string]bool) {
	g.leader.stopWatchersExcept(live)
	socks, _ := filepath.Glob(filepath.Join(g.d.Home, "run", leaderSockPrefix+"*"+leaderSockSuffix))
	for _, sock := range socks {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(sock), leaderSockPrefix), leaderSockSuffix)
		if live[id] {
			if listening(sock) {
				g.watchLeader(id, sock)
			}
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

// grokWakeTurnTimeout bounds the goroutine Wake leaves waiting for the queued
// turn to end, so a hung turn cannot leak a connection.
const grokWakeTurnTimeout = 30 * time.Minute

// dialSessionLeader connects to the session's own leader, or returns nil when
// the wake has to paste instead: no provider session yet, no leader running,
// or one this client refuses (logged, since that is a grok update to look at).
func (g *Grok) dialSessionLeader(ctx context.Context, sessionID, providerID string) *leaderClient {
	if providerID == "" {
		return nil
	}
	sock, ok := g.leaderSocket(sessionID)
	if !ok {
		return nil
	}
	if _, err := os.Stat(sock); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := dialLeader(ctx, sock)
	if err != nil {
		if errors.Is(err, ErrUnsupportedLeader) {
			g.d.Log("grok: leader for %s refused, waking by paste: %v", sessionID, err)
		}
		return nil
	}
	return c
}

// Wake delivers the notice as a session/prompt on the session's leader. A
// turn in progress gets no prompt: its mail already reaches the agent through
// the hooks, and a queued prompt would run as an extra metered turn after it
// synced, so Wake reports delivered=false and the runtime tries again next
// tick. The request only returns when
// that turn ends, so it runs on a goroutine and Wake reports delivered as soon
// as the prompt is written. It never errors: with the leader down, refused or
// failing, delivered=false lets the runtime paste (one Enter), without the
// backoff an error would add to that paste.
func (g *Grok) Wake(ctx context.Context, w WakeTarget) (bool, error) {
	if g.LeaderBusy(w.SessionID) {
		return false, nil
	}
	c := g.dialSessionLeader(ctx, w.SessionID, w.ProviderSessionID)
	if c == nil {
		return false, nil
	}
	wait, err := c.StartPrompt(w.ProviderSessionID, w.Notice)
	if err != nil {
		c.Close()
		g.d.Log("grok: leader prompt for %s: %v, waking by paste", w.SessionID, err)
		return false, nil
	}
	go func() {
		defer c.Close()
		t := time.AfterFunc(grokWakeTurnTimeout, func() { c.Close() })
		defer t.Stop()
		// A -32003 (rate limited) still means the prompt reached the session, so
		// any outcome here is only worth a log line.
		if err := wait(); err != nil {
			g.d.Log("grok: leader turn for %s ended with: %v", w.SessionID, err)
		}
	}()
	return true, nil
}

// RenameViaLeader sets the session title over the leader, as /rename does in
// the pane but without touching it. False means the runtime must paste /rename.
func (g *Grok) RenameViaLeader(ctx context.Context, sessionID, providerID, name string) bool {
	c := g.dialSessionLeader(ctx, sessionID, providerID)
	if c == nil {
		return false
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Rename(ctx, providerID, name); err != nil {
		g.d.Log("grok: leader rename for %s: %v", sessionID, err)
		return false
	}
	return true
}
