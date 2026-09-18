package adapter

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

// Fake is the adapter used by the runtime tests and scripts/e2e.sh. It launches
// cmd/swarm-fake-agent, which drives the MCP tools from a scenario file, so an
// end-to-end run never calls a model.
type Fake struct {
	base
	NotInstalled  bool
	AuthError     error
	NoSuperpowers bool
	Version       string
	WakeOK        bool // e2e can turn on a "native wake" to test the skip-the-paste path
	Dialogs       []Dialog
}

func NewFake(d Deps) *Fake {
	return &Fake{base: base{d: d, kind: kinds.Fake}, Version: "fake-1"}
}

func (f *Fake) Installed(context.Context) (string, bool) {
	if f.NotInstalled {
		return "", false
	}
	return f.Version, true
}
func (f *Fake) AuthOK(context.Context) error { return f.AuthError }
func (f *Fake) Models(context.Context) ([]catalog.CatalogModel, error) {
	return []catalog.CatalogModel{{ID: "fake-1", Label: "Fake 1", Efforts: []string{}, EffortEncoding: "flag"}}, nil
}
func (f *Fake) SuperpowersInstalled() bool { return !f.NoSuperpowers }

func (f *Fake) argv(s Spec) []string {
	return []string{s.Bin + "-fake-agent", "--session", s.SessionID, "--url", s.DaemonURL,
		"--name", s.AgentName, "--kickoff", s.Kickoff}
}
func (f *Fake) Launch(s Spec) (Launch, error) {
	return Launch{Argv: f.argv(s), Env: map[string]string{}}, nil
}
func (f *Fake) Resume(s Spec) (Launch, error) {
	l, _ := f.Launch(s)
	l.Argv = append(l.Argv, "--resume", s.ProviderSessionID)
	return l, nil
}

var (
	fakeProcess = regexp.MustCompile(`^swarm-fake-agent$`)
	fakeIdle    = regexp.MustCompile("(?m)^\u276f[\u00a0 ]$")
	fakeBusy    = regexp.MustCompile("(?m)^[\u273b\u273d\u2736\u2722\u00b7*] \\S+\u2026 \\(")
)

func (f *Fake) ProcessNames() []*regexp.Regexp { return []*regexp.Regexp{fakeProcess} }
func (f *Fake) IdlePrompt() *regexp.Regexp     { return fakeIdle }
func (f *Fake) Busy() *regexp.Regexp           { return fakeBusy }
func (f *Fake) StartupDialogs() []Dialog       { return f.Dialogs }
func (f *Fake) InterruptKeys() []string        { return []string{"Escape"} }
func (f *Fake) Idle(capture string) bool       { return idle(f, capture) }

func (f *Fake) Wake(context.Context, WakeTarget) (bool, error) { return f.WakeOK, nil }

// HookOutput mirrors claude's shapes so the e2e harness can reuse one client.
func (f *Fake) HookOutput(event string, d HookDecision) ([]byte, error) {
	if d.Block {
		if event == "Stop" {
			return json.Marshal(map[string]string{"decision": "block", "reason": d.Reason})
		}
		return json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": d.Reason}})
	}
	if d.Context == "" {
		return []byte(nil), nil
	}
	return json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": event, "additionalContext": d.Context}})
}

func (f *Fake) ParseHook(event string, stdin []byte) (HookInput, error) {
	var raw struct {
		SessionID      string `json:"session_id"`
		ToolName       string `json:"tool_name"`
		Source         string `json:"source"`
		Cwd            string `json:"cwd"`
		TranscriptPath string `json:"transcript_path"`
		ToolInput      struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if len(stdin) > 0 {
		if err := json.Unmarshal(stdin, &raw); err != nil {
			return HookInput{}, err
		}
	}
	return HookInput{ProviderSessionID: raw.SessionID, Event: event, ToolName: raw.ToolName,
		Command: raw.ToolInput.Command, Source: raw.Source, Cwd: raw.Cwd,
		TranscriptPath: raw.TranscriptPath,
		IsSwarmTool:    strings.HasPrefix(raw.ToolName, "mcp__swarm__")}, nil
}
