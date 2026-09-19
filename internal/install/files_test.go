package install_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/install"
)

func TestWriteIfChangedCreatesParentsThenSkipsIdenticalBytes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "hooks.json")
	wrote, err := install.WriteIfChanged(p, []byte("one\n"), 0o644)
	if err != nil || !wrote {
		t.Fatalf("first write = %v, %v", wrote, err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	before := st.ModTime()

	wrote, err = install.WriteIfChanged(p, []byte("one\n"), 0o644)
	if err != nil || wrote {
		t.Fatalf("second write = %v, %v; identical bytes must not be rewritten", wrote, err)
	}
	after, _ := os.Stat(p)
	if !after.ModTime().Equal(before) {
		t.Error("mtime changed on a no-op write")
	}

	wrote, err = install.WriteIfChanged(p, []byte("two\n"), 0o644)
	if err != nil || !wrote {
		t.Fatalf("changed write = %v, %v", wrote, err)
	}
	body, _ := os.ReadFile(p)
	if string(body) != "two\n" {
		t.Errorf("body = %q", body)
	}
}

func TestWriteIfChangedLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	if _, err := install.WriteIfChanged(filepath.Join(dir, "f.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "f.json" {
		t.Fatalf("dir = %v; the atomic write must rename, not leave a temp file", ents)
	}
}

func TestEditJSONKeepsEveryOtherKeyAndSkipsNoOps(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.json")
	// A fixture shaped like the operator's ~/.cursor/mcp.json: other servers must survive.
	seed := `{"mcpServers":{"vercel":{"command":"npx","args":["-y","mcp-remote"]}}}`
	if err := os.WriteFile(p, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	add := func(m map[string]any) error {
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
			m["mcpServers"] = servers
		}
		servers["swarm"] = map[string]any{"command": "/fake/bin/swarm", "args": []any{"mcp"}}
		return nil
	}
	wrote, err := install.EditJSON(p, true, add)
	if err != nil || !wrote {
		t.Fatalf("EditJSON = %v, %v", wrote, err)
	}
	var got map[string]any
	body, _ := os.ReadFile(p)
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, body)
	}
	servers := got["mcpServers"].(map[string]any)
	if _, ok := servers["vercel"]; !ok {
		t.Error("the user's own vercel server was dropped")
	}
	if _, ok := servers["swarm"]; !ok {
		t.Error("swarm was not added")
	}
	if !strings.HasSuffix(string(body), "\n") {
		t.Error("the file must end in a newline")
	}

	wrote, err = install.EditJSON(p, true, add)
	if err != nil || wrote {
		t.Fatalf("second EditJSON = %v, %v; an unchanged result must not rewrite", wrote, err)
	}
}

func TestEditJSONOnAMissingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.json")

	// create=false: leave it alone, report no change, no error. This is what the
	// removal pass needs — it must not create a config file the user never had.
	wrote, err := install.EditJSON(missing, false, func(m map[string]any) error {
		t.Fatal("edit must not run for a missing file when create is false")
		return nil
	})
	if err != nil || wrote {
		t.Fatalf("create=false on a missing file = %v, %v", wrote, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("the file was created anyway")
	}

	wrote, err = install.EditJSON(missing, true, func(m map[string]any) error {
		m["version"] = float64(1)
		return nil
	})
	if err != nil || !wrote {
		t.Fatalf("create=true = %v, %v", wrote, err)
	}
}

func TestEditJSONRefusesToTouchAFileItCannotParse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := install.EditJSON(p, true, func(map[string]any) error { return nil }); err == nil {
		t.Fatal("want an error; overwriting a config we cannot read would lose the user's data")
	}
	body, _ := os.ReadFile(p)
	if string(body) != "{not json" {
		t.Errorf("the unparseable file was modified: %q", body)
	}
}

func TestRemoveMarkedSpanTakesOnlyTheMarkedLines(t *testing.T) {
	// A fixture shaped like the operator's ~/.gemini/GEMINI.md: the user's own
	// "Agent Swarm Coordination" heading is NOT inside the markers and must survive.
	in := strings.Join([]string{
		"# My instructions",
		"",
		"### Agent Swarm Coordination and Task Hygiene",
		"- check the board first",
		"",
		"<!-- swarm:start -->",
		"# Agent Swarm coordination",
		"installed by v1",
		"<!-- swarm:end -->",
		"",
		"## After",
	}, "\n")
	out, removed := install.RemoveMarkedSpan(in, "<!-- swarm:start -->", "<!-- swarm:end -->")
	if !removed {
		t.Fatal("removed = false")
	}
	for _, keep := range []string{"# My instructions", "### Agent Swarm Coordination and Task Hygiene", "- check the board first", "## After"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	for _, gone := range []string{"swarm:start", "swarm:end", "installed by v1"} {
		if strings.Contains(out, gone) {
			t.Errorf("kept %q:\n%s", gone, out)
		}
	}
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("blank runs not collapsed:\n%q", out)
	}

	if _, removed := install.RemoveMarkedSpan(out, "<!-- swarm:start -->", "<!-- swarm:end -->"); removed {
		t.Error("a second pass must report nothing removed")
	}
}

func TestRemoveMarkedSpanLeavesAnUnterminatedSpanAlone(t *testing.T) {
	in := "a\n<!-- swarm:start -->\nb\nc\n"
	out, removed := install.RemoveMarkedSpan(in, "<!-- swarm:start -->", "<!-- swarm:end -->")
	if removed || out != in {
		t.Fatalf("removed = %v, out = %q; without an end marker we must not guess where the block ends", removed, out)
	}
}

func TestRemoveLinkRemovesASymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "swarm")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	removed, realDir, err := install.RemoveLink(link)
	if err != nil || !removed || realDir {
		t.Fatalf("RemoveLink = %v, %v, %v", removed, realDir, err)
	}
	// §20: "removed as links, never followed".
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("the symlink target was deleted: %v", err)
	}
}

func TestRemoveLinkReportsARealDirectoryInsteadOfDeletingIt(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "swarm")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	removed, realDir, err := install.RemoveLink(real)
	if err != nil || removed || !realDir {
		t.Fatalf("RemoveLink = %v, %v, %v; a real folder needs the agent's own uninstall path", removed, realDir, err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatal("the real folder was deleted")
	}
}

func TestRemoveLinkOnAMissingPathIsANoOp(t *testing.T) {
	removed, realDir, err := install.RemoveLink(filepath.Join(t.TempDir(), "nope"))
	if err != nil || removed || realDir {
		t.Fatalf("= %v, %v, %v", removed, realDir, err)
	}
}

func TestCopyFilePreservesBytesAndCreatesParents(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.toml")
	if err := os.WriteFile(src, []byte("# swarm:start\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "backups", "config-1", "src.toml")
	if err := install.CopyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dst)
	if err != nil || string(body) != "# swarm:start\n" {
		t.Fatalf("copy = %q, %v", body, err)
	}
}
