package catalog

import (
	"bytes"
	"encoding/json"
	"time"
)

type grokEntry struct {
	Info struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		Hidden          bool   `json:"hidden"`
		ReasoningEffort string `json:"reasoning_effort"`
		Efforts         []struct {
			ID      string `json:"id"`
			Default bool   `json:"default"`
		} `json:"reasoning_efforts"`
	} `json:"info"`
}

// ParseGrokModels reads grok's models_cache.json. The cache keys models by id
// in the order the server lists them, so the object is walked token by token
// (a Go map would lose that order and with it the default).
func ParseGrokModels(cacheJSON []byte) ([]CatalogModel, error) {
	var c struct {
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(cacheJSON, &c); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(c.Models))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errNoModels
	}
	var models []CatalogModel
	for dec.More() {
		if _, err := dec.Token(); err != nil { // the model key
			return nil, err
		}
		var e grokEntry
		if err := dec.Decode(&e); err != nil {
			return nil, err
		}
		if e.Info.ID == "" || e.Info.Hidden {
			continue
		}
		m := CatalogModel{ID: e.Info.ID, Label: e.Info.Name, DefaultEffort: e.Info.ReasoningEffort, EffortEncoding: "flag"}
		if m.Label == "" {
			m.Label = m.ID
		}
		for _, ef := range e.Info.Efforts {
			m.Efforts = append(m.Efforts, ef.ID)
			if ef.Default {
				m.DefaultEffort = ef.ID
			}
		}
		models = append(models, m)
	}
	if len(models) == 0 {
		return nil, errNoModels
	}
	return models, nil
}

// grokCacheFetchedAt is when grok wrote the cache; the zero time when unreadable.
func grokCacheFetchedAt(cacheJSON []byte) time.Time {
	var c struct {
		FetchedAt string `json:"fetched_at"`
	}
	if json.Unmarshal(cacheJSON, &c) != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, c.FetchedAt)
	return t
}
