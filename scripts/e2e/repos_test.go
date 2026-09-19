//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Scenario 22: repo discovery. The fixture HOME (scripts/e2e.sh sets the
// daemon's HOME to SWARM_E2E_FIXTURE_HOME, exactly so this test can build a
// controlled tree there instead of the real one) covers a workspace folder
// of symlinks, a .code-workspace file, remote owners and a hidden folder
// that must be skipped.
func TestScenario22RepoDiscovery(t *testing.T) {
	h := newHarness(t)
	home := os.Getenv("SWARM_E2E_FIXTURE_HOME")
	if home == "" {
		t.Skip("SWARM_E2E_FIXTURE_HOME not set — run through `make e2e`, not `go test` directly")
	}

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// scripts/e2e.sh gives every real `make e2e` run a fresh fixture home, but
	// a repeated `go test -run TestScenario22` against one already-running
	// dev daemon reuses the same directory — unique() keeps a rerun from
	// colliding with the last run's own fixture repos.
	suffix := unique()
	initRepo := func(name, remote string) string {
		dir := filepath.Join(home, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		git(dir, "init", "-q", dir)
		git(dir, "remote", "add", "origin", remote)
		return dir
	}

	// two repos under the same remote owner, for the remote_owner group
	projA := initRepo("proj-a-"+suffix, "git@github.com:acct-"+suffix+"/proj-a.git")
	projB := initRepo("proj-b-"+suffix, "https://github.com/acct-"+suffix+"/proj-b.git")

	// a hidden top-level folder is never walked at all
	initRepo(".hidden-repo-"+suffix, "git@github.com:acct-"+suffix+"/hidden.git")

	// a workspace folder of symlinks, for the workspace_dir group
	wsDir := filepath.Join(home, "workspace-"+suffix)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projA, filepath.Join(wsDir, "link-a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projB, filepath.Join(wsDir, "link-b")); err != nil {
		t.Fatal(err)
	}

	// a .code-workspace file, for the code_workspace group
	cw := `{"folders":[{"path":"proj-a-` + suffix + `"},{"path":"proj-b-` + suffix + `"}]}`
	if err := os.WriteFile(filepath.Join(home, "team-"+suffix+".code-workspace"), []byte(cw), 0o644); err != nil {
		t.Fatal(err)
	}

	var stats map[string]any
	h.doT(t, http.MethodPost, "/api/repos/rescan", nil, &stats)

	var body struct {
		All    []map[string]any `json:"all"`
		Groups []map[string]any `json:"groups"`
	}
	h.doT(t, http.MethodGet, "/api/repos", nil, &body)

	names := map[string]bool{}
	for _, r := range body.All {
		names[r["name"].(string)] = true
	}
	if !names["proj-a-"+suffix] || !names["proj-b-"+suffix] {
		t.Fatalf("all = %+v, want proj-a-%s and proj-b-%s", body.All, suffix, suffix)
	}
	if names[".hidden-repo-"+suffix] || names["hidden-repo-"+suffix] {
		t.Fatalf("all = %+v, a hidden top-level folder must never be scanned", body.All)
	}

	groupsBySource := map[string]map[string]bool{}
	for _, g := range body.Groups {
		src, _ := g["source"].(string)
		name, _ := g["name"].(string)
		if groupsBySource[src] == nil {
			groupsBySource[src] = map[string]bool{}
		}
		groupsBySource[src][name] = true
	}
	if !groupsBySource["workspace_dir"]["workspace-"+suffix] {
		t.Errorf("groups = %+v, want a workspace_dir group named workspace-%s", body.Groups, suffix)
	}
	if !groupsBySource["code_workspace"]["team-"+suffix] {
		t.Errorf("groups = %+v, want a code_workspace group named team-%s", body.Groups, suffix)
	}
	if !groupsBySource["remote_owner"]["acct-"+suffix] {
		t.Errorf("groups = %+v, want a remote_owner group named acct-%s", body.Groups, suffix)
	}
}
