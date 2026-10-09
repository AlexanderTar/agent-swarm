package adapter

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const museCompaction = `context_compaction={"provider_context_limit_tokens":200000,"soft_threshold":0.9,"hard_threshold":0.98}`

func TestClaudeLaunchCarriesAutocompactOnlyWithACap(t *testing.T) {
	d := testDeps(t)
	s := claudeSpec(t, d)
	s.Role = "orchestrator"
	s.LowTokenCap = 200000
	l, err := newClaude(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(l.Argv, "--autocompact")
	if i < 0 || l.Argv[i+1] != "200000" {
		t.Fatalf("argv lacks --autocompact 200000: %v", l.Argv)
	}
	s.LowTokenCap = 0
	l, err = newClaude(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(l.Argv, "--autocompact") {
		t.Fatalf("uncapped launch has --autocompact: %v", l.Argv)
	}
}

func TestCodexLaunchCarriesAutoCompactLimitOnlyWithACap(t *testing.T) {
	s := codexSpec(t)
	s.Role = "orchestrator"
	s.LowTokenCap = 150000
	l, err := newCodex(testDeps(t)).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(l.Argv, "model_auto_compact_token_limit=150000")
	if i < 1 || l.Argv[i-1] != "-c" {
		t.Fatalf("argv lacks -c model_auto_compact_token_limit=150000: %v", l.Argv)
	}
	s.LowTokenCap = 0
	l, err = newCodex(testDeps(t)).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(l.Argv, " "), "model_auto_compact_token_limit") {
		t.Fatalf("uncapped launch has the codex limit: %v", l.Argv)
	}
}

func TestMuseLaunchCarriesContextCompactionOnlyWithACap(t *testing.T) {
	s := museSpec(t)
	s.Role = "orchestrator"
	s.LowTokenCap = 200000
	l, err := newMuse(testDeps(t)).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(l.Argv, museCompaction)
	if i < 1 || l.Argv[i-1] != "-c" {
		t.Fatalf("argv lacks -c %s: %q", museCompaction, l.Argv)
	}
	if l.Argv[len(l.Argv)-1] != s.Kickoff {
		t.Fatalf("the kickoff must stay last: %q", l.Argv)
	}
	s.LowTokenCap = 0
	l, err = newMuse(testDeps(t)).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(l.Argv, " "), "context_compaction") {
		t.Fatalf("uncapped launch has context_compaction: %q", l.Argv)
	}
}

func writeCursorCLIConfig(t *testing.T, d Deps, body string) {
	t.Helper()
	p := filepath.Join(d.UserHome, ".cursor", "cli-config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCursorLaunchAddsContext200kOnlyWhenTheCatalogListsIt(t *testing.T) {
	const listed = `{"modelParameters":{"claude-opus-4-8":[{"id":"context","value":"200k"}],"gpt-5":[{"id":"context","value":"300k"}]}}`
	cases := []struct {
		name, cfg, model string
		cap              int
		want             string
	}{
		{"listed", listed, "claude-opus-4-8", 200000, "claude-opus-4-8[context=200k]"},
		{"model without 200k", listed, "gpt-5", 200000, "gpt-5"},
		{"model not in catalog", listed, "auto", 200000, "auto"},
		{"no config file", "", "claude-opus-4-8", 200000, "claude-opus-4-8"},
		{"no cap", listed, "claude-opus-4-8", 0, "claude-opus-4-8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := testDeps(t)
			if c.cfg != "" {
				writeCursorCLIConfig(t, d, c.cfg)
			}
			s := cursorSpec(t)
			s.Model, s.Role, s.LowTokenCap = c.model, "orchestrator", c.cap
			l, err := newCursor(d).Launch(s)
			if err != nil {
				t.Fatal(err)
			}
			i := slices.Index(l.Argv, "--model")
			if i < 0 || l.Argv[i+1] != c.want {
				t.Fatalf("--model = %q, want %q (%v)", l.Argv[i+1], c.want, l.Argv)
			}
		})
	}
}

func TestAgyLaunchIgnoresTheCap(t *testing.T) {
	d := testDeps(t)
	s := agySpec(t)
	base, err := newAgy(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Role, s.LowTokenCap = "orchestrator", 200000
	capped, err := newAgy(testDeps(t)).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(base.Argv, " ") != strings.Join(capped.Argv, " ") {
		t.Fatalf("agy argv changed with a cap:\n%v\n%v", base.Argv, capped.Argv)
	}
}
