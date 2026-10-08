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
