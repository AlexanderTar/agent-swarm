package adapter

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestAgyStreamJSONProtocolProbe is a live spike against the real agy CLI and
// a real, signed-in account. It makes real, paid API calls, so it is skipped
// unless AGY_LIVE_PROBE=1 is set (never in CI). Its job is to nail down the
// exact NDJSON wire schema for injecting a turn into a live agy conversation
// via `--input-format stream-json`, and to prove doing so repeatedly against
// the SAME --conversation id doesn't corrupt or reorder history -- see
// docs/specs/2026-09-22-agy-native-wake-stream-json.md, Decision 6.
func TestAgyStreamJSONProtocolProbe(t *testing.T) {
	if os.Getenv("AGY_LIVE_PROBE") != "1" {
		t.Skip("live probe against real agy CLI + API; set AGY_LIVE_PROBE=1 to run")
	}

	t.Run("SchemaConfirmation", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			status, response, _ := runAgyStreamTurn(t, "", "reply with the exact single word PROBE_A and nothing else")
			if status != "SUCCESS" || !strings.Contains(response, "PROBE_A") {
				t.Fatalf("run %d: status=%q response=%q, want SUCCESS containing PROBE_A", i, status, response)
			}
		}
	})

	t.Run("ConcurrencySafety", func(t *testing.T) {
		status, _, conversationID := runAgyStreamTurn(t, "", "reply with the exact single word ANCHOR and nothing else")
		if status != "SUCCESS" || conversationID == "" {
			t.Fatalf("anchor turn: status=%q conversationID=%q", status, conversationID)
		}
		for _, marker := range []string{"MARKER_1", "MARKER_2", "MARKER_3"} {
			status, _, _ := runAgyStreamTurn(t, conversationID, "reply with the exact single word "+marker+" and nothing else")
			if status != "SUCCESS" {
				t.Fatalf("marker %s: status=%q", marker, status)
			}
		}
		status, response, _ := runAgyStreamTurn(t, conversationID,
			"list every marker word you were just told, in the exact order you received them, space-separated, and nothing else")
		if status != "SUCCESS" {
			t.Fatalf("recap turn: status=%q", status)
		}
		i1, i2, i3 := strings.Index(response, "MARKER_1"), strings.Index(response, "MARKER_2"), strings.Index(response, "MARKER_3")
		if i1 < 0 || i2 < 0 || i3 < 0 || !(i1 < i2 && i2 < i3) {
			t.Fatalf("recap response = %q, want MARKER_1 MARKER_2 MARKER_3 in order", response)
		}
	})
}

type agyStreamEvent struct {
	Event          string `json:"event"` // "init" carries conversation_id at top level
	ConversationID string `json:"conversation_id"`
	Result         *struct {
		Status   string `json:"status"`
		Response string `json:"response"`
	} `json:"result"`
}

// probeAgyHome builds the SAME isolated HOME setupEnv builds for a real swarm
// spawn (whole-directory symlink of the real ~/.gemini/antigravity-cli, its
// own mcp_config.json). Production Wake() will run under this exact
// environment, so the probe must too -- the real $HOME's extra MCP servers
// (e.g. agentmemory via `npx`) would otherwise add a confounding cold-start
// delay that never happens for a real swarm-spawned agent.
func probeAgyHome(t *testing.T) map[string]string {
	t.Helper()
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Home: t.TempDir(), UserHome: realHome, Bin: "/usr/local/bin/swarm"}
	env, err := newAgy(deps).setupEnv(Spec{SessionID: "probe_" + t.Name()})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// runAgyStreamTurn drives one turn of `agy --input-format stream-json` via a
// real pipe (StdinPipe/StdoutPipe), not a simulated terminal, and returns the
// final result's status, response text and the conversation id observed on
// the init event. conversationID == "" starts a fresh conversation. stdin is
// held open until the result event arrives (or the context times out), since
// closing it early is itself a hypothesis under test, not a given.
func runAgyStreamTurn(t *testing.T, conversationID, promptText string) (status, response, gotConversationID string) {
	t.Helper()

	args := []string{"--input-format", "stream-json", "--output-format", "stream-json",
		"--dangerously-skip-permissions"}
	if conversationID != "" {
		args = append([]string{"--conversation", conversationID}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "agy", args...)
	env := probeAgyHome(t)
	cmd.Env = append(os.Environ(), "HOME="+env["HOME"])

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{
		"event":   "user",
		"message": map[string]any{"content": promptText},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(append(payload, '\n')); err != nil {
		t.Fatal(err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var ev agyStreamEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Event == "init" && ev.ConversationID != "" {
			gotConversationID = ev.ConversationID
		}
		if ev.Event == "result" && ev.Result != nil {
			status = ev.Result.Status
			response = ev.Result.Response
			break // got the turn's outcome; stop reading, then tear down below
		}
	}
	_ = stdin.Close()
	cancel()
	_ = cmd.Wait()
	return status, response, gotConversationID
}
