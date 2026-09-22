package adapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

type Agy struct{ base }

func newAgy(d Deps) *Agy { return &Agy{base{d: d, kind: kinds.Agy}} }

func init() { register(kinds.Agy, func(d Deps) Adapter { return newAgy(d) }) }

// setupEnv isolates agy's HOME so the swarm MCP config and custom instructions
// never leak into the user's real ~/.gemini, while auth still works via
// symlinks (mirrors codex.go's setupEnv).
func (a *Agy) setupEnv(s Spec) (map[string]string, error) {
	agyHome := filepath.Join(a.d.launchDir(s.SessionID), "agy-home")
	if err := os.MkdirAll(filepath.Join(agyHome, ".gemini"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(agyHome, ".gemini", "config"), 0o700); err != nil {
		return nil, err
	}
	// Symlink the whole directory rather than an allowlist of named files:
	// first-run state (e.g. antigravity_state.pbtxt's onboarding-completed
	// flag) lives here too, and a per-file allowlist that misses one makes
	// agy think every swarm-spawned session is a fresh install and show the
	// interactive setup wizard, which never starts (P0, 2026-09-22).
	realAntigravityCLI := filepath.Join(a.d.UserHome, ".gemini", "antigravity-cli")
	symAntigravityCLI := filepath.Join(agyHome, ".gemini", "antigravity-cli")
	if _, err := os.Stat(realAntigravityCLI); err == nil {
		_ = os.Remove(symAntigravityCLI)
		if err := os.Symlink(realAntigravityCLI, symAntigravityCLI); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(symAntigravityCLI, 0o700); err != nil {
		return nil, err
	}
	mcpCfg, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"swarm": map[string]any{"command": s.Bin, "args": []string{"mcp"}},
	}})
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(agyHome, ".gemini", "config", "mcp_config.json"), mcpCfg, 0o644); err != nil {
		return nil, err
	}
	if s.Instructions != "" {
		if err := writeFileAtomic(filepath.Join(agyHome, ".gemini", "AGENTS.md"),
			[]byte(s.Instructions), 0o644); err != nil {
			return nil, err
		}
	}
	return map[string]string{"HOME": agyHome}, nil
}

// Launch is §11.1. The model is the exact suffixed slug for the chosen effort;
// --effort is never passed (P0-12).
func (a *Agy) Launch(s Spec) (Launch, error) {
	env, err := a.setupEnv(s)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Argv: []string{"agy", "-i", s.Kickoff, "--model", s.Model,
		"--dangerously-skip-permissions"}, Env: env}, nil
}

func (a *Agy) Resume(s Spec) (Launch, error) {
	env, err := a.setupEnv(s)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Argv: []string{"agy", "--conversation", s.ProviderSessionID, "-i", s.Kickoff,
		"--model", s.Model, "--dangerously-skip-permissions"}, Env: env}, nil
}

var (
	agyProcess = []*regexp.Regexp{regexp.MustCompile(`^agy$`)}
	// P0-4: ">" alone between rule lines, with "? for shortcuts" in the footer.
	agyIdle  = regexp.MustCompile("(?m)^(?:\u001b\\[[0-9;]*m)*>(?:\u001b\\[[0-9;]*m)*\\s*$")
	agyReady = regexp.MustCompile(`\? for shortcuts`)
	agyBusy  = regexp.MustCompile(`esc to cancel`)
	agyTrust = regexp.MustCompile(`Do you trust the contents of this project\?`)
)

func (a *Agy) ProcessNames() []*regexp.Regexp { return agyProcess }
func (a *Agy) IdlePrompt() *regexp.Regexp     { return agyIdle }
func (a *Agy) Busy() *regexp.Regexp           { return agyBusy }

// Idle also needs the ready footer: the prompt line alone appears while working.
func (a *Agy) Idle(capture string) bool { return idle(a, capture) && agyReady.MatchString(capture) }

func (a *Agy) StartupDialogs() []Dialog { return []Dialog{{Match: agyTrust, Keys: []string{"Enter"}}} }
func (a *Agy) PromptPatterns() []PromptMatcher {
	return []PromptMatcher{
		{Match: agyTrust, Title: "Trust this project", Action: "Enter"},
	}
}
func (a *Agy) InterruptKeys() []string { return []string{"Escape"} }

func (a *Agy) HookOutput(event string, d HookDecision) ([]byte, error) {
	if d.Block {
		if event == "Stop" {
			return json.Marshal(map[string]string{"decision": "continue", "reason": d.Reason})
		}
		return json.Marshal(map[string]string{"decision": "deny", "reason": d.Reason})
	}
	if d.Context == "" {
		return nil, nil
	}
	// P0-2: ephemeralMessage is visible to the model for this invocation only;
	// userMessage would show up as a user prompt.
	return json.Marshal(map[string]any{"injectSteps": []any{
		map[string]string{"ephemeralMessage": d.Context}}})
}

func (a *Agy) ParseHook(event string, stdin []byte) (HookInput, error) {
	var raw struct {
		ConversationID string `json:"conversationId"`
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcriptPath"`
		ModelName      string `json:"modelName"`
		ToolName       string `json:"tool_name"`
		ToolCall       struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		} `json:"toolCall"`
		ToolInput    json.RawMessage `json:"tool_input"`
		ToolResponse json.RawMessage `json:"tool_response"`
	}
	if len(stdin) > 0 {
		if err := json.Unmarshal(stdin, &raw); err != nil {
			return HookInput{}, err
		}
	}
	var parsedArgs struct {
		ServerName  string `json:"ServerName"`
		ToolName    string `json:"ToolName"`
		CommandLine string `json:"CommandLine"`
	}
	toolArgs := raw.ToolCall.Args
	if len(toolArgs) == 0 && len(raw.ToolInput) > 0 {
		toolArgs = raw.ToolInput
	}
	if len(toolArgs) > 0 {
		_ = json.Unmarshal(toolArgs, &parsedArgs)
	}
	toolName := raw.ToolCall.Name
	if toolName == "" && raw.ToolName != "" {
		toolName = raw.ToolName
	}
	isSwarm := (toolName == "call_mcp_tool" && parsedArgs.ServerName == "swarm") ||
		strings.HasPrefix(toolName, "mcp_swarm_") ||
		(parsedArgs.ServerName == "swarm")
	if toolName == "call_mcp_tool" && parsedArgs.ToolName != "" {
		toolName = parsedArgs.ToolName
	}
	var cmd string
	if toolName == "run_command" {
		cmd = parsedArgs.CommandLine
	}
	provSessionID := raw.ConversationID
	if provSessionID == "" && raw.SessionID != "" {
		provSessionID = raw.SessionID
	}
	return HookInput{
		ProviderSessionID: provSessionID,
		Event:             event,
		ToolName:          toolName,
		Command:           cmd,
		TranscriptPath:    raw.TranscriptPath,
		RawToolInput:      toolArgs,
		ToolResponse:      raw.ToolResponse,
		IsSwarmTool:       isSwarm,
	}, nil
}

func (a *Agy) Installed(ctx context.Context) (string, bool) {
	out, err := a.d.Run(ctx, "agy", "--version")
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

func (a *Agy) AuthOK(ctx context.Context) error {
	tok := filepath.Join(a.d.UserHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if _, err := os.Stat(tok); err != nil {
		return fmt.Errorf("agy isn't signed in. Run `agy login` in a terminal.")
	}
	if _, err := a.d.Run(ctx, "agy", "models"); err != nil {
		return fmt.Errorf("agy isn't signed in. Run `agy login` in a terminal.")
	}
	return nil
}

func (a *Agy) Models(ctx context.Context) ([]catalog.CatalogModel, error) {
	return nil, fmt.Errorf("use catalog.AgyFetcher: %w", errNotImplemented)
}

func (a *Agy) SuperpowersInstalled() bool {
	matches, _ := filepath.Glob(filepath.Join(a.d.UserHome, ".gemini", "config", "plugins", "superpowers*", "skills", "brainstorming", "SKILL.md"))
	return len(matches) > 0
}

// agyWakeMessage is the wire schema Google's docs give for a stream-json
// turn (antigravity.google/docs/cli/headless/), confirmed live against a
// signed-in account: {"event":"user","message":{"content":"<text>"}}. See
// docs/specs/2026-09-22-agy-native-wake-stream-json.md, finding 7.
type agyWakeMessage struct {
	Event   string `json:"event"`
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

// agyWakeResultTimeout bounds how long drainWakeTurn waits for a woken turn
// to finish before forcibly killing it. A real wake notice can trigger tool
// calls, so this is generous, but it must not leak the process forever if
// the turn hangs.
const agyWakeResultTimeout = 5 * time.Minute

// Wake injects a message into the live agy conversation via a short-lived
// side-channel process, the same pattern Codex.Wake uses (`codex queue
// --thread`) -- the interactive session Launch started is never touched.
// Unlike codex's near-instant queue command, driving an agy turn to its
// result event takes real wall-clock time, so Wake returns as soon as the
// write succeeds and hands the process off to drainWakeTurn: blocking here
// would stall WakeDue's per-tick loop for every other pending agent.
func (a *Agy) Wake(ctx context.Context, w WakeTarget) (bool, error) {
	agyHome := filepath.Join(a.d.launchDir(w.SessionID), "agy-home")
	proc, err := a.d.StartEnv(ctx, map[string]string{"HOME": agyHome}, "agy",
		"--conversation", w.ProviderSessionID,
		"--input-format", "stream-json", "--output-format", "stream-json",
		"--dangerously-skip-permissions")
	if err != nil {
		return false, err
	}
	var msg agyWakeMessage
	msg.Event = "user"
	msg.Message.Content = w.Notice
	payload, err := json.Marshal(msg)
	if err != nil {
		proc.Kill()
		return false, err
	}
	if _, err := proc.Stdin.Write(append(payload, '\n')); err != nil {
		proc.Kill()
		return false, err
	}
	go drainWakeTurn(proc)
	return true, nil
}

// drainWakeTurn waits for the turn Wake started to reach its result event
// (or agyWakeResultTimeout, whichever is first), then closes stdin and kills
// the process. It runs detached from Wake's caller.
func drainWakeTurn(proc *execx.Proc) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(proc.Stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), `"event":"result"`) {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(agyWakeResultTimeout):
	}
	_ = proc.Stdin.Close()
	proc.Kill()
}

// ForgetFolder removes one trustedWorkspaces entry (§11.5). agy stores the path
// as given, not the realpath, so nothing is resolved here. The edit is skipped
// with a log line when the file changed since it was read.
func (a *Agy) ForgetFolder(ctx context.Context, path string) error {
	p := filepath.Join(a.d.UserHome, ".gemini", "antigravity-cli", "settings.json")
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		a.d.Log("agy: settings.json does not parse, leaving it alone: %v", err)
		return nil
	}
	var ws []string
	if v, ok := doc["trustedWorkspaces"]; ok {
		if err := json.Unmarshal(v, &ws); err != nil {
			return nil
		}
	}
	kept := slices.DeleteFunc(slices.Clone(ws), func(s string) bool { return s == path })
	if len(kept) == len(ws) {
		return nil
	}
	next, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	doc["trustedWorkspaces"] = next
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	// only write when the file is still what we read
	now, err := os.ReadFile(p)
	if err != nil || string(now) != string(raw) {
		a.d.Log("agy: settings.json changed while editing, skipping the trust cleanup for %s", path)
		return nil
	}
	return writeFileAtomic(p, out, 0o644)
}
