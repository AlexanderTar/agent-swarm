package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

type Cursor struct{ base }

func newCursor(d Deps) *Cursor { return &Cursor{base{d: d, kind: kinds.Cursor}} }

func init() { register(kinds.Cursor, func(d Deps) Adapter { return newCursor(d) }) }

// PreRunOutput is the placeholder the spawner replaces with the trimmed stdout of
// the last PreRun command (cursor's create-chat id, §11.1).
const PreRunOutput = "{{prerun}}"

// CursorMCPEnv is the env block `swarm install` writes into ~/.cursor/mcp.json.
// Cursor passes no environment to MCP servers, and an unset variable arrives as
// the literal text ${env:NAME}, which the shim treats as unset (P0-1, L16).
func CursorMCPEnv() map[string]string {
	return map[string]string{
		"SWARM_URL":        "${env:SWARM_URL}",
		"SWARM_SESSION":    "${env:SWARM_SESSION}",
		"SWARM_TOKEN_FILE": "${env:SWARM_TOKEN_FILE}",
		"SWARM_AGENT_KIND": "cursor",
	}
}

func (c *Cursor) argv(s Spec, chat string) []string {
	a := []string{"cursor-agent", "--resume", chat, "--yolo", "--trust", "--approve-mcps",
		"--model", s.Model, "--workspace", s.Cwd}
	for _, p := range s.PluginDirs {
		a = append(a, "--plugin-dir", p)
	}
	return append(a, s.Kickoff)
}

// setupEnv isolates cursor's MCP config to just the swarm server and, when
// instructions are set, writes a workspace AGENTS.md (mirrors codex.go's
// setupEnv; cursor has no HOME-wide config dir to isolate, only CURSOR_DATA_DIR).
func (c *Cursor) setupEnv(s Spec) (map[string]string, error) {
	cursorHome := filepath.Join(c.d.launchDir(s.SessionID), "cursor-home")
	if err := os.MkdirAll(cursorHome, 0o700); err != nil {
		return nil, err
	}
	mcpCfg, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"swarm": map[string]any{"command": s.Bin, "args": []string{"mcp"}, "env": CursorMCPEnv()},
	}})
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(cursorHome, "mcp.json"), mcpCfg, 0o644); err != nil {
		return nil, err
	}
	if s.Instructions != "" && s.Cwd != "" {
		if err := os.MkdirAll(s.Cwd, 0o755); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(filepath.Join(s.Cwd, "AGENTS.md"),
			[]byte(s.Instructions), 0o644); err != nil {
			return nil, err
		}
	}
	return map[string]string{"CURSOR_DATA_DIR": cursorHome}, nil
}

func (c *Cursor) Launch(s Spec) (Launch, error) {
	env, err := c.setupEnv(s)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Argv: c.argv(s, PreRunOutput), Env: env,
		PreRun: [][]string{{"cursor-agent", "create-chat"}}}, nil
}

func (c *Cursor) Resume(s Spec) (Launch, error) {
	env, err := c.setupEnv(s)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Argv: c.argv(s, s.ProviderSessionID), Env: env}, nil
}

var (
	cursorProcess = []*regexp.Regexp{regexp.MustCompile(`^node$`)}
	// P0-4: "→ " followed by nothing or an entirely dim placeholder, and no
	// "ctrl+c to stop" on that line.
	cursorIdle = regexp.MustCompile("(?m)^(?:\u001b\\[[0-9;]*m|\\s)*\u2192\\s*(?:(?:\u001b\\[[0-9;]*m)*(?:Add a follow-up|[^\u001b\n]*\u001b\\[(?:0;)?2m[^\n]*))?(?:\u001b\\[[0-9;]*m|\\s)*$")
	cursorBusy = regexp.MustCompile(`ctrl\+c to stop`)
)

func (c *Cursor) ProcessNames() []*regexp.Regexp { return cursorProcess }
func (c *Cursor) IdlePrompt() *regexp.Regexp     { return cursorIdle }
func (c *Cursor) Busy() *regexp.Regexp           { return cursorBusy }
func (c *Cursor) Idle(capture string) bool       { return idle(c, capture) }

var (
	cursorAllowDeny = regexp.MustCompile(`(?i)allow\s*/\s*deny`)
	cursorContinue  = regexp.MustCompile(`(?i)do you want to continue\?`)
)

func (c *Cursor) StartupDialogs() []Dialog { return nil } // --yolo --trust --approve-mcps
func (c *Cursor) PromptPatterns() []PromptMatcher {
	return []PromptMatcher{
		{Match: cursorAllowDeny, Title: "Permission prompt (Allow/Deny)", Action: "Enter"},
		{Match: cursorContinue, Title: "Continue confirmation", Action: "y"},
	}
}
func (c *Cursor) InterruptKeys() []string  { return []string{"C-c"} }

func (c *Cursor) HookOutput(event string, d HookDecision) ([]byte, error) {
	if d.Block {
		if event == "stop" {
			return json.Marshal(map[string]string{"followup_message": d.Reason})
		}
		return json.Marshal(map[string]string{"permission": "deny",
			"user_message": d.Reason, "agent_message": d.Reason})
	}
	if d.Context == "" {
		return nil, nil
	}
	return json.Marshal(map[string]string{"additional_context": d.Context})
}

func (c *Cursor) ParseHook(event string, stdin []byte) (HookInput, error) {
	var raw struct {
		ConversationID string          `json:"conversation_id"`
		TranscriptPath *string         `json:"transcript_path"`
		ToolName       string          `json:"tool_name"`
		ToolInput      json.RawMessage `json:"tool_input"`
		ToolResponse   json.RawMessage `json:"tool_response"`
		Prompt         string          `json:"prompt"`
	}
	if len(stdin) > 0 {
		if err := json.Unmarshal(stdin, &raw); err != nil {
			return HookInput{}, err
		}
	}
	var tp string
	if raw.TranscriptPath != nil {
		tp = *raw.TranscriptPath
	}
	var cmd string
	if len(raw.ToolInput) > 0 {
		var inputWithCmd struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(raw.ToolInput, &inputWithCmd)
		cmd = inputWithCmd.Command
	}
	return HookInput{
		ProviderSessionID: raw.ConversationID,
		Event:             event,
		ToolName:          raw.ToolName,
		Command:           cmd,
		TranscriptPath:    tp,
		RawToolInput:      raw.ToolInput,
		ToolResponse:      raw.ToolResponse,
		Prompt:            raw.Prompt,
		IsSwarmTool:       strings.HasPrefix(raw.ToolName, "MCP:swarm_"),
	}, nil
}

func (c *Cursor) Installed(ctx context.Context) (string, bool) {
	out, err := c.d.Run(ctx, "cursor-agent", "--version")
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", false
	}
	return fields[len(fields)-1], true
}

func (c *Cursor) AuthOK(ctx context.Context) error {
	out, err := c.d.Run(ctx, "cursor-agent", "status")
	if err != nil || !strings.Contains(string(out), "Logged in") {
		return fmt.Errorf("Cursor isn't signed in. Run `cursor-agent login` in a terminal.")
	}
	return nil
}

func (c *Cursor) Models(ctx context.Context) ([]catalog.CatalogModel, error) {
	return nil, fmt.Errorf("use catalog.CursorFetcher: %w", errNotImplemented)
}

func (c *Cursor) SuperpowersInstalled() bool {
	m1, _ := filepath.Glob(filepath.Join(c.d.UserHome, ".cursor", "plugins", "cache", "*", "superpowers", "*", "skills", "brainstorming", "SKILL.md"))
	if len(m1) > 0 {
		return true
	}
	if _, err := os.Stat(filepath.Join(c.d.UserHome, ".cursor", "plugins", "local", "superpowers", "skills", "brainstorming", "SKILL.md")); err == nil {
		return true
	}
	return false
}

func (c *Cursor) Wake(ctx context.Context, sess WakeTarget) (bool, error) {
	return false, nil
}
