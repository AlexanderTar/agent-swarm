package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

type Claude struct{ base }

func newClaude(d Deps) *Claude { return &Claude{base{d: d, kind: kinds.Claude}} }

// Registering from this file keeps adapter.go untouched, so Tasks 6-9 never
// contend for it (they run in the same worktree).
func init() { register(kinds.Claude, func(d Deps) Adapter { return newClaude(d) }) }

// claudeSettings is the per-launch --settings JSON (L23, §11.1). Attribution is
// off, and advisorModel is present only in native advisor mode (L28).
func (c *Claude) settingsJSON(s Spec) ([]byte, error) {
	hook := func(event string) []any {
		return []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": s.Bin + " hook claude " + event, "timeout": 3}}}}
	}
	no := false
	cfg := map[string]any{
		"attribution":         map[string]string{"commit": "", "pr": ""},
		"includeCoAuthoredBy": &no,
		"hooks": map[string]any{
			"SessionStart": hook("SessionStart"), "UserPromptSubmit": hook("UserPromptSubmit"),
			"PreToolUse": hook("PreToolUse"), "PostToolUse": hook("PostToolUse"),
			"PreCompact": hook("PreCompact"), "Stop": hook("Stop"),
		},
	}
	if s.AdvisorModel != "" {
		cfg["advisorModel"] = s.AdvisorModel
	}
	return json.Marshal(cfg)
}

func (c *Claude) flags(s Spec) ([]string, error) {
	mcp, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"swarm": map[string]any{"type": "stdio", "command": s.Bin, "args": []string{"mcp"}}}})
	if err != nil {
		return nil, err
	}
	mcpPath, err := c.d.writeLaunchFile(s.SessionID, "claude-mcp.json", mcp)
	if err != nil {
		return nil, err
	}
	set, err := c.settingsJSON(s)
	if err != nil {
		return nil, err
	}
	setPath, err := c.d.writeLaunchFile(s.SessionID, "claude-settings.json", set)
	if err != nil {
		return nil, err
	}
	a := []string{"-n", s.AgentName, "--model", s.Model}
	if s.Effort != "" {
		a = append(a, "--effort", s.Effort)
	}
	return append(a, "--dangerously-skip-permissions", "--mcp-config", mcpPath,
		"--settings", setPath, "--dangerously-load-development-channels", "server:swarm"), nil
}

func (c *Claude) Launch(s Spec) (Launch, error) {
	f, err := c.flags(s)
	if err != nil {
		return Launch{}, err
	}
	argv := append([]string{"claude", "--session-id", newUUIDv4()}, f...)
	return Launch{Argv: append(argv, "--", s.Kickoff), Env: map[string]string{}}, nil
}

func (c *Claude) Resume(s Spec) (Launch, error) {
	f, err := c.flags(s)
	if err != nil {
		return Launch{}, err
	}
	argv := append([]string{"claude", "--resume", s.ProviderSessionID}, f...)
	return Launch{Argv: append(argv, "--", s.Kickoff), Env: map[string]string{}}, nil
}

var (
	claudeProcess = []*regexp.Regexp{regexp.MustCompile(`^\d+\.\d+\.\d+$`), regexp.MustCompile(`^claude$`)}
	// P0-4, extended 2026-09-19 (live incident): "❯" + U+00A0 alone between
	// two rule lines used to be the whole idle prompt. Claude 2.1.278 now
	// draws a dim "next action" suggestion right after the prompt (SGR 2,
	// e.g. "\x1b[2mcheck on s0.1 progress\x1b[0m") when idle with an empty
	// input box, and a leading color-reset escape before the "❯" itself
	// once that suggestion needs a different color than the line above it.
	// The old regex required nothing but the bare prompt, so every session
	// showing a suggestion was never detected as idle: watchStartup's Idle
	// check never fired (falling through to the stall-timeout branch instead
	// of a clean startup) and wake.go's idle-paste never fired either, so a
	// real pending message just sat in the pane forever, renotified as
	// "undeliverable" on every wake tick. The fix tolerates leading SGR
	// resets before "❯" and an SGR-2-wrapped span after it, but the
	// optional trailing content must still start with SGR 2 specifically --
	// a real human draft typed into the box (pane-input-nonempty.txt) renders
	// in the default color with no escape prefix at all, so it still
	// correctly fails to match.
	claudeIdle = regexp.MustCompile("(?m)^(?:\x1b\\[[0-9;]*m)*\u276f[\u00a0 ](?:\x1b\\[2m.*)?\\s*$")
	// P0-4: the spinner, e.g. "✽ Beboppin'… (48s · ↓ 114 tokens)".
	claudeBusy     = regexp.MustCompile("(?m)^[\u273b\u273d\u2736\u2722\u00b7*] \\S+\u2026 \\(")
	claudeTrust    = regexp.MustCompile(`Is this a project you created or one you trust\?`)
	claudeTrustYes = regexp.MustCompile(`Yes, I trust this folder`)
	claudeDev      = regexp.MustCompile(`I am using this for local development`)
)

func (c *Claude) ProcessNames() []*regexp.Regexp { return claudeProcess }
func (c *Claude) IdlePrompt() *regexp.Regexp     { return claudeIdle }
func (c *Claude) Busy() *regexp.Regexp           { return claudeBusy }
func (c *Claude) Idle(capture string) bool       { return idle(c, capture) }

func (c *Claude) StartupDialogs() []Dialog {
	return []Dialog{
		{Match: claudeTrust, Require: claudeTrustYes, Keys: []string{"Down", "Enter"}},
		{Match: claudeDev, Keys: []string{"Enter"}},
	}
}

func (c *Claude) PromptPatterns() []PromptMatcher {
	return []PromptMatcher{
		{Match: claudeTrust, Require: claudeTrustYes, Title: "Trust this project", Action: "Down+Enter"},
		{Match: claudeDev, Title: "Confirm local development", Action: "Enter"},
	}
}

func (c *Claude) InterruptKeys() []string { return []string{"Escape"} }

// Wake goes through the shim's channel bridge (§11.3 step 1): the daemon
// publishes a wake event, the shim emits notifications/claude/channel.
func (c *Claude) Wake(ctx context.Context, w WakeTarget) (bool, error) {
	if c.d.PublishWake == nil {
		return false, nil
	}
	if err := c.d.PublishWake(ctx, w.SessionID, w.Notice); err != nil {
		return false, err
	}
	return true, nil
}

func (c *Claude) HookOutput(event string, d HookDecision) ([]byte, error) {
	if d.Block {
		if event == "Stop" {
			return json.Marshal(map[string]string{"decision": "block", "reason": d.Reason})
		}
		return json.Marshal(map[string]any{
			"hookSpecificOutput": map[string]string{
				"hookEventName":            event,
				"permissionDecision":       "deny",
				"permissionDecisionReason": d.Reason,
			},
		})
	}
	if d.Context == "" {
		return nil, nil
	}
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]string{
			"additionalContext": d.Context,
			"hookEventName":     event,
		},
	})
}

func (c *Claude) ParseHook(event string, stdin []byte) (HookInput, error) {
	var raw struct {
		SessionID      string          `json:"session_id"`
		ToolName       string          `json:"tool_name"`
		Source         string          `json:"source"`
		Cwd            string          `json:"cwd"`
		TranscriptPath string          `json:"transcript_path"`
		ToolInput      json.RawMessage `json:"tool_input"`
		ToolResponse   json.RawMessage `json:"tool_response"`
	}
	if len(stdin) > 0 {
		if err := json.Unmarshal(stdin, &raw); err != nil {
			return HookInput{}, err
		}
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
		ProviderSessionID: raw.SessionID,
		Event:             event,
		ToolName:          raw.ToolName,
		Command:           cmd,
		Source:            raw.Source,
		Cwd:               raw.Cwd,
		TranscriptPath:    raw.TranscriptPath,
		RawToolInput:      raw.ToolInput,
		ToolResponse:      raw.ToolResponse,
		IsSwarmTool:       strings.HasPrefix(raw.ToolName, "mcp__swarm__"),
	}, nil
}

func (c *Claude) Installed(ctx context.Context) (string, bool) {
	out, err := c.d.Run(ctx, "claude", "--version")
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

func (c *Claude) AuthOK(ctx context.Context) error {
	out, err := c.d.Run(ctx, "claude", "auth", "status", "--json")
	if err != nil {
		return fmt.Errorf("Claude isn't signed in. Run `claude /login` in a terminal.")
	}
	var res struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if err := json.Unmarshal(out, &res); err != nil || !res.LoggedIn {
		return fmt.Errorf("Claude isn't signed in. Run `claude /login` in a terminal.")
	}
	return nil
}

func (c *Claude) Models(ctx context.Context) ([]catalog.CatalogModel, error) {
	return nil, fmt.Errorf("use catalog.ClaudeFetcher: %w", errNotImplemented)
}

func (c *Claude) SuperpowersInstalled() bool {
	b1, _ := filepath.Glob(filepath.Join(c.d.UserHome, ".claude", "plugins", "cache", "*", "superpowers", "*", "skills", "brainstorming", "SKILL.md"))
	b2, _ := filepath.Glob(filepath.Join(c.d.UserHome, ".claude", "plugins", "cache", "superpowers", "*", "skills", "brainstorming", "SKILL.md"))
	t1, _ := filepath.Glob(filepath.Join(c.d.UserHome, ".claude", "plugins", "cache", "*", "superpowers", "*", "skills", "test-driven-development", "SKILL.md"))
	t2, _ := filepath.Glob(filepath.Join(c.d.UserHome, ".claude", "plugins", "cache", "superpowers", "*", "skills", "test-driven-development", "SKILL.md"))
	return (len(b1) > 0 || len(b2) > 0) && (len(t1) > 0 || len(t2) > 0)
}
