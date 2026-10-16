package advisor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
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
	got, err := TranscriptPath(runtime.Cursor, "/Users/u", "", "/private/tmp/repos/cursor/r1", "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	want := "/Users/u/.cursor/projects/private-tmp-repos-cursor-r1/agent-transcripts/chat-1/chat-1.jsonl"
	if got != want {
		t.Fatalf("cursor path = %q, want %q", got, want)
	}
	agy, _ := TranscriptPath(runtime.Agy, "/Users/u", "", "/w", "conv-1")
	if agy != "/Users/u/.gemini/antigravity-cli/brain/conv-1/.system_generated/logs/transcript_full.jsonl" {
		t.Fatalf("agy path = %q", agy)
	}
}

// BUG-24: Claude Code encodes every non-alphanumeric char of the cwd as "-",
// so a dot (as in ~/.swarm) becomes a dash too.
func TestTranscriptPathClaudeEncodesDots(t *testing.T) {
	got, err := TranscriptPath(runtime.Claude, "/Users/u", "", "/Users/x/.swarm/work/a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/Users/u/.claude/projects/-Users-x--swarm-work-a/s1.jsonl"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestReadGrokTranscript(t *testing.T) {
	turns, err := ReadTranscript(runtime.Grok, "testdata/grok/chat_history.jsonl", 30)
	if err != nil {
		t.Fatal(err)
	}
	want := []Turn{
		{Role: "user", Text: "Use list_dir once, then summarise."},
		{Role: "assistant", Tool: "list_dir"},
		{Role: "assistant", Text: "The directory contains file.txt."},
		{Role: "assistant", Text: "GOLF-STOP"},
	}
	if !reflect.DeepEqual(turns, want) {
		t.Fatalf("turns = %+v\nwant    %+v", turns, want)
	}
}

// The grok transcript lives under the Swarm grok home, in a folder named for
// the url-encoded cwd that the daemon does not have to re-derive.
func TestGrokTranscriptPathIsUnderTheGrokHome(t *testing.T) {
	swarmHome := t.TempDir()
	dir := filepath.Join(grokhome.Dir(swarmHome), "sessions", "%2Fw%2Fa", "sess-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := TranscriptPath(runtime.Grok, "/Users/u", swarmHome, "/w/a", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "chat_history.jsonl"); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if _, err := TranscriptPath(runtime.Grok, "/Users/u", swarmHome, "/w/a", "no-such-session"); err == nil {
		t.Fatal("an unknown session has no transcript path")
	}
	if _, err := TranscriptPath(runtime.Grok, "/Users/u", swarmHome, "/w/a", ""); err == nil {
		t.Fatal("an empty provider session id must not glob every session")
	}
}

func TestMuseAdvisorTranscript(t *testing.T) {
	turns, err := ReadTranscript(runtime.Muse, "testdata/muse/session-sample.jsonl", 30)
	if err != nil {
		t.Fatal(err)
	}
	want := []Turn{
		{Role: "user", Text: "Review STORY-88."},
		{Role: "assistant", Text: "Using the swarm skill."},
		{Role: "assistant", Tool: "read_skill"},
		{Role: "assistant", Text: "Verdict: approve."},
	}
	if !reflect.DeepEqual(turns, want) {
		t.Fatalf("turns = %+v\nwant    %+v", turns, want)
	}
}

// muse keeps one dated folder per session: sessions/YYYY/MM/DD/<id>/session.jsonl.
func TestMuseTranscriptPath(t *testing.T) {
	userHome := t.TempDir()
	dir := filepath.Join(userHome, ".local", "share", "muse", "sessions", "2026", "10", "16", "sess-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := TranscriptPath(runtime.Muse, userHome, "", "/w", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "session.jsonl"); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if _, err := TranscriptPath(runtime.Muse, userHome, "", "/w", "no-such"); err == nil {
		t.Fatal("an unknown session has no transcript path")
	}
	if _, err := TranscriptPath(runtime.Muse, userHome, "", "/w", ""); err == nil {
		t.Fatal("an empty provider session id must not glob every session")
	}
}
