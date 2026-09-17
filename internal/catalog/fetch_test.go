package catalog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

var bg = context.Background()

const secret = "sk-ant-oat01-SECRET-TOKEN"

func keychain(extra map[string]execx.Result) *execx.Fake {
	r := map[string]execx.Result{
		"security find-generic-password -s Claude Code-credentials -a alex -w": {Out: `{"claudeAiOauth":{"accessToken":"` + secret + `","expiresAt":1}}` + "\n"},
		"claude --version": {Out: "2.1.274 (Claude Code)\n"},
	}
	for k, v := range extra {
		r[k] = v
	}
	return &execx.Fake{Responses: r}
}

func TestClaudeFetcherPagesAndHeaders(t *testing.T) {
	var page struct {
		Data []json.RawMessage `json:"data"`
	}
	json.Unmarshal(fixture(t, "v1-models.json"), &page)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for h, want := range map[string]string{
			"Authorization":     "Bearer " + secret,
			"anthropic-version": "2023-06-01",
			"anthropic-beta":    "oauth-2025-04-20",
			"User-Agent":        "claude-code/2.1.274",
		} {
			if got := r.Header.Get(h); got != want {
				t.Errorf("%s = %q", h, got)
			}
		}
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		first, rest := page.Data[:6], page.Data[6:]
		if r.URL.Query().Get("after_id") == "" {
			json.NewEncoder(w).Encode(map[string]any{"data": first, "has_more": true, "last_id": "claude-opus-4-7"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": rest, "has_more": false, "last_id": "claude-sonnet-4-5-20250929"})
	}))
	defer srv.Close()
	f := &ClaudeFetcher{Run: keychain(nil).Runner(), HTTP: srv.Client(), BaseURL: srv.URL, User: "alex"}
	got, err := f.Fetch(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 11 || got.Source != "api.anthropic.com/v1/models" || got.Models[1].ID != "claude-opus-5" {
		t.Fatalf("fetched = %+v", got)
	}
	// haiku's newest model is on page 2: aliases must be assigned after merging pages.
	want, _ := ParseClaudeModels(fixture(t, "v1-models.json"))
	if !reflect.DeepEqual(got.Models, want) || got.Models[3].ID != "claude-haiku-4-5-20251001" {
		t.Fatalf("paged models differ from single-page parse:\n%+v\n%+v", got.Models, want)
	}
	if !slices.Equal(seen, []string{"/v1/models?limit=100", "/v1/models?after_id=claude-opus-4-7&limit=100"}) {
		t.Fatalf("requests = %v", seen)
	}
	if v, err := f.Version(bg); v != "2.1.274" || err != nil || f.Kind() != "claude" {
		t.Fatalf("version = %q %v", v, err)
	}
}

func TestClaudeFetcherFailuresNeverLeakTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "token "+r.Header.Get("Authorization")+" expired", http.StatusUnauthorized)
	}))
	defer srv.Close()
	f := &ClaudeFetcher{Run: keychain(nil).Runner(), HTTP: srv.Client(), BaseURL: srv.URL, User: "alex"}
	_, err := f.Fetch(bg)
	if err == nil || err.Error() != "api.anthropic.com returned 401" {
		t.Fatalf("err = %v", err)
	}
	locked := keychain(map[string]execx.Result{
		"security find-generic-password -s Claude Code-credentials -a alex -w": {Out: secret, Err: errors.New("exit 44: " + secret)},
	})
	f.Run = locked.Runner()
	if _, err := f.Fetch(bg); err == nil || err.Error() != "Couldn't read the Claude sign-in from the keychain." {
		t.Fatalf("keychain err = %v", err)
	}
	empty := keychain(map[string]execx.Result{
		"security find-generic-password -s Claude Code-credentials -a alex -w": {Out: `{"claudeAiOauth":{}}`},
	})
	f.Run = empty.Runner()
	if _, err := f.Fetch(bg); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("empty token err = %v", err)
	}
}

func TestClaudeFetcherCapsPaging(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "has_more": true, "last_id": fmt.Sprintf("m&%d", requests)})
	}))
	defer srv.Close()
	f := &ClaudeFetcher{Run: keychain(nil).Runner(), HTTP: srv.Client(), BaseURL: srv.URL, User: "alex"}
	_, err := f.Fetch(bg)
	if err == nil || err.Error() != "api.anthropic.com returned more than 50 pages" || requests != 50 {
		t.Fatalf("err = %v after %d requests", err, requests)
	}
}

func TestClaudeFetcherEscapesAfterID(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Query().Get("after_id"))
		if len(got) == 1 {
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "has_more": true, "last_id": "a&b=c d"})
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"claude-opus-5","display_name":"Opus 5","created_at":"2026-07-15T00:00:00Z"}],"has_more":false}`)
	}))
	defer srv.Close()
	f := &ClaudeFetcher{Run: keychain(nil).Runner(), HTTP: srv.Client(), BaseURL: srv.URL, User: "alex"}
	if _, err := f.Fetch(bg); err != nil || len(got) != 2 || got[1] != "a&b=c d" {
		t.Fatalf("after_id = %q, %v", got, err)
	}
}

type fakeProc struct {
	requests []map[string]any
}

// start returns a Starter that answers JSON-RPC lines using reply.
func (p *fakeProc) start(t *testing.T, reply func(req map[string]any) []string) execx.Starter {
	return func(ctx context.Context, name string, args ...string) (*execx.Proc, error) {
		if got := name + " " + strings.Join(args, " "); got != "codex -s read-only -a never app-server" {
			t.Errorf("argv = %q", got)
		}
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		go func() {
			sc := bufio.NewScanner(inR)
			for sc.Scan() {
				var req map[string]any
				json.Unmarshal(sc.Bytes(), &req)
				p.requests = append(p.requests, req)
				for _, line := range reply(req) {
					fmt.Fprintln(outW, line)
				}
			}
			outW.Close()
		}()
		return &execx.Proc{Stdin: inW, Stdout: outR, Kill: func() { inW.Close(); outW.Close() }}, nil
	}
}

func TestCodexFetcherAppServer(t *testing.T) {
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	json.Unmarshal(fixture(t, "model-list.json"), &list)
	p := &fakeProc{}
	f := &CodexFetcher{Timeout: time.Second, CacheFile: "/nonexistent", Start: p.start(t, func(req map[string]any) []string {
		switch req["method"] {
		case "initialize":
			return []string{`{"method":"remoteControl/status/changed","params":{}}`, `{"id":1,"result":{"userAgent":"swarm"}}`}
		case "model/list":
			params, _ := req["params"].(map[string]any)
			if params["cursor"] == nil {
				page, _ := json.Marshal(map[string]any{"data": list.Data[:1], "nextCursor": "c2"})
				return []string{`{"method":"noise"}`, fmt.Sprintf(`{"id":%v,"result":%s}`, req["id"], page)}
			}
			page, _ := json.Marshal(map[string]any{"data": list.Data[1:], "nextCursor": nil})
			return []string{fmt.Sprintf(`{"id":%v,"result":%s}`, req["id"], page)}
		}
		return nil
	})}
	got, err := f.Fetch(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 5 || got.DefaultModel != "gpt-6-astra" || got.Source != "codex app-server model/list" {
		t.Fatalf("fetched = %+v", got)
	}
	methods := []string{}
	for _, r := range p.requests {
		methods = append(methods, fmt.Sprint(r["method"]))
	}
	if !slices.Equal(methods, []string{"initialize", "initialized", "model/list", "model/list"}) {
		t.Fatalf("methods = %v", methods)
	}
	init := p.requests[0]["params"].(map[string]any)["clientInfo"].(map[string]any)
	if init["name"] != "swarm" || init["version"] == "" {
		t.Errorf("clientInfo = %v", init)
	}
	first := p.requests[2]["params"].(map[string]any)
	if first["includeHidden"] != false || p.requests[3]["params"].(map[string]any)["cursor"] != "c2" {
		t.Errorf("list params = %v / %v", first, p.requests[3]["params"])
	}
	if _, ok := p.requests[1]["id"]; ok {
		t.Error("initialized is a notification (no id)")
	}
}

func TestCodexFetcherFallsBackToCache(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "models_cache.json")
	os.WriteFile(cache, fixture(t, "models_cache.json"), 0o644)
	hung := &fakeProc{}
	f := &CodexFetcher{Timeout: 50 * time.Millisecond, CacheFile: cache,
		Start: hung.start(t, func(map[string]any) []string { return nil })}
	start := time.Now()
	got, err := f.Fetch(bg)
	if err != nil || len(got.Models) != 3 || got.Source != "~/.codex/models_cache.json" || time.Since(start) > time.Second {
		t.Fatalf("fallback = %+v, %v in %v", got, err, time.Since(start))
	}
	f.Start = func(context.Context, string, ...string) (*execx.Proc, error) {
		return nil, errors.New("codex: not found")
	}
	f.CacheFile = "/nonexistent"
	if _, err := f.Fetch(bg); err == nil || !strings.Contains(err.Error(), "codex: not found") {
		t.Fatalf("both failing: %v", err)
	}
	errProc := &fakeProc{}
	f.Start = errProc.start(t, func(req map[string]any) []string {
		return []string{fmt.Sprintf(`{"id":%v,"error":{"code":-32600,"message":"not signed in"}}`, req["id"])}
	})
	if _, err := f.Fetch(bg); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("rpc error: %v", err)
	}
	f.Run = (&execx.Fake{Responses: map[string]execx.Result{"codex --version": {Out: "codex-cli 0.154.0\n"}}}).Runner()
	if v, _ := f.Version(bg); v != "0.154.0" || f.Kind() != "codex" {
		t.Fatalf("version = %q", v)
	}
}

func TestAgyAndCursorFetchers(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(settings, fixture(t, "agy-settings.json"), 0o644)
	run := (&execx.Fake{Responses: map[string]execx.Result{
		"agy models":             {Out: string(fixture(t, "agy-models.txt"))},
		"agy --version":          {Out: "1.2.5\n"},
		"cursor-agent models":    {Out: string(fixture(t, "cursor-models.txt"))},
		"cursor-agent --version": {Out: "2026.09.10-4c1f2ab\n"},
	}}).Runner()
	agy := &AgyFetcher{Run: run, SettingsFile: settings}
	got, err := agy.Fetch(bg)
	if err != nil || got.DefaultModel != "claude-sonnet-4-6" || got.Source != "agy models" || len(got.Models) != 7 { // fixture has 7 groups (Task 12)
		t.Fatalf("agy = %+v %v", got, err)
	}
	cur := &CursorFetcher{Run: run}
	got, err = cur.Fetch(bg)
	if err != nil || got.DefaultModel != "auto" || got.Source != "cursor-agent models" {
		t.Fatalf("cursor = %+v %v", got, err)
	}
	if v, _ := agy.Version(bg); v != "1.2.5" {
		t.Errorf("agy version %q", v)
	}
	if v, _ := cur.Version(bg); v != "2026.09.10" {
		t.Errorf("cursor version %q", v)
	}
	missing := (&execx.Fake{}).Runner()
	if _, err := (&AgyFetcher{Run: missing}).Version(bg); err == nil {
		t.Error("missing binary must fail Version")
	}
	if _, err := (&CursorFetcher{Run: missing}).Fetch(bg); err == nil {
		t.Error("failing command must fail Fetch")
	}
	odd := (&execx.Fake{Responses: map[string]execx.Result{"agy --version": {Out: "unknown\n"}}}).Runner()
	if _, err := (&AgyFetcher{Run: odd}).Version(bg); err == nil {
		t.Error("unparseable version must fail")
	}
	if fs := DefaultFetchers("/Users/x", "x"); len(fs) != 4 || fs[0].Kind() != "claude" || fs[3].Kind() != "cursor" {
		t.Errorf("DefaultFetchers = %v", fs)
	}
}
