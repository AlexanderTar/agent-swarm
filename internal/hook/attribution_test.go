package hook

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAttributionCheckBlocksTrailersInEveryForm(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "msg.txt"),
		[]byte("subject\n\nCo-authored-by: Cursor <cursoragent@example.com>\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "body.md"),
		[]byte("Fixes the thing.\n\n🤖 Generated with Claude Code\n"), 0o644)
	a := AttrCheck{ReadFile: os.ReadFile}
	blocked := []struct{ name, cmd string }{
		{"dash m upper", `git commit -m "one

Co-Authored-By: Probe <p@example.com>"`},
		{"dash m lower", `git commit -m "one

Co-authored-by: Probe <p@example.com>"`},
		{"heredoc", "git commit -F - <<'EOF'\nsubject\n\nCo-Authored-By: X <x@y>\nEOF"},
		{"printf substitution", `git commit -m "$(printf 'one\n\nCo-Authored-By: X <x@y>')"`},
		{"dash F file", "git commit -F msg.txt"},
		{"long file flag", "git commit --file=msg.txt"},
		{"pr body", `gh pr create --title t --body "Generated with Claude Code"`},
		{"pr body file", "gh pr create --title t --body-file body.md"},
		{"pr edit", `gh pr edit 3 --body "🤖 Generated with Claude Code"`},
	}
	for _, c := range blocked {
		got, reason := a.Block(c.cmd, dir)
		if !got {
			t.Errorf("%s: should be blocked", c.name)
			continue
		}
		if reason != AttrReason {
			t.Errorf("%s: reason = %q, want %q", c.name, reason, AttrReason)
		}
	}
}

func TestAttributionCheckBlocksSigningOptOut(t *testing.T) {
	a := AttrCheck{ReadFile: os.ReadFile}
	for _, cmd := range []string{
		"git commit --no-gpg-sign -m x",
		"git -c commit.gpgsign=false commit -m x",
		"git merge --no-gpg-sign feature",
	} {
		got, reason := a.Block(cmd, t.TempDir())
		if !got || reason != SignReason {
			t.Errorf("%q: blocked = %v, reason = %q", cmd, got, reason)
		}
	}
}

func TestAttributionCheckAllowsNormalCommands(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "clean.txt"), []byte("subject\n\nbody\n"), 0o644)
	a := AttrCheck{ReadFile: os.ReadFile}
	for _, cmd := range []string{
		`git commit -m "feat: add the login form"`,
		"git commit -F clean.txt",
		`gh pr create --title t --body "This work was co-authored in a pairing session."`,
		"git status",
		"pnpm test",
		"git log --format='%an'",
		`git commit -m "docs: mention Generated with care"`,
	} {
		if got, reason := a.Block(cmd, dir); got {
			t.Errorf("%q must be allowed, got %q", cmd, reason)
		}
	}
}

// Only git/gh write commands are inspected; nothing else is read from disk.
func TestAttributionCheckIgnoresUnrelatedCommands(t *testing.T) {
	var reads []string
	a := AttrCheck{ReadFile: func(p string) ([]byte, error) { reads = append(reads, p); return nil, nil }}
	if got, _ := a.Block("cat msg.txt", t.TempDir()); got {
		t.Error("cat must not be blocked")
	}
	if len(reads) != 0 {
		t.Fatalf("read %v; only git/gh write commands are inspected", reads)
	}
}

// A path that does not resolve is not a reason to allow or to error.
func TestAttributionCheckToleratesAMissingFile(t *testing.T) {
	a := AttrCheck{ReadFile: os.ReadFile}
	if got, _ := a.Block("git commit -F nope.txt", t.TempDir()); got {
		t.Error("a missing file cannot contain a trailer, so allow")
	}
}
