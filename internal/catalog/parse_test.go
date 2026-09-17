package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func byID(t *testing.T, ms []CatalogModel, id string) CatalogModel {
	t.Helper()
	m, ok := Find(ms, id)
	if !ok {
		t.Fatalf("model %q not found in %v", id, ids(ms))
	}
	return m
}

func ids(ms []CatalogModel) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestParseClaudeModels(t *testing.T) {
	ms, hasMore, last, err := ParseClaudePage(fixture(t, "v1-models.json"))
	if err != nil || hasMore || last != "claude-sonnet-4-5-20250929" || len(ms) != 11 {
		t.Fatalf("page = %d models, %v %q %v", len(ms), hasMore, last, err)
	}
	want := []string{"claude-fable-5-1", "claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5-20251001",
		"claude-fable-5", "claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-opus-4-6",
		"claude-opus-4-5-20251101", "claude-sonnet-4-5-20250929"}
	if !slices.Equal(ids(ms), want) {
		t.Fatalf("order = %v", ids(ms))
	}
	for alias, id := range map[string]string{"fable": "claude-fable-5-1", "opus": "claude-opus-5", "sonnet": "claude-sonnet-5", "haiku": "claude-haiku-4-5-20251001"} {
		if m := byID(t, ms, alias); m.ID != id {
			t.Errorf("alias %s → %s", alias, m.ID)
		}
	}
	opus := byID(t, ms, "claude-opus-5")
	if opus.Label != "Claude Opus 5" || !slices.Equal(opus.Efforts, EffortLevels) || opus.DefaultEffort != "" ||
		opus.EffortEncoding != "flag" || !opus.AdvisorCapable || !slices.Equal(opus.Aliases, []string{"opus"}) {
		t.Errorf("opus = %+v", opus)
	}
	if m := byID(t, ms, "claude-sonnet-4-6"); !slices.Equal(m.Efforts, []string{"low", "medium", "high", "max"}) || m.SupportsEffort("xhigh") {
		t.Errorf("sonnet 4.6 efforts = %v", m.Efforts)
	}
	if m := byID(t, ms, "claude-opus-4-5-20251101"); !slices.Equal(m.Efforts, []string{"low", "medium", "high"}) {
		t.Errorf("opus 4.5 efforts = %v", m.Efforts)
	}
	haiku := byID(t, ms, "haiku")
	if haiku.Efforts == nil || len(haiku.Efforts) != 0 || haiku.AdvisorCapable || haiku.SupportsEffort("low") {
		t.Errorf("haiku = %+v", haiku)
	}
	if byID(t, ms, "claude-fable-5").Aliases != nil {
		t.Error("older fable must not carry the alias")
	}
	if _, err := ParseClaudeModels(fixture(t, "v1-models.json")); err != nil {
		t.Errorf("full page: %v", err)
	}
	if _, err := ParseClaudeModels([]byte("{")); err == nil {
		t.Error("bad JSON must fail")
	}
	if _, err := ParseClaudeModels([]byte(`{"data":[]}`)); err == nil {
		t.Error("an empty list is an error")
	}
	fb := ClaudeAliasFallback()
	if !slices.Equal(ids(fb), []string{"fable", "opus", "sonnet", "haiku"}) || len(byID(t, fb, "haiku").Efforts) != 0 ||
		byID(t, fb, "opus").Label != "Opus (latest)" || !byID(t, fb, "opus").AdvisorCapable {
		t.Errorf("fallback = %+v", fb)
	}
}

func TestParseCodexModelList(t *testing.T) {
	ms, def, next, err := ParseCodexModelList(fixture(t, "model-list.json"))
	want := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"}
	if err != nil || def != "gpt-6-astra" || next != "" || !slices.Equal(ids(ms), want) {
		t.Fatalf("list = %v %q %q %v", ids(ms), def, next, err)
	}
	astra := ms[0]
	if astra.Label != "GPT-6-Astra" || !astra.IsDefault || astra.DefaultEffort != "medium" || astra.EffortEncoding != "flag" ||
		!slices.Equal(astra.Efforts, []string{"low", "medium", "high", "xhigh", "max", "ultra"}) {
		t.Errorf("astra = %+v", astra)
	}
	if m := byID(t, ms, "gpt-5.6-luna"); m.IsDefault || !slices.Equal(m.Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("luna = %+v", m)
	}
	if m := byID(t, ms, "gpt-5.5"); m.DefaultEffort != "medium" || !slices.Equal(m.Efforts, []string{"low", "medium", "high", "xhigh"}) {
		t.Errorf("gpt-5.5 = %+v", m)
	}
	if astra.LaunchModel("high") != "gpt-6-astra" {
		t.Error("flag encoding launches the id")
	}
	ms, _, _, _ = ParseCodexModelList([]byte(`{"data":[{"id":"y","model":"y","displayName":"Y","defaultReasoningEffort":"high","supportedReasoningEfforts":[{"reasoningEffort":"high"},{"reasoningEffort":"xhigh"}]}]}`))
	if ms[0].DefaultEffort != "high" {
		t.Errorf("no medium → model default, got %q", ms[0].DefaultEffort)
	}
	for _, own := range []string{"", "low"} {
		body := `{"data":[{"id":"z","model":"z","displayName":"Z","defaultReasoningEffort":"` + own + `","supportedReasoningEfforts":[{"reasoningEffort":"high"},{"reasoningEffort":"xhigh"}]}]}`
		ms, _, _, _ = ParseCodexModelList([]byte(body))
		if ms[0].DefaultEffort != "xhigh" {
			t.Errorf("own default %q not offered → highest, got %q", own, ms[0].DefaultEffort)
		}
	}
	cache, _ := ParseCodexModelsCache([]byte(`{"models":[{"slug":"z","visibility":"list","default_reasoning_level":"","supported_reasoning_levels":[{"effort":"high"}]}]}`))
	if cache[0].DefaultEffort != "high" {
		t.Errorf("cache: empty own default → highest, got %q", cache[0].DefaultEffort)
	}
	_, _, next, _ = ParseCodexModelList([]byte(`{"data":[{"id":"x","model":"","displayName":"X","hidden":true,"supportedReasoningEfforts":[]}],"nextCursor":"c2"}`))
	if next != "c2" {
		t.Errorf("cursor = %q", next)
	}
	ms, _, _, _ = ParseCodexModelList([]byte(`{"data":[{"id":"x","model":"","displayName":"X","hidden":true,"supportedReasoningEfforts":[]}]}`))
	if ms[0].ID != "x" || !ms[0].Hidden || ms[0].DefaultEffort != "" || ms[0].Efforts == nil {
		t.Errorf("id fallback = %+v", ms[0])
	}
	if _, _, _, err := ParseCodexModelList([]byte(`[]`)); err == nil {
		t.Error("bad shape must fail")
	}
}

func TestParseCodexModelsCache(t *testing.T) {
	ms, err := ParseCodexModelsCache(fixture(t, "models_cache.json"))
	if err != nil || !slices.Equal(ids(ms), []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.5"}) {
		t.Fatalf("cache = %v %v", ids(ms), err)
	}
	if ms[0].DefaultEffort != "medium" || ms[2].DefaultEffort != "high" || !slices.Contains(ms[0].Efforts, "ultra") {
		t.Errorf("cache models = %+v", ms)
	}
	if _, err := ParseCodexModelsCache([]byte(`{"models":[]}`)); err == nil {
		t.Error("empty cache is an error")
	}
	if _, err := ParseCodexModelsCache([]byte(`{`)); err == nil {
		t.Error("bad JSON must fail")
	}
}

func TestParseAgyModels(t *testing.T) {
	ms, def, err := ParseAgyModels(string(fixture(t, "agy-models.txt")), fixture(t, "agy-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.1-pro",
		"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b"}
	if !slices.Equal(ids(ms), want) || def != "claude-sonnet-4-6" {
		t.Fatalf("agy = %v default %q", ids(ms), def)
	}
	flash := byID(t, ms, "gemini-3.8-flash")
	if flash.Label != "Gemini 3.8 Flash" || !slices.Equal(flash.Efforts, []string{"low", "medium", "high"}) ||
		flash.DefaultEffort != "high" || flash.EffortEncoding != "slug" ||
		flash.LaunchModel("medium") != "gemini-3.8-flash-medium" || flash.LaunchModel("") != "gemini-3.8-flash-high" {
		t.Errorf("flash = %+v", flash)
	}
	if m := byID(t, ms, "gemini-3.1-pro"); !slices.Equal(m.Efforts, []string{"low", "high"}) || m.DefaultEffort != "high" {
		t.Errorf("pro = %+v", m)
	}
	sonnet := byID(t, ms, "claude-sonnet-4-6")
	if sonnet.Label != "Claude Sonnet 4.6 (Thinking)" || sonnet.Efforts == nil || len(sonnet.Efforts) != 0 ||
		sonnet.LaunchIDs != nil || sonnet.LaunchModel("") != "claude-sonnet-4-6" || !sonnet.IsDefault {
		t.Errorf("sonnet = %+v", sonnet)
	}
	if m := byID(t, ms, "gpt-oss-120b"); m.DefaultEffort != "medium" || m.Label != "GPT-OSS 120B" || m.LaunchModel("") != "gpt-oss-120b-medium" {
		t.Errorf("gpt-oss = %+v", m)
	}
	_, def, _ = ParseAgyModels(string(fixture(t, "agy-models.txt")), []byte(`{"model":"Gemini 3.8 Flash (Low)"}`))
	if def != "gemini-3.8-flash" {
		t.Errorf("display with effort → base, got %q", def)
	}
	_, def, _ = ParseAgyModels(string(fixture(t, "agy-models.txt")), []byte(`not json`))
	if def != "" {
		t.Errorf("bad settings → no default, got %q", def)
	}
	// agy knows only low, medium and high. A bare slug beside suffixed ones can't be
	// launched without --effort, so it is dropped (no "default" level, unlike cursor).
	ms, _, _ = ParseAgyModels("m-max\tM Max\nm\tM\nm-low\tM (Low)\nm-medium\tM (Medium)\nq\tQ\nq-low\tQ (Low)\n", nil)
	if !slices.Equal(ids(ms), []string{"m-max", "m", "q"}) {
		t.Fatalf("agy levels = %v", ids(ms))
	}
	if m := ms[0]; len(m.Efforts) != 0 || m.LaunchIDs != nil || m.LaunchModel("") != "m-max" {
		t.Errorf("agy bare-only = %+v", m)
	}
	if m := ms[1]; !slices.Equal(m.Efforts, []string{"low", "medium"}) || m.Label != "M" || m.DefaultEffort != "medium" ||
		m.SupportsEffort("default") || len(m.LaunchIDs) != 2 || m.LaunchModel("") != "m-medium" || m.LaunchModel("low") != "m-low" {
		t.Errorf("agy bare sibling = %+v", m)
	}
	if m := ms[2]; !slices.Equal(m.Efforts, []string{"low"}) || m.DefaultEffort != "low" || m.LaunchModel("") != "q-low" {
		t.Errorf("agy highest fallback = %+v", m)
	}
	if _, _, err := ParseAgyModels("Fetching available models...\n", nil); err == nil {
		t.Error("no models is an error")
	}
}

func TestParseCursorModels(t *testing.T) {
	ms, def, err := ParseCursorModels(string(fixture(t, "cursor-models.txt")))
	if err != nil {
		t.Fatal(err)
	}
	head := []string{"auto", "gpt-5.3-codex", "gpt-5.3-codex-fast", "gpt-5.2", "cursor-grok-4.6-fast", "composer-2.5",
		"claude-opus-5-thinking", "claude-opus-5-thinking-fast", "gpt-5.6-sol", "gpt-5.6-sol-fast"}
	if len(ms) != 63 || !slices.Equal(ids(ms)[:len(head)], head) || def != "auto" {
		t.Fatalf("cursor = %d %v default %q", len(ms), ids(ms), def)
	}
	auto := ms[0]
	if !auto.IsDefault || len(auto.Efforts) != 0 || auto.Label != "Auto" || auto.LaunchModel("") != "auto" || auto.LaunchIDs != nil {
		t.Errorf("auto = %+v", auto)
	}
	for _, m := range ms[1:] {
		if m.IsDefault {
			t.Errorf("only auto is default, got %s", m.ID)
		}
	}
	codex := byID(t, ms, "gpt-5.3-codex")
	if !slices.Equal(codex.Efforts, []string{"default", "low", "high", "xhigh"}) || codex.DefaultEffort != "default" ||
		codex.Label != "Codex 5.3" || codex.LaunchModel("") != "gpt-5.3-codex" || codex.LaunchModel("default") != "gpt-5.3-codex" ||
		codex.LaunchModel("xhigh") != "gpt-5.3-codex-xhigh" || codex.LaunchIDs["default"] != "gpt-5.3-codex" {
		t.Errorf("codex = %+v", codex)
	}
	fast := byID(t, ms, "gpt-5.3-codex-fast")
	if fast.Label != "Codex 5.3 Fast" || fast.LaunchModel("low") != "gpt-5.3-codex-low-fast" || fast.LaunchModel("") != "gpt-5.3-codex-fast" {
		t.Errorf("fast = %+v", fast)
	}
	gpt55 := byID(t, ms, "gpt-5.5")
	if !slices.Equal(gpt55.Efforts, []string{"none", "low", "medium", "high", "extra-high"}) || gpt55.DefaultEffort != "medium" ||
		gpt55.Label != "GPT-5.5 1M" || gpt55.LaunchModel("none") != "gpt-5.5-none" || gpt55.LaunchModel("extra-high") != "gpt-5.5-extra-high" {
		t.Errorf("gpt-5.5 = %+v", gpt55)
	}
	if m := byID(t, ms, "gpt-5.5-fast"); m.Label != "GPT-5.5 Fast" || m.LaunchModel("extra-high") != "gpt-5.5-extra-high-fast" {
		t.Errorf("gpt-5.5-fast = %+v", m)
	}
	sonnet := byID(t, ms, "claude-4.6-sonnet-thinking")
	if !slices.Equal(sonnet.Efforts, []string{"medium"}) || sonnet.DefaultEffort != "medium" ||
		sonnet.Label != "Claude Sonnet 4.6 1M Thinking" || sonnet.LaunchModel("") != "claude-4.6-sonnet-medium-thinking" {
		t.Errorf("sonnet 4.6 thinking = %+v", sonnet)
	}
	if m := byID(t, ms, "claude-4.6-sonnet"); m.LaunchModel("") != "claude-4.6-sonnet-medium" {
		t.Errorf("sonnet 4.6 = %+v", m)
	}
	thinking := byID(t, ms, "claude-opus-5-thinking")
	if thinking.Label != "Claude Opus 5 1M Thinking" || !slices.Equal(thinking.Efforts, EffortLevels) ||
		thinking.DefaultEffort != "high" || thinking.LaunchModel("") != "claude-opus-5-thinking-high" ||
		thinking.LaunchModel("max") != "claude-opus-5-thinking-max" {
		t.Errorf("thinking = %+v", thinking)
	}
	if m := byID(t, ms, "claude-4-sonnet-thinking"); len(m.Efforts) != 0 || m.LaunchModel("") != "claude-4-sonnet-thinking" {
		t.Errorf("sonnet 4 thinking = %+v", m)
	}
	if m := byID(t, ms, "gemini-3.6-flash"); !slices.Equal(m.Efforts, []string{"minimal", "low", "medium", "high"}) || m.DefaultEffort != "high" {
		t.Errorf("gemini 3.6 = %+v", m)
	}
	if m := byID(t, ms, "gpt-5.1"); !slices.Equal(m.Efforts, []string{"default", "low", "high"}) || m.DefaultEffort != "default" || m.Label != "GPT-5.1" {
		t.Errorf("gpt-5.1 = %+v", m)
	}
	if m := byID(t, ms, "composer-2.5"); len(m.Efforts) != 0 || m.DefaultEffort != "" || m.LaunchIDs != nil || m.Label != "Composer 2.5" {
		t.Errorf("composer = %+v", m)
	}
	if m := byID(t, ms, "claude-fable-5-1"); m.Label != "Claude Fable 5.1 1M (NO ZDR)" {
		t.Errorf("fable = %+v", m)
	}
	if m := byID(t, ms, "kimi-k3"); !slices.Equal(m.Efforts, []string{"low", "high", "max"}) || m.Label != "Kimi K3" || m.DefaultEffort != "high" {
		t.Errorf("kimi = %+v", m)
	}
	if m := byID(t, ms, "glm-5.2"); m.Label != "GLM 5.2" {
		t.Errorf("glm = %+v", m)
	}
	for _, m := range ms {
		if strings.Contains(m.Label, "\x1b") || strings.Contains(m.ID, "Tip") || m.EffortEncoding != "slug" {
			t.Errorf("unclean entry %+v", m)
		}
		for _, e := range m.Efforts {
			if m.LaunchIDs[e] == "" {
				t.Errorf("%s: no launch id for %q", m.ID, e)
			}
		}
	}
	// Only the fallback path: a group with none of the preferred levels takes its highest.
	ms, _, _ = ParseCursorModels("x-low - X Low\nx-minimal - X Minimal\n")
	if m := ms[0]; !slices.Equal(m.Efforts, []string{"minimal", "low"}) || m.DefaultEffort != "low" || m.Label != "X" {
		t.Errorf("fallback = %+v", m)
	}
	if _, _, err := ParseCursorModels("Available models\n"); err == nil {
		t.Error("no models is an error")
	}
}

func TestFindAndEffortHelpers(t *testing.T) {
	ms := []CatalogModel{{ID: "a", Aliases: []string{"alpha"}, Efforts: []string{"low"}}}
	if _, ok := Find(ms, "alpha"); !ok {
		t.Error("alias lookup")
	}
	if _, ok := Find(ms, "b"); ok {
		t.Error("unknown id")
	}
	if !ms[0].SupportsEffort("") || !ms[0].SupportsEffort("low") || ms[0].SupportsEffort("max") {
		t.Error("SupportsEffort: \"\" (default) is always allowed")
	}
	slug := CatalogModel{ID: "m", EffortEncoding: "slug", Efforts: []string{"low"}}
	if slug.LaunchModel("low") != "m-low" || slug.LaunchModel("default") != "m" || slug.LaunchModel("") != "m" {
		t.Error("slug without launch ids falls back to id-effort, and never to id-default")
	}
}
