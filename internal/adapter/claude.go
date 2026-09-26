package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/install"
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
		// Blank the user's global status line (e.g. ccstatusline via npx every 10s)
		// in agent panes. A no-output command, not null: null is rejected by Claude Code.
		"statusLine": map[string]any{"type": "command", "command": "true"},
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
	mcp, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"swarm": map[string]any{
				"type":    "stdio",
				"command": s.Bin,
				"args":    []string{"mcp"},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	mcpPath, err := c.d.writeLaunchFile(s.SessionID, "claude-mcp.json", mcp)
	if err != nil {
		return nil, err
	}
	if err := writeProjectSwarmConfig(s.Cwd, c.d.Home, mcp); err != nil {
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
	a = append(a, "--dangerously-skip-permissions",
		"--strict-mcp-config",
		"--mcp-config", mcpPath,
		"--settings", setPath,
		"--setting-sources", "project,local")
	if s.Instructions != "" {
		instrPath, err := c.d.writeLaunchFile(s.SessionID, "claude-instructions.md", []byte(s.Instructions))
		if err != nil {
			return nil, err
		}
		a = append(a, "--append-system-prompt-file", instrPath)
	}
	return append(a, "--dangerously-load-development-channels", "server:swarm"), nil
}

// writeProjectSwarmConfig makes the swarm skill and the "swarm" channel name
// resolvable under --setting-sources project,local, which excludes the
// "user" scope where `swarm install` writes ~/.claude/skills and where
// Claude Code's own channel-name registry apparently lives too (confirmed by
// direct, zero-delay reproduction -- see
// docs/specs/2026-09-23-claude-mcp-startup-race.md; it is not a startup
// race). It writes a project-scope .mcp.json, and links every registered
// skill (A1, unit 1.3) into the session's own scratch cwd -- swarm's own
// scratch dir (s.Home/work/<agent-name>), never a real git worktree the user
// touches (internal/runtime/agents.go MkdirAlls it before Launch/Resume,
// review round 2, M2: kept across attempts, not recreated) -- so both become
// visible without re-admitting the excluded user scope (and with it
// ~/.claude/CLAUDE.md,
// which --setting-sources project,local exists to keep out). Symlinking
// (rather than copying every skill's files, as before) matches WriteSkills'
// own choice for Claude and avoids re-copying the vendored skills' data on
// every single spawn -- ui-ux-pro-max alone is 3.1 MB.
func writeProjectSwarmConfig(cwd, swarmHome string, mcp []byte) error {
	if cwd == "" {
		return nil
	}
	if err := os.WriteFile(filepath.Join(cwd, ".mcp.json"), mcp, 0o600); err != nil {
		return err
	}
	skillsHome, err := install.SkillsHome(swarmHome)
	if err != nil {
		return err
	}
	skillsRoot := filepath.Join(cwd, ".claude", "skills")
	if err := adoptPreExistingSkills(skillsRoot); err != nil {
		return err
	}
	_, err = install.LinkSkills(skillsRoot, skillsHome, install.SkillLinkMode(install.KindClaude))
	return err
}

// adoptPreExistingSkills removes any non-symlink entry already at root.
// Everything under a session's scratch cwd is swarm's own by construction
// (internal/runtime/agents.go MkdirAlls s.Home/work/<agent-name> before
// Launch/Resume and keeps it across attempts, never a real git worktree the
// user touches -- review round 2, M2), so a real directory there -- left by
// an older, copy-based writeProjectSwarmConfig, say, or a previous attempt's
// run -- is never the user's own same-named skill the way it would be under a
// real, shared skills root; it is simply stale and must be replaced. A
// symlink is left alone: LinkSkills' own idempotency check (and its
// user-owned check, belt and braces) handles it.
func adoptPreExistingSkills(root string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

func (c *Claude) Launch(s Spec) (Launch, error) {
	if err := trustClaudeWorkspace(c.d.UserHome, s.Cwd); err != nil {
		c.d.Log("claude: pre-trust %s: %v", s.Cwd, err)
	}
	f, err := c.flags(s)
	if err != nil {
		return Launch{}, err
	}
	argv := append([]string{"claude", "--session-id", newUUIDv4()}, f...)
	return Launch{Argv: append(argv, "--", s.Kickoff), Env: map[string]string{}}, nil
}

func (c *Claude) Resume(s Spec) (Launch, error) {
	if err := trustClaudeWorkspace(c.d.UserHome, s.Cwd); err != nil {
		c.d.Log("claude: pre-trust %s: %v", s.Cwd, err)
	}
	f, err := c.flags(s)
	if err != nil {
		return Launch{}, err
	}
	argv := append([]string{"claude", "--resume", s.ProviderSessionID}, f...)
	return Launch{Argv: append(argv, "--", s.Kickoff), Env: map[string]string{}}, nil
}

// claudeConfigLockStaleAfter/claudeConfigLockRetryEvery/claudeConfigLockTimeout
// are D1's mkdir-lock protocol (dialog-needs-you spec §E, confirmed live as
// P-C5, Task 14a): Claude itself takes this same "<file>.lock" directory
// lock around its own writes to ~/.claude.json, held only for the instant of
// the write.
const (
	claudeConfigLockStaleAfter = 10 * time.Second
	claudeConfigLockRetryEvery = 100 * time.Millisecond
	claudeConfigLockTimeout    = 2 * time.Second
)

func claudeConfigPath(userHome string) string { return filepath.Join(userHome, ".claude.json") }

// withClaudeConfigLock runs fn while holding Claude's own mkdir lock around
// ~/.claude.json. It is best-effort: a lock that stays held for the whole
// 2s window is treated as busy, fn is skipped, and nil is returned -- the
// caller logs and moves on rather than blocking or failing a launch (D1: a
// pre-trust failure never blocks a spawn; the dialog auto-answer and the
// Needs-you escalation are the fallback).
func withClaudeConfigLock(userHome string, fn func() error) error {
	lock := claudeConfigPath(userHome) + ".lock"
	deadline := time.Now().Add(claudeConfigLockTimeout)
	for {
		err := os.Mkdir(lock, 0o755)
		if err == nil {
			defer os.Remove(lock)
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if fi, statErr := os.Stat(lock); statErr == nil && time.Since(fi.ModTime()) > claudeConfigLockStaleAfter {
			_ = os.Remove(lock) // stale; best-effort, then retry immediately
			continue
		}
		if time.Now().After(deadline) {
			return nil // busy for the whole window; skip the write, don't fail the launch
		}
		time.Sleep(claudeConfigLockRetryEvery)
	}
}

// trustClaudeWorkspace marks cwd (and its realpath, if it differs) trusted
// in Claude's global config, merging into any existing project entry, under
// Claude's own lock. It is idempotent and never rewrites the file when every
// key already has hasTrustDialogAccepted: true (D1, dialog-needs-you spec
// §E). Every other field of every project entry, and every other top-level
// key, survives untouched: the doc is decoded as map[string]json.RawMessage,
// so untouched entries keep their exact original bytes.
func trustClaudeWorkspace(userHome, cwd string) error {
	if userHome == "" {
		return nil
	}
	keys := []string{cwd}
	if real, err := filepath.EvalSymlinks(cwd); err == nil && real != cwd {
		keys = append(keys, real)
	}
	return withClaudeConfigLock(userHome, func() error {
		path := claudeConfigPath(userHome)
		for attempt := 0; attempt < 3; attempt++ {
			raw, err := os.ReadFile(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			mode := os.FileMode(0o600)
			if fi, statErr := os.Stat(path); statErr == nil {
				mode = fi.Mode().Perm()
			}
			var doc map[string]json.RawMessage
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &doc); err != nil {
					return fmt.Errorf("claude.json does not parse: %w", err)
				}
			}
			if doc == nil {
				doc = map[string]json.RawMessage{}
			}
			var projects map[string]json.RawMessage
			if len(doc["projects"]) > 0 {
				if err := json.Unmarshal(doc["projects"], &projects); err != nil {
					return fmt.Errorf("claude.json projects does not parse: %w", err)
				}
			}
			if projects == nil {
				projects = map[string]json.RawMessage{}
			}
			if claudeAllTrusted(projects, keys) {
				return nil // already trusted under every key; do not rewrite the file
			}
			for _, k := range keys {
				var entry map[string]json.RawMessage
				if len(projects[k]) > 0 {
					if err := json.Unmarshal(projects[k], &entry); err != nil {
						return fmt.Errorf("claude.json projects[%s] does not parse: %w", k, err)
					}
				}
				if entry == nil {
					entry = map[string]json.RawMessage{}
				}
				entry["hasTrustDialogAccepted"] = json.RawMessage("true")
				b, err := json.Marshal(entry)
				if err != nil {
					return err
				}
				projects[k] = b
			}
			pb, err := json.Marshal(projects)
			if err != nil {
				return err
			}
			doc["projects"] = pb
			out, err := json.Marshal(doc)
			if err != nil {
				return err
			}
			cur, _ := os.ReadFile(path)
			if !bytes.Equal(cur, raw) {
				continue // the file changed under us; re-read and merge again
			}
			return writeFileAtomic(path, out, mode)
		}
		return fmt.Errorf("claude.json kept changing under us")
	})
}

func claudeAllTrusted(projects map[string]json.RawMessage, keys []string) bool {
	for _, k := range keys {
		var entry struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		}
		if len(projects[k]) == 0 {
			return false
		}
		if err := json.Unmarshal(projects[k], &entry); err != nil || !entry.HasTrustDialogAccepted {
			return false
		}
	}
	return true
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
	//
	// 2026-09-21: Claude 2.1.278 also draws an empty prompt as "❯ NBSP" + SGR 7
	// + one space (the cursor cell); that cell must be a space so a draft with
	// the cursor on a character still fails.
	claudeIdle = regexp.MustCompile("(?m)^(?:\x1b\\[[0-9;]*m)*\u276f[\u00a0 ]" +
		"(?:\x1b\\[2m.*|(?:\x1b\\[[0-9;]*m)*\x1b\\[7m(?:\x1b\\[[0-9;]*m)*[ \u00a0]?(?:\x1b\\[[0-9;]*m)*)?\\s*$")
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
		{Match: claudeTrust, Require: claudeTrustYes, Keys: []string{"Down", "Enter"}, Title: "Trust this project"},
		{Match: claudeDev, Keys: []string{"Enter"}, Title: "Confirm local development"},
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
// "delivered" is whether anything was actually subscribed to this session, not
// merely whether the publish errored: with the shim not connected there is
// nobody on the other end, and claiming a delivery then records a wake that
// reached no one, holding the session silent until the cooldown expires
// instead of dropping to the paste fallback straight away.
func (c *Claude) Wake(ctx context.Context, w WakeTarget) (bool, error) {
	if c.d.PublishWake == nil {
		return false, nil
	}
	return c.d.PublishWake(ctx, w.SessionID, w.Notice)
}

// DiscoverSession is a no-op: the hook path (ParseHook) already populates
// ProviderSessionID for this kind.
func (c *Claude) DiscoverSession(context.Context, int, string) (string, bool) { return "", false }

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
		Prompt         string          `json:"prompt"`
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
		Prompt:            raw.Prompt,
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
