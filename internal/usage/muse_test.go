package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// The fixture is three verbatim usage events from a real redacted
// `muse export` (2026-09-23): events[i].envelope.payload.event.usage carries
// input/output/reasoning/cache tokens. record/quantity objects elsewhere in a
// full export duplicate the same turns and must NOT be summed.
func TestParseMuseExportSumsUsageEvents(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "muse-export.json"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := ParseMuseExport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 88820 || u.OutputTokens != 1390 || u.ReasoningTokens != 559 ||
		u.CacheReadTokens != 61907 || u.CacheWriteTokens != 0 {
		t.Errorf("usage = %+v, want exact fixture sums", u)
	}
	if u.Turns != 3 {
		t.Errorf("turns = %d, want 3", u.Turns)
	}
}

func TestParseMuseExportIgnoresQuantityDups(t *testing.T) {
	raw := []byte(`{"events":[
		{"envelope":{"payload":{"event":{"usage":{"input_tokens":10,"output_tokens":2}}}}},
		{"envelope":{"payload":{"event":{"record":{"quantity":{"input_tokens":10,"output_tokens":2}}}}}}
	]}`)
	u, err := ParseMuseExport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 10 || u.OutputTokens != 2 || u.Turns != 1 {
		t.Errorf("quantity dup was double-counted: %+v", u)
	}
}

// Live: exports a real session and proves nonzero token sums come back.
// Offline and free (reads the local session log), but gated like all live
// probes: CI machines have no muse sessions.
//
//	MUSE_LIVE_PROBE=1 MUSE_PROBE_SESSION=<uuid> go test ./internal/usage/ -run TestSessionUsageLive -v
func TestSessionUsageLive(t *testing.T) {
	if os.Getenv("MUSE_LIVE_PROBE") != "1" {
		t.Skip("live probe against real muse session log; set MUSE_LIVE_PROBE=1 to run")
	}
	id := os.Getenv("MUSE_PROBE_SESSION")
	if id == "" {
		t.Skip("set MUSE_PROBE_SESSION=<session uuid> to a retained muse session")
	}
	u, err := MuseSessionUsage(context.Background(), execx.Run, t.TempDir(), id)
	if err != nil {
		t.Fatal(err)
	}
	if u.Turns == 0 || u.InputTokens == 0 {
		t.Errorf("live export returned no usage: %+v", u)
	}
	t.Logf("turns=%d in=%d out=%d reasoning=%d", u.Turns, u.InputTokens, u.OutputTokens, u.ReasoningTokens)
}

func TestParseMuseExportRejectsGarbage(t *testing.T) {
	if _, err := ParseMuseExport([]byte(`{"events":[`)); err == nil {
		t.Error("truncated JSON must error")
	}
	if _, err := ParseMuseExport([]byte(`{"sessions":[]}`)); err == nil {
		t.Error("missing events must error, never an empty success")
	}
}

// ---------------------------------------------------------------------------
// Muse (MSP subscription quota) — the `muse serve` source.
// ---------------------------------------------------------------------------

// museUsagePayload is the exact usage member a real host returned on
// 2026-09-23 (docs/specs/2026-09-23-muse-usage-probe.md §4d).
const museUsagePayload = `{"window":{"usedPercent":0,"windowDurationMins":300,"resetsAtMs":1790208865000},` +
	`"weekly":{"usedPercent":9,"resetsAtMs":1790553600000},` +
	`"tier":"27681393394859588","observedAtMs":1790191416036}`

// fakeMuseHost is a *stateful* scripted MSP host. Muse.Fetch only sends
// turn/start once session/start is answered and only polls usage/read after
// that, so replies have to be driven by the requests actually arriving on
// stdin — a dumb playback like fakeAppServer's would deadlock. usageAfterTurn
// is the usage member returned once the turn has started; "" means the host
// never observes anything, which is the PAYG case.
func fakeMuseHost(t *testing.T, usageAfterTurn string, handshakeOnly bool,
	sent *[]string, spawns *int, killed *bool) execx.Starter {
	t.Helper()
	var mu sync.Mutex
	return func(ctx context.Context, name string, args ...string) (*execx.Proc, error) {
		if spawns != nil {
			mu.Lock()
			*spawns++
			mu.Unlock()
		}
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		go func() {
			turnStarted := false
			dec := json.NewDecoder(inR)
			for {
				var req struct {
					ID     *int   `json:"id"`
					Method string `json:"method"`
				}
				if err := dec.Decode(&req); err != nil {
					return
				}
				mu.Lock()
				if sent != nil {
					*sent = append(*sent, req.Method)
				}
				mu.Unlock()
				if req.ID == nil {
					continue // a notification: never answered
				}
				var body string
				switch req.Method {
				case "initialize":
					body = `{"serverInfo":{"name":"muse","version":"1.3.0"},"museHome":"/h",` +
						`"platformFamily":"unix","platformOs":"macos","schema":{"version":1,"fingerprint":"x"},` +
						`"grantedCapabilities":[],"experimentalApi":false,"userAgent":"muse-build/1.3.0"}`
				case "session/start":
					if handshakeOnly {
						continue // a host that goes silent after the handshake
					}
					body = `{"session":{"sessionId":"01a0cfb8-edfa-7e7b-9a7f-c3f19814398d","status":"idle"}}`
				case "turn/start":
					turnStarted = true
					body = `{"commandId":"c","status":"accepted","turnId":"t","startedNewTurn":true,"disposition":"started"}`
				case "usage/read":
					// Truthful absence before the turn's frame arrives: the
					// member is omitted, never null (ADR 32563 D2).
					body = `{}`
					if turnStarted && usageAfterTurn != "" {
						body = `{"usage":` + usageAfterTurn + `}`
					}
				default:
					continue
				}
				if _, err := fmt.Fprintf(outW, `{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n",
					*req.ID, body); err != nil {
					return
				}
			}
		}()
		return &execx.Proc{Stdin: inW, Stdout: outR, Kill: func() {
			if killed != nil {
				*killed = true
			}
			inR.Close()
			inW.Close()
			outR.Close()
			outW.Close()
		}}, nil
	}
}

func TestMuseFetchMapsSubscriptionUsage(t *testing.T) {
	m := &Muse{Start: ignoreEnv(fakeMuseHost(t, museUsagePayload, false, nil, nil, nil)),
		Dir: t.TempDir(), Timeout: 5 * time.Second}
	meters, headline, err := m.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 2 {
		t.Fatalf("got %d meters, want 2: %+v", len(meters), meters)
	}
	byID := map[string]Meter{}
	for _, mt := range meters {
		byID[mt.ID] = mt
	}
	five := byID["5h"]
	if five.Label != "5h" || five.Window != "5h" || five.UsedPct != 0 {
		t.Errorf("5h meter = %+v", five)
	}
	if five.ResetsAt == nil || !five.ResetsAt.Equal(time.UnixMilli(1790208865000)) {
		t.Errorf("5h resets at %v, want %v", five.ResetsAt, time.UnixMilli(1790208865000))
	}
	week := byID["weekly"]
	if week.Label != "Weekly" || week.Window != "weekly" || week.UsedPct != 9 {
		t.Errorf("weekly meter = %+v", week)
	}
	if week.ResetsAt == nil || !week.ResetsAt.Equal(time.UnixMilli(1790553600000)) {
		t.Errorf("weekly resets at %v, want %v", week.ResetsAt, time.UnixMilli(1790553600000))
	}
	if headline != "5h" {
		t.Errorf("headline = %q, want %q", headline, "5h")
	}
}

// recordingWriteCloser tees every line Muse.Fetch writes to the host.
type recordingWriteCloser struct {
	io.WriteCloser
	mu    *sync.Mutex
	lines *[]string
}

func (r *recordingWriteCloser) Write(p []byte) (int, error) {
	r.mu.Lock()
	*r.lines = append(*r.lines, strings.TrimSpace(string(p)))
	r.mu.Unlock()
	return r.WriteCloser.Write(p)
}

// A hyphen in clientInfo.name fails the handshake SILENTLY against a real
// host: the initialize response still arrives, the initialized notification
// is dropped (FR-008), and every later call answers notInitialized. Pinned
// here because no fake can reproduce a failure that looks like success.
func TestMuseHandshakeUsesAWireLegalClientName(t *testing.T) {
	var mu sync.Mutex
	var raw []string
	inner := fakeMuseHost(t, museUsagePayload, false, nil, nil, nil)
	start := func(ctx context.Context, name string, args ...string) (*execx.Proc, error) {
		proc, err := inner(ctx, name, args...)
		if err != nil {
			return nil, err
		}
		proc.Stdin = &recordingWriteCloser{WriteCloser: proc.Stdin, mu: &mu, lines: &raw}
		return proc, nil
	}
	m := &Muse{Start: ignoreEnv(start), Dir: t.TempDir(), Timeout: 5 * time.Second}
	if _, _, err := m.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var initParams struct {
		Params struct {
			ClientInfo struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		} `json:"params"`
	}
	var methods []string
	mu.Lock()
	defer mu.Unlock()
	for _, l := range raw {
		var probe struct {
			Method string `json:"method"`
		}
		if json.Unmarshal([]byte(l), &probe) != nil {
			continue
		}
		methods = append(methods, probe.Method)
		if probe.Method == "initialize" {
			if err := json.Unmarshal([]byte(l), &initParams); err != nil {
				t.Fatal(err)
			}
		}
	}
	legal := regexp.MustCompile(`^[a-z0-9_]+$`)
	if got := initParams.Params.ClientInfo.Name; !legal.MatchString(got) {
		t.Errorf("clientInfo.name = %q, must match ^[a-z0-9_]+$ or the handshake fails silently", got)
	}
	want := []string{"initialize", "initialized", "session/start", "turn/start", "usage/read"}
	if len(methods) < len(want) {
		t.Fatalf("sent %v, want at least %v", methods, want)
	}
	for i, w := range want {
		if methods[i] != w {
			t.Fatalf("sent %v, want the handshake order %v", methods, want)
		}
	}
}

func TestMuseFetchErrsWhenNoUsageIsObserved(t *testing.T) {
	m := &Muse{Start: ignoreEnv(fakeMuseHost(t, "", false, nil, nil, nil)),
		Dir: t.TempDir(), Timeout: 3 * time.Second}
	meters, _, err := m.Fetch(context.Background())
	if err == nil {
		t.Fatal("a host that observes nothing must error, never be an empty success")
	}
	if !strings.Contains(err.Error(), "no subscription usage") {
		t.Errorf("err = %v, want it to name the missing usage", err)
	}
	if len(meters) != 0 {
		t.Errorf("meters = %+v, want none", meters)
	}
}

func TestMuseFetchTimesOutAndKillsTheHost(t *testing.T) {
	killed := false
	m := &Muse{Start: ignoreEnv(fakeMuseHost(t, "", true, nil, nil, &killed)),
		Dir: t.TempDir(), Timeout: 50 * time.Millisecond}
	if _, _, err := m.Fetch(context.Background()); err == nil {
		t.Fatal("a host that goes silent must time out")
	}
	if !killed {
		t.Error("the host process must be killed on the way out")
	}
}

func TestMuseFetchServesTheCacheInsideTheProbeGap(t *testing.T) {
	spawns := 0
	c := newClk()
	m := &Muse{Start: ignoreEnv(fakeMuseHost(t, museUsagePayload, false, nil, &spawns, nil)),
		Dir: t.TempDir(), Timeout: 5 * time.Second, Now: c.Now}
	first, _, err := m.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if spawns != 1 {
		t.Fatalf("first fetch spawned %d hosts, want 1", spawns)
	}
	c.Advance(5 * time.Minute)
	again, headline, err := m.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if spawns != 1 {
		t.Errorf("a fetch inside the probe gap spent a turn (%d spawns); it must serve the cache", spawns)
	}
	if len(again) != len(first) || again[0].UsedPct != first[0].UsedPct || headline != "5h" {
		t.Errorf("cached fetch = %+v/%q, want the first observation %+v", again, headline, first)
	}
}

func TestMuseFetchProbesAgainAfterTheGap(t *testing.T) {
	spawns := 0
	c := newClk()
	m := &Muse{Start: ignoreEnv(fakeMuseHost(t, museUsagePayload, false, nil, &spawns, nil)),
		Dir: t.TempDir(), Timeout: 5 * time.Second, Now: c.Now}
	if _, _, err := m.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.Advance(16 * time.Minute)
	if _, _, err := m.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spawns != 2 {
		t.Errorf("spawned %d hosts, want 2 — the gap elapsed, so a fresh probe is due", spawns)
	}
}

// ignoreEnv adapts a plain fake Starter for tests that don't care about env.
func ignoreEnv(s execx.Starter) execx.StarterEnv {
	return func(ctx context.Context, _ map[string]string, name string, args ...string) (*execx.Proc, error) {
		return s(ctx, name, args...)
	}
}

// 1.4.x background reminder agents multiply a probe's model requests ~3.8x;
// the probe host must start with every one of them switched off.
func TestMuseProbeHostDisablesReminderAgents(t *testing.T) {
	inner := fakeMuseHost(t, museUsagePayload, false, nil, nil, nil)
	var got map[string]string
	m := &Muse{Start: func(ctx context.Context, env map[string]string, name string, args ...string) (*execx.Proc, error) {
		got = env
		return inner(ctx, name, args...)
	}, Dir: t.TempDir(), Timeout: 5 * time.Second}
	if _, _, err := m.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"TODO", "MEMORY", "SKILL", "GOAL", "VERIFY", "SCOPE"} {
		if v := got["MUSE_EXPERIMENTAL_"+k+"_REMINDER"]; v != "0" {
			t.Errorf("MUSE_EXPERIMENTAL_%s_REMINDER = %q, want \"0\"", k, v)
		}
	}
}

// museIndex creates a session-index.db like Muse's and returns a setter for
// max(updated_at_us), the only column the activity gate reads.
func museIndex(t *testing.T) (path string, touch func(us int64)) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "session-index.db")
	d, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, updated_at_us INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return path, func(us int64) {
		if _, err := d.Exec(`INSERT INTO sessions (id, updated_at_us) VALUES ('s', ?)
			ON CONFLICT(id) DO UPDATE SET updated_at_us = excluded.updated_at_us`, us); err != nil {
			t.Fatal(err)
		}
	}
}

func gatedMuse(t *testing.T, payload, index string, spawns *int, c *clk) *Muse {
	return &Muse{Start: ignoreEnv(fakeMuseHost(t, payload, false, nil, spawns, nil)),
		Dir: t.TempDir(), Timeout: 5 * time.Second, Now: c.Now, SessionIndex: index}
}

func TestMuseIdleSessionIndexNeverReprobesHoweverOld(t *testing.T) {
	idx, touch := museIndex(t)
	c := newClk()
	touch(c.Now().UnixMicro())
	spawns := 0
	m := gatedMuse(t, museUsagePayload, idx, &spawns, c)
	fetch := func() {
		t.Helper()
		if _, _, err := m.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	fetch()
	c.Advance(time.Hour) // past ProbeGap, Muse untouched
	fetch()
	c.Advance(72 * time.Hour) // far past the old 2h ceiling, still untouched
	fetch()
	if spawns != 1 {
		t.Fatalf("idle Muse was probed again (%d spawns); no idle re-probe at any age", spawns)
	}
}

func TestMuseActivityProbesAtMostOncePerGap(t *testing.T) {
	idx, touch := museIndex(t)
	c := newClk()
	touch(c.Now().UnixMicro())
	spawns := 0
	m := gatedMuse(t, museUsagePayload, idx, &spawns, c)
	fetch := func() {
		t.Helper()
		if _, _, err := m.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	fetch()
	c.Advance(5 * time.Minute)
	touch(c.Now().UnixMicro()) // Muse ran, but still inside the gap
	fetch()
	if spawns != 1 {
		t.Fatalf("activity inside ProbeGap must not probe, spawns = %d", spawns)
	}
	c.Advance(11 * time.Minute)
	fetch()
	fetch()
	if spawns != 2 {
		t.Fatalf("activity past the gap must probe exactly once, spawns = %d", spawns)
	}
	c.Advance(3 * time.Hour) // the probe saw index state 200; nothing new
	fetch()
	if spawns != 2 {
		t.Errorf("no new activity since the last probe, spawns = %d", spawns)
	}
}

func TestMuseNoCacheProbesOnlyOnRecentActivity(t *testing.T) {
	idx, touch := museIndex(t)
	c := newClk()
	touch(c.Now().Add(-time.Hour).UnixMicro()) // last Muse use an hour ago
	spawns := 0
	m := gatedMuse(t, museUsagePayload, idx, &spawns, c)
	meters, _, err := m.Fetch(context.Background())
	if err != nil || len(meters) != 0 || spawns != 0 {
		t.Fatalf("idle + no cache: meters %v, err %v, spawns %d; want none, no error, no probe", meters, err, spawns)
	}
	touch(c.Now().Add(-5 * time.Minute).UnixMicro()) // Muse used just now
	meters, _, err = m.Fetch(context.Background())
	if err != nil || len(meters) == 0 || spawns != 1 {
		t.Fatalf("recent activity + no cache must probe: meters %v, err %v, spawns %d", meters, err, spawns)
	}
}

func TestMuseSeededCacheProbesOnlyOnActivityAfterTheObservation(t *testing.T) {
	d := dbtest.Open(t)
	c := newClk()
	p := &Poller{DB: d, Events: events.New(d, c.Now), Settings: settingsWith(t, d, 300), Now: c.Now}
	seed := []Meter{{ID: "5h", Label: "5h", Window: "5h", UsedPct: 33}}
	ctx := context.Background()
	if err := p.storeSuccess(ctx, runtime.Muse, c.Now(), seed, "5h"); err != nil {
		t.Fatal(err)
	}
	idx, touch := museIndex(t)
	touch(c.Now().Add(-time.Hour).UnixMicro()) // older than the seeded observation
	spawns := 0
	m := gatedMuse(t, museUsagePayload, idx, &spawns, c)
	if err := m.SeedFromSnapshot(ctx, d); err != nil {
		t.Fatal(err)
	}
	c.Advance(10 * time.Hour)
	if got, _, err := m.Fetch(ctx); err != nil || spawns != 0 || got[0].UsedPct != 33 {
		t.Fatalf("idle since the seeded observation: got %+v, spawns %d, err %v", got, spawns, err)
	}
	touch(c.Now().UnixMicro()) // Muse ran after the observation
	if _, _, err := m.Fetch(ctx); err != nil || spawns != 1 {
		t.Errorf("activity after the seeded observation must probe: spawns %d, err %v", spawns, err)
	}
}

func TestMuseGatedCacheRollsPassedResetWindowsToZero(t *testing.T) {
	idx, touch := museIndex(t)
	c := newClk() // 2026-09-17 12:00 UTC: both windows still ahead
	touch(c.Now().UnixMicro())
	payload := `{"window":{"usedPercent":40,"windowDurationMins":300,"resetsAtMs":1790208865000},` +
		`"weekly":{"usedPercent":9,"resetsAtMs":1790553600000},"tier":"t","observedAtMs":1}`
	spawns := 0
	m := gatedMuse(t, payload, idx, &spawns, c)
	first, _, err := m.Fetch(context.Background())
	if err != nil || first[0].UsedPct != 40 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	c.Advance(7 * 24 * time.Hour) // past the 5h reset (09-24), before the weekly (09-28)
	got, _, err := m.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if spawns != 1 {
		t.Fatalf("spawns = %d, want 1", spawns)
	}
	if got[0].UsedPct != 0 || got[1].UsedPct != 9 {
		t.Errorf("used = %v/%v, want the passed 5h window at 0 and the weekly kept at 9", got[0].UsedPct, got[1].UsedPct)
	}
	if got[0].ResetsAt == nil || got[0].ResetsAt.UnixMilli() != 1790208865000 {
		t.Errorf("rolled window lost its raw reset stamp: %v", got[0].ResetsAt)
	}
	if m.cached[0].UsedPct != 40 {
		t.Error("rolling must not overwrite the cached observation")
	}
}

func TestMuseUnreadableSessionIndexFallsBackToTheGapRule(t *testing.T) {
	spawns := 0
	c := newClk()
	m := gatedMuse(t, museUsagePayload, filepath.Join(t.TempDir(), "missing.db"), &spawns, c)
	for _, adv := range []time.Duration{0, 5 * time.Minute, 16 * time.Minute} {
		c.Advance(adv)
		if _, _, err := m.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if spawns != 2 {
		t.Errorf("spawns = %d, want 2: an unreadable index must behave like today's 15m gap", spawns)
	}
}

// A gated Muse fetch replays an old reading: fetched_at must carry when it was
// really observed (the menubar shows "Updated 10h ago"), and that age alone
// must not flag the snapshot stale.
func TestMuseReplayedReadingKeepsItsObservationTimeAndIsNotStale(t *testing.T) {
	idx, touch := museIndex(t)
	c := newClk()
	touch(c.Now().UnixMicro())
	spawns := 0
	d := dbtest.Open(t)
	m := gatedMuse(t, museUsagePayload, idx, &spawns, c)
	p := &Poller{DB: d, Events: events.New(d, c.Now), Settings: settingsWith(t, d, 300), Now: c.Now,
		Log: func(string, ...any) {}, Sources: []Source{{Agent: runtime.Muse,
			Fetch: m.Fetch, ObservedAt: m.ObservedAt}}}
	ctx := context.Background()
	if err := p.RefreshOne(ctx, runtime.Muse); err != nil {
		t.Fatal(err)
	}
	observed := c.Now()
	c.Advance(10 * time.Hour)
	if err := p.RefreshOne(ctx, runtime.Muse); err != nil {
		t.Fatal(err)
	}
	snaps, err := p.Snapshots(ctx)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snaps = %+v, %v", snaps, err)
	}
	s := snaps[0]
	if spawns != 1 || s.Stale || s.Error != "" {
		t.Errorf("spawns = %d, stale = %v, error = %q; want 1 probe and a fresh snapshot", spawns, s.Stale, s.Error)
	}
	if !s.FetchedAt.Equal(observed) {
		t.Errorf("FetchedAt = %v, want the observation time %v", s.FetchedAt, observed)
	}
	if !s.AttemptedAt.Equal(c.Now()) {
		t.Errorf("AttemptedAt = %v, want the replay time %v", s.AttemptedAt, c.Now())
	}
}

func TestMuseSeededFromSnapshotDoesNotProbeWithinTheGap(t *testing.T) {
	d := dbtest.Open(t)
	c := newClk()
	p := &Poller{DB: d, Events: events.New(d, c.Now), Settings: settingsWith(t, d, 300), Now: c.Now}
	seed := []Meter{{ID: "5h", Label: "5h", Window: "5h", UsedPct: 33}}
	ctx := context.Background()
	if err := p.storeSuccess(ctx, runtime.Muse, c.Now(), seed, "5h"); err != nil {
		t.Fatal(err)
	}
	spawns := 0
	m := &Muse{Start: ignoreEnv(fakeMuseHost(t, museUsagePayload, false, nil, &spawns, nil)),
		Dir: t.TempDir(), Timeout: 5 * time.Second, Now: c.Now}
	if err := m.SeedFromSnapshot(ctx, d); err != nil {
		t.Fatal(err)
	}
	c.Advance(5 * time.Minute) // a daemon restart inside the gap
	got, headline, err := m.Fetch(ctx)
	if err != nil || spawns != 0 || len(got) != 1 || got[0].UsedPct != 33 || headline != "5h" {
		t.Fatalf("got %+v/%q, spawns %d, err %v; want the persisted snapshot with no probe", got, headline, spawns, err)
	}
	c.Advance(16 * time.Minute)
	if _, _, err := m.Fetch(ctx); err != nil || spawns != 1 {
		t.Errorf("past the gap the seeded cache must probe: spawns %d, err %v", spawns, err)
	}
}

func TestMuseSeedIgnoresMissingAndFailedSnapshots(t *testing.T) {
	d := dbtest.Open(t)
	c := newClk()
	p := &Poller{DB: d, Events: events.New(d, c.Now), Settings: settingsWith(t, d, 300), Now: c.Now}
	m := &Muse{Now: c.Now}
	if err := m.SeedFromSnapshot(context.Background(), d); err != nil || len(m.cached) != 0 {
		t.Fatalf("no row: err %v, cached %v", err, m.cached)
	}
	if err := p.storeFailure(context.Background(), runtime.Muse, c.Now(), errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if err := m.SeedFromSnapshot(context.Background(), d); err != nil || len(m.cached) != 0 {
		t.Errorf("failed row (fetched_at 0): err %v, cached %v; must not seed", err, m.cached)
	}
}

func TestMuseFailedProbesBackOffExponentiallyAndSuccessResets(t *testing.T) {
	failing := fakeMuseHost(t, "", true, nil, nil, nil)
	healthy := fakeMuseHost(t, museUsagePayload, false, nil, nil, nil)
	cur := failing
	m := &Muse{Start: ignoreEnv(func(ctx context.Context, name string, args ...string) (*execx.Proc, error) {
		return cur(ctx, name, args...)
	}), Dir: t.TempDir(), Timeout: 20 * time.Millisecond, Now: newClk().Now}
	retry := func() time.Duration {
		t.Helper()
		_, _, err := m.Fetch(context.Background())
		var rl *RateLimitError
		if !errors.As(err, &rl) {
			t.Fatalf("err = %v, want *RateLimitError so the poller backs off", err)
		}
		return rl.RetryAfter
	}
	for i, want := range []time.Duration{15 * time.Minute, 30 * time.Minute, 60 * time.Minute,
		2 * time.Hour, 2 * time.Hour} {
		if got := retry(); got != want {
			t.Errorf("failure %d: RetryAfter = %s, want %s", i+1, got, want)
		}
	}
	cur = healthy
	if _, _, err := m.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	cur = failing
	m.at = time.Time{} // force a probe again
	if got := retry(); got != 15*time.Minute {
		t.Errorf("after a success RetryAfter = %s, want the 15m start", got)
	}
}

// Live: spawns the real `muse serve`, SPENDS ONE MINIMAL TURN, and proves the
// meters come back shaped like every other source's. Gated because it costs
// quota and needs a logged-in muse.
//
//	MUSE_LIVE_PROBE=1 go test ./internal/usage/ -run TestMuseFetchLive -v
func TestMuseFetchLive(t *testing.T) {
	if os.Getenv("MUSE_LIVE_PROBE") != "1" {
		t.Skip("live probe spends one real muse turn; set MUSE_LIVE_PROBE=1 to run")
	}
	m := &Muse{Start: execx.StartEnv, Dir: t.TempDir(), Timeout: 120 * time.Second}
	meters, headline, err := m.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 2 || headline == "" {
		t.Fatalf("got %d meters, headline %q: %+v", len(meters), headline, meters)
	}
	for _, mt := range meters {
		if mt.Window == "" || mt.ResetsAt == nil {
			t.Errorf("meter %+v is missing its window or reset stamp", mt)
		}
		t.Logf("%s %s %.0f%% resets %s", mt.ID, mt.Window, mt.UsedPct, mt.ResetsAt.UTC())
	}
}

func TestMuseProbeInducedIndexBumpDoesNotRetrigger(t *testing.T) {
	idx, touch := museIndex(t)
	c := newClk()
	touch(c.Now().UnixMicro())
	spawns := 0
	host := fakeMuseHost(t, museUsagePayload, false, nil, &spawns, nil)
	m := &Muse{Dir: t.TempDir(), Timeout: 5 * time.Second, Now: c.Now, SessionIndex: idx,
		Start: func(ctx context.Context, _ map[string]string, name string, args ...string) (*execx.Proc, error) {
			touch(c.Now().UnixMicro() + 1) // the probe host itself bumps the index
			return host(ctx, name, args...)
		}}
	fetch := func() {
		t.Helper()
		if _, _, err := m.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	fetch()
	c.Advance(20 * time.Minute) // past ProbeGap; only the probe touched the index
	fetch()
	if spawns != 1 {
		t.Fatalf("probe-induced index bump re-triggered a probe, spawns = %d", spawns)
	}
}
