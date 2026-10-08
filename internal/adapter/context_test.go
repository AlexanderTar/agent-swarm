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
