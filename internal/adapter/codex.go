package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

type Codex struct{ base }

func newCodex(d Deps) *Codex { return &Codex{base{d: d, kind: kinds.Codex}} }

func init() { register(kinds.Codex, func(d Deps) Adapter { return newCodex(d) }) }

// mcpEnvVars is the list codex passes through from the parent environment (P0-1).
// Literal env={…} also works but would put session values in argv, so env_vars wins.
var mcpEnvVars = []string{"SWARM_URL", "SWARM_SESSION", "SWARM_TOKEN_FILE", "SWARM_AGENT_KIND"}

func (c *Codex) flags(s Spec) []string {
	quoted := make([]string, len(mcpEnvVars))
	for i, v := range mcpEnvVars {
		quoted[i] = `"` + v + `"`
	}
	a := []string{"--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust",
		"--no-alt-screen", "-m", s.Model}
	if s.Effort != "" {
		a = append(a, "-c", `model_reasoning_effort="`+s.Effort+`"`)
	}
	return append(a,
		"-c", `mcp_servers.swarm.command="`+s.Bin+`"`,
		"-c", `mcp_servers.swarm.args=["mcp"]`,
		"-c", `mcp_servers.swarm.env_vars=[`+strings.Join(quoted, ",")+`]`)
}

func (c *Codex) Launch(s Spec) (Launch, error) {
	return Launch{Argv: append(append([]string{"codex"}, c.flags(s)...), s.Kickoff),
		Env: map[string]string{}}, nil
}

func (c *Codex) Resume(s Spec) (Launch, error) {
	argv := append([]string{"codex", "resume", s.ProviderSessionID}, c.flags(s)...)
	return Launch{Argv: append(argv, s.Kickoff), Env: map[string]string{}}, nil
}

var (
	codexProcess = []*regexp.Regexp{regexp.MustCompile(`^codex$`)}
	// P0-4: "› " followed by nothing, or by an entirely dim placeholder.
	codexIdle      = regexp.MustCompile("(?m)^(?:\u001b\\[[0-9;]*m)*\u203a(?:\u001b\\[[0-9;]*m)*\\s*(?:\u001b\\[2m[^\u001b]*\u001b\\[0m)?\\s*$")
	codexTrust     = regexp.MustCompile(`Do you trust the contents of this directory\?`)
	codexRetire    = regexp.MustCompile(`retires on .*\n[\s\S]*Try new model`)
	codexHookTrust = regexp.MustCompile(`Hooks can run outside the sandbox`)
)

func (c *Codex) ProcessNames() []*regexp.Regexp { return codexProcess }
func (c *Codex) IdlePrompt() *regexp.Regexp     { return codexIdle }
func (c *Codex) Busy() *regexp.Regexp           { return nil } // the composer is not redrawn while working
func (c *Codex) Idle(capture string) bool       { return idle(c, capture) }

func (c *Codex) StartupDialogs() []Dialog {
	return []Dialog{
		{Match: codexTrust, Keys: []string{"Enter"}},
		{Match: codexRetire, Keys: []string{"Down", "Enter"}}, // Enter alone would switch the model
		{Match: codexHookTrust, Fail: true},                   // --dangerously-bypass-hook-trust should prevent it
	}
}

func (c *Codex) InterruptKeys() []string { return []string{"Escape"} }

func (c *Codex) HookOutput(event string, d HookDecision) ([]byte, error) {
	if d.Block {
		return json.Marshal(map[string]string{"decision": "block", "reason": d.Reason})
	}
	// P0-2: codex rejects additionalContext on PostCompact, so PreCompact does that work.
	if d.Context == "" || event == "PostCompact" {
		return nil, nil
	}
	return json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": event, "additionalContext": d.Context}})
}

func (c *Codex) ParseHook(event string, stdin []byte) (HookInput, error) {
	var raw struct {
		SessionID      string          `json:"session_id"`
		TurnID         string          `json:"turn_id"`
		ToolName       string          `json:"tool_name"`
		Command        string          `json:"command"`
		Source         string          `json:"source"`
		Cwd            string          `json:"cwd"`
		TranscriptPath string          `json:"transcript_path"`
		ToolInput      json.RawMessage `json:"tool_input"`
	}
	if len(stdin) > 0 {
		if err := json.Unmarshal(stdin, &raw); err != nil {
			return HookInput{}, err
		}
	}
	cmd := raw.Command
	if cmd == "" && len(raw.ToolInput) > 0 {
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
		IsSwarmTool:       strings.HasPrefix(raw.ToolName, "mcp__swarm__"),
	}, nil
}

func (c *Codex) Installed(ctx context.Context) (string, bool) {
	out, err := c.d.Run(ctx, "codex", "--version")
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", false
	}
	return fields[len(fields)-1], true
}

func (c *Codex) AuthOK(ctx context.Context) error {
	_, err := c.d.Run(ctx, "codex", "login", "status")
	if err != nil {
		return fmt.Errorf("Codex isn't signed in. Run `codex login` in a terminal.")
	}
	return nil
}

func (c *Codex) Models(ctx context.Context) ([]catalog.CatalogModel, error) {
	return nil, fmt.Errorf("use catalog.CodexFetcher: %w", errNotImplemented)
}

func (c *Codex) SuperpowersInstalled() bool {
	matches, _ := filepath.Glob(filepath.Join(c.d.UserHome, ".codex", "plugins", "cache", "*", "superpowers", "*", "skills", "brainstorming", "SKILL.md"))
	return len(matches) > 0
}

func (c *Codex) Wake(ctx context.Context, w WakeTarget) (bool, error) {
	_, err := c.d.Run(ctx, "codex", "queue", "--thread", w.ProviderSessionID, "--message", w.Notice)
	if err != nil {
		return false, err
	}
	return true, nil
}

// TrustFolder writes [projects."<realpath>"] trust_level = "trusted" into
// ~/.codex/config.toml (§11.5). The edit is structured, so every other key
// survives, and it is skipped when the entry already exists.
func (c *Codex) TrustFolder(ctx context.Context, path string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	cfg := filepath.Join(c.d.UserHome, ".codex", "config.toml")
	raw, err := os.ReadFile(cfg)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var doc map[string]any
	if len(raw) > 0 {
		if err := toml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("codex config.toml does not parse: %w", err)
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}
	projects, _ := doc["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	if e, ok := projects[real].(map[string]any); ok && e["trust_level"] == "trusted" {
		return nil // already trusted; do not rewrite the file
	}
	projects[real] = map[string]any{"trust_level": "trusted"}
	doc["projects"] = projects
	out, err := toml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(cfg, out, 0o644)
}
