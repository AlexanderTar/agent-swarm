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
// help. systemctl and loginctl are the same on Linux. Every real runner and
// starter must refuse them under `go test`. PATH holds stubs, so even a
// failing guard never reaches the real ones.
func TestRealRunnersRefuseLaunchctlUnderGoTest(t *testing.T) {
	dir := t.TempDir()
	managers := []string{"launchctl", "systemctl", "loginctl"}
	for _, name := range managers {
		stub := "#!/bin/sh\ntouch " + filepath.Join(dir, name+".ran") + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	ctx := context.Background()
	start := func(p *Proc, err error) ([]byte, error) {
		if p != nil {
			p.Kill()
		}
		return nil, err
	}
	for entry, run := range map[string]Runner{
		"Run":    Run,
		"RunFor": RunFor(time.Second),
		"RunEnv": func(ctx context.Context, n string, a ...string) ([]byte, error) {
			return RunEnv(ctx, nil, n, a...)
		},
		"Start": func(ctx context.Context, n string, a ...string) ([]byte, error) {
			return start(Start(ctx, n, a...))
		},
		"StartEnv": func(ctx context.Context, n string, a ...string) ([]byte, error) {
			return start(StartEnv(ctx, nil, n, a...))
		},
	} {
		for _, name := range managers {
			for _, argv0 := range []string{name, filepath.Join(dir, name)} {
				_, err := run(ctx, argv0, "--user", "stop", "dev.swarm.daemon.service")
				if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "test") {
					t.Errorf("%s(%s): err = %v, want a refusal", entry, argv0, err)
				}
			}
			if _, statErr := os.Stat(filepath.Join(dir, name+".ran")); statErr == nil {
				t.Errorf("%s: %s was executed", entry, name)
			}
		}
	}
}

// The guard names only the service managers: other commands still run.
func TestRealRunnersStillRunOtherCommandsUnderGoTest(t *testing.T) {
	out, err := Run(context.Background(), "sh", "-c", "echo systemctl")
	if err != nil || strings.TrimSpace(string(out)) != "systemctl" {
		t.Fatalf("Run(sh) = %q, %v", out, err)
	}
}
