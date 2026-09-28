package adapter

import (
	"bufio"
	"bytes"
	"os"
)

// transcriptTailBytes is readTranscriptTailLines' starting read window from
// the end of the file (2026-09-28-approval-summary-enforced, post-review):
// reading a whole multi-hour session's transcript on every PreToolUse call
// is wasteful when only the text since the last turn boundary ever matters;
// 4 MiB comfortably covers several turns of ordinary tool output.
const transcriptTailBytes = 4 << 20

// readTranscriptLines reads path (a JSONL transcript) line by line, trimming
// whitespace and dropping blank lines. A missing/unreadable file, or a
// scanner error partway through (a line over the 16 MiB per-line cap, or an
// I/O error), is reported as an error -- the caller then fails open. A
// malformed individual line is left for the caller's own per-line
// json.Unmarshal to skip.
func readTranscriptLines(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return scanTranscript(f)
}

// scanTranscript reads f (positioned wherever the caller left it) to EOF,
// trimming and dropping blank lines, and checks sc.Err(): a scanner error
// (a line over the 16 MiB per-line cap, or an I/O error partway through)
// must never be silently swallowed as "end of input" -- the caller's whole
// point is deciding whether to trust what it read, and a truncated or
// failed read is not that.
func scanTranscript(f *os.File) ([][]byte, error) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	var out [][]byte
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		out = append(out, append([]byte(nil), line...))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// readTranscriptTailLines is readTranscriptLines for a large transcript: it
// only reads the last transcriptTailBytes of the file, widening the window
// (x4) and retrying if hasBoundary finds no turn-boundary line in what it
// read -- each kind's AssistantTextSinceLastTurn only ever needs the text
// after the most recent boundary, so once one is found in the tail,
// anything further back can never matter. Falls back to reading the whole
// file once the window would cover it. A seek into the middle of the file
// starts mid-line, so that truncated first line is always dropped unless
// the read actually started at byte 0. Same error contract as
// readTranscriptLines: a missing file or a scan error is returned as an
// error, so the caller fails open.
func readTranscriptTailLines(path string, hasBoundary func(line []byte) bool) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	window := int64(transcriptTailBytes)
	for {
		start := size - window
		if start <= 0 {
			start = 0
		}
		if _, err := f.Seek(start, 0); err != nil {
			return nil, err
		}
		lines, err := scanTranscript(f)
		if err != nil {
			return nil, err
		}
		if start > 0 && len(lines) > 0 {
			lines = lines[1:]
		}
		if start == 0 {
			return lines, nil
		}
		for _, l := range lines {
			if hasBoundary(l) {
				return lines, nil
			}
		}
		window *= 4
	}
}
