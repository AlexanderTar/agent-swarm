// scenario.go holds the scenario format and the runner that drives it against
// the daemon's /mcp endpoint (P2 Task 37). It never calls a model: every step
// is either an MCP tool call, a file write, a wait, a pane draw or a sleep.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Scenario is one scripted agent (scripts/e2e/scenarios/<name>.json).
type Scenario struct {
	Name  string `json:"name"`
	Draw  string `json:"draw"`
	Steps []Step `json:"steps"`
}

// Step is one line of the script. Only one of Tool/WriteFile/WaitFor/ForEach/
// Draw/SleepMs/Exit is normally set; ExpectError modifies a Tool step.
type Step struct {
	Tool        string          `json:"tool"`
	Args        json.RawMessage `json:"args"`
	Save        string          `json:"save"`
	WaitFor     *WaitFor        `json:"wait_for"`
	WriteFile   *WriteFile      `json:"write_file"`
	ForEach     *ForEach        `json:"for_each"`
	SleepMs     int             `json:"sleep_ms"`
	Draw        string          `json:"draw"`
	ExpectError string          `json:"expect_error"`
	Exit        *int            `json:"exit"`
}

type WriteFile struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

type messageCount struct {
	Kind string `json:"kind"`
	N    int    `json:"n"`
}

type WaitFor struct {
	MessageKind  string        `json:"message_kind"`
	MessageCount *messageCount `json:"message_count"`
	TimeoutSec   int           `json:"timeout_sec"`
}

type ForEach struct {
	Of   string          `json:"of"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// namedBodies resolves a write_file body given as "@name" to a file under
// scripts/e2e/bodies/<name>.md (Task 38). A body that doesn't start with "@"
// is used literally.
func namedBody(dir, body string) (string, error) {
	name, ok := strings.CutPrefix(body, "@")
	if !ok {
		return body, nil
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".md"))
	if err != nil {
		return "", fmt.Errorf("named body %q: %w", body, err)
	}
	return string(b), nil
}

// Runner drives one scenario's steps in order against one daemon.
type Runner struct {
	URL, Token string
	Out        io.Writer
	// BodiesDir is where "@name" write_file bodies are read from; empty means
	// the literal string is used as-is (the unit tests never use @-bodies).
	BodiesDir string

	Saved map[string]map[string]any

	mu     sync.Mutex
	mode   string
	nextID int
}

func (r *Runner) setMode(m string) {
	r.mu.Lock()
	r.mode = m
	r.mu.Unlock()
}

// Mode is the pane's current draw mode, read by the drawing ticker in main.go.
func (r *Runner) Mode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mode == "" {
		return "idle"
	}
	return r.mode
}

var subRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// substitute replaces every ${...} token: ${item.FIELD} reads the current
// for_each item, ${NAME.field} reads an earlier step's saved result, and
// anything else reads the process environment.
func (r *Runner) substitute(item map[string]any, s string) string {
	return subRe.ReplaceAllStringFunc(s, func(m string) string {
		return r.resolve(item, m[2:len(m)-1])
	})
}

func (r *Runner) resolve(item map[string]any, key string) string {
	parts := strings.SplitN(key, ".", 2)
	if parts[0] == "item" {
		if item == nil {
			return ""
		}
		if len(parts) == 1 {
			return fmt.Sprint(item)
		}
		return fmt.Sprint(item[parts[1]])
	}
	if len(parts) == 2 {
		if m, ok := r.Saved[parts[0]]; ok {
			return fmt.Sprint(m[parts[1]])
		}
		return ""
	}
	return os.Getenv(key)
}

// resolveList reads NAME.field out of Saved for a for_each's "of".
func (r *Runner) resolveList(of string) ([]any, error) {
	parts := strings.SplitN(of, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("for_each.of %q must be NAME.field", of)
	}
	m, ok := r.Saved[parts[0]]
	if !ok {
		return nil, fmt.Errorf("for_each: no saved value %q", parts[0])
	}
	list, _ := m[parts[1]].([]any)
	return list, nil
}

// call makes one JSON-RPC tools/call request and returns the tool's raw JSON
// text result, or the tool's own error text (never a Go error) when the MCP
// result is IsError.
func (r *Runner) call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	r.mu.Lock()
	r.nextID++
	id := r.nextID
	r.mu.Unlock()
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": json.RawMessage(args)},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var env struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%s: bad response (status %d) %s: %w", name, resp.StatusCode, raw, err)
	}
	if env.Error != nil {
		return nil, errors.New(env.Error.Message)
	}
	if env.Result == nil || len(env.Result.Content) == 0 {
		return nil, fmt.Errorf("%s: empty result (status %d): %s", name, resp.StatusCode, raw)
	}
	text := env.Result.Content[0].Text
	if env.Result.IsError {
		return nil, errors.New(text)
	}
	return json.RawMessage(text), nil
}

// Run executes every step in order, substituting as it goes.
func (r *Runner) Run(ctx context.Context, sc Scenario) error {
	if r.Saved == nil {
		r.Saved = map[string]map[string]any{}
	}
	if sc.Draw != "" {
		r.setMode(sc.Draw)
	}
	for i, st := range sc.Steps {
		if err := r.runStep(ctx, st, nil); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return nil
}

func (r *Runner) runStep(ctx context.Context, st Step, item map[string]any) error {
	switch {
	case st.WriteFile != nil:
		path := r.substitute(item, st.WriteFile.Path)
		body, err := namedBody(r.BodiesDir, st.WriteFile.Body)
		if err != nil {
			return err
		}
		body = r.substitute(item, body)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(body), 0o644)

	case st.WaitFor != nil:
		return r.waitFor(ctx, st.WaitFor)

	case st.ForEach != nil:
		list, err := r.resolveList(st.ForEach.Of)
		if err != nil {
			return err
		}
		for _, el := range list {
			m, _ := el.(map[string]any)
			sub := Step{Tool: st.ForEach.Tool, Args: st.ForEach.Args}
			if err := r.runStep(ctx, sub, m); err != nil {
				return err
			}
		}
		return nil

	case st.SleepMs > 0:
		select {
		case <-time.After(time.Duration(st.SleepMs) * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}

	case st.Draw != "":
		r.setMode(st.Draw)
		return nil

	case st.Exit != nil:
		return nil // main.go reads sc.Steps again for the final exit code

	case st.Tool != "":
		argsStr := r.substitute(item, string(st.Args))
		result, err := r.call(ctx, st.Tool, json.RawMessage(argsStr))
		if st.ExpectError != "" {
			if err == nil {
				return fmt.Errorf("%s: expected an error containing %q, got none", st.Tool, st.ExpectError)
			}
			if !strings.Contains(err.Error(), st.ExpectError) {
				return fmt.Errorf("%s: error %q does not contain %q", st.Tool, err.Error(), st.ExpectError)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", st.Tool, err)
		}
		if r.Out != nil {
			fmt.Fprintf(r.Out, "%s -> %s\n", st.Tool, result)
		}
		if st.Save != "" {
			var m map[string]any
			if len(result) > 0 {
				if err := json.Unmarshal(result, &m); err != nil {
					return fmt.Errorf("%s: save %q: %w", st.Tool, st.Save, err)
				}
			}
			r.Saved[st.Save] = m
		}
		return nil
	}
	return nil
}

// exitCode is the last "exit" step's value, or nil if the scenario never had one.
func (sc Scenario) exitCode() *int {
	for i := len(sc.Steps) - 1; i >= 0; i-- {
		if sc.Steps[i].Exit != nil {
			return sc.Steps[i].Exit
		}
	}
	return nil
}

const pollEvery = time.Second

// waitFor polls swarm_sync until a message of the wanted kind (or enough of
// them) arrives, acking every message it sees along the way so a repeated
// poll never recounts one.
func (r *Runner) waitFor(ctx context.Context, wf *WaitFor) error {
	timeout := time.Duration(wf.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	count := 0
	for {
		args, _ := json.Marshal(map[string]any{"limit": 50})
		if res, err := r.call(ctx, "swarm_sync", args); err == nil {
			var out struct {
				Messages []struct {
					MsgID string `json:"msg_id"`
					Kind  string `json:"kind"`
				} `json:"messages"`
			}
			json.Unmarshal(res, &out)
			var ack []string
			found := false
			for _, m := range out.Messages {
				ack = append(ack, m.MsgID)
				if wf.MessageKind != "" && m.Kind == wf.MessageKind {
					found = true
				}
				if wf.MessageCount != nil && m.Kind == wf.MessageCount.Kind {
					count++
				}
			}
			if len(ack) > 0 {
				ackArgs, _ := json.Marshal(map[string]any{"ack": ack})
				r.call(ctx, "swarm_sync", ackArgs)
			}
			if found || (wf.MessageCount != nil && count >= wf.MessageCount.N) {
				return nil
			}
		}
		if time.Now().After(deadline) {
			desc := wf.MessageKind
			if wf.MessageCount != nil {
				desc = fmt.Sprintf("%d x %s (got %d)", wf.MessageCount.N, wf.MessageCount.Kind, count)
			}
			return fmt.Errorf("wait_for timed out waiting for %s", desc)
		}
		select {
		case <-time.After(pollEvery):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// drawPane writes one frame of the claude-style pane for the given mode, so
// the daemon's idle/busy regexes (internal/adapter) can match it through
// tmux capture-pane. idle and busy mirror internal/adapter.fakeIdle/fakeBusy
// exactly; typed and shell are scenario 13/15's own extra modes.
func drawPane(w io.Writer, mode string) {
	switch mode {
	case "busy":
		fmt.Fprint(w, "✽ Beboppin'… (3s · ↓ 12 tokens)\r\n")
	case "typed":
		fmt.Fprint(w, "❯ half typed\r\n")
	case "shell":
		fmt.Fprint(w, "$ \r\n")
	default: // "idle" and anything unrecognised
		fmt.Fprint(w, "❯ \r\n")
	}
}
