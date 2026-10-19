package execx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A test that reaches the real runners with launchctl would boot the live
// daemon out (BUG-100): launchd labels are machine-wide, a fake HOME doesn't
// help. The runners must refuse under `go test`. PATH holds a launchctl stub, so
// even a failing run never reaches the real one.
func TestRealRunnersRefuseLaunchctlUnderGoTest(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	stub := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(dir, "launchctl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	ctx := context.Background()
	for name, run := range map[string]Runner{
		"Run":    Run,
		"RunFor": RunFor(time.Second),
		"RunEnv": func(ctx context.Context, n string, a ...string) ([]byte, error) {
			return RunEnv(ctx, nil, n, a...)
		},
	} {
		_, err := run(ctx, "launchctl", "bootout", "gui/501/dev.swarm.daemon")
		if err == nil || !strings.Contains(err.Error(), "launchctl") || !strings.Contains(err.Error(), "test") {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Errorf("%s: launchctl was executed", name)
		}
	}
}
