package e2e

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/e2e.sh must refuse to start when a real agent CLI resolves on its
// pinned PATH: the daemon it starts would find and run it, and no real agent
// may run in e2e. Untagged (unlike the scenarios), so `go test ./...` runs it.
func TestE2EScriptRefusesARealAgentCLIOnItsPath(t *testing.T) {
	script, err := filepath.Abs("../e2e.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex", "agy", "cursor-agent", "muse", "grok"} {
		t.Run(agent, func(t *testing.T) {
			stubs, tmp := t.TempDir(), t.TempDir()
			marker := filepath.Join(tmp, "stub-ran")
			if err := os.WriteFile(filepath.Join(stubs, agent), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/bash", script)
			// Only the stub folder: the guard must not depend on what else is installed.
			cmd.Env = []string{"SWARM_E2E_PATH=" + stubs, "TMPDIR=" + tmp, "HOME=" + t.TempDir()}
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("e2e.sh = %v, want exit 1\n%s", err, out)
			}
			if want := filepath.Join(stubs, agent); !strings.Contains(string(out), want) {
				t.Errorf("output does not name %s:\n%s", want, out)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("e2e.sh ran the agent stub")
			}
			// It stops before any setup: its first mktemp would land in TMPDIR.
			if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
				t.Errorf("e2e.sh created %v before refusing", entries[0].Name())
			}
		})
	}
}
