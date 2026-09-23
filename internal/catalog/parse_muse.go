package catalog

import "encoding/json"

// museCatalog is one ~/.local/share/muse/model-catalog/*.json file: the muse
// CLI's own provider catalog cache, refreshed on every muse run.
type museCatalog struct {
	Rows []museRow `json:"rows"`
}

type museRow struct {
	ModelID    string `json:"model_id"`
	Visibility string `json:"visibility"`
	IsDefault  bool   `json:"is_default"`
	Variants   []struct {
		Tier string `json:"tier"`
	} `json:"reasoning_effort_variants"`
}

// ParseMuseModels parses one model-catalog file and resolves the default from
// settings.json's model when it matches a row, else from the is_default row.
// A missing catalog (fresh machine) is the caller's problem: MuseFetcher unions
// the settings model itself, so this function errors on empty rows only.
func ParseMuseModels(catalogJSON, settingsJSON []byte) ([]CatalogModel, string, error) {
	var c museCatalog
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		return nil, "", err
	}
	var models []CatalogModel
	marked := ""
	for _, r := range c.Rows {
		if r.ModelID == "" || (r.Visibility != "" && r.Visibility != "visible") {
			continue
		}
		if r.IsDefault {
			marked = r.ModelID
		}
		var efforts []string
		for _, v := range r.Variants {
			if v.Tier != "" {
				efforts = append(efforts, v.Tier)
			}
		}
		models = append(models, CatalogModel{
			ID: r.ModelID, Label: r.ModelID,
			Efforts: efforts, DefaultEffort: "high", EffortEncoding: "flag",
		})
	}
	if len(models) == 0 {
		return nil, "", errNoModels
	}
	def := marked
	if cfgModel := museConfiguredModel(settingsJSON); cfgModel != "" {
		if _, ok := Find(models, cfgModel); ok {
			def = cfgModel
		}
	}
	for i := range models {
		models[i].IsDefault = models[i].ID == def
	}
	return models, def, nil
}

// museConfiguredModel reads the model id settings.json pins, or "" if unset
// or unparsable. Shared by ParseMuseModels (per-file default) and MuseFetcher
// (cross-file default precedence, and the no-catalog backstop).
func museConfiguredModel(settingsJSON []byte) string {
	var cfg struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(settingsJSON, &cfg) != nil {
		return ""
	}
	return cfg.Model
}
