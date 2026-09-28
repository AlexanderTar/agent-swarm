package adapter

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestReadTranscriptTailLinesWidensUntilBoundaryFound is a post-review fix:
// when the initial 4 MiB tail window contains no line hasBoundary accepts,
// the window must widen (x4) and retry, rather than giving up or silently
// returning text with a missing boundary.
func TestReadTranscriptTailLinesWidensUntilBoundaryFound(t *testing.T) {
	p := filepath.Join(t.TempDir(), "widen.jsonl")
	var b bytes.Buffer
	// A boundary line, then >4 MiB of non-boundary lines after it, so the
	// first (4 MiB) window read from the end contains no boundary at all.
	b.WriteString(`{"marker":"boundary"}` + "\n")
	pad := bytes.Repeat([]byte("x"), 900)
	for b.Len() < 5<<20 {
		b.Write([]byte(`{"marker":"pad","pad":"`))
		b.Write(pad)
		b.WriteString("\"}\n")
	}
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	found := false
	lines, err := readTranscriptTailLines(p, func(line []byte) bool {
		return bytes.Contains(line, []byte(`"boundary"`))
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if bytes.Contains(l, []byte(`"boundary"`)) {
			found = true
		}
	}
	if !found {
		t.Fatal("readTranscriptTailLines did not widen far enough to find the boundary line")
	}
}

// TestReadTranscriptTailLinesNoBoundaryReadsWholeFile confirms a transcript
// with no boundary line anywhere still returns successfully once the window
// covers the whole file (start reaches 0), rather than looping forever or
// erroring.
func TestReadTranscriptTailLinesNoBoundaryReadsWholeFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "no-boundary.jsonl")
	if err := os.WriteFile(p, []byte(`{"marker":"only"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := readTranscriptTailLines(p, func(line []byte) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
}

// TestReadTranscriptTailLinesFailsOpenOnScanError confirms a scan error (a
// line over the 16 MiB per-line cap) propagates as an error, not a
// truncated success.
func TestReadTranscriptTailLinesFailsOpenOnScanError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "huge.jsonl")
	huge := append([]byte(`{"pad":"`), bytes.Repeat([]byte("x"), 17<<20)...)
	huge = append(huge, []byte(`"}`+"\n")...)
	if err := os.WriteFile(p, huge, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTranscriptTailLines(p, func(line []byte) bool { return false }); err == nil {
		t.Fatal("expected an error for a line over the scanner's buffer cap")
	}
}
