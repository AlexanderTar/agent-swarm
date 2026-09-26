// Package execx is the single seam for running external commands, so tests
// can assert exact argv without touching the machine.
package execx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Runner runs a command to completion and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Run is the real Runner. Stdin is closed; commands get at most 30 s if no deadline is set.
func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// RunnerEnv is Runner with an extra environment map merged over the ambient
// environment (mirrors StarterEnv for Start). Codex.Wake uses this to target
// a session's isolated CODEX_HOME instead of the daemon's ambient $HOME/.codex.
type RunnerEnv func(ctx context.Context, env map[string]string, name string, args ...string) ([]byte, error)

// RunEnv is the real RunnerEnv.
func RunEnv(ctx context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Proc is a running process with an open stdin (codex app-server needs it
// open between writes).
type Proc struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Kill   func()
}

// Starter starts a long-running process.
type Starter func(ctx context.Context, name string, args ...string) (*Proc, error)

// Start is the real Starter. Kill closes stdin, kills the process and reaps it.
func Start(ctx context.Context, name string, args ...string) (*Proc, error) {
	return startCmd(exec.CommandContext(ctx, name, args...))
}

// StarterEnv is Starter with an extra environment map merged over the
// ambient environment. It exists alongside Starter/Start rather than
// changing their signature, since most callers (catalog/usage app-server
// probes) need no override; a caller that must run a command under an
// isolated HOME (e.g. agy.Wake targeting a session's isolated agy-home)
// uses this instead.
type StarterEnv func(ctx context.Context, env map[string]string, name string, args ...string) (*Proc, error)

// StartEnv is the real StarterEnv.
func StartEnv(ctx context.Context, env map[string]string, name string, args ...string) (*Proc, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return startCmd(cmd)
}

func startCmd(cmd *exec.Cmd) (*Proc, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var once sync.Once
	return &Proc{Stdin: in, Stdout: out, Kill: func() {
		once.Do(func() {
			in.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}}, nil
}

// Result is a canned answer for Fake. Block makes the call wait for ctx.
type Result struct {
	Out   string
	Err   error
	Block bool
}

// Fake is a Runner for tests. Keys are the argv joined with single spaces.
type Fake struct {
	Responses map[string]Result
	mu        sync.Mutex
	calls     []string
}

func (f *Fake) Runner() Runner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		f.mu.Lock()
		f.calls = append(f.calls, key)
		r, ok := f.Responses[key]
		f.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("execx: unexpected command: %s", key)
		}
		if r.Block {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte(r.Out), r.Err
	}
}

func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
