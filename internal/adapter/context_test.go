package adapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

func TestReadContext(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	intp := func(n int) *int { return &n }
	cases := []struct {
		name   string
		kind   kinds.AgentKind
		path   string
		tokens int
		window *int
		ok     bool
	}{
		{"claude", kinds.Claude, "testdata/context/claude.jsonl", 93123, nil, true},
		{"codex", kinds.Codex, "testdata/context/codex-rollout.jsonl", 198007, intp(258400), true},
		{"agy", kinds.Agy, "testdata/context/agy-transcript_full.jsonl", 112378, nil, true},
		{"missing", kinds.Claude, "testdata/context/nope.jsonl", 0, nil, false},
		{"empty", kinds.Codex, empty, 0, nil, false},
		{"empty path", kinds.Agy, "", 0, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ReadContext(c.kind, c.path)
			if ok != c.ok || got.Tokens != c.tokens {
				t.Fatalf("ReadContext = %+v, %v; want tokens %d ok %v", got, ok, c.tokens, c.ok)
			}
			if (got.Window == nil) != (c.window == nil) || (c.window != nil && *got.Window != *c.window) {
				t.Fatalf("window = %v, want %v", got.Window, c.window)
			}
		})
	}
}

func TestReadContextClaudeReportsModel(t *testing.T) {
	got, ok := ReadContext(kinds.Claude, "testdata/context/claude.jsonl")
	if !ok || got.Model != "claude-opus-5-5" {
		t.Fatalf("claude = %+v, %v; want model claude-opus-5-5", got, ok)
	}
}

func TestReadContextMuseAndCursor(t *testing.T) {
	got, ok := ReadContext(kinds.Muse, "testdata/context/muse-session.jsonl")
	if !ok || got.Tokens != 157990 || got.Window != nil {
		t.Fatalf("muse = %+v, %v; want 157990 nil", got, ok)
	}
	// cursor has no per-turn token source: transcript bytes / CursorBytesPerToken.
	got, ok = ReadContext(kinds.Cursor, "testdata/context/cursor-transcript.jsonl")
	if !ok || got.Tokens != 100000 || got.Window != nil {
		t.Fatalf("cursor = %+v, %v; want 100000 nil", got, ok)
	}
	if _, ok := ReadContext(kinds.Cursor, "testdata/context/nope.jsonl"); ok {
		t.Fatal("missing cursor transcript must not sample")
	}
}

func TestCursorParseHookKeepsPreCompactTokens(t *testing.T) {
	raw, err := os.ReadFile("testdata/context/cursor-precompact.json")
	if err != nil {
		t.Fatal(err)
	}
	in, err := (&Cursor{}).ParseHook("preCompact", raw)
	if err != nil {
		t.Fatal(err)
	}
	if in.ContextTokens != 180000 || in.ContextWindow != 200000 {
		t.Fatalf("tokens/window = %d/%d, want 180000/200000", in.ContextTokens, in.ContextWindow)
	}
	other, _ := (&Cursor{}).ParseHook("stop", []byte(`{"conversation_id":"c1"}`))
	if other.ContextTokens != 0 || other.ContextWindow != 0 {
		t.Fatalf("non-preCompact hook carried context: %+v", other)
	}
}

func TestContextWindowTokens(t *testing.T) {
	cases := []struct {
		kind  kinds.AgentKind
		model string
		want  int
		ok    bool
	}{
		{kinds.Claude, "claude-opus-5-5", 1000000, true},
		{kinds.Claude, "claude-sonnet-5-5", 1000000, true},
		{kinds.Claude, "claude-haiku-5-5", 1000000, true},
		{kinds.Claude, "claude-fable-5-1", 1000000, true},
		{kinds.Claude, "claude-sonnet-4-5", 200000, true},
		{kinds.Claude, "claude-opus-4-8[1m]", 1000000, true},
		{kinds.Claude, "opus", 200000, true},
		{kinds.Agy, "gemini-3-pro", 0, false},
		{kinds.Muse, "anything", 0, false},
	}
	for _, c := range cases {
		got, ok := ContextWindowTokens(c.kind, c.model)
		if got != c.want || ok != c.ok {
			t.Errorf("ContextWindowTokens(%s, %q) = %d, %v; want %d, %v", c.kind, c.model, got, ok, c.want, c.ok)
		}
	}
}

func TestMuseSessionContext(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "share", "muse", "sessions", "2026", "10", "03", "prov-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/context/muse-session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Muse{d: Deps{UserHome: home}}
	if got, ok := m.SessionContext("prov-1"); !ok || got.Tokens != 157990 || got.Seq <= 0 {
		t.Fatalf("SessionContext = %+v, %v; want 157990", got, ok)
	}
	if _, ok := m.SessionContext("nope"); ok {
		t.Fatal("unknown provider session must not sample")
	}
}
