package execx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitDone(t *testing.T, d *Detached) {
	t.Helper()
	select {
	case <-d.Done:
	case <-time.After(10 * time.Second):
		t.Fatal("the detached process never exited")
	}
}

func TestStartDetachedReportsAFailureWithItsStderrAndMergedEnv(t *testing.T) {
	d, err := StartDetached(map[string]string{"SWARM_DETACH_PROBE": "from-env"}, "sh", "-c", `echo "no display: $SWARM_DETACH_PROBE" >&2; exit 3`)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, d)
	err = d.Err()
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "no display: from-env") {
		t.Fatalf("Err = %v", err)
	}
}

func TestStartDetachedRunsInItsOwnSessionWithoutADeadline(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ids")
	// A session leader's session id is its own pid.
	d, err := StartDetached(nil, "sh", "-c", `sleep 0.2; ps -o sid= -p $$ > "$0"; echo $$ >> "$0"`, out)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.Done:
		t.Fatal("exited before its sleep")
	case <-time.After(50 * time.Millisecond):
	}
	waitDone(t, d)
	if err := d.Err(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	f := strings.Fields(string(b))
	if len(f) != 2 || f[0] != f[1] {
		t.Fatalf("sid/pid = %q, want the process to lead its own session", b)
	}
}

func TestStartDetachedKeepsOnlyTheStartOfStderr(t *testing.T) {
	d, err := StartDetached(nil, "sh", "-c", `i=0; while [ $i -lt 2000 ]; do printf 'xxxxxxxxxx' >&2; i=$((i+1)); done; exit 1`)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, d)
	if err := d.Err(); err == nil || len(err.Error()) > detachedStderr+100 {
		t.Fatalf("Err length = %d", len(err.Error()))
	}
}

func TestStartDetachedReportsAMissingProgram(t *testing.T) {
	if _, err := StartDetached(nil, filepath.Join(t.TempDir(), "no-such-terminal")); err == nil {
		t.Fatal("started a program that does not exist")
	}
}
