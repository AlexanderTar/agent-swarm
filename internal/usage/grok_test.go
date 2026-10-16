package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
)

func TestParseGrokSessionUsage(t *testing.T) {
	raw, err := os.ReadFile("testdata/grok-usage-run1.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseGrokSessionUsage(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := GrokUsage{InputTokens: 84202, OutputTokens: 478, CachedTokens: 57216, ReasoningTokens: 380, Turns: 1, CostUSD: 0.085448}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseGrokSessionUsageRejectsDocWithoutSession(t *testing.T) {
	if _, err := ParseGrokSessionUsage([]byte(`{"sessionId":"x"}`)); err == nil {
		t.Fatal("a document with no session block must be an error, not zero spend")
	}
}

func TestGrokSessionUsageRunsWithGrokHome(t *testing.T) {
	raw, err := os.ReadFile("testdata/grok-usage-run1.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotEnv map[string]string
	var gotArgs []string
	run := func(_ context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
		gotEnv, gotArgs = env, append([]string{name}, args...)
		return raw, nil
	}
	u, err := GrokSessionUsage(context.Background(), run, "/swarm", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 84202 {
		t.Fatalf("usage not parsed: %+v", u)
	}
	if !reflect.DeepEqual(gotEnv, grokhome.Env("/swarm")) {
		t.Fatalf("env %v want %v", gotEnv, grokhome.Env("/swarm"))
	}
	if want := []string{"grok", "usage", "sess-1"}; !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("argv %v want %v", gotArgs, want)
	}
}

// fakeGrokStdio answers `grok agent stdio` JSON-RPC from fixture files:
// initialize and authenticate get {}, _x.ai/billing gets the fixture's result.
// It records argv, env and the methods called (authenticate with its methodId).
type fakeGrokStdio struct {
	billing string
	argv    []string
	env     map[string]string
	methods []string
}

func (f *fakeGrokStdio) start(t *testing.T) execx.StarterEnv {
	t.Helper()
	fixture, err := os.ReadFile(f.billing)
	if err != nil {
		t.Fatal(err)
	}
	var billing struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(fixture, &billing); err != nil {
		t.Fatal(err)
	}
	return func(_ context.Context, env map[string]string, name string, args ...string) (*execx.Proc, error) {
		f.env, f.argv = env, append([]string{name}, args...)
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		go func() {
			sc := bufio.NewScanner(inR)
			for sc.Scan() {
				var req struct {
					ID     int             `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if json.Unmarshal(sc.Bytes(), &req) != nil {
					continue
				}
				entry := req.Method
				if req.Method == "authenticate" {
					var p struct {
						MethodID string `json:"methodId"`
					}
					_ = json.Unmarshal(req.Params, &p)
					entry += ":" + p.MethodID
				}
				f.methods = append(f.methods, entry)
				result := json.RawMessage(`{}`)
				if req.Method == "_x.ai/billing" {
					result = billing.Result
				}
				resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				_, _ = outW.Write(append(resp, '\n'))
			}
		}()
		return &execx.Proc{Stdin: inW, Stdout: outR, Kill: func() { inW.Close(); outW.Close() }}, nil
	}
}

func TestGrokAccountMeters(t *testing.T) {
	fake := &fakeGrokStdio{billing: "testdata/grok-billing.json"}
	g := &Grok{SwarmHome: "/swarm", StartEnv: fake.start(t), Dir: t.TempDir()}
	meters, headline, err := g.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Date(2026, 10, 23, 13, 43, 37, 735000000, time.UTC)
	if len(meters) != 1 || headline != meters[0].ID {
		t.Fatalf("meters %+v headline %q", meters, headline)
	}
	m := meters[0]
	if m.UsedPct != 1.0 || m.Window != "weekly" || m.ResetsAt == nil || !m.ResetsAt.Equal(reset) {
		t.Fatalf("meter %+v, want 1%% weekly resetting %s", m, reset)
	}
	if want := []string{"initialize", "authenticate:cached_token", "_x.ai/billing"}; !reflect.DeepEqual(fake.methods, want) {
		t.Fatalf("methods %v want %v", fake.methods, want)
	}
	if want := []string{"grok", "agent", "stdio"}; !reflect.DeepEqual(fake.argv, want) {
		t.Fatalf("argv %v want %v", fake.argv, want)
	}
	if !reflect.DeepEqual(fake.env, grokhome.Env("/swarm")) {
		t.Fatalf("GROK_HOME env did not reach the billing exec: %v", fake.env)
	}
}

func TestGrokFreeTierHasNoMeter(t *testing.T) {
	fake := &fakeGrokStdio{billing: "testdata/grok-billing-free.json"}
	g := &Grok{SwarmHome: "/swarm", StartEnv: fake.start(t), Dir: t.TempDir()}
	if _, _, err := g.Fetch(context.Background()); !errors.Is(err, ErrNoData) {
		t.Fatalf("err %v, want ErrNoData: no creditUsagePercent means no meter", err)
	}
}

const grokQuota429 = "API error (status 429 Too Many Requests): subscription:free-usage-exhausted: You've used all the included free usage for model grok-4.7 for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 619434/600000."

func TestGrokExhaustionMeterWhenBillingGivesNone(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	fake := &fakeGrokStdio{billing: "testdata/grok-billing-free.json"}
	ex := &GrokExhaustion{}
	g := &Grok{SwarmHome: "/swarm", StartEnv: fake.start(t), Dir: t.TempDir(), Exhaustion: ex, Now: func() time.Time { return now }}
	if !ex.Note(grokQuota429, now) {
		t.Fatal("a 429 usage-exhausted error must be recorded")
	}
	meters, headline, err := g.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 1 || headline != meters[0].ID || meters[0].UsedPct != 100 {
		t.Fatalf("meters %+v headline %q, want one 100%% meter", meters, headline)
	}
	r := meters[0].ResetsAt
	if r == nil || !r.After(now) || r.After(now.Add(24*time.Hour)) {
		t.Fatalf("ResetsAt %v, want within 24h of %s", r, now)
	}
}

func TestGrokExhaustionIgnoredOnceItExpiresOrBillingHasAMeter(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	ex := &GrokExhaustion{}
	ex.Note(grokQuota429, now)
	if _, ok := ex.Active(now.Add(25 * time.Hour)); ok {
		t.Fatal("an exhaustion past its reset must not be reported")
	}
	// billing is authoritative: a real 1% reading beats an older 429.
	fake := &fakeGrokStdio{billing: "testdata/grok-billing.json"}
	g := &Grok{SwarmHome: "/swarm", StartEnv: fake.start(t), Dir: t.TempDir(), Exhaustion: ex, Now: func() time.Time { return now }}
	meters, _, err := g.Fetch(context.Background())
	if err != nil || meters[0].UsedPct != 1.0 {
		t.Fatalf("meters %+v err %v, want the billing 1%%", meters, err)
	}
}

func TestGrokExhaustionNoteClassifiesSignals(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, text string
		want       bool
		until      time.Time
	}{
		{"429 quota", grokQuota429, true, now.Add(24 * time.Hour)},
		{"retry_state exhausted", "retry_state: exhausted", true, now.Add(24 * time.Hour)},
		{"usage-limit card", "You hit your SuperGrok usage limit", true, now.Add(24 * time.Hour)},
		{"explicit reset", "Subscription quota exhausted. Your usage window resets at 2026-10-17T00:00:00Z.", true, time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)},
		{"network error", "connection reset by peer", false, time.Time{}},
		{"empty", "", false, time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ex := &GrokExhaustion{}
			if got := ex.Note(c.text, now); got != c.want {
				t.Fatalf("Note = %v want %v", got, c.want)
			}
			until, ok := ex.Active(now)
			if ok != c.want || (ok && !until.Equal(c.until)) {
				t.Fatalf("Active = %v,%v want %v,%v", until, ok, c.until, c.want)
			}
		})
	}
}
