package adapter

import (
	"bytes"
	"encoding/json"
	"io"
	"os"

	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

// contextTailBytes bounds how much of a transcript's end ReadContext scans.
const contextTailBytes = 256 << 10

// ContextSample is one end-of-turn reading of a session's context size.
// Window is nil when the source does not report one.
type ContextSample struct {
	Tokens int
	Window *int
}

// ReadContext reads the latest context sample from a kind's transcript.
// ok is false when the file is missing, empty or holds no usage line.
func ReadContext(kind kinds.AgentKind, transcriptPath string) (ContextSample, bool) {
	if transcriptPath == "" {
		return ContextSample{}, false
	}
	var parse func(line []byte) (ContextSample, bool)
	switch kind {
	case kinds.Claude:
		parse = claudeContextLine
	case kinds.Codex:
		parse = codexContextLine
	case kinds.Agy:
		parse = agyContextLine
	default:
		return ContextSample{}, false
	}
	lines, err := tailLines(transcriptPath, contextTailBytes)
	if err != nil {
		return ContextSample{}, false
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if s, ok := parse(lines[i]); ok {
			return s, true
		}
	}
	return ContextSample{}, false
}

// tailLines returns the complete lines within the last max bytes of a file.
func tailLines(path string, max int64) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := int64(0)
	if st.Size() > max {
		off = st.Size() - max
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil, err
	}
	lines := bytes.Split(buf, []byte("\n"))
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // first line is cut mid-way
	}
	return lines, nil
}

func claudeContextLine(line []byte) (ContextSample, bool) {
	var l struct {
		Type    string `json:"type"`
		Message struct {
			RawUsage json.RawMessage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &l) != nil || l.Type != "assistant" || len(l.Message.RawUsage) == 0 {
		return ContextSample{}, false
	}
	var u struct {
		Input    int `json:"input_tokens"`
		Creation int `json:"cache_creation_input_tokens"`
		Read     int `json:"cache_read_input_tokens"`
	}
	if json.Unmarshal(l.Message.RawUsage, &u) != nil {
		return ContextSample{}, false
	}
	return ContextSample{Tokens: u.Input + u.Creation + u.Read}, true
}

func codexContextLine(line []byte) (ContextSample, bool) {
	var l struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			Info *struct {
				Last struct {
					Input int `json:"input_tokens"`
				} `json:"last_token_usage"`
				Window int `json:"model_context_window"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &l) != nil || l.Type != "event_msg" || l.Payload.Type != "token_count" || l.Payload.Info == nil {
		return ContextSample{}, false
	}
	s := ContextSample{Tokens: l.Payload.Info.Last.Input}
	if w := l.Payload.Info.Window; w > 0 {
		s.Window = &w
	}
	return s, true
}

func agyContextLine(line []byte) (ContextSample, bool) {
	var l struct {
		Source string `json:"source"`
		Type   string `json:"type"`
		Input  int    `json:"input_tokens"`
		Read   int    `json:"cache_read_tokens"`
	}
	if json.Unmarshal(line, &l) != nil || l.Source != "MODEL" || l.Type != "PLANNER_RESPONSE" || l.Input+l.Read == 0 {
		return ContextSample{}, false
	}
	return ContextSample{Tokens: l.Input + l.Read}, true
}
