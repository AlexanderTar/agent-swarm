package catalog

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

var errNoModels = errors.New("no models in output")

// ---- claude ----

type claudePage struct {
	Data []struct {
		ID           string    `json:"id"`
		DisplayName  string    `json:"display_name"`
		CreatedAt    time.Time `json:"created_at"`
		Capabilities struct {
			Effort map[string]json.RawMessage `json:"effort"`
		} `json:"capabilities"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

var claudeFamily = regexp.MustCompile(`^claude-(fable|opus|sonnet|haiku)-`)
var aliasOrder = []string{"fable", "opus", "sonnet", "haiku"}

// ParseClaudePage parses one GET /v1/models page. Aliases come first (fable, opus,
// sonnet, haiku → the newest model of that family), then the rest newest first.
func ParseClaudePage(body []byte) ([]CatalogModel, bool, string, error) {
	var p claudePage
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, false, "", err
	}
	type dated struct {
		m      CatalogModel
		family string
		at     time.Time
	}
	var all []dated
	for _, d := range p.Data {
		m := CatalogModel{ID: d.ID, Label: d.DisplayName, Efforts: []string{}, EffortEncoding: "flag"}
		var supported bool // effort.supported is a bare boolean; each level is {"supported": bool}
		_ = json.Unmarshal(d.Capabilities.Effort["supported"], &supported)
		if supported {
			for _, lvl := range EffortLevels {
				var l struct{ Supported bool }
				_ = json.Unmarshal(d.Capabilities.Effort[lvl], &l)
				if l.Supported {
					m.Efforts = append(m.Efforts, lvl)
				}
			}
		}
		family := ""
		if f := claudeFamily.FindStringSubmatch(d.ID); f != nil {
			family = f[1]
		}
		m.AdvisorCapable = family == "fable" || family == "opus" || family == "sonnet"
		all = append(all, dated{m, family, d.CreatedAt})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	var head, tail []CatalogModel
	taken := map[int]bool{}
	for _, fam := range aliasOrder {
		for i, d := range all {
			if d.family == fam {
				d.m.Aliases = []string{fam}
				head = append(head, d.m)
				taken[i] = true
				break
			}
		}
	}
	for i, d := range all {
		if !taken[i] {
			tail = append(tail, d.m)
		}
	}
	return append(head, tail...), p.HasMore, p.LastID, nil
}

// ParseClaudeModels parses a single, complete page.
func ParseClaudeModels(body []byte) ([]CatalogModel, error) {
	ms, _, _, err := ParseClaudePage(body)
	if err == nil && len(ms) == 0 {
		err = errNoModels
	}
	return ms, err
}

// ClaudeAliasFallback is used when the Models API can't be reached and nothing is cached.
func ClaudeAliasFallback() []CatalogModel {
	var out []CatalogModel
	for _, fam := range aliasOrder {
		m := CatalogModel{ID: fam, Label: strings.ToUpper(fam[:1]) + fam[1:] + " (latest)", Aliases: []string{fam},
			Efforts: slices.Clone(EffortLevels), EffortEncoding: "flag", AdvisorCapable: fam != "haiku"}
		if fam == "haiku" {
			m.Efforts = []string{} // --effort is silently ignored for Haiku
		}
		out = append(out, m)
	}
	return out
}

// ---- codex ----

func codexDefaultEffort(efforts []string, own string) string {
	switch {
	case slices.Contains(efforts, "medium"):
		return "medium" // D2: GPT models default to medium
	case len(efforts) == 0:
		return ""
	case slices.Contains(efforts, own):
		return own
	default:
		return efforts[len(efforts)-1] // the model's own default isn't offered: take the highest
	}
}

// ParseCodexModelList parses the `result` of app-server `model/list`.
func ParseCodexModelList(result []byte) ([]CatalogModel, string, string, error) {
	var r struct {
		Data []struct {
			ID                        string `json:"id"`
			Model                     string `json:"model"`
			DisplayName               string `json:"displayName"`
			Hidden                    bool   `json:"hidden"`
			IsDefault                 bool   `json:"isDefault"`
			DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
			SupportedReasoningEfforts []struct {
				ReasoningEffort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return nil, "", "", err
	}
	var out []CatalogModel
	def := ""
	for _, d := range r.Data {
		id := d.Model
		if id == "" {
			id = d.ID
		}
		efforts := []string{}
		for _, e := range d.SupportedReasoningEfforts {
			efforts = append(efforts, e.ReasoningEffort)
		}
		out = append(out, CatalogModel{ID: id, Label: d.DisplayName, Efforts: efforts, EffortEncoding: "flag",
			DefaultEffort: codexDefaultEffort(efforts, d.DefaultReasoningEffort), Hidden: d.Hidden, IsDefault: d.IsDefault})
		if d.IsDefault {
			def = id
		}
	}
	next := ""
	if r.NextCursor != nil {
		next = *r.NextCursor
	}
	return out, def, next, nil
}

// ParseCodexModelsCache reads ~/.codex/models_cache.json (visibility "list" only).
func ParseCodexModelsCache(body []byte) ([]CatalogModel, error) {
	var c struct {
		Models []struct {
			Slug                     string `json:"slug"`
			DisplayName              string `json:"display_name"`
			DefaultReasoningLevel    string `json:"default_reasoning_level"`
			Visibility               string `json:"visibility"`
			SupportedReasoningLevels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, err
	}
	var out []CatalogModel
	for _, m := range c.Models {
		if m.Visibility != "list" {
			continue
		}
		efforts := []string{}
		for _, e := range m.SupportedReasoningLevels {
			efforts = append(efforts, e.Effort)
		}
		out = append(out, CatalogModel{ID: m.Slug, Label: m.DisplayName, Efforts: efforts, EffortEncoding: "flag",
			DefaultEffort: codexDefaultEffort(efforts, m.DefaultReasoningLevel)})
	}
	if len(out) == 0 {
		return nil, errNoModels
	}
	return out, nil
}

// ---- slug agents (agy, cursor) ----

// slugDialect says which level suffixes an agent uses, weakest first, whether
// a level may sit before `-thinking` (cursor `claude-4.6-sonnet-medium-thinking`),
// and whether a bare slug beside suffixed ones is offered as the "default" level.
// agy has no such level: its bare base slug fails without --effort (P0-12), so it is dropped.
type slugDialect struct {
	levels       []string
	thinking     bool
	defaultLevel bool
}

var (
	agyDialect    = slugDialect{levels: []string{"low", "medium", "high"}}
	cursorDialect = slugDialect{levels: []string{"none", "minimal", "low", "medium", "high", "xhigh", "extra-high", "max"}, thinking: true, defaultLevel: true}
)

// split returns the base id and level:
// "gpt-5.3-codex-low-fast" → ("gpt-5.3-codex-fast", "low"),
// "claude-4.6-sonnet-medium-thinking" → ("claude-4.6-sonnet-thinking", "medium").
func (d slugDialect) split(slug string) (base, level string) {
	stem, fast := strings.CutSuffix(slug, "-fast")
	body, thinking := stem, false
	if d.thinking {
		body, thinking = strings.CutSuffix(stem, "-thinking")
	}
	for _, l := range d.levels {
		// the longest match wins, so "-extra-high" isn't read as "-high"
		if b, ok := strings.CutSuffix(body, "-"+l); ok && len(l) > len(level) {
			base, level = b, l
		}
	}
	if level == "" {
		base, thinking = stem, false
	}
	if thinking {
		base += "-thinking"
	}
	if fast {
		base += "-fast"
	}
	return base, level
}

// rank orders a group's levels: "default" first, then the dialect's order.
func (d slugDialect) rank(level string) int {
	return slices.Index(d.levels, level) // "default" → -1
}

type slugLine struct{ slug, display string }

func (d slugDialect) group(lines []slugLine, stripEffort func(string) string) []CatalogModel {
	var order []string
	groups := map[string]*CatalogModel{}
	bareLabel := map[string]string{}
	for _, l := range lines {
		base, level := d.split(l.slug)
		m, ok := groups[base]
		if !ok {
			m = &CatalogModel{ID: base, EffortEncoding: "slug", Efforts: []string{}, LaunchIDs: map[string]string{}}
			groups[base] = m
			order = append(order, base)
		}
		if level == "" {
			level = DefaultLevel
			bareLabel[base] = l.display
		} else if m.Label == "" {
			m.Label = stripEffort(l.display)
		}
		m.LaunchIDs[level] = l.slug
		m.Efforts = append(m.Efforts, level)
	}
	out := make([]CatalogModel, 0, len(order))
	for _, base := range order {
		m := groups[base]
		label, bare := bareLabel[base]
		if bare && len(m.Efforts) == 1 {
			// a bare id with no suffixed siblings has no effort control
			m.Label, m.Efforts, m.LaunchIDs = label, []string{}, nil
			out = append(out, *m)
			continue
		}
		if bare && d.defaultLevel {
			m.Label = label
		} else if bare {
			m.Efforts = slices.DeleteFunc(m.Efforts, func(l string) bool { return l == DefaultLevel })
			delete(m.LaunchIDs, DefaultLevel)
		}
		sort.SliceStable(m.Efforts, func(i, j int) bool { return d.rank(m.Efforts[i]) < d.rank(m.Efforts[j]) })
		pref := "high"
		if strings.HasPrefix(base, "gpt-") {
			pref = "medium"
		}
		switch { // D2
		case slices.Contains(m.Efforts, pref):
			m.DefaultEffort = pref
		case slices.Contains(m.Efforts, DefaultLevel):
			m.DefaultEffort = DefaultLevel
		default:
			m.DefaultEffort = m.Efforts[len(m.Efforts)-1]
		}
		out = append(out, *m)
	}
	return out
}

var agyEffortLabel = regexp.MustCompile(`\s*\((Low|Medium|High)\)\s*$`)

// ParseAgyModels parses `agy models` (slug<TAB>Display Name) and the default from settings.json.
func ParseAgyModels(out string, settings []byte) ([]CatalogModel, string, error) {
	var lines []slugLine
	for _, line := range strings.Split(out, "\n") {
		slug, display, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if ok && strings.TrimSpace(slug) != "" {
			lines = append(lines, slugLine{strings.TrimSpace(slug), strings.TrimSpace(display)})
		}
	}
	if len(lines) == 0 {
		return nil, "", errNoModels
	}
	models := agyDialect.group(lines, func(s string) string { return agyEffortLabel.ReplaceAllString(s, "") })
	var cfg struct {
		Model string `json:"model"`
	}
	def := ""
	if json.Unmarshal(settings, &cfg) == nil && cfg.Model != "" {
		for _, l := range lines {
			if l.display == cfg.Model {
				def, _ = agyDialect.split(l.slug)
				break
			}
		}
	}
	for i := range models {
		models[i].IsDefault = models[i].ID == def
	}
	return models, def, nil
}

var (
	ansi              = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	cursorLine        = regexp.MustCompile(`^(\S+) - (.+?)(\s+\((current, )?default\)|\s+\(current\))?$`)
	cursorEffortWords = regexp.MustCompile(`\b(Extra High|None|Minimal|Low|Medium|High|Max)\b`)
)

// ParseCursorModels parses `cursor-agent models` (ANSI-coloured "slug - Display" lines).
func ParseCursorModels(out string) ([]CatalogModel, string, error) {
	var lines []slugLine
	def := ""
	for _, raw := range strings.Split(ansi.ReplaceAllString(out, ""), "\n") {
		raw = strings.TrimSpace(raw)
		if strings.HasPrefix(raw, "Tip:") {
			break
		}
		m := cursorLine.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		lines = append(lines, slugLine{m[1], m[2]})
		if strings.Contains(m[3], "default") {
			def, _ = cursorDialect.split(m[1])
		}
	}
	if len(lines) == 0 {
		return nil, "", errNoModels
	}
	models := cursorDialect.group(lines, func(s string) string {
		return strings.Join(strings.Fields(cursorEffortWords.ReplaceAllString(s, "")), " ")
	})
	for i := range models {
		models[i].IsDefault = models[i].ID == def
	}
	return models, def, nil
}
