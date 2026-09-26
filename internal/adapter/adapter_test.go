package adapter

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

func testDeps(t *testing.T) Deps {
	t.Helper()
	return Deps{Home: shortTempDir(t), UserHome: t.TempDir(), Bin: "/usr/local/bin/swarm",
		Run: (&execx.Fake{}).Runner(), Now: nowStub, Log: func(string, ...any) {}}
}

// shortTempDir is t.TempDir(), but without the full (often 60-100+ char)
// test name t.TempDir() nests under. Codex's setupEnv (codex.go) now refuses
// to build a CODEX_HOME whose app-server-control socket would exceed
// macOS's SUN_LEN, and Deps.Home feeds directly into that path -- a long
// test name is exactly the kind of "unrealistically long path" the guard is
// meant to catch in production, but it isn't a realistic swarm home (a real
// one looks like /Users/alex/.swarm), so it must not make every unrelated
// test fail this guard too.
func shortTempDir(t *testing.T) string {
	t.Helper()
	// "/tmp", not "" (os.TempDir()): on macOS the per-process $TMPDIR is
	// itself a long, randomized path (/var/folders/<hash>/T/), which alone
	// can burn most of the SUN_LEN budget before CODEX_HOME's own "/cx/<hash>"
	// suffix is even added.
	dir, err := os.MkdirTemp("/tmp", "sw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func nowStub() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }

func TestNewRejectsUnknownKinds(t *testing.T) {
	if _, err := New("opencode", testDeps(t)); err == nil {
		t.Fatal("opencode must not be a supported kind (L2)")
	}
}

// Every adapter registers itself from its own file; with all four landed the set
// is complete. This replaces the partial check Task 3 shipped.
func TestAllReturnsEveryRealKindPlusFake(t *testing.T) {
	got := All(testDeps(t))
	for _, k := range []kinds.AgentKind{kinds.Claude, kinds.Codex, kinds.Agy, kinds.Cursor, kinds.Muse, kinds.Fake} {
		a, ok := got[k]
		if !ok {
			t.Fatalf("All() is missing %s", k)
		}
		if a.Kind() != k {
			t.Errorf("%s adapter reports Kind() = %s", k, a.Kind())
		}
	}
	if len(got) != 6 {
		t.Fatalf("All() returned %d adapters", len(got))
	}
}

// Idle is the shared §11.3 rule: IdlePrompt matches and Busy matches nothing.
func TestIdleCombinesPromptAndBusy(t *testing.T) {
	a := NewFake(testDeps(t))
	idle := "─────\n❯ \n─────\n"
	busy := "✽ Beboppin'… (48s · ↓ 114 tokens)\n─────\n❯ \n─────\n"
	if !a.Idle(idle) {
		t.Error("idle capture should be idle")
	}
	if a.Idle(busy) {
		t.Error("a capture with a spinner is not idle even with the prompt drawn")
	}
	if a.Idle("$ \n") {
		t.Error("a shell prompt is not idle")
	}
}

func TestTrustAndForgetAreNoOpsByDefault(t *testing.T) {
	a := NewFake(testDeps(t))
	ctx := context.Background()
	if err := a.TrustFolder(ctx, "/tmp/x"); err != nil {
		t.Fatal(err)
	}
	if err := a.ForgetFolder(ctx, "/tmp/x"); err != nil {
		t.Fatal(err)
	}
}

// R1: the two shared helpers live here, so their tests do too.
func TestNewUUIDv4ShapeAndVersion(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 100 {
		id := newUUIDv4()
		if !re.MatchString(id) {
			t.Fatalf("newUUIDv4() = %q, not a v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("newUUIDv4() repeated %q", id)
		}
		seen[id] = true
	}
}

func TestWriteFileAtomicKeepsTheExistingModeAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want \"new\"", got)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 preserved", fi.Mode().Perm())
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Fatalf("%d entries left in the folder, want only config.toml: %v", len(ents), ents)
	}
}

// A new file takes the mode it is asked for, and a missing parent is created.
func TestWriteFileAtomicCreatesTheParent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "settings.json")
	if err := writeFileAtomic(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

// A parent path that is actually a file (not a directory) can't be mkdir'd
// into, and writeFileAtomic must surface that error rather than panic or
// silently drop the write.
func TestWriteFileAtomicPropagatesAMkdirFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(blocker, "sub", "settings.json")
	if err := writeFileAtomic(p, []byte("{}"), 0o644); err == nil {
		t.Fatal("expected an error when the parent path is a file")
	}
}

// launchDir and writeLaunchFile are the shared per-launch config helpers the
// real adapters (Tasks 7-9) use; nothing in this task calls them yet, so they
// need their own direct coverage.
func TestDepsLaunchDirAndWriteLaunchFile(t *testing.T) {
	d := testDeps(t)
	want := filepath.Join(d.Home, "run", "launch", "ses_1")
	if got := d.launchDir("ses_1"); got != want {
		t.Fatalf("launchDir() = %q, want %q", got, want)
	}
	p, err := d.writeLaunchFile("ses_1", "settings.json", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(want, "settings.json") {
		t.Fatalf("writeLaunchFile() path = %q", p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	dfi, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if dfi.Mode().Perm() != 0o700 {
		t.Fatalf("launch dir mode = %v, want 0700", dfi.Mode().Perm())
	}
}

// StripANSI removes SGR/CSI escape sequences so the menubar's hover-preview
// panel (agent-hover-preview spec, decision 5) can render plain monospaced
// text with no ANSI renderer of its own.
func TestStripANSIRemovesEscapeSequencesButKeepsPlainText(t *testing.T) {
	in := "\x1b[32mgreen\x1b[0m plain\r\n\x1b[1;31mbold red\x1b[0m\n\x1b[?25l\x1b[2K\x1b[Kcursor stuff"
	want := "green plain\r\nbold red\ncursor stuff"
	if got := StripANSI(in); got != want {
		t.Fatalf("StripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSIOnPlainTextIsUnchanged(t *testing.T) {
	in := "no escapes here\nsecond line\n"
	if got := StripANSI(in); got != in {
		t.Fatalf("StripANSI() = %q, want unchanged %q", got, in)
	}
}

func TestParseHookReadsThePromptField(t *testing.T) {
	d := testDeps(t)
	cases := []struct {
		name  string
		parse func(event string, stdin []byte) (HookInput, error)
		event string
		stdin string
	}{
		{"claude", newClaude(d).ParseHook, "UserPromptSubmit", `{"session_id":"s","prompt":"Use zod"}`},
		{"codex", newCodex(d).ParseHook, "UserPromptSubmit", `{"session_id":"s","turn_id":"t","prompt":"Use zod"}`},
		{"cursor", newCursor(d).ParseHook, "beforeSubmitPrompt", `{"conversation_id":"c","prompt":"Use zod"}`},
	}
	for _, c := range cases {
		in, err := c.parse(c.event, []byte(c.stdin))
		if err != nil || in.Prompt != "Use zod" {
			t.Errorf("%s: Prompt = %q, err = %v", c.name, in.Prompt, err)
		}
	}
	in, _ := newAgy(d).ParseHook("PreInvocation", []byte(`{"conversationId":"c"}`))
	if in.Prompt != "" {
		t.Errorf("agy Prompt = %q, want empty", in.Prompt)
	}
}
