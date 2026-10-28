package catalog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/kinds"
)

func TestEntriesAdvisorEnabledFollowsKind(t *testing.T) {
	first := &fakeFetcher{kind: kinds.Claude, version: "2.1.274", err: errors.New("offline")}
	second := &fakeFetcher{kind: kinds.Muse, version: "1.0.0", err: errors.New("offline")}
	s, _ := newCatalog(t, first, second)
	entries, _ := s.Entries(bg)
	for _, e := range entries {
		if e.AdvisorEnabled != e.Kind.AdvisorEnabled() {
			t.Errorf("%s advisor_enabled = %v, want %v", e.Kind, e.AdvisorEnabled, e.Kind.AdvisorEnabled())
		}
	}
	if !entries[0].AdvisorEnabled {
		t.Errorf("a kind with an advisor must report advisor_enabled")
	}
	b, _ := json.Marshal(entries[1])
	if !strings.Contains(string(b), `"advisor_enabled":`) {
		t.Errorf("json = %s", b)
	}
}
