package usage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// nopWriteCloser adapts an io.Writer (typically io.Discard) to the
// io.WriteCloser execx.Proc.Stdin wants.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// fakeAppServer is a scripted execx.Starter: it streams the given JSON-RPC
// lines over Stdout regardless of what Codex.Fetch writes to Stdin — a dumb
// playback fixture, not a stateful RPC responder, which is all Codex.Fetch's
// line-by-line scan needs to be exercised.
func fakeAppServer(t *testing.T, lines []string) execx.Starter {
	t.Helper()
	return func(ctx context.Context, name string, args ...string) (*execx.Proc, error) {
		pr, pw := io.Pipe()
		go func() {
			for _, l := range lines {
				if _, err := pw.Write([]byte(l + "\n")); err != nil {
					return
				}
			}
			// left open on purpose: Kill (via defer in Fetch) closes it. A real
			// app-server keeps the pipe open between requests too.
		}()
		return &execx.Proc{Stdin: nopWriteCloser{io.Discard}, Stdout: pr,
			Kill: func() { pw.Close(); pr.Close() }}, nil
	}
}

// hangingAppServer never writes anything to Stdout, so Codex.Fetch's timeout
// is the only thing that ends the call; killed records whether Kill ran.
func hangingAppServer(t *testing.T, killed *bool) execx.Starter {
	t.Helper()
	return func(ctx context.Context, name string, args ...string) (*execx.Proc, error) {
		pr, pw := io.Pipe()
		return &execx.Proc{Stdin: nopWriteCloser{io.Discard}, Stdout: pr,
			Kill: func() { *killed = true; pw.Close(); pr.Close() }}, nil
	}
}

// failingStarter simulates `codex app-server` not being available at all, so
// Fetch falls back to the rollout files.
func failingStarter(t *testing.T) execx.Starter {
	t.Helper()
	return func(ctx context.Context, name string, args ...string) (*execx.Proc, error) {
		return nil, os.ErrNotExist
	}
}

// TestCodexAppServerExchange's fixture is the real request/response shape,
// live-verified against codex-cli 0.154.0 (`codex app-server` over stdio):
// an `initialize` response for id 1, then `account/rateLimits/read`'s
// nested primary/secondary payload for id 2. A prior version of this
// fixture used a made-up `getRateLimits` method with a flat rateLimits
// array and no initialize round trip — codex-cli rejects that request
// outright ("unknown variant `getRateLimits`"), which is the real bug this
// fixture now guards against.
func TestCodexAppServerExchange(t *testing.T) {
	src := &Codex{Start: fakeAppServer(t, []string{
		`{"jsonrpc":"2.0","id":1,"result":{"userAgent":"test/0.0.0","codexHome":"/tmp","platformFamily":"unix","platformOs":"macos"}}`,
		`{"jsonrpc":"2.0","method":"notifications/ignore_me","params":{}}`, // a stray notification
		`{"jsonrpc":"2.0","id":2,"result":{"rateLimits":{
			"primary":{"windowDurationMins":300,"usedPercent":55.5,"resetsAt":1789700000},
			"secondary":{"windowDurationMins":10080,"usedPercent":8,"resetsAt":1790000000}}}}`,
	}), Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) },
		Timeout: 10 * time.Second}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Meters) != 2 || snap.HeadlineID != "primary" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Meters[0].Label != "5h" || snap.Meters[1].Label != "Weekly" {
		t.Fatalf("labels = %q, %q", snap.Meters[0].Label, snap.Meters[1].Label)
	}
	if snap.Meters[0].ResetsAt == nil || snap.Meters[0].ResetsAt.Unix() != 1789700000 {
		t.Fatalf("resets_at is unix seconds: %v", snap.Meters[0].ResetsAt)
	}
}

// TestCodexAppServerRPCErrorFallsBackFast pins the fix for the second real
// bug: the read loop used to treat a JSON-RPC error response the same as an
// untargeted notification (both have a nil Result), so it looped silently
// until the whole Timeout elapsed. It must now recognize the id-matched
// error and fail over to the rollout immediately.
func TestCodexAppServerRPCErrorFallsBackFast(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "09", "17")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "rollout-2026-09-17T10-00-00-abc.jsonl"), []byte(
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":33,"window_minutes":300}}}}`+"\n"), 0o644)

	src := &Codex{UserHome: home, Start: fakeAppServer(t, []string{
		`{"jsonrpc":"2.0","id":1,"result":{"userAgent":"test/0.0.0","codexHome":"/tmp","platformFamily":"unix","platformOs":"macos"}}`,
		`{"jsonrpc":"2.0","id":2,"error":{"code":-32600,"message":"Invalid request: unknown variant ` + "`getRateLimits`" + `"}}`,
	}), Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) },
		Timeout: 3 * time.Second}

	start := time.Now()
	snap, err := src.Fetch(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap.Source, "rollout") {
		t.Fatalf("source = %q, want the rollout fallback", snap.Source)
	}
	if elapsed >= time.Second {
		t.Fatalf("took %s to fail over; the RPC error should short-circuit the %s timeout", elapsed, src.Timeout)
	}
}

func TestCodexHungProcessIsKilledAfterTheTimeout(t *testing.T) {
	var killed bool
	src := &Codex{Start: hangingAppServer(t, &killed), Timeout: 50 * time.Millisecond,
		Now: func() time.Time { return time.Now() }}
	if _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("a hung app-server must time out")
	}
	if !killed {
		t.Fatal("the process must be killed")
	}
}

// §13: the fallback reads the newest token_count event from today's rollouts.
func TestCodexFallsBackToTheRollout(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "09", "17")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "rollout-2026-09-17T10-00-00-abc.jsonl"), []byte(
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":33,"window_minutes":300},"secondary":{"used_percent":4,"window_minutes":10080}}}}`+"\n"), 0o644)
	src := &Codex{UserHome: home, Start: failingStarter(t),
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }, Timeout: time.Second}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Meters) != 2 || snap.Meters[0].UsedPct != 33 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if !strings.Contains(snap.Source, "rollout") {
		t.Fatalf("source = %q", snap.Source)
	}
}

func TestCodexAppServerPrioritizesFiveHourHeadline(t *testing.T) {
	src := &Codex{Start: fakeAppServer(t, []string{
		`{"jsonrpc":"2.0","id":1,"result":{"userAgent":"test/0.0.0","codexHome":"/tmp","platformFamily":"unix","platformOs":"macos"}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"rateLimits":{
			"primary":{"windowDurationMins":10080,"usedPercent":10,"resetsAt":1790000000},
			"secondary":{"windowDurationMins":300,"usedPercent":42,"resetsAt":1789700000}}}}`,
	}), Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) },
		Timeout: 10 * time.Second}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.HeadlineID != "secondary" {
		t.Fatalf("snap.HeadlineID = %q, want secondary (5h window)", snap.HeadlineID)
	}
}

func TestCodexRolloutPrioritizesFiveHourHeadline(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "09", "17")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "rollout-2026-09-17T10-00-00-abc.jsonl"), []byte(
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":10,"window_minutes":10080},"secondary":{"used_percent":42,"window_minutes":300}}}}`+"\n"), 0o644)
	src := &Codex{UserHome: home, Start: failingStarter(t),
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }, Timeout: time.Second}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.HeadlineID != "secondary" {
		t.Fatalf("snap.HeadlineID = %q, want secondary (5h window)", snap.HeadlineID)
	}
}
