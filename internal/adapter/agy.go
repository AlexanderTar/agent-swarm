package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

type Agy struct{ base }

func newAgy(d Deps) *Agy { return &Agy{base{d: d, kind: kinds.Agy}} }

func init() { register(kinds.Agy, func(d Deps) Adapter { return newAgy(d) }) }

// Launch is §11.1. The model is the exact suffixed slug for the chosen effort;
// --effort is never passed (P0-12).
func (a *Agy) Launch(s Spec) (Launch, error) {
	return Launch{Argv: []string{"agy", "-i", s.Kickoff, "--model", s.Model,
		"--dangerously-skip-permissions"}, Env: map[string]string{}}, nil
}

func (a *Agy) Resume(s Spec) (Launch, error) {
	return Launch{Argv: []string{"agy", "--conversation", s.ProviderSessionID, "-i", s.Kickoff,
		"--model", s.Model, "--dangerously-skip-permissions"}, Env: map[string]string{}}, nil
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
func (a *Agy) InterruptKeys() []string  { return []string{"Escape"} }

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
		ConversationID string          `json:"conversationId"`
		SessionID      string          `json:"session_id"`
		TranscriptPath string          `json:"transcriptPath"`
		ModelName      string          `json:"modelName"`
		ToolName       string          `json:"tool_name"`
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

func (a *Agy) Wake(ctx context.Context, sess WakeTarget) (bool, error) {
	return false, nil
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
