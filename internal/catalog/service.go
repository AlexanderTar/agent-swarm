package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

const MaxAge = 24 * time.Hour

// notInstalled is the stored error for an agent whose binary isn't on PATH.
const notInstalled = "not installed"

type AgentCatalogEntry struct {
	Kind             kinds.AgentKind `json:"kind"`
	Installed        bool            `json:"installed"`
	Version          string          `json:"version"`
	AuthOK           bool            `json:"auth_ok"`
	AuthError        string          `json:"auth_error"`
	Superpowers      bool            `json:"superpowers"`
	Models           []CatalogModel  `json:"models"`
	DefaultModel     string          `json:"default_model"`
	CatalogSource    string          `json:"catalog_source"`
	CatalogFetchedAt time.Time       `json:"catalog_fetched_at"`
	CatalogStale     bool            `json:"catalog_stale"`
	CatalogError     string          `json:"catalog_error"`
}

// MarshalJSON emits catalog_fetched_at as integer ms (0 when never fetched) and models as [] rather than null.
func (e AgentCatalogEntry) MarshalJSON() ([]byte, error) {
	type alias AgentCatalogEntry
	a := alias(e)
	if a.Models == nil {
		a.Models = []CatalogModel{}
	}
	return json.Marshal(struct {
		alias
		CatalogFetchedAt int64 `json:"catalog_fetched_at"`
	}{a, db.Millis(e.CatalogFetchedAt)})
}

type Service struct {
	DB       *db.DB
	Events   *events.Store
	Fetchers []Fetcher
	Now      func() time.Time
	After    func(time.Duration) <-chan time.Time // nil means time.After
	Log      func(format string, args ...any)     // nil means no logging
	// Home is $HOME, for the superpowers-installed glob check. Empty in tests
	// that don't care about Superpowers (it just reports false everywhere).
	Home string
	mu   sync.Mutex
}

type row struct {
	version, models, def, source, err string
	fetched, attempted                int64
}

func (s *Service) load(ctx context.Context, kind kinds.AgentKind) (row, bool, error) {
	var r row
	err := s.DB.QueryRowContext(ctx, `SELECT agent_version, models_json, COALESCE(default_model, ''), source,
		COALESCE(error, ''), fetched_at, attempted_at FROM model_catalog WHERE agent_kind = ?`, kind).
		Scan(&r.version, &r.models, &r.def, &r.source, &r.err, &r.fetched, &r.attempted)
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	return r, err == nil, err
}

func (s *Service) save(ctx context.Context, kind kinds.AgentKind, r row) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO model_catalog (agent_kind, agent_version, models_json, default_model,
		source, error, fetched_at, attempted_at) VALUES (?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?)
		ON CONFLICT(agent_kind) DO UPDATE SET agent_version = excluded.agent_version, models_json = excluded.models_json,
		default_model = excluded.default_model, source = excluded.source, error = excluded.error,
		fetched_at = excluded.fetched_at, attempted_at = excluded.attempted_at`,
		kind, r.version, r.models, r.def, r.source, r.err, r.fetched, r.attempted)
	return err
}

func modelsJSON(ms []CatalogModel) string {
	if ms == nil {
		ms = []CatalogModel{}
	}
	b, _ := json.Marshal(ms)
	return string(b)
}

// superpowersInstalled is the same §12.4 usability check as
// internal/install.SuperpowersOK (kept as a small glob-only duplicate here to
// avoid a new install<->catalog package dependency for one boolean check; keep
// the two glob sets in sync if either changes). An empty home (tests that don't
// care) always reports false.
func superpowersInstalled(home string, kind kinds.AgentKind) bool {
	if home == "" {
		return false
	}
	var globs []string
	switch kind {
	case kinds.Claude:
		globs = []string{filepath.Join(home, ".claude", "plugins", "cache", "*", "superpowers*", "*", "skills", "brainstorming", "SKILL.md")}
	case kinds.Codex:
		globs = []string{filepath.Join(home, ".codex", "plugins", "cache", "*", "superpowers", "*", "skills", "brainstorming", "SKILL.md")}
	case kinds.Agy:
		globs = []string{filepath.Join(home, ".gemini", "config", "plugins", "superpowers*", "skills", "brainstorming", "SKILL.md")}
	case kinds.Cursor:
		globs = []string{
			filepath.Join(home, ".cursor", "plugins", "cache", "*", "superpowers", "*", "skills", "brainstorming", "SKILL.md"),
			filepath.Join(home, ".cursor", "plugins", "local", "superpowers", "skills", "brainstorming", "SKILL.md"),
		}
	case kinds.Muse:
		museBase := filepath.Join(home, ".local", "share", "muse", "plugins", "cache", "*", "superpowers", "*")
		globs = []string{
			filepath.Join(museBase, "skills", "brainstorming", "SKILL.md"),
			filepath.Join(museBase, "package", "skills", "brainstorming", "SKILL.md"),
		}
	default:
		return false
	}
	for _, g := range globs {
		if hits, _ := filepath.Glob(g); len(hits) > 0 {
			return true
		}
	}
	return false
}

func (s *Service) Entries(ctx context.Context) ([]AgentCatalogEntry, error) {
	out := []AgentCatalogEntry{}
	for _, f := range s.Fetchers {
		r, found, err := s.load(ctx, f.Kind())
		if err != nil {
			return nil, err
		}
		e := AgentCatalogEntry{Kind: f.Kind(), Models: []CatalogModel{}}
		e.Superpowers = superpowersInstalled(s.Home, f.Kind())
		if found {
			e.Installed, e.Version = r.err != notInstalled, r.version
			e.DefaultModel, e.CatalogSource, e.CatalogError = r.def, r.source, r.err
			if err := json.Unmarshal([]byte(r.models), &e.Models); err != nil {
				e.CatalogError = "cached models are unreadable: " + err.Error()
			}
			if e.Models == nil {
				e.Models = []CatalogModel{}
			}
			e.CatalogFetchedAt = db.FromMillis(r.fetched)
			e.CatalogStale = e.CatalogError != "" || r.fetched == 0 || s.Now().Sub(e.CatalogFetchedAt) >= MaxAge
			// A successful fetch could only have happened with working auth (the claude
			// fetcher needs a real keychain token, codex/agy/cursor need a real CLI
			// session); a stored error other than "not installed" means it wasn't.
			e.AuthOK = e.Installed && r.err == ""
			e.AuthError = r.err
			if !e.Installed {
				e.AuthError = ""
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// Refresh re-fetches agents whose cache needs it (all of them when force) and publishes catalog.changed.
func (s *Service) Refresh(ctx context.Context, force bool) ([]AgentCatalogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.Fetchers {
		if err := s.refreshOne(ctx, f, force); err != nil {
			return nil, err
		}
	}
	entries, err := s.Entries(ctx)
	if err != nil {
		return nil, err
	}
	_, err = s.Events.Publish(ctx, events.CatalogChanged, entries)
	return entries, err
}

func (s *Service) refreshOne(ctx context.Context, f Fetcher, force bool) error {
	now := db.Millis(s.Now())
	old, found, err := s.load(ctx, f.Kind())
	if err != nil {
		return err
	}
	if !found {
		old = row{models: "[]"}
	}
	version, verr := f.Version(ctx)
	if errors.Is(verr, exec.ErrNotFound) {
		old.version, old.err, old.attempted = "", notInstalled, now
		return s.save(ctx, f.Kind(), old)
	}
	if verr != nil { // installed but --version failed: keep the cache, retry next refresh
		old.err, old.attempted = verr.Error(), now
		return s.save(ctx, f.Kind(), old)
	}
	fresh := found && old.version == version && old.err == "" && now-old.fetched < MaxAge.Milliseconds()
	if fresh && !force {
		return nil
	}
	got, ferr := f.Fetch(ctx)
	if ferr == nil {
		return s.save(ctx, f.Kind(), row{version: version, models: modelsJSON(got.Models), def: got.DefaultModel,
			source: got.Source, fetched: now, attempted: now})
	}
	next := old
	next.version, next.err, next.attempted = version, ferr.Error(), now
	if next.models == "[]" && f.Kind() == kinds.Claude {
		next.models, next.source = modelsJSON(ClaudeAliasFallback()), "aliases"
	}
	return s.save(ctx, f.Kind(), next)
}

func (s *Service) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

// ModelsFor returns the cached models and default for kind (nil when never fetched).
func (s *Service) ModelsFor(ctx context.Context, kind kinds.AgentKind) ([]CatalogModel, string, error) {
	r, found, err := s.load(ctx, kind)
	if err != nil || !found {
		return nil, "", err
	}
	var ms []CatalogModel
	if err := json.Unmarshal([]byte(r.models), &ms); err != nil {
		s.logf("catalog: cached %s models are unreadable: %v", kind, err)
		return nil, "", nil
	}
	return ms, r.def, nil
}

// Installed lists agents whose binary was found at the last refresh, in fetcher order.
func (s *Service) Installed(ctx context.Context) []kinds.AgentKind {
	var out []kinds.AgentKind
	for _, f := range s.Fetchers {
		if r, found, _ := s.load(ctx, f.Kind()); found && r.err != notInstalled {
			out = append(out, f.Kind())
		}
	}
	return out
}

// Loop refreshes at start and then hourly (refreshOne skips fresh caches).
func (s *Service) Loop(ctx context.Context) {
	after := s.After
	if after == nil {
		after = time.After
	}
	for {
		if _, err := s.Refresh(ctx, false); err != nil && ctx.Err() == nil {
			s.logf("catalog refresh: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-after(time.Hour):
		}
	}
}
