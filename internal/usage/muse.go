package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// MuseUsage is summed per-turn token counts for one muse session, read from a
// redacted `muse export`. The CLI emits no quota endpoint and no token stream
// on stdout, so the export file is the only usage source (probed 2026-09-23).
type MuseUsage struct {
	Turns                             int
	InputTokens, OutputTokens         int
	ReasoningTokens                   int
	CacheReadTokens, CacheWriteTokens int
}

// museUsageEvent is the only object summed: events[i].envelope.payload.event
// .usage. record/quantity objects duplicate the same turns and are ignored.
type museUsageEvent struct {
	Usage *struct {
		InputTokens      int `json:"input_tokens"`
		OutputTokens     int `json:"output_tokens"`
		ReasoningTokens  int `json:"reasoning_tokens"`
		CacheReadTokens  int `json:"cache_read_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"usage"`
}

type museExportEnvelope struct {
	Payload *struct {
		Event *museUsageEvent `json:"event"`
	} `json:"payload"`
}

type museExportDoc struct {
	Events []struct {
		Envelope *museExportEnvelope `json:"envelope"`
	} `json:"events"`
}

// ParseMuseExport sums the per-turn usage objects of one export document.
// Missing events is an error, never an empty success: callers must not mistake
// "nothing exported" for "nothing spent".
func ParseMuseExport(raw []byte) (MuseUsage, error) {
	var doc museExportDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return MuseUsage{}, err
	}
	if doc.Events == nil {
		return MuseUsage{}, fmt.Errorf("muse export has no events array")
	}
	var out MuseUsage
	for _, e := range doc.Events {
		if e.Envelope == nil || e.Envelope.Payload == nil || e.Envelope.Payload.Event == nil {
			continue
		}
		u := e.Envelope.Payload.Event.Usage
		if u == nil {
			continue
		}
		out.Turns++
		out.InputTokens += u.InputTokens
		out.OutputTokens += u.OutputTokens
		out.ReasoningTokens += u.ReasoningTokens
		out.CacheReadTokens += u.CacheReadTokens
		out.CacheWriteTokens += u.CacheWriteTokens
	}
	return out, nil
}

// SessionUsage exports one muse session (redacted) and sums its token usage.
// The export lands in dir and is removed afterwards; dir must exist.
func SessionUsage(ctx context.Context, run execx.Runner, dir, sessionID string) (MuseUsage, error) {
	out := filepath.Join(dir, "muse-export-"+sessionID+".json")
	if _, err := run(ctx, "muse", "export", "--session", sessionID, "--redacted", "--out", out); err != nil {
		return MuseUsage{}, err
	}
	defer os.Remove(out)
	raw, err := os.ReadFile(out)
	if err != nil {
		return MuseUsage{}, err
	}
	return ParseMuseExport(raw)
}
