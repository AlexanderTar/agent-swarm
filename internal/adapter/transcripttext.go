package adapter

import (
	"bufio"
	"bytes"
	"os"
)

// readTranscriptLines reads path (a JSONL transcript) line by line, trimming
// whitespace and dropping blank lines. Shared by every kind's
// AssistantTextSinceLastTurn (2026-09-28-approval-summary-enforced): a
// missing or unreadable file is reported as an error (the caller then fails
// open), a malformed individual line is simply skipped by the caller's own
// per-line json.Unmarshal.
func readTranscriptLines(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
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
	return out, nil
}
