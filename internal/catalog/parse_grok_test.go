package catalog

import (
	"slices"
	"testing"
)

func TestParseGrokModels(t *testing.T) {
	got, err := ParseGrokModels(fixture(t, "grok-models-cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if want := []string{"grok-4.7", "grok-4.7-build-fast", "grok-4.6", "grok-4.5"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want the cache's own order %v", ids, want)
	}
	m := got[0]
	if m.Label != "Grok 4.7" || m.DefaultEffort != "high" || m.EffortEncoding != "flag" {
		t.Errorf("grok-4.7 = %+v", m)
	}
	if want := []string{"xhigh", "high", "medium", "low"}; !slices.Equal(m.Efforts, want) {
		t.Errorf("efforts = %v, want %v", m.Efforts, want)
	}
	if want := []string{"high", "medium", "low"}; !slices.Equal(got[3].Efforts, want) {
		t.Errorf("grok-4.5 efforts = %v, want %v", got[3].Efforts, want)
	}
}

func TestParseGrokModelsSkipsHiddenAndRejectsEmpty(t *testing.T) {
	got, err := ParseGrokModels([]byte(`{"models":{
		"a":{"info":{"id":"a","name":"A","hidden":true}},
		"b":{"info":{"id":"b","name":"B","supports_reasoning_effort":false}}}}`))
	if err != nil || len(got) != 1 || got[0].ID != "b" || len(got[0].Efforts) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
	for _, bad := range []string{`{"models":{}}`, `{}`} {
		if _, err := ParseGrokModels([]byte(bad)); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
	if _, err := ParseGrokModels([]byte(`not json`)); err == nil {
		t.Error("garbage must fail")
	}
}
