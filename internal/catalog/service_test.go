package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

type fakeFetcher struct {
	kind    runtime.AgentKind
	version string
	verErr  error
	models  []CatalogModel
	err     error
	fetches int
}

func (f *fakeFetcher) Kind() runtime.AgentKind { return f.kind }
func (f *fakeFetcher) Version(context.Context) (string, error) {
	return f.version, f.verErr
}
func (f *fakeFetcher) Fetch(context.Context) (Fetched, error) {
	f.fetches++
	if f.err != nil {
		return Fetched{}, f.err
	}
	return Fetched{Models: f.models, DefaultModel: f.models[0].ID, Source: string(f.kind) + " source"}, nil
}

type clk struct{ t time.Time }

func (c *clk) now() time.Time { return c.t }

func newCatalog(t *testing.T, fs ...Fetcher) (*Service, *clk) {
	d := dbtest.Open(t)
	c := &clk{t: time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)}
	return &Service{DB: d, Events: events.New(d, c.now), Fetchers: fs, Now: c.now}, c
}

var m1 = []CatalogModel{{ID: "m1", Label: "M1", Efforts: []string{"low", "high"}, EffortEncoding: "flag"}}
var m2 = []CatalogModel{{ID: "m2", Label: "M2", Efforts: []string{}, EffortEncoding: "flag"}}

func TestRefreshRules(t *testing.T) {
	codex := &fakeFetcher{kind: runtime.Codex, version: "0.154.0", models: m1}
	s, c := newCatalog(t, codex)
	entries, err := s.Refresh(bg, false)
	if err != nil || codex.fetches != 1 {
		t.Fatalf("first refresh: %v, fetches %d", err, codex.fetches)
	}
	e := entries[0]
	if e.Kind != runtime.Codex || !e.Installed || e.Version != "0.154.0" || e.DefaultModel != "m1" ||
		e.CatalogSource != "codex source" || e.CatalogStale || e.CatalogError != "" || !e.CatalogFetchedAt.Equal(c.t) ||
		len(e.Models) != 1 || e.AuthOK || e.Superpowers {
		t.Fatalf("entry = %+v", e)
	}

	c.t = c.t.Add(23 * time.Hour)
	s.Refresh(bg, false)
	if codex.fetches != 1 {
		t.Fatal("fresh cache must not refetch")
	}
	codex.version = "0.155.0"
	s.Refresh(bg, false)
	if codex.fetches != 2 {
		t.Fatal("a version change must refetch")
	}
	c.t = c.t.Add(25 * time.Hour)
	s.Refresh(bg, false)
	if codex.fetches != 3 {
		t.Fatal("a 24 h old cache must refetch")
	}
	s.Refresh(bg, true)
	if codex.fetches != 4 {
		t.Fatal("force must refetch")
	}

	fetchedAt := c.t
	c.t = c.t.Add(time.Hour)
	codex.err = errors.New("chatgpt authentication required")
	entries, _ = s.Refresh(bg, true)
	e = entries[0]
	if !e.CatalogStale || e.CatalogError != "chatgpt authentication required" || len(e.Models) != 1 ||
		!e.CatalogFetchedAt.Equal(fetchedAt) || e.DefaultModel != "m1" {
		t.Fatalf("failed refresh = %+v", e)
	}
	codex.err = nil
	s.Refresh(bg, false) // a failed last attempt is retried without force
	if codex.fetches != 6 {
		t.Fatalf("fetches = %d", codex.fetches)
	}
	if entries, _ := s.Entries(bg); entries[0].CatalogStale {
		t.Fatal("successful retry clears stale")
	}
	c.t = c.t.Add(25 * time.Hour)
	if entries, _ := s.Entries(bg); !entries[0].CatalogStale {
		t.Fatal("an old cache is stale")
	}
	evs, _ := s.Events.After(bg, 0, 100)
	if len(evs) == 0 || evs[len(evs)-1].Type != events.CatalogChanged {
		t.Fatalf("events = %+v", evs)
	}
	var payload []map[string]any
	if err := json.Unmarshal(evs[len(evs)-1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if at, ok := payload[0]["catalog_fetched_at"].(float64); !ok || int64(at) != fetchedAt.Add(time.Hour).UnixMilli() {
		t.Fatalf("event catalog_fetched_at = %v", payload[0]["catalog_fetched_at"])
	}
}

func TestEntryJSON(t *testing.T) {
	b, err := json.Marshal(AgentCatalogEntry{Kind: runtime.Claude, CatalogFetchedAt: time.UnixMilli(1234).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"catalog_fetched_at":1234`, `"models":[]`, `"auth_error":""`, `"catalog_error":""`, `"kind":"claude"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s missing %s", b, want)
		}
	}
	var keys map[string]json.RawMessage
	json.Unmarshal(b, &keys)
	if len(keys) != 12 {
		t.Errorf("fields = %d: %s", len(keys), b)
	}
	b, _ = json.Marshal([]AgentCatalogEntry{{}})
	if !strings.Contains(string(b), `"catalog_fetched_at":0`) {
		t.Errorf("zero time = %s", b)
	}
}

func TestFallbacksAndNotInstalled(t *testing.T) {
	claude := &fakeFetcher{kind: runtime.Claude, version: "2.1.274", err: errors.New("api.anthropic.com returned 401")}
	agy := &fakeFetcher{kind: runtime.Agy, version: "1.2.5", err: errors.New("no models in output")}
	cursor := &fakeFetcher{kind: runtime.Cursor, verErr: fmt.Errorf("cursor-agent: %w", exec.ErrNotFound)}
	s, _ := newCatalog(t, claude, agy, cursor)
	entries, _ := s.Refresh(bg, false)
	if len(entries) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	if e := entries[0]; !e.Installed || !e.CatalogStale || e.CatalogSource != "aliases" || len(e.Models) != 4 ||
		e.CatalogError != "api.anthropic.com returned 401" || !e.CatalogFetchedAt.IsZero() {
		t.Errorf("claude = %+v", e)
	}
	if e := entries[1]; !e.Installed || !e.CatalogStale || len(e.Models) != 0 || e.Models == nil {
		t.Errorf("agy = %+v", e)
	}
	if e := entries[2]; e.Installed || e.Version != "" || cursor.fetches != 0 {
		t.Errorf("cursor = %+v", e)
	}
	if got := s.Installed(bg); !slices.Equal(got, []runtime.AgentKind{runtime.Claude, runtime.Agy}) {
		t.Errorf("Installed = %v", got)
	}
	ms, def, err := s.ModelsFor(bg, runtime.Claude)
	if err != nil || len(ms) != 4 || def != "" {
		t.Errorf("ModelsFor(claude) = %v %q %v", ms, def, err)
	}
	if ms, _, err := s.ModelsFor(bg, runtime.Codex); err != nil || ms != nil {
		t.Errorf("unknown kind = %v %v", ms, err)
	}
	fresh := &fakeFetcher{kind: runtime.Codex, version: "1", models: m2}
	s2, _ := newCatalog(t, fresh)
	if entries, _ := s2.Entries(bg); entries[0].Installed || entries[0].Models == nil {
		t.Errorf("never refreshed = %+v", entries[0])
	}
}

func TestTokenIsNeverPersisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad token "+r.Header.Get("Authorization"), http.StatusUnauthorized)
	}))
	defer srv.Close()
	claude := &ClaudeFetcher{Run: keychain(nil).Runner(), HTTP: srv.Client(), BaseURL: srv.URL, User: "alex"}
	s, _ := newCatalog(t, claude)
	s.Refresh(bg, true)
	rows, _ := s.DB.Query(`SELECT agent_kind || agent_version || models_json || COALESCE(default_model, '') || source || COALESCE(error, '') FROM model_catalog
		UNION ALL SELECT payload_json FROM events`)
	defer rows.Close()
	for rows.Next() {
		var v string
		rows.Scan(&v)
		if strings.Contains(v, secret) || strings.Contains(v, "Bearer") {
			t.Fatalf("token persisted: %s", v)
		}
	}
}

func TestLoopRefreshesHourly(t *testing.T) {
	codex := &fakeFetcher{kind: runtime.Codex, version: "1", models: m1}
	s, _ := newCatalog(t, codex)
	ticks := make(chan time.Time)
	waiting := make(chan struct{})
	s.After = func(d time.Duration) <-chan time.Time {
		if d != time.Hour {
			t.Errorf("interval = %v", d)
		}
		waiting <- struct{}{} // hand control to the test before the loop waits
		return ticks
	}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { s.Loop(ctx); close(done) }()
	<-waiting // first refresh finished
	codex.version = "2"
	ticks <- time.Now()
	<-waiting // second refresh (version change) finished
	cancel()
	<-done
	if codex.fetches != 2 {
		t.Fatalf("fetches = %d", codex.fetches)
	}
}

func TestStaleAtExactlyMaxAge(t *testing.T) {
	codex := &fakeFetcher{kind: runtime.Codex, version: "1", models: m1}
	s, c := newCatalog(t, codex)
	s.Refresh(bg, false)
	c.t = c.t.Add(MaxAge)
	if entries, _ := s.Entries(bg); !entries[0].CatalogStale {
		t.Fatal("a cache exactly MaxAge old is stale (refresh refetches it)")
	}
	s.Refresh(bg, false)
	if codex.fetches != 2 {
		t.Fatalf("fetches = %d", codex.fetches)
	}
}

func TestVersionFailureOtherThanMissingKeepsInstalled(t *testing.T) {
	codex := &fakeFetcher{kind: runtime.Codex, version: "1", models: m1}
	s, _ := newCatalog(t, codex)
	s.Refresh(bg, false)
	codex.verErr = errors.New("codex: signal: killed")
	entries, err := s.Refresh(bg, false)
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if !e.Installed || e.Version != "1" || e.CatalogError != "codex: signal: killed" || !e.CatalogStale ||
		len(e.Models) != 1 || codex.fetches != 1 {
		t.Fatalf("entry = %+v, fetches %d", e, codex.fetches)
	}
	if got := s.Installed(bg); !slices.Equal(got, []runtime.AgentKind{runtime.Codex}) {
		t.Fatalf("Installed = %v", got)
	}
	// never seen before and --version fails for a reason other than a missing binary
	agy := &fakeFetcher{kind: runtime.Agy, verErr: errors.New("agy: exit status 2: boom")}
	s2, _ := newCatalog(t, agy)
	entries, _ = s2.Refresh(bg, false)
	if e := entries[0]; !e.Installed || e.CatalogError != "agy: exit status 2: boom" || agy.fetches != 0 {
		t.Fatalf("agy = %+v", e)
	}
	codex.verErr = fmt.Errorf("codex: %w", exec.ErrNotFound)
	entries, _ = s.Refresh(bg, false)
	if e := entries[0]; e.Installed || e.CatalogError != "not installed" {
		t.Fatalf("missing binary = %+v", e)
	}
}

func TestCorruptModelsJSON(t *testing.T) {
	codex := &fakeFetcher{kind: runtime.Codex, version: "1", models: m1}
	s, _ := newCatalog(t, codex)
	var logged []string
	s.Log = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	s.Refresh(bg, false)
	if _, err := s.DB.Exec(`UPDATE model_catalog SET models_json = '{oops'`); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Entries(bg)
	if err != nil {
		t.Fatal(err)
	}
	if e := entries[0]; !strings.HasPrefix(e.CatalogError, "cached models are unreadable") || !e.CatalogStale ||
		e.Models == nil || len(e.Models) != 0 {
		t.Fatalf("entry = %+v", e)
	}
	ms, def, err := s.ModelsFor(bg, runtime.Codex)
	if ms != nil || def != "" || err != nil || len(logged) != 1 || !strings.Contains(logged[0], "codex") {
		t.Fatalf("ModelsFor = %v %q %v, logged %q", ms, def, err, logged)
	}
}

func TestLoopLogsRefreshErrors(t *testing.T) {
	codex := &fakeFetcher{kind: runtime.Codex, version: "1", models: m1}
	s, _ := newCatalog(t, codex)
	logged := make(chan string, 4)
	s.Log = func(format string, args ...any) { logged <- fmt.Sprintf(format, args...) }
	waiting := make(chan struct{})
	s.After = func(time.Duration) <-chan time.Time {
		waiting <- struct{}{}
		return make(chan time.Time)
	}
	s.DB.Close()
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { s.Loop(ctx); close(done) }()
	<-waiting
	cancel()
	<-done
	select {
	case msg := <-logged:
		if !strings.Contains(msg, "catalog refresh") {
			t.Fatalf("log = %q", msg)
		}
	default:
		t.Fatal("refresh error was not logged")
	}
}
