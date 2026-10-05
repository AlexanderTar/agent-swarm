package adapter

import (
	"path/filepath"
	"testing"
)

func TestCodexObserveModel(t *testing.T) {
	ctxHigh := `{"timestamp":"t","type":"turn_context","payload":{"turn_id":"1","model":"gpt-5.5","effort":"high"}}`
	ctxMed := `{"timestamp":"t","type":"turn_context","payload":{"turn_id":"2","model":"gpt-5.5","effort":"medium"}}`
	msg := `{"timestamp":"t","type":"response_item","payload":{"type":"message","role":"user","content":[]}}`
	noEffort := `{"timestamp":"t","type":"turn_context","payload":{"turn_id":"3","model":"gpt-5.5"}}`
	cases := []struct {
		name          string
		lines         []string
		model, effort string
		ok            bool
	}{
		{"last turn_context wins", []string{ctxHigh, msg, ctxMed, msg}, "gpt-5.5", "medium", true},
		{"malformed tolerated", []string{ctxHigh, `{oops`}, "gpt-5.5", "high", true},
		{"missing effort is not a miss of the model", []string{ctxHigh, noEffort}, "gpt-5.5", "", true},
		{"no turn_context", []string{msg}, "", "", false},
	}
	for _, c := range cases {
		m, e, ok := (&Codex{}).ObserveModel(writeLines(t, c.lines...), "")
		if m != c.model || e != c.effort || ok != c.ok {
			t.Errorf("%s: got %q/%q/%v, want %q/%q/%v", c.name, m, e, ok, c.model, c.effort, c.ok)
		}
	}
	if _, _, ok := (&Codex{}).ObserveModel(filepath.Join(t.TempDir(), "missing"), ""); ok {
		t.Error("missing file must not be ok")
	}
}
