package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
