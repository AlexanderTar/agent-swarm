package advisor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// readLines reads path line by line. A missing or unreadable file is an
// error the caller reports; a malformed line is simply not returned.
func readLines(path string) ([][]byte, error) {
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
	return out, nil // a scanner error (e.g. a line over the buffer) is treated as end of input
}

func lastTurns(turns []Turn, maxTurns int) []Turn {
	if maxTurns > 0 && len(turns) > maxTurns {
		return turns[len(turns)-maxTurns:]
	}
	return turns
}

// ReadTranscript reads an agent's own working transcript for the advisor
// context (§11.6, P0-13). Every reader skips a malformed line rather than
// failing, and returns at most the last maxTurns turns.
func ReadTranscript(kind runtime.AgentKind, path string, maxTurns int) ([]Turn, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	var turns []Turn
	switch kind {
	case runtime.Claude:
		turns = readClaudeLines(lines)
	case runtime.Codex:
		turns = readCodexLines(lines)
	case runtime.Agy:
		turns = readAgyLines(lines)
	case runtime.Cursor:
		turns = readCursorLines(lines)
	default:
		return nil, fmt.Errorf("%s has no transcript reader", kind.Display())
	}
	return lastTurns(turns, maxTurns), nil
}

// ---------- claude ----------

type claudeContentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Content json.RawMessage `json:"content"`
}

type claudeRecord struct {
	Type    string `json:"type"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []claudeContentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var texts []string
		for _, b := range blocks {
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

func readClaudeLines(lines [][]byte) []Turn {
	var out []Turn
	for _, line := range lines {
		var r claudeRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		if r.Type != "assistant" && r.Type != "user" {
			continue
		}
		role := r.Message.Role
		if role == "" {
			role = r.Type
		}
		if len(r.Message.Content) == 0 {
			out = append(out, Turn{Role: role})
			continue
		}
		// content is either a plain string or a list of typed blocks.
		var s string
		if json.Unmarshal(r.Message.Content, &s) == nil {
			out = append(out, Turn{Role: role, Text: s})
			continue
		}
		var blocks []claudeContentBlock
		if err := json.Unmarshal(r.Message.Content, &blocks); err != nil {
			out = append(out, Turn{Role: role})
			continue
		}
		var texts []string
		var any bool
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
				any = true
			case "tool_use":
				out = append(out, Turn{Role: role, Tool: b.Name})
				any = true
			case "tool_result":
				out = append(out, Turn{Role: "tool", Text: toolResultText(b.Content)})
				any = true
			}
		}
		if len(texts) > 0 {
			out = append(out, Turn{Role: role, Text: strings.Join(texts, "\n")})
		} else if !any {
			// a turn with only e.g. a thinking block still counts as a turn
			out = append(out, Turn{Role: role})
		}
	}
	return out
}

// ---------- codex ----------

type codexEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Name    string `json:"name"`
	CallID  string `json:"call_id"`
	Output  string `json:"output"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func readCodexLines(lines [][]byte) []Turn {
	var out []Turn
	for _, line := range lines {
		var e codexEnvelope
		if err := json.Unmarshal(line, &e); err != nil || e.Type != "response_item" {
			continue
		}
		var p codexPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			continue
		}
		switch p.Type {
		case "message":
			role := p.Role
			if role == "" {
				role = "assistant"
			}
			var texts []string
			for _, c := range p.Content {
				if c.Text != "" {
					texts = append(texts, c.Text)
				}
			}
			out = append(out, Turn{Role: role, Text: strings.Join(texts, "\n")})
		case "function_call", "custom_tool_call":
			out = append(out, Turn{Role: "assistant", Tool: p.Name})
		case "function_call_output", "custom_tool_call_output":
			out = append(out, Turn{Role: "tool", Text: p.Output})
		}
	}
	return out
}

// ---------- agy ----------

type agyToolCall struct {
	Name string `json:"name"`
}

type agyRecord struct {
	Type      string        `json:"type"`
	Content   string        `json:"content"`
	ToolCalls []agyToolCall `json:"tool_calls"`
}

// agyRoles maps the P0-13 record types this reader keeps to a Turn role.
// EPHEMERAL_MESSAGE (Swarm's own injected notices, P0-2) is not in this map
// and so is always dropped, whatever its content is.
var agyRoles = map[string]string{
	"USER_INPUT":       "user",
	"PLANNER_RESPONSE": "assistant",
	"GENERIC":          "tool",
	"SYSTEM_MESSAGE":   "system",
}

func readAgyLines(lines [][]byte) []Turn {
	var out []Turn
	for _, line := range lines {
		var r agyRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		role, ok := agyRoles[r.Type]
		if !ok {
			continue
		}
		if r.Content != "" {
			out = append(out, Turn{Role: role, Text: r.Content})
		}
		for _, c := range r.ToolCalls {
			out = append(out, Turn{Role: role, Tool: c.Name})
		}
		if r.Content == "" && len(r.ToolCalls) == 0 {
			out = append(out, Turn{Role: role})
		}
	}
	return out
}

// ---------- cursor ----------

// cursor tool_use blocks carry no result (P0-13), so their "input" is never read here.
type cursorContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

type cursorRecord struct {
	Type    string `json:"type"` // "turn_ended" has no role/message
	Role    string `json:"role"`
	Message struct {
		Content []cursorContentBlock `json:"content"`
	} `json:"message"`
}

func readCursorLines(lines [][]byte) []Turn {
	var out []Turn
	for _, line := range lines {
		var r cursorRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		if r.Role == "" {
			continue // e.g. {"type":"turn_ended",...}
		}
		var texts []string
		var any bool
		for _, b := range r.Message.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
				any = true
			case "tool_use":
				out = append(out, Turn{Role: r.Role, Tool: b.Name})
				any = true
			}
		}
		if len(texts) > 0 {
			out = append(out, Turn{Role: r.Role, Text: strings.Join(texts, "\n")})
		} else if !any {
			out = append(out, Turn{Role: r.Role})
		}
	}
	return out
}

// ---------- transcript path derivation ----------

// encodeCwd turns a workspace path into the directory-name encoding an
// agent's own transcript store uses: every "/" becomes "-". trimLeading
// drops the resulting leading dash (cursor's convention); claude keeps it.
// The path is resolved through EvalSymlinks first ("realpath"); a cwd that
// no longer exists (or never did, e.g. in a test) falls back to the path as
// given rather than erroring the whole lookup.
func encodeCwd(cwd string, trimLeading bool) string {
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		real = cwd
	}
	enc := strings.ReplaceAll(real, "/", "-")
	if trimLeading {
		enc = strings.TrimPrefix(enc, "-")
	}
	return enc
}

// TranscriptPath derives the path to an agent's own transcript when its hook
// does not report one directly (§11.6): cursor sends transcript_path: null
// and agy sends workspacePaths: [] (P0-2). Claude and codex hooks already
// report transcript_path, so callers do not usually need this for them.
func TranscriptPath(kind runtime.AgentKind, userHome, cwd, providerSessionID string) (string, error) {
	switch kind {
	case runtime.Cursor:
		enc := encodeCwd(cwd, true)
		return filepath.Join(userHome, ".cursor", "projects", enc, "agent-transcripts",
			providerSessionID, providerSessionID+".jsonl"), nil
	case runtime.Agy:
		return filepath.Join(userHome, ".gemini", "antigravity-cli", "brain", providerSessionID,
			".system_generated", "logs", "transcript_full.jsonl"), nil
	case runtime.Claude:
		enc := encodeCwd(cwd, false)
		return filepath.Join(userHome, ".claude", "projects", enc, providerSessionID+".jsonl"), nil
	}
	return "", fmt.Errorf("%s has no derivable transcript path; use the hook's transcript_path", kind.Display())
}
