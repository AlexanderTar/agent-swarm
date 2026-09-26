package repos

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

var bg = context.Background()

func TestParseRemoteOwner(t *testing.T) {
	cases := map[string]string{
		"https://github.com/EndurioApp/endurio-chat.git": "EndurioApp",
		"git@github.com:AlexanderTar/agent-swarm.git":    "AlexanderTar",
		"ssh://git@github.com/Owner/repo.git":            "Owner",
		"https://gitlab.example.com/group/sub/repo":      "group",
		"https://user:pw@github.com/Acme/tool":           "Acme",
		"":                                               "",
		"/local/path/repo.git":                           "",
		"https://github.com/onlyowner":                   "",
		"git@github.com:solo":                            "",
	}
	for in, want := range cases {
		if got := ParseRemoteOwner(in); got != want {
			t.Errorf("ParseRemoteOwner(%q) = %q, want %q", in, got, want)
		}
	}
}

func gitFake(extra map[string]execx.Result) *execx.Fake {
	r := map[string]execx.Result{
		"git -C /r config --get remote.origin.url": {Out: "git@github.com:EndurioApp/endurio-chat.git\n"},
	}
	for k, v := range extra {
		r[k] = v
	}
	return &execx.Fake{Responses: r}
}

var fail = execx.Result{Err: errors.New("exit status 1")}

func TestDefaultBranchOrder(t *testing.T) {
	cases := []struct {
		name string
		resp map[string]execx.Result
		want string
	}{
		{"origin HEAD", map[string]execx.Result{
			"git -C /r symbolic-ref --quiet refs/remotes/origin/HEAD": {Out: "refs/remotes/origin/develop\n"},
		}, "develop"},
		{"main", map[string]execx.Result{
			"git -C /r symbolic-ref --quiet refs/remotes/origin/HEAD": fail,
			"git -C /r show-ref --verify --quiet refs/heads/main":     {},
		}, "main"},
		{"master", map[string]execx.Result{
			"git -C /r symbolic-ref --quiet refs/remotes/origin/HEAD": fail,
			"git -C /r show-ref --verify --quiet refs/heads/main":     fail,
			"git -C /r show-ref --verify --quiet refs/heads/master":   {},
		}, "master"},
		{"current", map[string]execx.Result{
			"git -C /r symbolic-ref --quiet refs/remotes/origin/HEAD": fail,
			"git -C /r show-ref --verify --quiet refs/heads/main":     fail,
			"git -C /r show-ref --verify --quiet refs/heads/master":   fail,
			"git -C /r branch --show-current":                         {Out: "feature/x\n"},
		}, "feature/x"},
	}
	for _, c := range cases {
		f := gitFake(c.resp)
		info := ReadGitInfo(bg, f.Runner(), "/r")
		if info.DefaultBranch != c.want {
			t.Errorf("%s: branch = %q", c.name, info.DefaultBranch)
		}
		if info.RemoteOwner != "EndurioApp" || info.RemoteURL != "git@github.com:EndurioApp/endurio-chat.git" {
			t.Errorf("%s: remote = %+v", c.name, info)
		}
		for _, call := range f.Calls() {
			for _, net := range []string{"fetch", "pull", "ls-remote", "remote update"} {
				if strings.Contains(call, net) {
					t.Errorf("%s: network command %q", c.name, call)
				}
			}
		}
	}
}

func TestNoRemoteMeansNoOwner(t *testing.T) {
	f := &execx.Fake{Responses: map[string]execx.Result{
		"git -C /r config --get remote.origin.url":                fail,
		"git -C /r symbolic-ref --quiet refs/remotes/origin/HEAD": fail,
		"git -C /r show-ref --verify --quiet refs/heads/main":     {},
	}}
	if info := ReadGitInfo(bg, f.Runner(), "/r"); info.RemoteURL != "" || info.RemoteOwner != "" || info.DefaultBranch != "main" {
		t.Fatalf("info = %+v", info)
	}
}

func TestReadGitInfoOnRealRepo(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main", dir}, {"-C", dir, "remote", "add", "origin", "https://github.com/AlexanderTar/x.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	info := ReadGitInfo(bg, execx.Run, dir)
	if info.RemoteOwner != "AlexanderTar" || info.DefaultBranch != "main" {
		t.Fatalf("info = %+v", info)
	}
	if !IsRepo(dir) || Dirty(bg, execx.Run, dir) {
		t.Fatal("fresh repo should be a repo and clean")
	}
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644)
	if !Dirty(bg, execx.Run, dir) {
		t.Fatal("untracked file should make it dirty")
	}
}

func TestDirtyTreatsErrorsAsClean(t *testing.T) {
	f := &execx.Fake{Responses: map[string]execx.Result{"git -C /r status --porcelain": fail}}
	if Dirty(bg, f.Runner(), "/r") {
		t.Fatal("error must not report dirty")
	}
}

func TestMainRepoOf(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "app")
	os.MkdirAll(filepath.Join(main, ".git", "worktrees", "app-feature"), 0o755)
	wt := filepath.Join(root, "app-feature")
	os.MkdirAll(wt, 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(main, ".git", "worktrees", "app-feature")+"\n"), 0o644)
	rel := filepath.Join(root, "app-rel")
	os.MkdirAll(rel, 0o755)
	os.WriteFile(filepath.Join(rel, ".git"), []byte("gitdir: ../app/.git/worktrees/app-rel\n"), 0o644)
	sub := filepath.Join(main, "vendor-sub")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../.git/modules/vendor-sub\n"), 0o644)

	for path, want := range map[string]string{main: main, wt: main, rel: main} {
		if got, ok := MainRepoOf(path); !ok || got != want {
			t.Errorf("MainRepoOf(%s) = %q, %v", path, got, ok)
		}
	}
	for _, path := range []string{sub, root, filepath.Join(root, "missing")} {
		if _, ok := MainRepoOf(path); ok {
			t.Errorf("MainRepoOf(%s) should be false", path)
		}
	}
	if IsRepo(wt) || IsRepo(sub) || !IsRepo(main) {
		t.Error("IsRepo must require a .git directory")
	}
}

// gitCommand runs Git against a local temporary repository; no network is involved.
func gitCommand(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func primaryFixture(t *testing.T) (main, linked, directoryLinked, clone string) {
	t.Helper()
	root := t.TempDir()
	main = filepath.Join(root, "worktree-project")
	linked = filepath.Join(root, "linked")
	directoryLinked = filepath.Join(root, "directory-linked")
	clone = filepath.Join(root, "clone")
	gitCommand(t, "init", "-q", "-b", "main", main)
	gitCommand(t, "-C", main, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-q", "--allow-empty", "-m", "initial")
	gitCommand(t, "-C", main, "remote", "add", "origin", "https://example.invalid/owner/repo.git")
	gitCommand(t, "-C", main, "worktree", "add", "-q", "-b", "linked", linked)
	gitCommand(t, "clone", "-q", "--no-hardlinks", main, clone)
	gitCommand(t, "-C", clone, "remote", "set-url", "origin", "https://example.invalid/owner/repo.git")
	gitCommand(t, "-C", main, "worktree", "add", "-q", "-b", "directory-linked", directoryLinked)
	linkedGitDir := strings.TrimSpace(strings.TrimPrefix(string(mustReadFile(t, filepath.Join(directoryLinked, ".git"))), "gitdir: "))
	if err := os.Remove(filepath.Join(directoryLinked, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directoryLinked, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HEAD", "commondir", "gitdir", "index"} {
		body := mustReadFile(t, filepath.Join(linkedGitDir, name))
		if name == "commondir" {
			body = []byte(filepath.Join(main, ".git") + "\n")
		}
		if name == "gitdir" {
			body = []byte(filepath.Join(directoryLinked, ".git") + "\n")
		}
		if err := os.WriteFile(filepath.Join(directoryLinked, ".git", name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPrimaryRepoIdentity(t *testing.T) {
	main, linked, directoryLinked, clone := primaryFixture(t)
	invalid := filepath.Join(t.TempDir(), "invalid")
	if err := os.MkdirAll(filepath.Join(invalid, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{main, true}, {linked, false}, {directoryLinked, false}, {clone, true}, {invalid, false},
	} {
		if got := PrimaryRepo(bg, execx.Run, tc.path); got != tc.want {
			t.Errorf("PrimaryRepo(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
