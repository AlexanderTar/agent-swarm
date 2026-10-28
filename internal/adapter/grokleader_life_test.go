package adapter

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/execx"
)

// TestMain doubles as the fake `grok agent leader`: the test binary re-executed
// with GROK_FAKE_LEADER=<socket> listens there like the real leader, and like
// it removes the socket on SIGTERM.
func TestMain(m *testing.M) {
	if sock := os.Getenv("GROK_FAKE_LEADER"); sock != "" {
		ln, err := net.Listen("unix", sock)
		if err != nil {
			os.Exit(2)
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() { <-time.After(time.Minute); c.Close() }() // hold it open
			}
		}()
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		<-ch
		ln.Close() // removes the socket file
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startFakeLeader(t *testing.T, sock string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "GROK_FAKE_LEADER="+sock)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; i < 200; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return cmd
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake leader never listened")
	return nil
}

func TestLeaderStartStop(t *testing.T) {
	t.Run("launch and resume start the leader on a per-session socket", func(t *testing.T) {
		d := testDeps(t)
		a := newGrok(d)
		s := grokSpec(t)
		sock := filepath.Join(d.Home, "run", "grok-leader-"+s.SessionID+".sock")
		l, err := a.Launch(s)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"--leader", "--leader-socket", sock}
		i := slices.Index(l.Argv, "--leader")
		if i < 0 || !slices.Equal(l.Argv[i:i+3], want) || l.Argv[len(l.Argv)-1] != s.Kickoff {
			t.Fatalf("argv = %q, want %q before the kickoff", l.Argv, want)
		}
		if _, err := os.Stat(filepath.Dir(sock)); err != nil {
			t.Errorf("run dir for the socket must exist: %v", err)
		}
		s.ProviderSessionID = "f1b864d7-1f9e-4ca4-9309-d32b9bc755f7"
		r, err := a.Resume(s)
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.Index(r.Argv, "--leader"); i < 0 || !slices.Equal(r.Argv[i:i+3], want) {
			t.Errorf("resume argv = %q, want %q", r.Argv, want)
		}
	})

	t.Run("no leader flags when the socket path would not fit sun_path", func(t *testing.T) {
		d := testDeps(t)
		d.Home = filepath.Join(d.Home, strings.Repeat("x", 90))
		var logged string
		d.Log = func(f string, _ ...any) { logged = f }
		a := newGrok(d)
		l, err := a.Launch(grokSpec(t))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"--leader", "--leader-socket", "--no-leader"} {
			if slices.Contains(l.Argv, f) {
				t.Errorf("argv = %q, must run without a leader and without the unproven %s (wake falls back to paste)", l.Argv, f)
			}
		}
		if !strings.Contains(logged, "leader") {
			t.Errorf("the skipped leader must be logged, got %q", logged)
		}
	})

	t.Run("launch removes a stale socket and keeps a live one", func(t *testing.T) {
		d := testDeps(t)
		a := newGrok(d)
		s := grokSpec(t)
		sock := filepath.Join(d.Home, "run", "grok-leader-"+s.SessionID+".sock")
		os.MkdirAll(filepath.Dir(sock), 0o700)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		ln.(*net.UnixListener).SetUnlinkOnClose(false) // leave the file behind, as a crashed leader does
		ln.Close()
		if _, err := os.Stat(sock); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if _, err := a.Launch(s); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(sock); !os.IsNotExist(err) {
			t.Errorf("stale socket survived Launch: %v", err)
		}

		startFakeLeader(t, sock)
		if _, err := a.Launch(s); err != nil {
			t.Fatal(err)
		}
		if c, err := net.Dial("unix", sock); err != nil {
			t.Errorf("Launch removed a live leader's socket: %v", err)
		} else {
			c.Close()
		}
	})

	t.Run("sweep stops a finished session's leader by pid and spares live ones", func(t *testing.T) {
		d := testDeps(t)
		fake := &execx.Fake{}
		d.Run = fake.Runner()
		d.Start = func(context.Context, string, ...string) (*execx.Proc, error) {
			t.Error("the sweep must not start any process")
			return nil, nil
		}
		d.RunEnv = func(context.Context, map[string]string, string, ...string) ([]byte, error) {
			t.Error("the sweep must never run `grok leader kill` or any other command")
			return nil, nil
		}
		a := newGrok(d)
		run := filepath.Join(d.Home, "run")
		os.MkdirAll(run, 0o700)
		deadSock := filepath.Join(run, "grok-leader-ses_dead.sock")
		liveSock := filepath.Join(run, "grok-leader-ses_live.sock")
		dead := startFakeLeader(t, deadSock)
		live := startFakeLeader(t, liveSock)
		staleSock := filepath.Join(run, "grok-leader-ses_gone.sock")
		ln, _ := net.Listen("unix", staleSock)
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		ln.Close()

		a.SweepLeaders(map[string]bool{"ses_live": true})

		waited := make(chan error, 1)
		go func() { waited <- dead.Wait() }()
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			t.Fatal("the finished session's leader is still running")
		}
		if dead.ProcessState == nil || dead.ProcessState.ExitCode() != 0 {
			t.Errorf("leader did not exit through SIGTERM: %v", dead.ProcessState)
		}
		for _, p := range []string{deadSock, staleSock} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("%s still exists: %v", filepath.Base(p), err)
			}
		}
		if live.Process.Signal(syscall.Signal(0)) != nil {
			t.Error("a live session's leader was stopped")
		}
		if _, err := os.Stat(liveSock); err != nil {
			t.Errorf("a live session's socket was removed: %v", err)
		}
	})
}
