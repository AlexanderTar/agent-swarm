package ids

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The golden file is shared with the menubar (P4) and the board, so the three
// implementations of agent-name normalisation cannot drift (spec §4).
func TestKebabGolden(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "kebab_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version      int    `json:"version"`
		ErrorMessage string `json:"error_message"`
		Cases        []struct {
			Input  string `json:"input"`
			Max    int    `json:"max"`
			Output string `json:"output"`
			Error  bool   `json:"error"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ErrorMessage != ErrEmptyName.Error() {
		t.Fatalf("the golden's error message is %q, ids.ErrEmptyName is %q", doc.ErrorMessage, ErrEmptyName)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("no golden cases")
	}
	for _, c := range doc.Cases {
		got, err := KebabMax(c.Input, c.Max)
		if c.Error {
			if err == nil {
				t.Errorf("KebabMax(%q, %d) = %q, want an error", c.Input, c.Max, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("KebabMax(%q, %d): %v", c.Input, c.Max, err)
			continue
		}
		if got != c.Output {
			t.Errorf("KebabMax(%q, %d) = %q, want %q", c.Input, c.Max, got, c.Output)
		}
	}
}
