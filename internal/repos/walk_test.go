package repos

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixtureHome builds the §23.3 scan fixture and returns its real path.
func fixtureHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := func(p string) string {
		full := filepath.Join(home, p)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		return full
	}
	repo := func(p string) string { dir(filepath.Join(p, ".git")); return filepath.Join(home, p) }
	file := func(p, body string) {
		if err := os.WriteFile(filepath.Join(home, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, p string) {
		if err := os.Symlink(target, filepath.Join(home, p)); err != nil {
			t.Fatal(err)
		}
	}

	repo("GitHub/normal")
	repo("GitHub/app")
	repo("a/b/c/d/e/f/g/h/deep")       // 9 levels
	repo("GitHub/normal/nested/inner") // nested inside a repo
	dir("GitHub/wt")                   // worktree of app
	file("GitHub/wt/.git", "gitdir: "+filepath.Join(home, "GitHub/app/.git/worktrees/wt")+"\n")
	dir("GitHub/normal/sub") // submodule
	file("GitHub/normal/sub/.git", "gitdir: ../.git/modules/sub\n")
	for _, skip := range []string{"node_modules", "vendor", "Pods", "DerivedData", "target", ".venv"} {
		repo(filepath.Join("proj", skip, "pkg"))
	}
	repo(".tool/plugin")
	repo("Library/Mobile/repo")
	repo("Downloads/repo")
	repo("Music/album/repo")
	repo("Pictures/photo/repo")
	repo("Movies/film/repo")
	repo("locked/repo")
	dir("loop")
	link(filepath.Join(home, "loop"), "loop/self") // symlink loop
	link(filepath.Join(home, "loop/b"), "loop/a")
	link(filepath.Join(home, "loop/a"), "loop/b")
	link(filepath.Join(home, "GitHub"), "alias") // a second route to the same repos
	link(filepath.Join(home, "Music"), "MusicLink")

	dir("Workspaces/endurio")
	link(filepath.Join(home, "GitHub/normal"), "Workspaces/endurio/chat")
	link(filepath.Join(home, "GitHub/app"), "Workspaces/endurio/app")
	dir("notes")
	link(filepath.Join(home, "notes"), "Workspaces/endurio/notes") // not a repo
	file("Workspaces/endurio/readme.md", "x")
	link(filepath.Join(home, "Workspaces/endurio/readme.md"), "Workspaces/endurio/readme-link")
	file("Workspaces/endurio.code-workspace", `{"folders":[{"path":"../GitHub/normal"},{"path":"`+
		filepath.Join(home, "a/b/c/d/e/f/g/h/deep")+`"},{"path":"../GitHub/wt"},{"path":"../missing"}]}`)
	file("bad.code-workspace", `{"folders": [`)

	if err := os.Chmod(filepath.Join(home, "locked"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(home, "locked"), 0o755) })
	return home
}

func TestWalkFindsTheRightRepos(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read the locked folder")
	}
	home := fixtureHome(t)
	w := Walk(home, []string{"~/Downloads"})
	var rel []string
	for _, r := range w.Repos {
		p, _ := filepath.Rel(home, r)
		rel = append(rel, p)
	}
	want := []string{"GitHub/app", "GitHub/normal", "GitHub/normal/nested/inner", "a/b/c/d/e/f/g/h/deep"}
	if !slices.Equal(rel, want) {
		t.Fatalf("repos = %v, want %v", rel, want)
	}
	if w.Errors < 1 {
		t.Fatalf("unreadable folder not counted: %d", w.Errors)
	}
	ws := slices.Clone(w.Workspaces)
	slices.Sort(ws)
	if !slices.Equal(ws, []string{filepath.Join(home, "Workspaces/endurio.code-workspace"), filepath.Join(home, "bad.code-workspace")}) {
		t.Fatalf("workspaces = %v", ws)
	}
	for _, targets := range w.LinkDirs {
		for _, target := range targets {
			if strings.Contains(target, "Music") {
				t.Errorf("symlink target in skipped dir was retained: %s", target)
			}
		}
	}
}

func TestGroups(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read the locked folder")
	}
	home := fixtureHome(t)
	w := Walk(home, nil)
	p := func(rel string) string { return filepath.Join(home, rel) }
	owners := map[string]string{
		p("GitHub/normal"):              "EndurioApp",
		p("GitHub/app"):                 "EndurioApp",
		p("a/b/c/d/e/f/g/h/deep"):       "Solo",
		p("GitHub/normal/nested/inner"): "",
		p("not/scanned"):                "EndurioApp",
	}
	got := Groups(w, owners)
	want := []Group{
		{Name: "endurio", Source: "workspace_dir", Origin: p("Workspaces/endurio"), Repos: []string{p("GitHub/app"), p("GitHub/normal")}},
		{Name: "endurio", Source: "code_workspace", Origin: p("Workspaces/endurio.code-workspace"),
			Repos: []string{p("GitHub/app"), p("GitHub/normal"), p("a/b/c/d/e/f/g/h/deep")}},
		{Name: "EndurioApp", Source: "remote_owner", Repos: []string{p("GitHub/app"), p("GitHub/normal")}},
	}
	if len(got) != len(want) {
		t.Fatalf("groups = %+v", got)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Source != want[i].Source || got[i].Origin != want[i].Origin ||
			!slices.Equal(got[i].Repos, want[i].Repos) {
			t.Errorf("group %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestExpandHome(t *testing.T) {
	cases := map[string]string{"~/Downloads": "/h/Downloads", "~": "/h", "/abs/x/": "/abs/x", "rel": "/h/rel"}
	for in, want := range cases {
		if got := ExpandHome(in, "/h"); got != want {
			t.Errorf("ExpandHome(%q) = %q", in, got)
		}
	}
}
