package repos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
)

type fakeGit struct {
	owners map[string]string // repo base name → owner
	remote map[string]string // repo base name → full remote url (overrides owners)
	dirty  map[string]bool
	hang   map[string]bool
	calls  atomic.Int64
	gate   chan struct{} // when set, the first call waits for it
	start  chan struct{}
	once   sync.Once
}

func (g *fakeGit) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	g.calls.Add(1)
	if g.gate != nil {
		g.once.Do(func() { close(g.start); <-g.gate })
	}
	base := filepath.Base(args[1])
	switch strings.Join(args[2:], " ") {
	case "config --get remote.origin.url":
		if u, ok := g.remote[base]; ok {
			return []byte(u + "\n"), nil
		}
		if o, ok := g.owners[base]; ok {
			return []byte("git@github.com:" + o + "/" + base + ".git\n"), nil
		}
	case "symbolic-ref --quiet refs/remotes/origin/HEAD":
		return []byte("refs/remotes/origin/main\n"), nil
	case "status --porcelain":
		if g.hang[base] {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if g.dirty[base] {
			return []byte(" M file.go\n"), nil
		}
		return nil, nil
	}
	return nil, errors.New("exit status 1")
}

var bgc = context.Background()

func newService(t *testing.T, home string, g *fakeGit) *Service {
	d := dbtest.Open(t)
	now := func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }
	return &Service{DB: d, Events: events.New(d, now), Run: g.run, Home: home, Now: now,
		Excludes: func(context.Context) []string { return nil }, DirtyTimeout: 50 * time.Millisecond}
}

func mkRepo(t *testing.T, home, rel string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, rel, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, rel)
}

func realTemp(t *testing.T) string {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func names(rs []Repo) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

func searchHome(t *testing.T) (*Service, string) {
	home := realTemp(t)
	for _, r := range []string{"GitHub/endurio-chat", "GitHub/endurio-app", "GitHub/agent-swarm", "chat-archive/old", "misc/tool"} {
		mkRepo(t, home, r)
	}
	os.MkdirAll(filepath.Join(home, "Workspaces/endurio"), 0o755)
	os.Symlink(filepath.Join(home, "GitHub/endurio-chat"), filepath.Join(home, "Workspaces/endurio/chat"))
	os.Symlink(filepath.Join(home, "GitHub/endurio-app"), filepath.Join(home, "Workspaces/endurio/app"))
	os.WriteFile(filepath.Join(home, "endurio.code-workspace"), []byte(`{"folders":[{"path":"GitHub/endurio-chat"}]}`), 0o644)
	g := &fakeGit{
		owners: map[string]string{"endurio-chat": "EndurioApp", "endurio-app": "EndurioApp", "agent-swarm": "AlexanderTar"},
		remote: map[string]string{"tool": "https://github.com/ChatOps/tool.git"},
		dirty:  map[string]bool{"endurio-chat": true},
		hang:   map[string]bool{"agent-swarm": true},
	}
	s := newService(t, home, g)
	if _, err := s.Scan(bgc); err != nil {
		t.Fatal(err)
	}
	return s, home
}

func TestScanStoresReposAndGroups(t *testing.T) {
	s, home := searchHome(t)
	all, err := s.All(bgc)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names(all), []string{"agent-swarm", "endurio-app", "endurio-chat", "old", "tool"}) {
		t.Fatalf("all = %v", names(all))
	}
	chat := all[2]
	if chat.Path != filepath.Join(home, "GitHub/endurio-chat") || chat.RemoteOwner != "EndurioApp" ||
		chat.DefaultBranch != "main" || chat.Source != "scan" || !strings.HasPrefix(chat.ID, "repo_") ||
		!slices.Equal(chat.Groups, []string{"EndurioApp", "endurio"}) {
		t.Fatalf("chat = %+v", chat)
	}
	groups, err := s.GroupViews(bgc)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Name != "endurio" || groups[0].Source != "workspace_dir" ||
		!slices.Equal(names(groups[0].Repos), []string{"endurio-app", "endurio-chat"}) ||
		groups[1].Name != "EndurioApp" || groups[1].Source != "remote_owner" {
		t.Fatalf("groups = %+v", groups)
	}
	if s.ScannedAt().IsZero() || s.Scanning() {
		t.Fatal("scan state not reported")
	}
	evs, _ := s.Events.After(bgc, 0, 10)
	if len(evs) != 1 || evs[0].Type != events.ReposChanged {
		t.Fatalf("events = %+v", evs)
	}
}

func TestSearchRanksAndChecksDirty(t *testing.T) {
	s, _ := searchHome(t)
	all, _ := s.All(bgc)
	if err := s.MarkUsed(bgc, all[1].ID); err != nil { // endurio-app
		t.Fatal(err)
	}
	cases := map[string][]string{
		"chat":       {"endurio-chat", "old", "tool"},
		"ENDURIO":    {"endurio-app", "endurio-chat"},
		"endurioapp": {"endurio-app", "endurio-chat"},
		"nothing":    nil,
	}
	for q, want := range cases {
		got, err := s.Search(bgc, q, 30)
		if err != nil || !slices.Equal(names(got), want) {
			t.Errorf("Search(%q) = %v, %v", q, names(got), err)
		}
	}
	got, _ := s.Search(bgc, "", 2)
	if !slices.Equal(names(got), []string{"endurio-app", "agent-swarm"}) {
		t.Fatalf("empty query = %v", names(got))
	}
	got, _ = s.Search(bgc, "chat", 30)
	if !got[0].Dirty || got[1].Dirty {
		t.Fatalf("dirty flags = %+v", got)
	}
	start := time.Now()
	got, _ = s.Search(bgc, "agent", 30)
	if len(got) != 1 || got[0].Dirty || time.Since(start) > time.Second {
		t.Fatalf("hung git must time out: %+v in %v", got, time.Since(start))
	}
	recent, _ := s.Recent(bgc, 5)
	if !slices.Equal(names(recent), []string{"endurio-app"}) || recent[0].LastUsedAt == 0 {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestMissingAndManualRepos(t *testing.T) {
	home := realTemp(t)
	gone := mkRepo(t, home, "GitHub/gone")
	mkRepo(t, home, "GitHub/stays")
	hidden := mkRepo(t, home, ".tools/secret")
	s := newService(t, home, &fakeGit{})
	s.Scan(bgc)

	r, err := s.AddManual(bgc, hidden)
	if err != nil || r.Source != "manual" || r.Name != "secret" {
		t.Fatalf("AddManual = %+v, %v", r, err)
	}
	if _, err := s.AddManual(bgc, filepath.Join(home, "GitHub")); !errors.Is(err, ErrNotRepo) ||
		err.Error() != "No git repository found in this folder." {
		t.Fatalf("not a repo: %v", err)
	}
	if _, err := s.AddManual(bgc, filepath.Join(home, "nope")); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("missing folder: %v", err)
	}

	os.RemoveAll(gone)
	stats, err := s.Scan(bgc)
	if err != nil || stats.Found != 1 || stats.Missing != 1 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	all, _ := s.All(bgc)
	want := map[string]bool{"gone": true, "secret": false, "stays": false}
	if len(all) != 3 {
		t.Fatalf("all = %v", names(all))
	}
	for _, r := range all {
		if r.Missing != want[r.Name] {
			t.Errorf("%s missing = %v", r.Name, r.Missing)
		}
		if r.Name == "secret" && r.Source != "manual" {
			t.Errorf("manual source overwritten: %s", r.Source)
		}
	}
	mkRepo(t, home, "GitHub/gone")
	s.Scan(bgc)
	all, _ = s.All(bgc)
	for _, r := range all {
		if r.Missing {
			t.Errorf("%s still missing after it came back", r.Name)
		}
	}
}

func TestSecondScanJoinsTheRunningOne(t *testing.T) {
	home := realTemp(t)
	mkRepo(t, home, "GitHub/a")
	g := &fakeGit{gate: make(chan struct{}), start: make(chan struct{})}
	s := newService(t, home, g)
	var wg sync.WaitGroup
	results := make([]ScanStats, 2)
	wg.Add(1)
	go func() { defer wg.Done(); results[0], _ = s.Scan(bgc) }()
	<-g.start
	if !s.Scanning() {
		t.Fatal("Scanning() must be true mid-scan")
	}
	wg.Add(1)
	go func() { defer wg.Done(); results[1], _ = s.Scan(bgc) }()
	time.Sleep(20 * time.Millisecond)
	close(g.gate)
	wg.Wait()
	if results[0] != results[1] || results[0].Found != 1 {
		t.Fatalf("results = %+v", results)
	}
	if n := g.calls.Load(); n > 3 { // one scan: remote, symbolic-ref, (no more)
		t.Fatalf("git calls = %d, second scan must not rerun", n)
	}
}

func TestLoopRunsOnScheduleAndTrigger(t *testing.T) {
	home := realTemp(t)
	mkRepo(t, home, "GitHub/a")
	s := newService(t, home, &fakeGit{})
	ticks := make(chan time.Time)
	var asked atomic.Int64
	s.After = func(d time.Duration) <-chan time.Time {
		asked.Store(int64(d))
		return ticks
	}
	ctx, cancel := context.WithCancel(bgc)
	defer cancel()
	go s.Loop(ctx, func(context.Context) time.Duration { return 6 * time.Hour })
	waitFor(t, func() bool { all, _ := s.All(bgc); return len(all) == 1 })
	waitFor(t, func() bool { return asked.Load() != 0 }) // the loop is now waiting
	if time.Duration(asked.Load()) != 6*time.Hour {
		t.Fatalf("interval = %v", time.Duration(asked.Load()))
	}
	mkRepo(t, home, "GitHub/b")
	ticks <- time.Now()
	waitFor(t, func() bool { all, _ := s.All(bgc); return len(all) == 2 })
	mkRepo(t, home, "GitHub/c")
	s.Trigger()
	waitFor(t, func() bool { all, _ := s.All(bgc); return len(all) == 3 })
}

func TestExcludesAreApplied(t *testing.T) {
	home := realTemp(t)
	mkRepo(t, home, "GitHub/a")
	mkRepo(t, home, "Downloads/b")
	s := newService(t, home, &fakeGit{})
	s.Excludes = func(context.Context) []string { return []string{"~/Downloads"} }
	stats, _ := s.Scan(bgc)
	if stats.Found != 1 {
		t.Fatalf("found = %d", stats.Found)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLoopLogsFailedScan(t *testing.T) {
	home := realTemp(t)
	mkRepo(t, home, "GitHub/a")
	s := newService(t, home, &fakeGit{})
	s.DB.Close()
	logged := make(chan string, 4)
	s.Log = func(format string, args ...any) { logged <- fmt.Sprintf(format, args...) }
	s.After = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	ctx, cancel := context.WithCancel(bgc)
	defer cancel()
	go s.Loop(ctx, func(context.Context) time.Duration { return time.Hour })
	select {
	case msg := <-logged:
		if !strings.HasPrefix(msg, "repos: scheduled scan failed: ") {
			t.Fatalf("log = %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed scan was not logged")
	}
}

func TestScanPublishesEvenIfCallerCancelled(t *testing.T) {
	home := realTemp(t)
	mkRepo(t, home, "GitHub/a")
	s := newService(t, home, &fakeGit{})
	ctx, cancel := context.WithCancel(bgc)
	cancel()
	if _, err := s.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.Events.After(bgc, 0, 10)
	if len(evs) != 1 || evs[0].Type != events.ReposChanged {
		t.Fatalf("events = %+v", evs)
	}
}

func TestScanJoinerHonoursItsContext(t *testing.T) {
	home := realTemp(t)
	mkRepo(t, home, "GitHub/a")
	g := &fakeGit{gate: make(chan struct{}), start: make(chan struct{})}
	s := newService(t, home, g)
	done := make(chan struct{})
	go func() { defer close(done); s.Scan(bgc) }()
	<-g.start
	ctx, cancel := context.WithTimeout(bgc, 20*time.Millisecond)
	defer cancel()
	if _, err := s.Scan(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("joiner err = %v", err)
	}
	close(g.gate)
	<-done
}
