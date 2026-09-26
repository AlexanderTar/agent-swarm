package advisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func TestReadTranscriptPerAgent(t *testing.T) {
	cases := []struct {
		kind runtime.AgentKind
		file string
	}{
		{runtime.Agy, "agy/transcript_full-sample.jsonl"},
		{runtime.Cursor, "cursor/agent-transcript-sample.jsonl"},
		{runtime.Codex, "codex/rollout-sample.jsonl"},
		{runtime.Claude, "claude/transcript-plain-assistant.jsonl"},
	}
	for _, c := range cases {
		turns, err := ReadTranscript(c.kind, filepath.Join("testdata", c.file), 30)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if len(turns) == 0 {
			t.Fatalf("%s: no turns read from %s", c.kind, c.file)
		}
		for _, turn := range turns {
			if turn.Role == "" {
				t.Errorf("%s: a turn has no role: %+v", c.kind, turn)
			}
		}
	}
}

// P0-2: Swarm's own injected notices are dropped from the agy transcript.
func TestAgyTranscriptDropsSwarmEphemeralMessages(t *testing.T) {
	turns, err := ReadTranscript(runtime.Agy, "testdata/agy/transcript_full-sample.jsonl", 30)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if strings.Contains(turn.Text, "[swarm]") || runtime.IsDaemonPrompt(turn.Text) {
			t.Fatalf("a Swarm notice leaked into the advisor context: %q", turn.Text)
		}
	}
}

// P0-13: the cursor transcript has no tool results, so tool calls are names only.
func TestCursorTranscriptHasToolNamesWithoutResults(t *testing.T) {
	turns, err := ReadTranscript(runtime.Cursor, "testdata/cursor/agent-transcript-sample.jsonl", 30)
	if err != nil {
		t.Fatal(err)
	}
	var sawTool bool
	for _, turn := range turns {
		if turn.Tool != "" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Fatal("tool_use entries become turns with a Tool name")
	}
}

func TestReadTranscriptKeepsTheLastNTurns(t *testing.T) {
	turns, err := ReadTranscript(runtime.Agy, "testdata/agy/transcript_full-sample.jsonl", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) > 2 {
		t.Fatalf("got %d turns, want at most 2", len(turns))
	}
}

func TestReadTranscriptToleratesAMissingOrCorruptFile(t *testing.T) {
	if _, err := ReadTranscript(runtime.Claude, "testdata/nope.jsonl", 30); err == nil {
		t.Fatal("a missing file is an error the caller reports")
	}
	// a half-written last line is skipped, not fatal
	p := filepath.Join(t.TempDir(), "partial.jsonl")
	if err := os.WriteFile(p, []byte(`{"role":"user","message":{"content":[{"type":"text","text":"hi"}]}}`+"\n{\"role\":"), 0o644); err != nil {
		t.Fatal(err)
	}
	turns, err := ReadTranscript(runtime.Cursor, p, 30)
	if err != nil || len(turns) != 1 {
		t.Fatalf("turns = %v, err = %v", turns, err)
	}
}

// §11.6: cursor's folder is the realpath of the workspace with slashes as dashes.
func TestTranscriptPath(t *testing.T) {
	got, err := TranscriptPath(runtime.Cursor, "/Users/u", "/private/tmp/repos/cursor/r1", "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	want := "/Users/u/.cursor/projects/private-tmp-repos-cursor-r1/agent-transcripts/chat-1/chat-1.jsonl"
	if got != want {
		t.Fatalf("cursor path = %q, want %q", got, want)
	}
	agy, _ := TranscriptPath(runtime.Agy, "/Users/u", "/w", "conv-1")
	if agy != "/Users/u/.gemini/antigravity-cli/brain/conv-1/.system_generated/logs/transcript_full.jsonl" {
		t.Fatalf("agy path = %q", agy)
	}
}
