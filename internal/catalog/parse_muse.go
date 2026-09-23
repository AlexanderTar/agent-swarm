package catalog

import (
	"encoding/json"
	"strings"
)

// museCatalog is one ~/.local/share/muse/model-catalog/*.json file: the muse
// CLI's own provider catalog cache, refreshed on every muse run.
type museCatalog struct {
	Rows []museRow `json:"rows"`
}

type museRow struct {
	ModelID string `json:"model_id"`
	// DisplayLabel is provider-supplied, same as model_id in every row seen
	// live 2026-09-23 -- but trusted verbatim when it ever differs, same
	// discipline as claude/codex's DisplayName.
	DisplayLabel string `json:"display_label"`
	Visibility   string `json:"visibility"`
	IsDefault    bool   `json:"is_default"`
	Variants     []struct {
		Tier string `json:"tier"`
	} `json:"reasoning_effort_variants"`
}

// museLabel prefers the provider's own display_label when it actually says
// something the id doesn't; otherwise it title-cases the id's own segments
// ("muse-spark-1.3-contributor" -> "Spark 1.3 Contributor"), dropping a
// leading "muse-" the way claude's alias labels drop "Claude" -- short
// enough not to truncate in the menubar's fixed-width model picker.
func museLabel(id, displayLabel string) string {
	if displayLabel != "" && displayLabel != id {
		return displayLabel
	}
	stem := strings.TrimPrefix(id, "muse-")
	parts := strings.Split(stem, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
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
			ID: r.ModelID, Label: museLabel(r.ModelID, r.DisplayLabel),
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
