package advisor

import (
	"fmt"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// maxContextChars is §11.6's cap on the whole context document.
const maxContextChars = 40000

// maxToolOutput is §11.6's per-turn cut for a tool's output.
const maxToolOutput = 300

// maxCheckpoints and maxTranscriptTurns are §11.6's per-section caps.
const maxCheckpoints = 3
const maxTranscriptTurns = 30

// Turn is one line of transcript handed to an advisor.
type Turn struct {
	Role, Text, Tool string
}

// ContextInput is BuildContext's input (§11.6).
type ContextInput struct {
	Brief       string
	Checkpoints []runtime.Checkpoint
	Requests    []runtime.Request
	Question    string
	Focus       []string
	Transcript  []Turn
}

func truncateTool(t Turn) string {
	if t.Text == "" {
		return t.Tool
	}
	text := t.Text
	if len(text) > maxToolOutput {
		text = text[:maxToolOutput]
	}
	if t.Tool != "" {
		return fmt.Sprintf("%s: %s", t.Tool, text)
	}
	return text
}

func turnLine(t Turn) string {
	role := t.Role
	if role == "" {
		role = "tool"
	}
	return fmt.Sprintf("- **%s**: %s", role, truncateTool(t))
}

// BuildContext renders the sections in order, then trims transcript turns
// oldest-first until the whole document is at most 40,000 characters. The
// brief and the question are never dropped.
func BuildContext(in ContextInput) string {
	var head strings.Builder
	head.WriteString("## Brief\n")
	if in.Brief != "" {
		head.WriteString(in.Brief)
	}
	head.WriteString("\n\n## Recent checkpoints\n")
	cps := in.Checkpoints
	if len(cps) > maxCheckpoints {
		cps = cps[len(cps)-maxCheckpoints:]
	}
	for _, c := range cps {
		fmt.Fprintf(&head, "- **%s** (%s): %s\n", c.Kind, c.ItemID, c.Summary)
	}
	head.WriteString("\n## Open requests\n")
	for _, r := range in.Requests {
		fmt.Fprintf(&head, "- **%s**: %s\n", r.Kind, r.Prompt)
	}
	head.WriteString("\n## Files to read\n")
	for _, f := range in.Focus {
		fmt.Fprintf(&head, "- %s\n", f)
	}
	headStr := head.String()

	turns := in.Transcript
	if len(turns) > maxTranscriptTurns {
		turns = turns[len(turns)-maxTranscriptTurns:]
	}
	tail := fmt.Sprintf("\n## Question\n%s\n", in.Question)

	// Trim transcript turns oldest-first until the document fits the cap.
	for {
		var body strings.Builder
		body.WriteString("\n## Transcript\n")
		for _, t := range turns {
			body.WriteString(turnLine(t))
			body.WriteString("\n")
		}
		out := headStr + body.String() + tail
		if len(out) <= maxContextChars || len(turns) == 0 {
			return out
		}
		turns = turns[1:]
	}
}
