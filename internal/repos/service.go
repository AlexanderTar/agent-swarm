package repos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

var ErrNotRepo = errors.New("No git repository found in this folder.")

type Repo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Path          string   `json:"path"`
	RemoteURL     string   `json:"remote_url"`
	RemoteOwner   string   `json:"remote_owner"`
	DefaultBranch string   `json:"default_branch"`
	Source        string   `json:"source"`
	Groups        []string `json:"groups"`
	Missing       bool     `json:"missing"`
	Dirty         bool     `json:"dirty"`
	LastUsedAt    int64    `json:"last_used_at,omitempty"`
}

type GroupView struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Repos  []Repo `json:"repos"`
}

type ScanStats struct {
	Found   int `json:"found"`
	Missing int `json:"missing"`
}

type Service struct {
	DB           *db.DB
	Events       *events.Store
	Run          execx.Runner
	Home         string
	Excludes     func(context.Context) []string
	Now          func() time.Time
	After        func(time.Duration) <-chan time.Time // nil means time.After
	DirtyTimeout time.Duration                        // 0 means 2 s
	Stat         func(string) (os.FileInfo, error)    // nil means os.Stat (missing detection)
	Log          func(format string, args ...any)     // nil means no logging
	Ctx          context.Context                      // bounds shared scans (daemon lifetime); nil means Background
	Joined       func()                               // test hook: called when Scan joins an already-running scan

	mu        sync.Mutex
	cur       *scanCall
	scannedAt time.Time
	trigger   chan struct{}
}

type scanCall struct {
	done  chan struct{}
	stats ScanStats
	err   error
}

// Scan walks the home folder. Scans are shared and run under s.Ctx, so a caller's
// ctx only bounds its own wait: the scan finishes and publishes for everyone else.
func (s *Service) Scan(ctx context.Context) (ScanStats, error) {
	s.mu.Lock()
	c := s.cur
	joining := c != nil
	if c == nil {
		c = &scanCall{done: make(chan struct{})}
		s.cur = c
		go s.runScan(c)
	}
	s.mu.Unlock()
	if joining && s.Joined != nil {
		s.Joined()
	}
	select {
	case <-c.done:
		return c.stats, c.err
	case <-ctx.Done():
		return ScanStats{}, ctx.Err()
	}
}

func (s *Service) runScan(c *scanCall) {
	ctx := s.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	defer close(c.done)
	if _, err := s.Events.Publish(ctx, events.ReposChanged, map[string]any{"scanning": true}); err != nil && ctx.Err() == nil {
		s.logf("repos: publish repos.changed failed: %v", err)
	}
	c.stats, c.err = s.scan(ctx) // a cancelled scan stores nothing
	s.mu.Lock()
	s.cur = nil
	if c.err == nil {
		s.scannedAt = s.Now()
	}
	s.mu.Unlock()
	if c.err == nil { // stored results are published even after a late cancel
		if _, err := s.Events.Publish(context.WithoutCancel(ctx), events.ReposChanged,
			map[string]any{"scanning": false, "found": c.stats.Found}); err != nil {
			s.logf("repos: publish repos.changed failed: %v", err)
		}
	}
}

func (s *Service) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func (s *Service) Scanning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil
}

func (s *Service) ScannedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scannedAt
}

func (s *Service) scan(ctx context.Context) (ScanStats, error) {
	var excludes []string
	if s.Excludes != nil {
		excludes = s.Excludes(ctx)
	}
	w, err := walk(ctx, s.Home, excludes)
	if err != nil {
		return ScanStats{}, err
	}
	infos := map[string]GitInfo{}
	owners := map[string]string{}
	for _, p := range w.Repos {
		if err := ctx.Err(); err != nil {
			return ScanStats{}, err
		}
		infos[p] = ReadGitInfo(ctx, s.Run, p)
		owners[p] = infos[p].RemoteOwner
	}
	if err := ctx.Err(); err != nil { // git calls may have failed from the cancel
		return ScanStats{}, err
	}
	groups := Groups(w, owners)
	stats := ScanStats{Found: len(w.Repos)}
	// Missing detection stats known repos before the transaction: one path on an
	// unmounted mount can block for seconds, and the single SQLite writer must
	// not be held for that long (L12: short transactions).
	known, err := s.knownRepos(ctx)
	if err != nil {
		return ScanStats{}, err
	}
	stat := s.Stat
	if stat == nil {
		stat = os.Stat
	}
	gone := map[string]bool{}
	for _, r := range known {
		if _, err := stat(r.path); err != nil {
			gone[r.id] = true
			stats.Missing++
		}
	}
	now := db.Millis(s.Now())
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error { // a cancel mid-transaction rolls it back
		for _, p := range w.Repos {
			info := infos[p]
			_, err := tx.ExecContext(ctx, `INSERT INTO repos (id, path, name, remote_url, remote_owner, default_branch,
				source, created_at, updated_at) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), 'scan', ?, ?)
				ON CONFLICT(path) DO UPDATE SET name = excluded.name, remote_url = excluded.remote_url,
				remote_owner = excluded.remote_owner, default_branch = excluded.default_branch,
				missing = 0, updated_at = excluded.updated_at`,
				ids.New("repo"), p, filepath.Base(p), info.RemoteURL, info.RemoteOwner, info.DefaultBranch, now, now)
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM repo_groups`); err != nil {
			return err
		}
		for _, g := range groups {
			for _, p := range g.Repos {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO repo_groups (repo_id, name, source, origin)
					SELECT id, ?, ?, NULLIF(?, '') FROM repos WHERE path = ?`, g.Name, g.Source, g.Origin, p); err != nil {
					return err
				}
			}
		}
		for _, r := range known {
			if gone[r.id] != r.missing {
				if _, err := tx.ExecContext(ctx, `UPDATE repos SET missing = ?, updated_at = ? WHERE id = ?`, gone[r.id], now, r.id); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return stats, err
}

type repoRow struct {
	id, path string
	missing  bool
}

func (s *Service) knownRepos(ctx context.Context) ([]repoRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, path, missing FROM repos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repoRow
	for rows.Next() {
		var r repoRow
		if err := rows.Scan(&r.id, &r.path, &r.missing); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) triggerChan() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.trigger == nil {
		s.trigger = make(chan struct{}, 1)
	}
	return s.trigger
}

// Trigger asks Loop for an early scan (used when scan_excludes change).
func (s *Service) Trigger() {
	select {
	case s.triggerChan() <- struct{}{}:
	default:
	}
}

// Loop scans now, then every interval(ctx), and whenever Trigger is called.
func (s *Service) Loop(ctx context.Context, interval func(context.Context) time.Duration) {
	after := s.After
	if after == nil {
		after = time.After
	}
	trig := s.triggerChan()
	for {
		if _, err := s.Scan(ctx); err != nil && ctx.Err() == nil {
			s.logf("repos: scheduled scan failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-after(interval(ctx)):
		case <-trig:
		}
	}
}

// AddManual registers any folder with a .git directory, hidden or excluded.
func (s *Service) AddManual(ctx context.Context, path string) (Repo, error) {
	abs, err := filepath.Abs(path)
	if err != nil || !IsRepo(abs) {
		return Repo{}, ErrNotRepo
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Repo{}, ErrNotRepo
	}
	info := ReadGitInfo(ctx, s.Run, real)
	now := db.Millis(s.Now())
	_, err = s.DB.ExecContext(ctx, `INSERT INTO repos (id, path, name, remote_url, remote_owner, default_branch,
		source, created_at, updated_at) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), 'manual', ?, ?)
		ON CONFLICT(path) DO UPDATE SET missing = 0, updated_at = excluded.updated_at`,
		ids.New("repo"), real, filepath.Base(real), info.RemoteURL, info.RemoteOwner, info.DefaultBranch, now, now)
	if err != nil {
		return Repo{}, err
	}
	if _, err := s.Events.Publish(ctx, events.ReposChanged, map[string]any{"scanning": s.Scanning()}); err != nil {
		return Repo{}, fmt.Errorf("publish repos.changed: %w", err)
	}
	list, err := s.query(ctx, `WHERE r.path = ?`, real)
	if err != nil || len(list) == 0 {
		return Repo{}, err
	}
	return list[0], nil
}

func (s *Service) MarkUsed(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE repos SET last_used_at = ? WHERE id = ?`, db.Millis(s.Now()), id)
	return err
}

func (s *Service) query(ctx context.Context, where string, args ...any) ([]Repo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id, r.name, r.path, COALESCE(r.remote_url, ''), COALESCE(r.remote_owner, ''),
		COALESCE(r.default_branch, ''), r.source, r.missing, COALESCE(r.last_used_at, 0),
		COALESCE((SELECT group_concat(name, char(31)) FROM (SELECT DISTINCT name FROM repo_groups g WHERE g.repo_id = r.id ORDER BY name)), '')
		FROM repos r `+where+` ORDER BY lower(r.name), r.path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repo{}
	for rows.Next() {
		var r Repo
		var groups string
		if err := rows.Scan(&r.ID, &r.Name, &r.Path, &r.RemoteURL, &r.RemoteOwner, &r.DefaultBranch,
			&r.Source, &r.Missing, &r.LastUsedAt, &groups); err != nil {
			return nil, err
		}
		r.Groups = []string{}
		if groups != "" {
			r.Groups = strings.Split(groups, "\x1f")
			sort.Strings(r.Groups)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) All(ctx context.Context) ([]Repo, error) { return s.query(ctx, "") }

// Recent returns up to limit repos, most recently used first; limit <= 0 returns none.
func (s *Service) Recent(ctx context.Context, limit int) ([]Repo, error) {
	if limit <= 0 {
		return []Repo{}, nil
	}
	list, err := s.query(ctx, `WHERE r.last_used_at IS NOT NULL`)
	sort.SliceStable(list, func(i, j int) bool { return list[i].LastUsedAt > list[j].LastUsedAt })
	if len(list) > limit {
		list = list[:limit]
	}
	return list, err
}

// GroupViews merges same-named groups across sources; folder-based groups come first.
func (s *Service) GroupViews(ctx context.Context) ([]GroupView, error) {
	all, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]Repo{}
	for _, r := range all {
		byID[r.ID] = r
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT name, source, repo_id FROM repo_groups`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := map[string]*GroupView{}
	members := map[string]map[string]bool{}
	for rows.Next() {
		var name, source, id string
		if err := rows.Scan(&name, &source, &id); err != nil {
			return nil, err
		}
		v, ok := views[name]
		if !ok {
			v = &GroupView{Name: name, Source: source}
			views[name] = v
			members[name] = map[string]bool{}
		}
		if sourceOrder[source] < sourceOrder[v.Source] {
			v.Source = source
		}
		members[name][id] = true
	}
	out := []GroupView{}
	for name, v := range views {
		for _, r := range all { // keeps name order
			if members[name][r.ID] {
				v.Repos = append(v.Repos, r)
			}
		}
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		fi, fj := out[i].Source != "remote_owner", out[j].Source != "remote_owner"
		if fi != fj {
			return fi
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, rows.Err()
}

// Search matches name, path, remote and groups (case-insensitive). limit <= 0 means 30.
func (s *Service) Search(ctx context.Context, q string, limit int) ([]Repo, error) {
	if limit <= 0 {
		limit = 30
	}
	all, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	type ranked struct {
		r    Repo
		rank int
	}
	var hits []ranked
	for _, r := range all {
		rank := -1
		switch {
		case q == "" || strings.Contains(strings.ToLower(r.Name), q):
			rank = 1
		case strings.Contains(strings.ToLower(r.Path), q):
			rank = 2
		case strings.Contains(strings.ToLower(r.RemoteURL), q) || strings.Contains(strings.ToLower(strings.Join(r.Groups, "\n")), q):
			rank = 3
		}
		if rank < 0 {
			continue
		}
		if r.LastUsedAt > 0 {
			rank = 0
		}
		hits = append(hits, ranked{r, rank})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].r.LastUsedAt > hits[j].r.LastUsedAt
	})
	out := []Repo{}
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		out = append(out, h.r)
	}
	s.fillDirty(ctx, out)
	return out, nil
}

func (s *Service) fillDirty(ctx context.Context, list []Repo) {
	timeout := s.DirtyTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	var wg sync.WaitGroup
	for i := range list[:min(len(list), 30)] {
		if list[i].Missing {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			list[i].Dirty = Dirty(c, s.Run, list[i].Path)
		}()
	}
	wg.Wait()
}
