package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/notifyrules"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// fakeTmux records every call and serves scripted captures.
type fakeTmux struct {
	started  []string // "<name>|<cwd>|<argv joined>"
	env      map[string]map[string]string
	captures map[string][]string // per session: one capture per poll, the last repeats
	pasted   []string
	keys     []string
	killed   []string
	panes    []Pane
	n        map[string]int
	clk      *testClock // the Store's clock, so a test can advance it: tm.clk.Advance(d)
}

func newFakeTmux() *fakeTmux {
	return &fakeTmux{env: map[string]map[string]string{}, captures: map[string][]string{},
		n: map[string]int{}}
}
func (f *fakeTmux) Start(ctx context.Context, name, cwd string, env map[string]string, argv []string) error {
	f.started = append(f.started, name+"|"+cwd+"|"+strings.Join(argv, " "))
	f.env[name] = env
	return nil
}
func (f *fakeTmux) Panes(context.Context) ([]Pane, error) { return f.panes, nil }
func (f *fakeTmux) Capture(ctx context.Context, name string, lines int) (string, error) {
	seq := f.captures[name]
	if len(seq) == 0 {
		return "─────\n❯ \n─────\n", nil // idle by default
	}
	i := f.n[name]
	if i >= len(seq) {
		i = len(seq) - 1
	}
	f.n[name] = i + 1
	return seq[i], nil
}
func (f *fakeTmux) PasteLine(ctx context.Context, name, line string) error {
	f.pasted = append(f.pasted, name+"|"+line)
	return nil
}
func (f *fakeTmux) Keys(ctx context.Context, name string, keys ...string) error {
	f.keys = append(f.keys, name+"|"+strings.Join(keys, ","))
	return nil
}
func (f *fakeTmux) Env(ctx context.Context, name, key string) (string, error) {
	return f.env[name][key], nil
}
func (f *fakeTmux) Kill(ctx context.Context, name string) error {
	f.killed = append(f.killed, name)
	return nil
}

// testClock is the one clock every runtime test shares (R12). It advances 1 ms on
// every read, so `created_at` values order instead of all landing on the same
// millisecond and passing `acceptedSince`/`rootState` comparisons by `>=`
// equality. Advance moves it by hand, and After — the Store.After seam — advances
// it by the duration it was asked to wait and then fires at once. That is what
// makes a poll loop like watchStartup reach its deadline: with a frozen clock
// `for s.Now().Before(deadline)` never ends, so the 30-second failure branch of
// §11.5 is unreachable and untested.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Millisecond)
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *testClock) After(d time.Duration) <-chan time.Time {
	c.Advance(d)
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

// newStore wires a Store on a migrated temp DB with the fake adapter and tmux.
func newStore(t *testing.T) (*Store, *fakeTmux, *adapter.Fake) {
	t.Helper()
	d := dbtest.Open(t)
	home := t.TempDir()
	clk := newTestClock()
	ev := events.New(d, clk.Now)
	it := &items.Store{DB: d, Events: ev, Now: clk.Now}
	cat := &catalog.Service{DB: d, Events: ev, Now: clk.Now, Log: func(string, ...any) {}}
	seedFakeCatalog(t, d)
	if _, err := d.ExecContext(context.Background(), `INSERT INTO settings (key, value_json, updated_at) VALUES ('enabled_agents', '["claude", "fake"]', 1)`); err != nil {
		t.Fatal(err)
	}
	st := &settings.Store{DB: d, Events: ev, Now: clk.Now, ModelsFor: cat.ModelsFor,
		Installed: func(context.Context) []AgentKind { return []AgentKind{Fake} }}
	fa := adapter.NewFake(adapter.Deps{Home: home, UserHome: t.TempDir(),
		Bin: "/usr/local/bin/swarm", Run: execx.Run, Log: func(string, ...any) {}})
	tm := newFakeTmux()
	tm.clk = clk
	s := &Store{DB: d, Events: ev, Items: it, Settings: st, Catalog: cat, Home: home,
		Now: clk.Now, Log: func(string, ...any) {}, Tmux: tm,
		// D25: Preflight calls s.Worktree.SigningOK for every repo, so the field is
		// wired here, not in Task 21. Run is the real execx.Run because the git calls
		// go against temp repos gitRepoNoSigning creates.
		Worktree:  &worktree.Service{DB: d, Run: execx.Run, Now: clk.Now, Log: func(string, ...any) {}, Home: home},
		Notify:    &fakeNotifier{t: t},
		Adapters:  map[AgentKind]adapter.Adapter{Fake: fa},
		Bin:       "/usr/local/bin/swarm",
		DaemonURL: "http://127.0.0.1:17778", // F3: never the live daemon's port in a fixture
		OSEnv:     func(k string) string { return map[string]string{"USER": "u", "HOME": home, "PATH": "/usr/bin"}[k] },
		BaseEnv: func(getenv func(string) string) map[string]string {
			return map[string]string{"USER": getenv("USER"), "HOME": getenv("HOME"), "PATH": getenv("PATH")}
		},
		After: clk.After,
		Go:    func(f func()) { f() }, // D28: inline, so the assertions are deterministic
	}
	return s, tm, fa
}

// fakeNotifier records what the runtime raised, and — this is the fix for
// the detection gap that let ~15 missing-Args bugs (plus OnWorktreeRetained's
// missing ROOT-KEY) ship undetected through this entire package's test suite
// — validates each one against the real §17.5 template from notifyrules
// (the leaf-package extraction of internal/notify's own Rules table; notify
// itself can't be imported here without a cycle, since it imports runtime
// for NotifyInput). A call site that builds a Kind with the wrong Args now
// fails the test that exercises it, the same way notify.Render would fail
// closed against a real, wired notify.Service — which is exactly what let
// these bugs through before: nothing in this package's own suite ever
// called Render for real.
type fakeNotifier struct {
	t      *testing.T
	mu     sync.Mutex
	raised []NotifyInput
}

func (f *fakeNotifier) Raise(ctx context.Context, tx *sql.Tx, n NotifyInput) error {
	f.mu.Lock()
	f.raised = append(f.raised, n)
	f.mu.Unlock()
	if f.t == nil {
		return nil
	}
	f.t.Helper()
	rule, ok := notifyrules.Rules[n.Kind]
	if !ok {
		f.t.Fatalf("notify: unknown kind %q (fakeNotifier/notifyrules.Rules disagree with notify.Rules)", n.Kind)
		return nil
	}
	for _, ph := range notifyrules.Placeholders(rule.Body) {
		if _, ok := n.Args[ph]; !ok {
			f.t.Fatalf("notify: %s is missing %s (Args = %v)", n.Kind, ph, n.Args)
		}
	}
	return nil
}

func (f *fakeNotifier) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.raised))
	for _, n := range f.raised {
		out = append(out, n.Kind)
	}
	return out
}

func notified(t *testing.T, s *Store, kind string) NotifyInput {
	t.Helper()
	f := s.Notify.(*fakeNotifier)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.raised {
		if n.Kind == kind {
			return n
		}
	}
	kinds := make([]string, 0, len(f.raised))
	for _, n := range f.raised {
		kinds = append(kinds, n.Kind)
	}
	t.Fatalf("no %s notification; raised %v", kind, kinds)
	return NotifyInput{}
}

func notifiedCount(s *Store, kind string) int {
	f := s.Notify.(*fakeNotifier)
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int
	for _, in := range f.raised {
		if in.Kind == kind {
			n++
		}
	}
	return n
}

func lastNotified(t *testing.T, s *Store) NotifyInput {
	t.Helper()
	f := s.Notify.(*fakeNotifier)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.raised) == 0 {
		t.Fatal("nothing was notified")
	}
	return f.raised[len(f.raised)-1]
}

func TestStartSpikeCreatesTheItemTheAgentAndTheSession(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	key, a, queued, err := s.StartSpike(ctx, SpikeInput{Name: "Investigate login crash",
		Intent: "debug", Kind: Fake, Model: "fake-1",
		Request: "Users see a crash after the second login attempt."})
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("the first spawn is not queued")
	}
	if !strings.HasPrefix(key, "SPIKE-") {
		t.Fatalf("key = %q", key)
	}
	if a.Name != "investigate-login-crash" {
		t.Fatalf("name = %q", a.Name)
	}
	if a.Role != RoleOrchestrator || a.State != AgentActive {
		t.Fatalf("agent = %+v", a)
	}
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if it.Status != items.Draft || it.SpikeIntent != "debug" {
		t.Fatalf("item = %+v", it)
	}
	if it.Brief != "Users see a crash after the second login attempt." {
		t.Fatalf("the request text becomes the brief: %q", it.Brief)
	}
	// one tmux session in the neutral folder (I3)
	if len(tm.started) != 1 || !strings.Contains(tm.started[0], filepath.Join(s.Home, "work", a.Name)) {
		t.Fatalf("started = %v", tm.started)
	}
	if fi, err := os.Stat(filepath.Join(s.Home, "work", a.Name)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("neutral folder mode = %v (%v)", fi, err)
	}
	// the token is on disk 0600 and hashed in the row, never in argv
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokPath := filepath.Join(s.Home, "run", "tokens", ses.ID)
	tok, err := os.ReadFile(tokPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(tokPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %v", fi.Mode().Perm())
	}
	if strings.Contains(tm.started[0], strings.TrimSpace(string(tok))) {
		t.Fatal("the token must never appear in argv")
	}
	if tm.env[a.Name]["SWARM_TOKEN_FILE"] != tokPath {
		t.Fatalf("SWARM_TOKEN_FILE = %q", tm.env[a.Name]["SWARM_TOKEN_FILE"])
	}
	for _, k := range []string{"USER", "HOME", "PATH", "SWARM_URL", "SWARM_SESSION", "SWARM_AGENT_KIND"} {
		if tm.env[a.Name][k] == "" {
			t.Errorf("%s missing from the tmux environment (L9)", k)
		}
	}
	// the assignment message is waiting, and it carries the rendered brief
	var kind, payload string
	if err := s.DB.QueryRowContext(ctx, `SELECT kind, payload_json FROM messages WHERE to_agent_id = ?`, a.ID).
		Scan(&kind, &payload); err != nil {
		t.Fatal(err)
	}
	if kind != "assignment" || !strings.Contains(payload, key) {
		t.Fatalf("message = %s %s", kind, payload)
	}
}

func TestStartSpikeWithChoreIntentAndLongBrief(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	longBrief := strings.Repeat("Detailed maintenance instructions. ", 60) // ~2160 chars (> 600)
	key, a, _, err := s.StartSpike(ctx, SpikeInput{
		Name:    "First pass cleanup",
		Intent:  "chore",
		Kind:    Fake,
		Model:   "fake-1",
		Request: longBrief,
	})
	if err != nil {
		t.Fatalf("expected chore spike with long brief to succeed, got: %v", err)
	}
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if it.SpikeIntent != "chore" {
		t.Fatalf("expected spike_intent 'chore', got: %q", it.SpikeIntent)
	}
	if it.Brief != longBrief {
		t.Fatalf("expected brief to match longBrief exactly, got len %d vs %d", len(it.Brief), len(longBrief))
	}
	if a.Name != "first-pass-cleanup" {
		t.Fatalf("name = %q", a.Name)
	}
}

// §17.3: a user-typed name that is taken is refused, not silently suffixed (P4 carry).
func TestStartSpikeRefusesADuplicateUserTypedName(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	in := SpikeInput{Name: "Login crash", Intent: "debug", Kind: Fake, Model: "fake-1"}
	if _, _, _, err := s.StartSpike(ctx, in); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := s.StartSpike(ctx, in)
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Message != "This agent name is already in use." || ie.Code != items.CodeConflict {
		t.Fatalf("err = %v", err)
	}
}

// §4: a daemon-generated worker name gets the -2 suffix instead.
func TestSpawnSuffixesADaemonGeneratedName(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	root := seedEpicWithTask(t, s)
	first, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: root, Brief: BriefInput{Objective: "do it"}})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: root, Brief: BriefInput{Objective: "do it again"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "write-the-failing-test-coder" || second.Name != "write-the-failing-test-coder-2" {
		t.Fatalf("names = %q, %q, want write-the-failing-test-coder and write-the-failing-test-coder-2", first.Name, second.Name)
	}
}

// Required fix 4 (I-5): agent.changed (contracts §5) fires from both Spawn
// and StartOrchestrator, not just StartSpike/Cancel/Retry/Ack.
func TestSpawnAndStartOrchestratorPublishAgentChanged(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	root := seedEpicWithTask(t, s)
	countAgentChanged := func() int {
		evs, _ := s.Events.After(ctx, 0, 1000)
		n := 0
		for _, e := range evs {
			if e.Type == events.AgentChanged {
				n++
			}
		}
		return n
	}
	before := countAgentChanged()
	if _, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: root, Brief: BriefInput{Objective: "do it"}}); err != nil {
		t.Fatal(err)
	}
	afterSpawn := countAgentChanged()
	if afterSpawn != before+1 {
		t.Fatalf("agent.changed count after Spawn = %d, want %d", afterSpawn, before+1)
	}
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"}); err != nil {
		t.Fatal(err)
	}
	afterOrch := countAgentChanged()
	if afterOrch != afterSpawn+1 {
		t.Fatalf("agent.changed count after StartOrchestrator = %d, want %d", afterOrch, afterSpawn+1)
	}
}

// §5: at most one live session per agent, and one orchestrator per top-level item.
func TestSpawnRefusesASecondOrchestratorForTheSameRoot(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Message != "This item already has an orchestrator." {
		t.Fatalf("err = %v", err)
	}
}

// A startup-dialog stall (watchStartup) must tell the parent the same way an
// interrupted or crashed child does, or the orchestrator never learns the
// child didn't start and just waits on it forever.
func TestFailSessionRelaysToParent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	if wSes.FailureText != nil {
		t.Fatalf("fresh session FailureText = %v, want nil", wSes.FailureText)
	}
	const paneText = "Is this a project you created or one you trust?\n"
	if err := s.failSession(ctx, w, wSes, paneText); err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, w.ID)
	if err != nil || ses.State != Failed {
		t.Fatalf("session = %+v, err = %v", ses, err)
	}
	// The full pane text stays on the row, queryable after the tmux pane is
	// gone, not just as a shortened notification argument.
	if ses.FailureText == nil || *ses.FailureText != paneText {
		t.Fatalf("FailureText = %v, want %q", ses.FailureText, paneText)
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'`,
		orch.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("relay message count = %d, err = %v", count, err)
	}
}

// §11.4: preflight order, with the §17.3 copy for each failure.
func TestPreflightFailures(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		set  func(*Store, *adapter.Fake)
		want string
	}{
		{"not installed", func(s *Store, f *adapter.Fake) { f.NotInstalled = true },
			"Fake isn't installed on this Mac."},
		{"not signed in", func(s *Store, f *adapter.Fake) { f.AuthError = errors.New("no token") },
			"Fake isn't signed in. Run `fake login` in a terminal."},
		{"no superpowers", func(s *Store, f *adapter.Fake) { f.NoSuperpowers = true },
			"Install the superpowers plugin for Fake to run orchestrators."},
	}
	for _, c := range cases {
		s, _, f := newStore(t)
		c.set(s, f)
		err := s.Preflight(ctx, PreflightInput{Kind: Fake, Model: "fake-1", Role: RoleOrchestrator})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	// a model that is not in the catalog
	s, _, _ := newStore(t)
	if err := s.Preflight(ctx, PreflightInput{Kind: Fake, Model: "gone-9", Role: RoleCoder}); err == nil ||
		err.Error() != "Choose a model available for this agent." {
		t.Errorf("model err = %v", err)
	}
}

// §11.4 step 7: signing off fails the spawn with the repo name.
func TestPreflightRefusesARepoWithSigningOff(t *testing.T) {
	s, _, _ := newStore(t)
	repo := gitRepoNoSigning(t)
	err := s.Preflight(context.Background(), PreflightInput{Kind: Fake, Model: "fake-1",
		Role: RoleCoder, RepoPaths: []string{repo}})
	if err == nil || !strings.HasPrefix(err.Error(), "Commit signing is off for ") {
		t.Fatalf("err = %v", err)
	}
}

// §10.2: a preflight failure leaves the spike in draft with a failed agent and no session.
func TestStartSpikeOnPreflightFailureLeavesADraftAndAFailedAgent(t *testing.T) {
	s, tm, f := newStore(t)
	f.NoSuperpowers = true
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Look into it", Intent: "feature",
		Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatalf("StartSpike must succeed and record the failure: %v", err)
	}
	it, _ := s.Items.Get(ctx, key)
	if it.Status != items.Draft {
		t.Fatalf("status = %s", it.Status)
	}
	if len(tm.started) != 0 {
		t.Fatalf("nothing should be spawned: %v", tm.started)
	}
	if _, err := s.LatestSession(ctx, a.ID); err == nil {
		t.Fatal("no session row belongs to a preflight failure (contracts: session === null)")
	}
	if got := lastNotified(t, s).Kind; got != "agent.preflight_failed" {
		t.Fatalf("notification = %q", got)
	}
}

// §11.5: dialogs are answered once each, with the exact keys, only when required.
func TestSpawnAnswersStartupDialogsOnce(t *testing.T) {
	s, tm, f := newStore(t)
	f.Dialogs = []adapter.Dialog{
		{Match: regexp.MustCompile(`Is this a project you created or one you trust\?`),
			Require: regexp.MustCompile(`Yes, I trust this folder`), Keys: []string{"Down", "Enter"}},
	}
	tm.captures["look-into-it"] = []string{
		"Is this a project you created or one you trust?\n  No, exit\n  Yes, I trust this folder\n",
		"Is this a project you created or one you trust?\n  No, exit\n  Yes, I trust this folder\n",
		"─────\n❯ \n─────\n",
	}
	if _, _, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Look into it",
		Intent: "feature", Kind: Fake, Model: "fake-1"}); err != nil {
		t.Fatal(err)
	}
	var sent int
	for _, k := range tm.keys {
		if k == "look-into-it|Down,Enter" {
			sent++
		}
	}
	if sent != 1 {
		t.Fatalf("the keys were sent %d times, want once", sent)
	}
}

// P0-crash-1 (2026-09-19): the real incident. Claude 2.1.278 renders a
// highlighted menu option with a separate color escape around every word
// ("\x1b[38;5;153mI\x1b[39m \x1b[38;5;153mam\x1b[39m ..."), so the plain
// multi-word Dialog.Match for the dev-channels warning never matched the raw
// -e capture and the orchestrator's prompt sat unanswered forever. Dialog
// matching must strip ANSI codes first.
func TestSpawnAnswersAStartupDialogSplitByPerWordAnsiCodes(t *testing.T) {
	s, tm, f := newStore(t)
	f.Dialogs = []adapter.Dialog{
		{Match: regexp.MustCompile(`I am using this for local development`), Keys: []string{"Enter"}},
	}
	tm.captures["look-into-it"] = []string{
		"\x1b[38;5;153mI\x1b[39m \x1b[38;5;153mam\x1b[39m \x1b[38;5;153musing\x1b[39m " +
			"\x1b[38;5;153mthis\x1b[39m \x1b[38;5;153mfor\x1b[39m \x1b[38;5;153mlocal\x1b[39m " +
			"\x1b[38;5;153mdevelopment\x1b[39m\n",
		"─────\n❯ \n─────\n",
	}
	if _, _, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Look into it",
		Intent: "feature", Kind: Fake, Model: "fake-1"}); err != nil {
		t.Fatal(err)
	}
	var sent int
	for _, k := range tm.keys {
		if k == "look-into-it|Enter" {
			sent++
		}
	}
	if sent != 1 {
		t.Fatalf("keys sent %d times, want once (dialog split by per-word ANSI codes must still match): keys=%v", sent, tm.keys)
	}
}

// §11.1, §11.5: a Fail dialog fails the spawn with the pane text in the error.
func TestSpawnFailsOnAFailDialog(t *testing.T) {
	s, tm, f := newStore(t)
	f.Dialogs = []adapter.Dialog{
		{Match: regexp.MustCompile(`Hooks can run outside the sandbox`), Fail: true},
	}
	tm.captures["look-into-it"] = []string{"Hooks can run outside the sandbox after you trust them.\n"}
	_, a, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Look into it",
		Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != Failed {
		t.Fatalf("session state = %s", ses.State)
	}
}

// §10.7: cancel kills the pane, releases the reservations and finishes the agent.
func TestCancelKillsAndFinishes(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bye", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Cancel(ctx, a.Name, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != AgentFinished {
		t.Fatalf("agent state = %s", out.State)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	if ses.State != Cancelled {
		t.Fatalf("session state = %s", ses.State)
	}
	if len(tm.keys) == 0 || !strings.HasSuffix(tm.keys[len(tm.keys)-1], "|Escape") {
		t.Fatalf("interrupt keys were not sent: %v", tm.keys)
	}
	// P0-crash-1: startSession now kills any stale pane under this name
	// before it starts one, so the spawn itself contributes a (harmless,
	// no-op-on-a-fresh-name) kill too; Cancel's own kill is the second.
	if len(tm.killed) != 2 || tm.killed[0] != a.Name || tm.killed[1] != a.Name {
		t.Fatalf("killed = %v", tm.killed)
	}
	// the item status does not change (§10.7)
	it, _ := s.Items.Get(ctx, "SPIKE-1")
	if it.Status != items.Draft {
		t.Fatalf("item status = %s", it.Status)
	}
}

// §10.7: retry starts a new attempt and a new generation with a fresh token.
func TestRetryStartsANewAttemptAndRevokesTheOldToken(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Again", Intent: "feature", Kind: Fake, Model: "fake-1"})
	first, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'crashed' WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retry(ctx, a.Name, "the reviewer found a missing test", "", ""); err != nil {
		t.Fatal(err)
	}
	next, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Attempt != 2 || next.Generation != 2 {
		t.Fatalf("session = attempt %d, generation %d", next.Attempt, next.Generation)
	}
	if next.TokenHash == first.TokenHash {
		t.Fatal("a new generation needs a new token")
	}
	if _, err := os.Stat(filepath.Join(s.Home, "run", "tokens", first.ID)); !os.IsNotExist(err) {
		t.Fatal("the old token file must be deleted")
	}
	// the note reaches the agent as an assignment_update (I8)
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'`, a.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("assignment_update count = %d", n)
	}
}

// P0-crash-1 (2026-09-19): a real incident where a crashed session's tmux
// pane was never actually dead (reconcile can mark 'crashed' while the pane
// is still alive, e.g. stuck at an unanswered prompt) -- retrying without
// killing that stale pane first made tmux new-session fail with "duplicate
// session", stranding the retry and leaving the original pane orphaned
// forever. startSession must kill any pane under this agent's name before
// starting a new one.
func TestRetryKillsAStalePaneBeforeStartingTheNewOne(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Again", Intent: "feature", Kind: Fake, Model: "fake-1"})
	first, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'crashed' WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retry(ctx, a.Name, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tm.killed, a.Name) {
		t.Fatalf("killed = %v, want it to include %q before the new session started", tm.killed, a.Name)
	}
}

// Same incident: when the new attempt's own tmux new-session still fails
// (Kill couldn't save it -- a real "duplicate session" from something else
// racing this same name, or any other tmux error), the session must be left
// 'failed' with a reason, not stuck as an orphaned 'spawning' row that only
// the next reconcile tick resolves into a confusing second 'crashed' state.
func TestRetryFailsCleanlyWhenTmuxStillRefusesToStart(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Again", Intent: "feature", Kind: Fake, Model: "fake-1"})
	first, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'crashed' WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	s.Tmux = &erroringTmux{fakeTmux: tm, startErr: errors.New("tmux new-session Again: exit status 1: duplicate session: Again")}
	if _, err := s.Retry(ctx, a.Name, "", "", ""); err == nil {
		t.Fatal("expected the tmux error back")
	}
	next, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.State != Failed {
		t.Fatalf("state = %s, want failed (not left as an orphaned spawning row)", next.State)
	}
}

// §10.7: ack moves a crashed agent to history.
func TestAckMovesTheAgentToAcknowledged(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Acked", Intent: "feature", Kind: Fake, Model: "fake-1"})
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'crashed' WHERE agent_id = ?`, a.ID)
	if err := s.Ack(ctx, a.Name); err != nil {
		t.Fatal(err)
	}
	out, _ := s.Agent(ctx, a.Name)
	if out.State != AgentAcknowledged {
		t.Fatalf("state = %s", out.State)
	}
}

// Required fix 4 (I-5): agent.changed (contracts §5) must fire from every
// route that changes an agent's state, so P3's board invalidates its agent
// list — StartSpike (via Spawn's shared insert path), Cancel, Retry and Ack
// here; StartOrchestrator, Spawn, Pause and Resume are covered elsewhere.
func TestCancelRetryAndAckPublishAgentChanged(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	countAgentChanged := func() int {
		evs, _ := s.Events.After(ctx, 0, 1000)
		n := 0
		for _, e := range evs {
			if e.Type == events.AgentChanged {
				n++
			}
		}
		return n
	}
	before := countAgentChanged()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Events", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	afterSpawn := countAgentChanged()
	if afterSpawn != before+1 {
		t.Fatalf("agent.changed count after StartSpike = %d, want %d", afterSpawn, before+1)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'crashed' WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retry(ctx, a.Name, "", "", ""); err != nil {
		t.Fatal(err)
	}
	afterRetry := countAgentChanged()
	if afterRetry != afterSpawn+1 {
		t.Fatalf("agent.changed count after Retry = %d, want %d", afterRetry, afterSpawn+1)
	}
	if err := s.Ack(ctx, a.Name); err != nil {
		t.Fatal(err)
	}
	afterAck := countAgentChanged()
	if afterAck != afterRetry+1 {
		t.Fatalf("agent.changed count after Ack = %d, want %d", afterAck, afterRetry+1)
	}
	if _, err := s.Cancel(ctx, a.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	afterCancel := countAgentChanged()
	if afterCancel != afterAck+1 {
		t.Fatalf("agent.changed count after Cancel = %d, want %d", afterCancel, afterAck+1)
	}
}

// §7: terminal publishes terminal.open and reports the tmux name.
func TestTerminalPublishesTheEvent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Term", Intent: "feature", Kind: Fake, Model: "fake-1"})
	name, by, err := s.Terminal(ctx, a.Name)
	if err != nil {
		t.Fatal(err)
	}
	if name != a.Name || by != "menubar" {
		t.Fatalf("terminal = %q, %q", name, by)
	}
	evs, _ := s.Events.After(ctx, 0, 100)
	var found bool
	for _, e := range evs {
		if e.Type == events.TerminalOpen {
			found = true
		}
	}
	if !found {
		t.Fatal("no terminal.open event")
	}
}

func seedFakeCatalog(t *testing.T, d *db.DB) {
	t.Helper()
	_, err := d.ExecContext(context.Background(), `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('fake','fake-1','[{"id":"fake-1","label":"Fake 1","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":false}]','fake-1','test',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
}

// §11.5: a pane that never goes idle and shows no dialog fails after 30 s.
func TestStartupTimesOutAfterThirtySeconds(t *testing.T) {
	s, tm, _ := newStore(t)
	// One capture, repeated: never idle, matches no dialog.
	tm.captures["stuck"] = []string{"Loading…\n"}
	_, a, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Stuck",
		Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != Failed {
		t.Fatalf("session state = %s, want failed after the 30 s deadline", ses.State)
	}
	if got := s.Notify.(*fakeNotifier).kinds(); !slices.Contains(got, "agent.preflight_failed") {
		t.Fatalf("raised %v, want agent.preflight_failed", got)
	}
}

// §11.5 (2026-09-19, live incident): continuous new output must not trip the
// stall timeout even past 30s of elapsed time -- only real inactivity should.
// A real subtask doing legitimate startup work (installs, an advisor call)
// got marked failed at exactly spawn+30s under the old fixed-deadline logic,
// even though its pane stayed alive and kept producing new output for
// minutes; every retry that false failure triggered killed and restarted the
// pane, discarding real progress.
func TestStartupSurvivesBusyOutputPastThirtySeconds(t *testing.T) {
	s, tm, _ := newStore(t)
	// 70 distinct captures = 35s of changing output at the 500ms poll
	// interval, past the old fixed 30s deadline, then one idle capture.
	var captures []string
	for i := 0; i < 70; i++ {
		captures = append(captures, fmt.Sprintf("Crunching… (%ds)\n", i))
	}
	captures = append(captures, "─────\n❯ \n─────\n")
	tm.captures["busy"] = captures
	_, a, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Busy",
		Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != Running {
		t.Fatalf("session state = %s, want running: continuous new output must not fail the spawn", ses.State)
	}
}

// §11.5 (2026-09-20, live incident): a session that clears its startup
// dialogs and launches straight into continuous, genuinely busy work (never
// once matching IdlePrompt) must be recognized as done starting the moment
// the pane shows real busy output -- not left reporting Spawning for its
// entire work duration and eventually false-failed by startupCeiling. Observed
// live: "s1-review-2" (Claude/Opus) sat mid-tmux showing "✻ Twisting… (6m
// 40s · ↓ 15.3k tokens)" -- clearly busy and productive -- while session.state
// still read "spawning". ad.Busy() matching is just as much proof the startup
// phase is over as ad.Idle() becoming true.
func TestStartupTransitionsToRunningWhenBusyWithoutEverGoingIdle(t *testing.T) {
	s, tm, _ := newStore(t)
	// Every poll shows the same busy spinner line: never idle, matches no
	// dialog. Repeating (rather than varying) the capture also proves the fix
	// doesn't rely on the stall-timeout's "changing output" reset -- it must
	// end the spawning phase on the very first busy poll.
	tm.captures["s1-review-2"] = []string{"✻ Twisting… (6m 40s · ↓ 15.3k tokens)\n"}
	_, a, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "s1-review-2",
		Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != Running {
		t.Fatalf("session state = %s, want running: a busy pane that never idles must still end the spawning phase", ses.State)
	}
}

func TestSessionByToken(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Token test", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokPath := filepath.Join(s.Home, "run", "tokens", ses.ID)
	tok, err := os.ReadFile(tokPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionByToken(ctx, string(tok))
	if err != nil {
		t.Fatalf("SessionByToken: %v", err)
	}
	if got.ID != ses.ID {
		t.Fatalf("got ID %s, want %s", got.ID, ses.ID)
	}
	if _, err := s.SessionByToken(ctx, "invalid-token"); err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestTerminalOpened(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	if err := s.TerminalOpened(ctx, "agent", "menubar"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentTree(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTwoTasks(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "sub"}, ParentAgentID: orch.ID})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := s.AgentTree(ctx, "EPIC-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 2 {
		t.Fatalf("len(tree) = %d, want 2", len(tree))
	}
	if tree[0].ID != orch.ID || tree[1].ID != child.ID {
		t.Fatalf("tree = %+v", tree)
	}
}

func TestLoginCommand(t *testing.T) {
	if got := loginCommand(Claude); got != "claude /login" {
		t.Errorf("Claude = %q", got)
	}
	if got := loginCommand(Codex); got != "codex login" {
		t.Errorf("Codex = %q", got)
	}
	if got := loginCommand(Agy); got != "agy login" {
		t.Errorf("Agy = %q", got)
	}
	if got := loginCommand(Cursor); got != "cursor-agent login" {
		t.Errorf("Cursor = %q", got)
	}
	if got := loginCommand(Fake); got != "fake login" {
		t.Errorf("Fake = %q", got)
	}
}

func TestStartOrchestratorPreflightFailed(t *testing.T) {
	s, _, f := newStore(t)
	f.NotInstalled = true
	ctx := context.Background()
	seedEpicWithTask(t, s)
	_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Fatalf("err = %v, want isn't installed", err)
	}
}

func TestRetryWithNote(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'failed' WHERE agent_id = ?`, a.ID)
	retried, err := s.Retry(ctx, a.Name, "please retry with extra care", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID != a.ID {
		t.Fatalf("retried agent = %+v", retried)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", ses.Attempt)
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'`, a.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("assignment_update message count = %d, err = %v", count, err)
	}
}

func TestBaseEnv(t *testing.T) {
	// A nil BaseEnv inside runtime is a programming error: Spawn panics with `runtime: BaseEnv is not wired`.
	s := &Store{}
	defer func() {
		r := recover()
		if r != "runtime: BaseEnv is not wired" {
			t.Fatalf("panic = %v, want 'runtime: BaseEnv is not wired'", r)
		}
	}()
	s.baseEnv(func(string) string { return "" })
}

func TestBaseEnvUsesWiredFunction(t *testing.T) {
	s := &Store{
		BaseEnv: func(getenv func(string) string) map[string]string {
			return map[string]string{"PATH": getenv("PATH"), "GNUPGHOME": "/tmp/gpg"}
		},
	}
	env := s.baseEnv(func(k string) string {
		if k == "PATH" {
			return "/bin"
		}
		return ""
	})
	if env["PATH"] != "/bin" || env["GNUPGHOME"] != "/tmp/gpg" {
		t.Fatalf("env = %v", env)
	}
}

func TestStoreDefaults(t *testing.T) {
	s := &Store{}
	_ = s.now()
	ch := s.after(time.Millisecond)
	<-ch
	done := make(chan struct{})
	s.go_(func() { close(done) })
	<-done
}

func TestPreflightEffortAndRepoNotFound(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	// unsupported effort
	err := s.Preflight(ctx, PreflightInput{Kind: Fake, Model: "fake-1", Role: RoleCoder, Effort: "high"})
	if err == nil || !strings.Contains(err.Error(), "isn't available for fake-1") {
		t.Fatalf("effort err = %v", err)
	}
	// non-existent repo path
	err = s.Preflight(ctx, PreflightInput{Kind: Fake, Model: "fake-1", Role: RoleCoder, RepoPaths: []string{"/nonexistent/path/here"}})
	if err == nil || !strings.Contains(err.Error(), "Repository is unavailable") {
		t.Fatalf("repo err = %v", err)
	}
}

func TestStartOrchestratorBranches(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 1, 4, 4)
	seedEpicWithTask(t, s)
	// Start first orchestrator with empty model -> defaults to fake-1
	orch1, queued, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake})
	if err != nil || queued {
		t.Fatalf("orch1 err = %v, queued = %v", err, queued)
	}
	if orch1.Model != "fake-1" {
		t.Fatalf("orch1 model = %q", orch1.Model)
	}
	// Conflict: already has an orchestrator
	_, _, err = s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake})
	if err == nil || !strings.Contains(err.Error(), "already has an orchestrator") {
		t.Fatalf("expected conflict, got %v", err)
	}
	// Second epic with max_orchestrators=1 -> queued
	ep2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Second Epic"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id = ?`, ep2.ID)
	orch2, queued, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep2.Key, Kind: Fake})
	if err != nil || !queued {
		t.Fatalf("orch2 err = %v, queued = %v", err, queued)
	}
	if orch2.State != AgentQueued {
		t.Fatalf("state = %s", orch2.State)
	}
}

func TestCancelFinishedAgent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "To cancel", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	// Cancel once -> moves to finished
	if _, err := s.Cancel(ctx, a.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	// Cancel again is idempotent
	if _, err := s.Cancel(ctx, a.Name, "", ""); err != nil {
		t.Fatalf("expected idempotent cancel, got: %v", err)
	}
	if _, err := s.Cancel(ctx, "nonexistent-agent", "", ""); err == nil {
		t.Fatal("expected error cancelling nonexistent agent")
	}
}

func TestSpawnMissingItem(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "NONEXISTENT", Role: RoleCoder, Kind: Fake, Model: "fake-1"})
	if err == nil {
		t.Fatal("expected error for nonexistent item")
	}
}

// TestRetryFromFinished proves retry works from a "finished" agent state (as
// opposed to "active") as long as the session itself ended in a retryable
// state. It used to reach that scenario via Cancel, whose session lands on
// Cancelled — but Cancelled is not retryable (§10.7's action table: cancelled
// agents show under Finished with no actions; §8.1's swarm_control text lists
// only completed/failed/crashed/interrupted), so this now reaches "finished
// agent, crashed session" directly, the same way completedCurrent's sibling
// tests already do.
func TestRetryFromFinished(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'crashed' WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := s.Retry(ctx, a.Name, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if retried.State != AgentActive {
		t.Fatalf("state = %s, want active", retried.State)
	}
}

// TestRetryRefusesAWrongSessionState is Task 41's fix: Retry used to have no
// state guard at all, unlike Pause/Resume/Cancel, so an MCP swarm_control
// caller could retry a session that was still running, paused, cancelled, or
// anything else §8.1 doesn't call retryable ("completed, failed, crashed or
// interrupted").
func TestRetryRefusesAWrongSessionState(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	// a freshly-spawned agent's session is live (running/spawning), not
	// retryable.
	if _, err := s.Retry(ctx, a.Name, "", "", ""); err == nil {
		t.Fatal("expected an error retrying a live session")
	} else if ie, ok := err.(*items.Error); !ok || ie.Code != items.CodeConflict {
		t.Fatalf("err = %v, want a CodeConflict items.Error", err)
	}

	// Cancelled is explicitly excluded too (§10.7: cancelled agents are
	// terminal, shown under Finished with no actions).
	if _, err := s.Cancel(ctx, a.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retry(ctx, a.Name, "", "", ""); err == nil {
		t.Fatal("expected an error retrying a cancelled session")
	} else if ie, ok := err.(*items.Error); !ok || ie.Code != items.CodeConflict {
		t.Fatalf("err = %v, want a CodeConflict items.Error", err)
	}
}

// Fix round 1, Important #1: PeekIdempotent's own length check must fail
// closed BEFORE Retry's external side effect (starting a new tmux session,
// flipping the agent active) runs, not only inside IdemTx/Idempotent at the
// final DB write -- reviewer-reproduced: without this, a >64-char request_id
// let the side effect run, then failed with 400 anyway, leaving the mutation
// applied under an error response.
func TestRetryRefusesAnOverLongRequestIDBeforeTheSideEffect(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'crashed' WHERE agent_id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	before, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	startedBefore := len(tm.started)
	tooLong := strings.Repeat("x", 65)
	_, err = s.Retry(ctx, a.Name, "with a note", "ses_caller", tooLong)
	if err == nil {
		t.Fatal("expected a bad_request refusal for an over-long request_id")
	}
	if ie, ok := err.(*items.Error); !ok || ie.Code != items.CodeBadRequest {
		t.Fatalf("err = %v, want a CodeBadRequest items.Error", err)
	}
	if len(tm.started) != startedBefore {
		t.Fatalf("tmux started %d times, want still %d: an over-long request_id must refuse BEFORE the side effect",
			len(tm.started), startedBefore)
	}
	after, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID || after.Attempt != before.Attempt || after.Generation != before.Generation {
		t.Fatalf("a new session must not have been started: before = %+v, after = %+v", before, after)
	}
	if after.State != Crashed {
		t.Fatalf("the original session's state must be untouched: state = %s", after.State)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'`,
		a.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the retry note must not have been sent: assignment_update count = %d", n)
	}
}

// Task 41: a repeated request_id must not spawn a second agent, and (this is
// the part a merely-typed-return-cache-hit wouldn't prove on its own) must
// not start a second tmux session for it either.
// TestSpawnRequestIDReplaysEvenWithAnExplicitNameOrOrchestratorRole proves
// the guard runs before Spawn's own pre-existing checks that read the row
// the first call just wrote: resolveName refuses an explicit name already
// taken, and the orchestrator-uniqueness check refuses a second orchestrator
// on the same root. Both would otherwise fire on a replay (spuriously,
// since the "conflict" is the first call's own agent) unless the idempotency
// check happens first.
func TestSpawnRequestIDReplaysEvenWithAnExplicitNameOrOrchestratorRole(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	in := SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Name: "the-worker", Brief: BriefInput{Objective: "task"}, SessionID: "ses_caller", RequestID: "req-name"}
	a1, _, err := s.Spawn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	a2, _, err := s.Spawn(ctx, in)
	if err != nil {
		t.Fatalf("replay with an explicit name must not hit resolveName's own-name conflict: %v", err)
	}
	if a2.ID != a1.ID {
		t.Fatalf("replay = %+v, want the same agent", a2)
	}

	orchIn := SpawnInput{ItemKey: "EPIC-1", Role: RoleOrchestrator, Kind: Fake, Model: "fake-1",
		Brief: BriefInput{Objective: "orch"}, SessionID: "ses_caller", RequestID: "req-orch"}
	o1, _, err := s.Spawn(ctx, orchIn)
	if err != nil {
		t.Fatal(err)
	}
	o2, _, err := s.Spawn(ctx, orchIn)
	if err != nil {
		t.Fatalf("replay with role orchestrator must not hit the one-orchestrator-per-root conflict: %v", err)
	}
	if o2.ID != o1.ID {
		t.Fatalf("replay = %+v, want the same agent", o2)
	}
}

func TestSpawnRequestIDReplaysInsteadOfSpawningTwice(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	in := SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Brief: BriefInput{Objective: "task"}, SessionID: "ses_caller", RequestID: "req-1"}
	a1, queued1, err := s.Spawn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if queued1 {
		t.Fatal("this spawn must not queue")
	}
	a2, queued2, err := s.Spawn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if queued2 || a2.ID != a1.ID {
		t.Fatalf("replay = %+v/%v, want the same agent, queued=false", a2, queued2)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("agents = %d, want 1", n)
	}
	if len(tm.started) != 1 {
		t.Fatalf("tmux started %d times, want 1: %v", len(tm.started), tm.started)
	}
}

func TestSpawnWithoutOrDistinctRequestIDsEachSpawn(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	base := SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Brief: BriefInput{Objective: "task"}, SessionID: "ses_caller"}
	a := base
	a.RequestID = "req-a"
	if _, _, err := s.Spawn(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := base
	b.RequestID = "req-b"
	if _, _, err := s.Spawn(ctx, b); err != nil {
		t.Fatal(err)
	}
	c := base
	if _, _, err := s.Spawn(ctx, c); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("agents = %d, want 3", n)
	}
	if len(tm.started) != 3 {
		t.Fatalf("tmux started %d times, want 3: %v", len(tm.started), tm.started)
	}
}

// A queued spawn's replay must not re-raise agent.queued, and must not admit
// a second agent row either.
func TestSpawnQueuedRequestIDReplaysWithoutDoubleNotify(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	setLimits(t, s, 4, 0, 0) // max_agents=0: every non-orchestrator spawn queues
	in := SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Brief: BriefInput{Objective: "task"}, SessionID: "ses_caller", RequestID: "req-1"}
	a1, queued1, err := s.Spawn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !queued1 {
		t.Fatal("this spawn must queue")
	}
	a2, queued2, err := s.Spawn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !queued2 || a2.ID != a1.ID {
		t.Fatalf("replay = %+v/%v, want the same agent, queued=true", a2, queued2)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("agents = %d, want 1", n)
	}
	notif := s.Notify.(*fakeNotifier)
	count := 0
	for _, k := range notif.kinds() {
		if k == "agent.queued" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("agent.queued raised %d times, want 1", count)
	}
}

func TestSpawnDefaultsKindAndModel(t *testing.T) {
	s, _, fa := newStore(t)
	s.Adapters[Claude] = fa
	ctx := context.Background()
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('claude','1','[{"id":"opus","label":"Opus","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":false}]','opus','test',1,1)`)
	seedEpicWithTask(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Claude || a.Model != "opus" {
		t.Fatalf("agent = %+v", a)
	}
}

// TestSpawnUsesRoleDefaultModelNotCatalogFirst guards against the model
// fallback silently defaulting to whichever model the catalog happens to
// list first (Claude's catalog puts the "fable" alias first) instead of the
// role's configured default -- the bug behind agent-swarm's usage overrun
// where coder/orchestrator workers were dispatched on Fable.
func TestSpawnUsesRoleDefaultModelNotCatalogFirst(t *testing.T) {
	s, _, fa := newStore(t)
	s.Adapters[Claude] = fa
	ctx := context.Background()
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('claude','1','[{"id":"claude-fable-5-1","label":"Fable","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":true},{"id":"sonnet","label":"Sonnet","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":true}]','claude-fable-5-1','test',1,1)`)
	seedEpicWithTask(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Claude || a.Model != "sonnet" {
		t.Fatalf("agent = %+v, want role default model sonnet, not catalog-first fable", a)
	}
}

// TestStartOrchestratorUsesRoleDefaultModelNotCatalogFirst is the same guard
// for StartOrchestrator, which had the identical bug.
func TestStartOrchestratorUsesRoleDefaultModelNotCatalogFirst(t *testing.T) {
	s, _, fa := newStore(t)
	s.Adapters[Claude] = fa
	ctx := context.Background()
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('claude','1','[{"id":"claude-fable-5-1","label":"Fable","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":true},{"id":"opus","label":"Opus","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":true}]','claude-fable-5-1','test',1,1)`)
	seedEpicWithTask(t, s)
	a, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Claude || a.Model != "opus" {
		t.Fatalf("agent = %+v, want role default model opus, not catalog-first fable", a)
	}
}

func TestSpawnOrchestratorConflict(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Spawn(ctx, SpawnInput{ItemKey: "EPIC-1", Role: RoleOrchestrator, Kind: Fake, Model: "fake-1"})
	if err == nil || !strings.Contains(err.Error(), "already has an orchestrator") {
		t.Fatalf("expected conflict, got %v", err)
	}
}

type fakeAdapterWithPreRun struct {
	*adapter.Fake
}

func (f *fakeAdapterWithPreRun) Launch(spec adapter.Spec) (adapter.Launch, error) {
	return adapter.Launch{
		PreRun: [][]string{{"echo", "prerun-output"}},
		Argv:   []string{"fake-agent", adapter.PreRunOutput},
	}, nil
}

func TestSpawnWithPreRun(t *testing.T) {
	s, _, fa := newStore(t)
	s.Adapters[Fake] = &fakeAdapterWithPreRun{Fake: fa}
	fakeExec := &execx.Fake{
		Responses: map[string]execx.Result{
			"echo prerun-output": {Out: "prerun-output\n"},
		},
	}
	s.Exec = fakeExec.Runner()
	ctx := context.Background()
	seedEpicWithTask(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1", Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name == "" {
		t.Fatal("empty name")
	}
	if calls := fakeExec.Calls(); len(calls) != 1 || calls[0] != "echo prerun-output" {
		t.Fatalf("prerun calls = %v, want ['echo prerun-output']", calls)
	}
}

// fakeAdvisor is a minimal test double for the three-line runtime.Advisor
// interface (Task 12b brief), for tests that only need to confirm
// resolveAdvisor's wiring reaches s.Advisor.Mode, not Mode's own logic.
type fakeAdvisor struct{ mode string }

func (f fakeAdvisor) Mode(AgentKind, AgentKind, string, bool) string { return f.mode }
func (f fakeAdvisor) Ask(context.Context, string, string, []string, time.Duration) (Advice, error) {
	return Advice{}, errors.New("not used in this test")
}

// advisorCols reads back the four advisor_* columns for one agent row, the
// same way other tests in this file assert on inserted columns.
func advisorCols(t *testing.T, s *Store, agentID string) (kind, model, effort, mode string) {
	t.Helper()
	err := s.DB.QueryRowContext(context.Background(), `SELECT COALESCE(advisor_kind, ''),
		COALESCE(advisor_model, ''), COALESCE(advisor_effort, ''), COALESCE(advisor_mode, '')
		FROM agents WHERE id = ?`, agentID).Scan(&kind, &model, &effort, &mode)
	if err != nil {
		t.Fatal(err)
	}
	return kind, model, effort, mode
}

// TestSpawnUsesSettingsAdvisorDefaultWhenNoneChosen is Task 12b case 1: a
// Spawn with no Advisor on the input picks up the Settings role default
// (roleDefaults[RoleAdvisor] = {Claude, "fable", ""}) and, with a wired
// Advisor, its resolved mode.
func TestSpawnUsesSettingsAdvisorDefaultWhenNoneChosen(t *testing.T) {
	s, _, _ := newStore(t)
	s.Advisor = fakeAdvisor{mode: "simulated"}
	ctx := context.Background()
	seedEpicWithTask(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	kind, model, _, mode := advisorCols(t, s, a.ID)
	if kind != "claude" || model != "fable" {
		t.Fatalf("advisor kind/model = %q/%q, want claude/fable (Settings default)", kind, model)
	}
	if mode != "simulated" {
		t.Fatalf("advisor mode = %q, want simulated (from the wired fakeAdvisor)", mode)
	}
}

// TestSpawnAdvisorNoneOverridesSettings is Task 12b case 2: an explicit
// AdvisorChoice{None: true} wins over a real Settings advisor default.
func TestSpawnAdvisorNoneOverridesSettings(t *testing.T) {
	s, _, _ := newStore(t)
	s.Advisor = fakeAdvisor{mode: "simulated"}
	ctx := context.Background()
	seedEpicWithTask(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Advisor: &AdvisorChoice{None: true}, Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	kind, model, effort, mode := advisorCols(t, s, a.ID)
	if kind != "" || model != "" || effort != "" || mode != "" {
		t.Fatalf("advisor cols = %q/%q/%q/%q, want all empty (explicit none)", kind, model, effort, mode)
	}
}

// TestSpawnExplicitAdvisorChoiceOverridesSettings is Task 12b case 3: an
// explicit AdvisorChoice wins over whatever Settings has.
func TestSpawnExplicitAdvisorChoiceOverridesSettings(t *testing.T) {
	s, _, _ := newStore(t)
	s.Advisor = fakeAdvisor{mode: "native"}
	ctx := context.Background()
	seedEpicWithTask(t, s)
	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Advisor: &AdvisorChoice{Kind: Codex, Model: "some-model"}, Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	kind, model, _, mode := advisorCols(t, s, a.ID)
	if kind != "codex" || model != "some-model" {
		t.Fatalf("advisor kind/model = %q/%q, want codex/some-model (explicit choice)", kind, model)
	}
	if mode != "native" {
		t.Fatalf("advisor mode = %q, want native (from the wired fakeAdvisor)", mode)
	}
}

// TestSpawnPassesSettingsInstructionsToSpec is Task 6: Settings.Instructions,
// once persisted, must reach the adapter.Spec that Launch/Resume receives on
// every spawn -- not just get stored and never read.
func TestSpawnPassesSettingsInstructionsToSpec(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)

	// newStore's Settings.Get/Put round-trips EnabledAgents through
	// validate(), which rejects "fake" (not a real kinds.AgentKind) -- the
	// same reason newStore itself seeds enabled_agents with a raw INSERT
	// instead of Put. Match that idiom here for instructions.
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_at) VALUES ('instructions', ?, 1)`,
		`"# Test Instructions\nAlways verify."`); err != nil {
		t.Fatal(err)
	}

	_, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if fa.LastSpec.Instructions != "# Test Instructions\nAlways verify." {
		t.Fatalf("LastSpec.Instructions = %q, want the persisted Settings.Instructions", fa.LastSpec.Instructions)
	}
}

// TestSpawnPassesEmptyInstructionsWhenSettingsUnset is Task 6's default-case
// counterpart: no Settings.Instructions set means Spec.Instructions stays "".
func TestSpawnPassesEmptyInstructionsWhenSettingsUnset(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)

	_, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Brief: BriefInput{Objective: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if fa.LastSpec.Instructions != "" {
		t.Fatalf("LastSpec.Instructions = %q, want empty (no Settings.Instructions set)", fa.LastSpec.Instructions)
	}
}

// TestStartOrchestratorUsesSettingsAdvisorDefault is Task 12b case 4
// (StartOrchestrator half): the same resolveAdvisor wiring reached
// StartOrchestrator, not just Spawn.
func TestStartOrchestratorUsesSettingsAdvisorDefault(t *testing.T) {
	s, _, _ := newStore(t)
	s.Advisor = fakeAdvisor{mode: "simulated"}
	ctx := context.Background()
	seedEpicWithTask(t, s)
	a, queued, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil || queued {
		t.Fatalf("err = %v, queued = %v", err, queued)
	}
	kind, model, _, mode := advisorCols(t, s, a.ID)
	if kind != "claude" || model != "fable" || mode != "simulated" {
		t.Fatalf("advisor kind/model/mode = %q/%q/%q, want claude/fable/simulated", kind, model, mode)
	}
}

// TestStartSpikeUsesSettingsAdvisorDefault is Task 12b case 4 (StartSpike
// half, success path): the same resolveAdvisor wiring reached StartSpike.
func TestStartSpikeUsesSettingsAdvisorDefault(t *testing.T) {
	s, _, _ := newStore(t)
	s.Advisor = fakeAdvisor{mode: "simulated"}
	ctx := context.Background()
	_, a, queued, err := s.StartSpike(ctx, SpikeInput{Name: "Spike advisor", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil || queued {
		t.Fatalf("err = %v, queued = %v", err, queued)
	}
	kind, model, _, mode := advisorCols(t, s, a.ID)
	if kind != "claude" || model != "fable" || mode != "simulated" {
		t.Fatalf("advisor kind/model/mode = %q/%q/%q, want claude/fable/simulated", kind, model, mode)
	}
}

func TestSpawnWorkerPopulatesParentNameInBrief(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, orch, _, err := s.StartSpike(ctx, SpikeInput{
		Name:   "Parent Spike",
		Intent: "feature",
		Kind:   Fake,
		Model:  "fake-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// RoleResearcher, not RoleCoder: a spike has no task children yet (it
	// hasn't materialized), and gated roles can no longer be spawned on
	// anything but a task. Parent-name population is role-agnostic, so this
	// still exercises what the test is actually about.
	worker, _, err := s.Spawn(ctx, SpawnInput{
		ItemKey:       key,
		Role:          RoleResearcher,
		Kind:          Fake,
		Model:         "fake-1",
		ParentAgentID: orch.ID,
		Name:          "child-coder",
		Brief:         BriefInput{Objective: "Implement feature"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(worker.Brief, fmt.Sprintf("parent: %s", orch.Name)) {
		t.Fatalf("expected brief to contain 'parent: %s', got:\n%s", orch.Name, worker.Brief)
	}
}

func TestSpawnResolvesAgentFromModelAndNormalizesImplementer(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)

	a, _, err := s.Spawn(ctx, SpawnInput{
		ItemKey: "TASK-1",
		Role:    "implementer",
		Model:   "gpt-6-astra",
		Kind:    "",
		Brief:   BriefInput{Objective: "task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Role != RoleCoder {
		t.Fatalf("agent.Role = %q, want %q", a.Role, RoleCoder)
	}
	if a.Kind != Codex {
		t.Fatalf("agent.Kind = %q, want %q", a.Kind, Codex)
	}

	story, err := s.Items.Create(ctx, items.CreateInput{
		Type:      items.Story,
		ParentKey: "EPIC-1",
		Title:     "Story without tasks",
	}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id = ?`, story.ID); err != nil {
		t.Fatal(err)
	}

	_, _, err = s.Spawn(ctx, SpawnInput{
		ItemKey: story.Key,
		Role:    "implementer",
		Model:   "gpt-6-astra",
		Kind:    "",
		Brief:   BriefInput{Objective: "task"},
	})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("Spawn coder on a task, not %s.", story.Key)) {
		t.Fatalf("expected rejection 'Spawn coder on a task, not %s.', got %v", story.Key, err)
	}
}

func TestOrchestratorRoleOverridesInheritedByWorkers(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)

	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{
		ItemKey: "EPIC-1",
		Kind:    Codex,
		Model:   "gpt-6-astra",
		Roles: map[Role]settings.RoleDefault{
			RoleCoder: {Agent: Codex, Model: "gpt-6-astra", Effort: "high"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	worker, _, err := s.Spawn(ctx, SpawnInput{
		ItemKey:       "TASK-1",
		ParentAgentID: orch.ID,
		Role:          RoleCoder,
		Kind:          "",
		Model:         "",
		Brief:         BriefInput{Objective: "task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if worker.Kind != Codex {
		t.Fatalf("worker.Kind = %q, want %q", worker.Kind, Codex)
	}
	if worker.Model != "gpt-6-astra" {
		t.Fatalf("worker.Model = %q, want %q", worker.Model, "gpt-6-astra")
	}
	if worker.Effort != "high" {
		t.Fatalf("worker.Effort = %q, want %q", worker.Effort, "high")
	}

	loadedOrch, err := s.AgentByID(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedOrch.RoleOverrides[RoleCoder].Model != "gpt-6-astra" {
		t.Fatalf("loadedOrch role override model = %q, want %q", loadedOrch.RoleOverrides[RoleCoder].Model, "gpt-6-astra")
	}

	tree, err := s.AgentTree(ctx, "EPIC-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) < 2 {
		t.Fatalf("tree length = %d, want at least 2", len(tree))
	}
	if tree[0].RoleOverrides[RoleCoder].Model != "gpt-6-astra" {
		t.Fatalf("tree[0] role override model = %q, want %q", tree[0].RoleOverrides[RoleCoder].Model, "gpt-6-astra")
	}
}

