package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
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
	m := &Muse{Start: fakeMuseHost(t, museUsagePayload, false, nil, nil, nil),
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
	m := &Muse{Start: start, Dir: t.TempDir(), Timeout: 5 * time.Second}
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
	m := &Muse{Start: fakeMuseHost(t, "", false, nil, nil, nil),
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
	m := &Muse{Start: fakeMuseHost(t, "", true, nil, nil, &killed),
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
	m := &Muse{Start: fakeMuseHost(t, museUsagePayload, false, nil, &spawns, nil),
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
	m := &Muse{Start: fakeMuseHost(t, museUsagePayload, false, nil, &spawns, nil),
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

// Live: spawns the real `muse serve`, SPENDS ONE MINIMAL TURN, and proves the
// meters come back shaped like every other source's. Gated because it costs
// quota and needs a logged-in muse.
//
//	MUSE_LIVE_PROBE=1 go test ./internal/usage/ -run TestMuseFetchLive -v
func TestMuseFetchLive(t *testing.T) {
	if os.Getenv("MUSE_LIVE_PROBE") != "1" {
		t.Skip("live probe spends one real muse turn; set MUSE_LIVE_PROBE=1 to run")
	}
	m := &Muse{Start: execx.Start, Dir: t.TempDir(), Timeout: 120 * time.Second}
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
