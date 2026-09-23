package catalog

import "testing"

// museCatalogFixture is a trimmed but verbatim-shaped excerpt of the real
// ~/.local/share/muse/model-catalog/*.json rows found live on 2026-09-23:
// display_label is present but byte-identical to model_id in every row, so
// the label must come from the id transform, not from this field, for these
// four models.
const museCatalogFixture = `{
  "rows": [
    {"model_id": "muse-spark-1.3", "display_label": "muse-spark-1.3",
     "visibility": "visible", "is_default": false,
     "reasoning_effort_variants": [{"tier": "high"}]},
    {"model_id": "muse-spark-1.3-contributor", "display_label": "muse-spark-1.3-contributor",
     "visibility": "visible", "is_default": true,
     "reasoning_effort_variants": [{"tier": "high"}]},
    {"model_id": "muse-spark-1.2", "display_label": "muse-spark-1.2",
     "visibility": "visible", "is_default": false,
     "reasoning_effort_variants": [{"tier": "high"}]},
    {"model_id": "muse-spark-1.2-contributor", "display_label": "muse-spark-1.2-contributor",
     "visibility": "visible", "is_default": false,
     "reasoning_effort_variants": [{"tier": "high"}]}
  ]
}`

func TestParseMuseModelsFriendlyLabels(t *testing.T) {
	models, _, err := ParseMuseModels([]byte(museCatalogFixture), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"muse-spark-1.3":             "Spark 1.3",
		"muse-spark-1.3-contributor": "Spark 1.3 Contributor",
		"muse-spark-1.2":             "Spark 1.2",
		"muse-spark-1.2-contributor": "Spark 1.2 Contributor",
	}
	if len(models) != len(want) {
		t.Fatalf("got %d models, want %d", len(models), len(want))
	}
	for _, m := range models {
		wantLabel, ok := want[m.ID]
		if !ok {
			t.Errorf("unexpected model id %q", m.ID)
			continue
		}
		if m.Label != wantLabel {
			t.Errorf("id %q: label = %q, want %q", m.ID, m.Label, wantLabel)
		}
	}
}

func TestParseMuseModelsPrefersDisplayLabel(t *testing.T) {
	raw := `{"rows": [
		{"model_id": "muse-spark-1.4", "display_label": "Spark Preview",
		 "visibility": "visible", "is_default": true,
		 "reasoning_effort_variants": [{"tier": "high"}]}
	]}`
	models, _, err := ParseMuseModels([]byte(raw), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Label != "Spark Preview" {
		t.Fatalf("models = %+v, want label %q", models, "Spark Preview")
	}
}

func TestParseMuseModelsTransformNoPrefix(t *testing.T) {
	raw := `{"rows": [
		{"model_id": "other-model-2", "display_label": "other-model-2",
		 "visibility": "visible", "is_default": true,
		 "reasoning_effort_variants": [{"tier": "high"}]}
	]}`
	models, _, err := ParseMuseModels([]byte(raw), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Label != "Other Model 2" {
		t.Fatalf("models = %+v, want label %q", models, "Other Model 2")
	}
}
