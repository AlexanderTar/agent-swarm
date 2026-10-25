package execx

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// Detached is a process started apart from the daemon by StartDetached.
type Detached struct {
	Done <-chan struct{} // closed when the process exits
	Err  func() error    // after Done: its exit error with the start of its stderr, or nil
}

// Detacher starts a process detached from the daemon. env is the child's
// whole environment (os.Environ's format): nothing is inherited, so a caller
// can leave a stale variable out instead of only overriding it.
type Detacher func(env []string, name string, args ...string) (*Detached, error)

// detachedStderr is how much of a detached process's stderr Err reports.
const detachedStderr = 4096

// StartDetached is the real Detacher, for windows the daemon opens (a
// terminal): the process gets its own session, no stdin or stdout, and no
// deadline, unlike Run's 30 s, which would kill a terminal that stays in the
// foreground. It is reaped when it exits. Refused for the service managers
// under `go test`, like every other entry point.
func StartDetached(env []string, name string, args ...string) (*Detached, error) {
	if err := refuseUnderTest(name); err != nil {
		return nil, err
	}
	cmd := exec.Command(name, args...)
	cmd.Env = append([]string{}, env...) // non-nil: a nil Env would inherit the daemon's
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stderr := &capped{max: detachedStderr}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	return &Detached{Done: done, Err: func() error {
		<-done
		if waitErr == nil {
			return nil
		}
		return fmt.Errorf("%w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}}, nil
}

// capped keeps the first max bytes written to it.
type capped struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (c *capped) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
