package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveScenarioPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "happy-feature-spike.json"), `{}`)
	writeFile(t, filepath.Join(dir, "_default.json"), `{}`)
	t.Setenv("SWARM_E2E_SCENARIOS_DIR", dir)

	if p, err := resolveScenarioPath("/abs/whatever.json"); err != nil || p != "/abs/whatever.json" {
		t.Fatalf("explicit --scenario must never be substituted: %q %v", p, err)
	}

	t.Setenv("SWARM_FAKE_SCENARIO", "happy-feature-spike")
	if p, err := resolveScenarioPath(""); err != nil || p != filepath.Join(dir, "happy-feature-spike.json") {
		t.Fatalf("bare name = %q %v", p, err)
	}

	// A daemon-generated collision suffix (or any name with no matching file)
	// falls back to the shared default rather than failing the spawn.
	t.Setenv("SWARM_FAKE_SCENARIO", "happy-feature-spike-2")
	if p, err := resolveScenarioPath(""); err != nil || p != filepath.Join(dir, "_default.json") {
		t.Fatalf("unmatched name = %q %v, want the default", p, err)
	}

	t.Setenv("SWARM_FAKE_SCENARIO", "")
	if _, err := resolveScenarioPath(""); err == nil {
		t.Fatal("no scenario at all must be an error")
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
