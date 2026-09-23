package usage

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func TestPollerJitterMinimumGapAndStale(t *testing.T) {
	d := dbtest.Open(t)
	at := newClk()
	calls := map[runtime.AgentKind]int{}
	p := &Poller{DB: d, Events: events.New(d, at.Now),
		Settings: settingsWith(t, d, 300), Now: at.Now,
		Jitter:  func(d time.Duration) time.Duration { return d }, // deterministic
		Sources: []Source{stubSource(runtime.Claude, calls), stubSource(runtime.Codex, calls)},
		Log:     func(string, ...any) {}}
	ctx := context.Background()
	if err := p.RefreshOne(ctx, runtime.Claude); err != nil {
		t.Fatal(err)
	}
	at.Advance(30 * time.Second)
	err := p.RefreshOne(ctx, runtime.Claude)
	if err == nil || !strings.Contains(err.Error(), "limit_reached") {
		t.Fatalf("a manual refresh is allowed once every 60 s: %v", err)
	}
	at.Advance(31 * time.Second)
	if err := p.RefreshOne(ctx, runtime.Claude); err != nil {
		t.Fatal(err)
	}
	if calls[runtime.Claude] != 2 {
		t.Fatalf("claude fetched %d times", calls[runtime.Claude])
	}
	// stale when the last success is older than 3 × poll
	at.Advance(16 * time.Minute)
	snaps, _ := p.Snapshots(ctx)
	for _, s := range snaps {
		if s.Agent == runtime.Claude && !s.Stale {
			t.Fatal("a snapshot older than 3 × usage_poll_sec is stale")
		}
	}
}

func TestPollerSkipsDisabledAgentsAndKeepsOldMeters(t *testing.T) {
	d := dbtest.Open(t)
	at := newClk()
	calls := map[runtime.AgentKind]int{}
	failing := failingSource(runtime.Codex, errors.New("network is unreachable"))
	p := &Poller{DB: d, Events: events.New(d, at.Now),
		Settings: settingsWithEnabled(t, d, runtime.Claude), Now: at.Now,
		Jitter:  func(d time.Duration) time.Duration { return d },
		Sources: []Source{stubSource(runtime.Claude, calls), failing}, Log: func(string, ...any) {}}
	ctx := context.Background()
	if err := p.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if calls[runtime.Claude] != 1 {
		t.Fatalf("claude was polled %d times", calls[runtime.Claude])
	}
	if calls[runtime.Codex] != 0 {
		t.Fatalf("codex is not enabled in Settings; it was polled %d times", calls[runtime.Codex])
	}

	// enable codex and let its fetch fail: the previous meters and fetched_at survive
	setEnabled(t, p.Settings, runtime.Claude, runtime.Codex)
	at.Advance(6 * time.Minute)
	if err := p.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(6 * time.Minute)
	if err := p.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	snaps, err := p.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var codex, claude Snapshot
	for _, sn := range snaps {
		switch sn.Agent {
		case runtime.Codex:
			codex = sn
		case runtime.Claude:
			claude = sn
		}
	}
	if codex.Error == "" {
		t.Fatal("a failed fetch records the error")
	}
	if !codex.AttemptedAt.After(codex.FetchedAt) {
		t.Fatal("attempted_at moves on a failure; fetched_at does not")
	}
	if !codex.Stale {
		t.Fatal("attempted_at > fetched_at means stale")
	}
	if len(claude.Meters) == 0 {
		t.Fatal("the successful claude snapshot kept its meters")
	}
}

// A source's own Retry-After (RateLimitError) must be honored: re-fetching
// before it elapses only extends the real endpoint's penalty further (this
// is the exact bug that left Claude's usage permanently 429ing — every 300s
// poll re-tripped a bucket that wanted ~17 minutes to clear). This proves
// the poller skips the network call until the backoff clears, then resumes.
func TestPollerHonorsASourcesRetryAfterBackoff(t *testing.T) {
	d := dbtest.Open(t)
	at := newClk()
	calls := map[runtime.AgentKind]int{}
	rateLimited := true
	src := Source{Agent: runtime.Claude, Fetch: func(ctx context.Context) ([]Meter, string, error) {
		calls[runtime.Claude]++
		if rateLimited {
			return nil, "", &RateLimitError{Err: errors.New("claude: status 429"), RetryAfter: 10 * time.Minute}
		}
		return []Meter{{ID: "five_hour", Label: "5h", Window: "5h", UsedPct: 5}}, "five_hour", nil
	}}
	p := &Poller{DB: d, Events: events.New(d, at.Now),
		Settings: settingsWithEnabled(t, d, runtime.Claude), Now: at.Now,
		Jitter:  func(d time.Duration) time.Duration { return d },
		Sources: []Source{src}, Log: func(string, ...any) {}}
	ctx := context.Background()

	if err := p.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if calls[runtime.Claude] != 1 {
		t.Fatalf("first poll: fetched %d times, want 1", calls[runtime.Claude])
	}

	// Still inside the 10-minute backoff: must not hit the source again.
	at.Advance(5 * time.Minute)
	if err := p.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if calls[runtime.Claude] != 1 {
		t.Fatalf("mid-backoff poll: fetched %d times, want still 1 (no re-fetch before Retry-After elapses)", calls[runtime.Claude])
	}
	snaps, err := p.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snaps[0].Error == "" || !strings.Contains(snaps[0].Error, "retry in") {
		t.Fatalf("error = %q, want it to mention the remaining backoff", snaps[0].Error)
	}

	// Past the backoff, and the source has recovered: fetch resumes and succeeds.
	rateLimited = false
	at.Advance(6 * time.Minute)
	if err := p.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if calls[runtime.Claude] != 2 {
		t.Fatalf("post-backoff poll: fetched %d times, want 2", calls[runtime.Claude])
	}
	snaps, err = p.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps[0].Meters) == 0 {
		t.Fatal("the recovered fetch should have stored real meters")
	}
}

// S-4: the default must be inert. If this ever returns sources, every `make e2e`
// run reads the user's login keychain and calls api.anthropic.com.
func TestSourcesFromEnvIsEmptyUnlessExplicitlyEnabled(t *testing.T) {
	for _, v := range []string{"", "1", "true", "yes", "dev"} {
		got := SourcesFromEnv(func(string) string { return v },
			t.TempDir(), "u", http.DefaultClient, nil, nil)
		if len(got) != 0 {
			t.Fatalf("SWARM_USAGE=%q gave %d sources; only \"live\" enables them", v, len(got))
		}
	}
	live := SourcesFromEnv(func(k string) string {
		if k == "SWARM_USAGE" {
			return "live"
		}
		return ""
	}, t.TempDir(), "u", http.DefaultClient, nil, nil)
	if len(live) != 5 {
		t.Fatalf("SWARM_USAGE=live gave %d sources, want 5", len(live))
	}
}
