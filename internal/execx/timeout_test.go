package execx

import (
	"context"
	"strings"
	"testing"
	"time"
)

// §12.4: plugin commands get 120 s. Run caps everything at 30 s, so RunFor exists.
func TestRunForUsesTheGivenTimeoutInsteadOfRunsThirtySeconds(t *testing.T) {
	run := RunFor(50 * time.Millisecond)
	start := time.Now()
	_, err := run(context.Background(), "sleep", "5")
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v; the 50 ms timeout was not applied", d)
	}
}

func TestRunForStillCapturesStdoutAndStderr(t *testing.T) {
	run := RunFor(5 * time.Second)
	out, err := run(context.Background(), "printf", "hello")
	if err != nil || string(out) != "hello" {
		t.Fatalf("= %q, %v", out, err)
	}
	if _, err := run(context.Background(), "sh", "-c", "echo boom >&2; exit 3"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want stderr in the message", err)
	}
}
