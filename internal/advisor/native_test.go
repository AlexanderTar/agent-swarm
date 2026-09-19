package advisor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// splitLines and joinLines let a test feed a transcript in two halves. bytes.Split
// on "\n" leaves a trailing empty element for a file ending in a newline, which
// joinLines puts back, so the halves concatenate to exactly the original bytes.
func splitLines(b []byte) [][]byte { return bytes.Split(b, []byte("\n")) }

func joinLines(ls [][]byte) []byte { return bytes.Join(ls, []byte("\n")) }

// P0-14: the five lines of one response give exactly one row.
func TestParseNativeAdvisorDeduplicatesByRequestID(t *testing.T) {
	b, err := os.ReadFile("testdata/claude/transcript-advisor-entries.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseNativeAdvisor(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Model != "claude-sonnet-5" {
		t.Errorf("model = %q", e.Model)
	}
	if e.Input != 67484 || e.Output != 9668 {
		t.Errorf("tokens = %d in, %d out", e.Input, e.Output)
	}
	if e.RequestID == "" || e.Index != 1 {
		t.Errorf("entry = %+v; the advisor element is iterations[1]", e)
	}
	if e.Answer == "" {
		t.Error("the answer comes from the advisor_tool_result content block")
	}
}

func TestParseNativeAdvisorIgnoresPlainAssistantLines(t *testing.T) {
	b, _ := os.ReadFile("testdata/claude/transcript-plain-assistant.jsonl")
	got, err := ParseNativeAdvisor(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d entries from a transcript with no advisor call", len(got))
	}
}

// A response split across two reads must still give one row.
func TestScanTranscriptIsIncrementalAndIdempotent(t *testing.T) {
	s, seed := newAdvisorService(t)
	ctx := context.Background()
	full, _ := os.ReadFile("testdata/claude/transcript-advisor-entries.jsonl")
	lines := splitLines(full)
	p := filepath.Join(t.TempDir(), "session.jsonl")
	os.WriteFile(p, joinLines(lines[:2]), 0o644)
	if err := s.ScanTranscript(ctx, seed.SessionID, p); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, joinLines(lines), 0o644)
	if err := s.ScanTranscript(ctx, seed.SessionID, p); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(ctx, seed.AgentName)
	if err != nil {
		t.Fatal(err)
	}
	var native int
	for _, r := range rows {
		if r.Mode == "native" {
			native++
			if r.CostUSD != nil {
				t.Error("native rows have no cost (P0-14)")
			}
			if r.Question != "(built-in advisor)" {
				t.Errorf("question = %q", r.Question)
			}
		}
	}
	if native != 1 {
		t.Fatalf("native rows = %d, want 1 across two reads", native)
	}
	// A third scan with no new bytes adds nothing. Compare against the count taken
	// before it — the earlier draft compared `len(rows)` with itself, which is a
	// tautology that can never fail (D66).
	before := len(rows)
	if err := s.ScanTranscript(ctx, seed.SessionID, p); err != nil {
		t.Fatal(err)
	}
	rows, err = s.List(ctx, seed.AgentName)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != before {
		t.Fatalf("a scan with no new bytes added %d rows", len(rows)-before)
	}
}

func TestScanTranscriptToleratesAMissingFile(t *testing.T) {
	s, seed := newAdvisorService(t)
	if err := s.ScanTranscript(context.Background(), seed.SessionID, "/nope/none.jsonl"); err != nil {
		t.Fatalf("a missing transcript is not an error: %v", err)
	}
}

// The unique index is what makes the dedupe safe across a daemon restart. A
// restart is a *new Service* reading the same DB and the same file, so that is
// what the test builds — not a ResetOffsets() method existing on the production
// type only so a test can clear a map (D65).
func TestSourceRequestIDSurvivesADaemonRestart(t *testing.T) {
	first, seed := newAdvisorService(t)
	ctx := context.Background()
	full, err := os.ReadFile("testdata/claude/transcript-advisor-entries.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, full, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := first.ScanTranscript(ctx, seed.SessionID, p); err != nil {
		t.Fatal(err)
	}
	// The restart: a second Service over the same *db.DB, with an empty offset map.
	second := &Service{DB: first.DB, Events: first.Events, Home: first.Home,
		UserHome: first.UserHome, Adapters: first.Adapters, Run: first.Run,
		MaxConcurrent: first.MaxConcurrent, Timeout: first.Timeout,
		Now: first.Now, Log: first.Log}
	if err := second.ScanTranscript(ctx, seed.SessionID, p); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := first.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM advice WHERE mode = 'native'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("native rows = %d after a restart rescan, want 1", n)
	}
}
