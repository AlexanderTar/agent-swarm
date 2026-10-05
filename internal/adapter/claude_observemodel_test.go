package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLines(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaudeObserveModel(t *testing.T) {
	opus := `{"type":"assistant","effort":"high","message":{"model":"claude-opus-5-5","role":"assistant","content":[]}}`
	sonnet := `{"type":"assistant","effort":"medium","message":{"model":"claude-sonnet-5","role":"assistant","content":[]}}`
	synthetic := `{"type":"assistant","message":{"model":"<synthetic>","role":"assistant","content":[]}}`
	sidechain := `{"type":"assistant","isSidechain":true,"effort":"low","message":{"model":"claude-haiku-4-5","role":"assistant","content":[]}}`
	user := `{"type":"user","message":{"role":"user","content":"hi"}}`
	cases := []struct {
		name          string
		lines         []string
		model, effort string
		ok            bool
	}{
		{"last assistant wins", []string{opus, user, sonnet, user}, "claude-sonnet-5", "medium", true},
		{"effort switch same model", []string{opus, `{"type":"assistant","effort":"low","message":{"model":"claude-opus-5-5"}}`}, "claude-opus-5-5", "low", true},
		{"synthetic skipped", []string{opus, synthetic}, "claude-opus-5-5", "high", true},
		{"sidechain skipped", []string{sonnet, sidechain}, "claude-sonnet-5", "medium", true},
		{"malformed tolerated", []string{opus, `{not json`}, "claude-opus-5-5", "high", true},
		{"no assistant", []string{user}, "", "", false},
	}
	for _, c := range cases {
		m, e, ok := (&Claude{}).ObserveModel(writeLines(t, c.lines...), "")
		if m != c.model || e != c.effort || ok != c.ok {
			t.Errorf("%s: got %q/%q/%v, want %q/%q/%v", c.name, m, e, ok, c.model, c.effort, c.ok)
		}
	}
	if _, _, ok := (&Claude{}).ObserveModel(filepath.Join(t.TempDir(), "missing"), ""); ok {
		t.Error("missing file must not be ok")
	}
}
