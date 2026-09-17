package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// repoWire is Repo on the wire (contracts §3.6): unknown git facts and last_used_at are null.
type repoWire struct {
	repos.Repo
	RemoteURL     *string  `json:"remote_url"`
	RemoteOwner   *string  `json:"remote_owner"`
	DefaultBranch *string  `json:"default_branch"`
	Groups        []string `json:"groups"`
	LastUsedAt    *int64   `json:"last_used_at"`
}

type groupWire struct {
	Name   string     `json:"name"`
	Source string     `json:"source"`
	Repos  []repoWire `json:"repos"`
}

func repoOut(r repos.Repo) repoWire {
	w := repoWire{Repo: r, RemoteURL: orNull(r.RemoteURL), RemoteOwner: orNull(r.RemoteOwner),
		DefaultBranch: orNull(r.DefaultBranch), Groups: orEmpty(r.Groups)}
	if r.LastUsedAt > 0 {
		w.LastUsedAt = &r.LastUsedAt
	}
	return w
}

func reposOut(list []repos.Repo) []repoWire {
	out := make([]repoWire, 0, len(list))
	for _, r := range list {
		out = append(out, repoOut(r))
	}
	return out
}

func (s *Server) configRoutes() []route {
	return []route{
		{"GET", "/api/settings", authDaemon, s.getSettings},
		{"PUT", "/api/settings", authDaemon, s.putSettings},
		{"GET", "/api/catalog", authDaemon, s.getCatalog},
		{"POST", "/api/catalog/refresh", authDaemon, s.refreshCatalog},
		{"GET", "/api/repos", authDaemon, s.getRepos},
		{"POST", "/api/repos", authDaemon, s.addRepo},
		{"POST", "/api/repos/rescan", authDaemon, s.rescan},
		{"GET", "/api/kb/search", authDaemon, s.kbSearch},
		{"GET", "/api/kb/status", authDaemon, s.kbStatus},
		{"GET", "/api/kb/{slug...}", authDaemon, s.kbGet},
	}
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	cur, err := s.Settings.Get(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var next settings.Settings
	if err := readJSON(r, &next); err != nil {
		s.writeErr(w, err)
		return
	}
	prev, err := s.Settings.Get(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	saved, err := s.Settings.Put(r.Context(), next)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if !slices.Equal(prev.ScanExcludes, saved.ScanExcludes) {
		s.Repos.Trigger()
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) getCatalog(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Catalog.Entries(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) refreshCatalog(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Catalog.Refresh(r.Context(), true)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) getRepos(w http.ResponseWriter, r *http.Request) {
	ctx, q := r.Context(), r.URL.Query().Get("q")
	matches, err := s.Repos.Search(ctx, q, 100_000)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	byID := map[string]repos.Repo{}
	for _, m := range matches {
		byID[m.ID] = m // carries dirty
	}
	recent, err := s.Repos.Recent(ctx, 10)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	recentOut := []repoWire{}
	for _, rp := range recent {
		if m, ok := byID[rp.ID]; ok {
			recentOut = append(recentOut, repoOut(m))
		}
	}
	groups, err := s.Repos.GroupViews(ctx)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	groupsOut := []groupWire{}
	for _, g := range groups {
		var kept []repos.Repo
		for _, rp := range g.Repos {
			if m, ok := byID[rp.ID]; ok {
				kept = append(kept, m)
			}
		}
		if len(kept) > 0 {
			groupsOut = append(groupsOut, groupWire{Name: g.Name, Source: g.Source, Repos: reposOut(kept)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recent": recentOut, "groups": groupsOut, "all": reposOut(matches),
		"scanned_at": db.Millis(s.Repos.ScannedAt()), "scanning": s.Repos.Scanning(),
	})
}

func (s *Server) addRepo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	if body.Path == "" {
		s.writeErr(w, repos.ErrNotRepo)
		return
	}
	path := body.Path
	if path == "~" || strings.HasPrefix(path, "~/") {
		path = repos.ExpandHome(path, s.Repos.Home)
	}
	rp, err := s.Repos.AddManual(r.Context(), path)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, repoOut(rp))
}

func (s *Server) rescan(w http.ResponseWriter, r *http.Request) {
	stats, err := s.Repos.Scan(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) kbSearch(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil {
			s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "limit must be a number."))
			return
		}
		limit = min(max(n, 1), 50)
	}
	hits, err := s.KB.Search(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

func (s *Server) kbStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.KB.Status(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) kbGet(w http.ResponseWriter, r *http.Request) {
	doc, err := s.KB.Get(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}
