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

// symlinkIfExists symlinks src at dst when src exists; it is a silent no-op
// otherwise (a test-only Deps, or a machine where `swarm install` hasn't run
// for agy yet -- setupEnv must not fail just because optional state is missing).
func symlinkIfExists(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	_ = os.Remove(dst)
	return os.Symlink(src, dst)
}

// linkAgyCLIDir builds dst as a per-entry mirror of real (D6, dialog-needs-you
// spec): every entry of real except "settings.json" is symlinked as-is, so
// login, onboarding state and everything else agy keeps in this directory
// works exactly as the old whole-dir symlink did; settings.json is instead a
// per-launch copy of the real file's JSON with cwd added to
// trustedWorkspaces, so trust is scoped to this one session without ever
// writing to the real, shared settings.json.
//
// If dst is already a symlink (a legacy whole-dir layout from before this
// fix, on a Resume that reuses an already-spawned agy-home), it is left
// exactly as it is: replacing it would risk pulling real content out from
// under a session that's already running against it. That branch logs,
// since this session then has no per-session trust entry.
func linkAgyCLIDir(real, dst, cwd string, logf func(string, ...any)) error {
	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			logf("agy: %s is a legacy whole-dir symlink; leaving it, so %s is not trusted per session", dst, cwd)
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(real)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, e := range entries {
		if e.Name() == "settings.json" {
			continue
		}
		link := filepath.Join(dst, e.Name())
		if _, err := os.Lstat(link); err == nil {
			continue // already linked (a resume against the same agy-home)
		}
		if err := os.Symlink(filepath.Join(real, e.Name()), link); err != nil {
			return err
		}
	}
	return writeAgyTrustedSettings(filepath.Join(real, "settings.json"), filepath.Join(dst, "settings.json"), cwd)
}

// writeAgyTrustedSettings writes dst as realSettings' JSON with cwd added to
// trustedWorkspaces (D6). A missing or unparseable real file is not an
// error: dst becomes a minimal settings file trusting just cwd.
//
// The copy keeps the real file's mode (review round 3, item 5); 0o644 only
// when there is no real file.
func writeAgyTrustedSettings(realSettings, dst, cwd string) error {
	var doc map[string]json.RawMessage
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(realSettings); err == nil {
		mode = fi.Mode().Perm()
	}
	if raw, err := os.ReadFile(realSettings); err == nil {
		if uerr := json.Unmarshal(raw, &doc); uerr != nil {
			doc = nil // doesn't parse; fall through to the minimal file
		}
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	var ws []string
	if len(doc["trustedWorkspaces"]) > 0 {
		_ = json.Unmarshal(doc["trustedWorkspaces"], &ws)
	}
	if !slices.Contains(ws, cwd) {
		ws = append(ws, cwd)
	}
	wsBytes, err := json.Marshal(ws)
	if err != nil {
		return err
	}
	doc["trustedWorkspaces"] = wsBytes
	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(dst, out, mode); err != nil {
		return err
	}
	return os.Chmod(dst, mode) // writeFileAtomic keeps a stale copy's old mode on Resume
}

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
	// Per-entry symlinks, not a whole-dir symlink: first-run state (e.g.
	// antigravity_state.pbtxt's onboarding-completed flag) lives here too,
	// and a per-file allowlist that misses one makes agy think every
	// swarm-spawned session is a fresh install and show the interactive
	// setup wizard, which never starts (P0, 2026-09-22) -- linkAgyCLIDir
	// symlinks every entry it finds, so nothing is missed by name. The one
	// exception is settings.json (D6, dialog-needs-you spec): it is a
	// per-launch copy with this session's workspace trusted, so login stays
	// intact (everything else symlinked) but trust is scoped per session.
	realAntigravityCLI := filepath.Join(a.d.UserHome, ".gemini", "antigravity-cli")
	symAntigravityCLI := filepath.Join(agyHome, ".gemini", "antigravity-cli")
	if err := linkAgyCLIDir(realAntigravityCLI, symAntigravityCLI, s.Cwd, a.d.Log); err != nil {
		return nil, err
	}
	// A7 (2026-09-25, package PA): agy actually reads skills from
	// $HOME/.gemini/config/skills (Config.SkillsDir(KindAgy)) and migrates
	// ~/.gemini/antigravity-cli/skills away from on first run in a fresh HOME
	// -- which every swarm spawn used to give it, chaining the REAL
	// ~/.gemini/antigravity-cli/skills one hop deeper into that session's own
	// launch folder on every new session (docs/plans/2026-09-24-skill-symlink-probe.md,
	// "Follow-up"). Link the real config/skills directly instead, and give
	// agy-home its own `.migrated` marker (a plain copy of the real one's
	// bytes, never a symlink to it -- a symlink there would just hand a
	// spawned agy a write path back into the real ~/.gemini/config tree,
	// exactly what this fix removes) so a spawned agy has no first-run
	// migration left to perform at all.
	realConfigSkills := filepath.Join(a.d.UserHome, ".gemini", "config", "skills")
	if err := os.MkdirAll(realConfigSkills, 0o755); err != nil {
		return nil, err
	}
	symConfigSkills := filepath.Join(agyHome, ".gemini", "config", "skills")
	// setupEnv also runs on Resume, and agyHome is keyed by session ID, so a
	// Resume of an already-spawned session reuses the same agy-home -- which
	// may hold real, already-migrated content (the exact live chain this
	// package fixes has its terminal directory sitting at a past session's
	// agy-home/.gemini/config/skills). Never RemoveAll here: only create the
	// link when nothing is there yet, and leave anything else -- a real dir,
	// or a symlink pointing elsewhere -- untouched rather than risk deleting
	// live content out from under a Resume.
	if fi, err := os.Lstat(symConfigSkills); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if err := os.Symlink(realConfigSkills, symConfigSkills); err != nil {
			return nil, err
		}
	} else if fi.Mode()&os.ModeSymlink != 0 {
		if cur, err := os.Readlink(symConfigSkills); err != nil || cur != realConfigSkills {
			a.d.Log("agy: %s is a symlink to %q, not the real config/skills; leaving it alone", symConfigSkills, cur)
		}
	} else {
		a.d.Log("agy: %s already exists and is not a symlink; leaving it alone (a legacy agy-home keeps reading its own content)", symConfigSkills)
	}
	realMigrated := filepath.Join(a.d.UserHome, ".gemini", "config", ".migrated")
	migratedBody, err := os.ReadFile(realMigrated)
	if err != nil {
		migratedBody = nil // no real marker yet: an empty one in agy-home is enough
	}
	if err := writeFileAtomic(filepath.Join(agyHome, ".gemini", "config", ".migrated"), migratedBody, 0o644); err != nil {
		return nil, err
	}

	// swarm's hook wiring (~/.gemini/config/hooks.json) lives outside
	// antigravity-cli, and without it a spawned agy has no hook interception
	// at all. Symlink it.
	if err := symlinkIfExists(filepath.Join(a.d.UserHome, ".gemini", "config", "hooks.json"),
		filepath.Join(agyHome, ".gemini", "config", "hooks.json")); err != nil {
		return nil, err
	}
	if err := symlinkIfExists(filepath.Join(a.d.UserHome, ".gemini", "config", "plugins"),
		filepath.Join(agyHome, ".gemini", "config", "plugins")); err != nil {
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

func (a *Agy) StartupDialogs() []Dialog {
	return []Dialog{{Match: agyTrust, Keys: []string{"Enter"}, Title: "Trust this project"}}
}
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
	argv := []string{"--conversation", w.ProviderSessionID,
		"--input-format", "stream-json", "--output-format", "stream-json"}
	// P0 fix (docs/specs/2026-09-26-agy-launch-model.md): without --model,
	// every woken turn silently ran on agy's global default model instead
	// of the one Swarm assigned this agent.
	if w.Model != "" {
		argv = append(argv, "--model", w.Model)
	}
	argv = append(argv, "--dangerously-skip-permissions")
	proc, err := a.d.StartEnv(ctx, map[string]string{"HOME": agyHome}, "agy", argv...)
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

// DiscoverSession is a no-op: the hook path (ParseHook) already populates
// ProviderSessionID for this kind.
func (a *Agy) DiscoverSession(context.Context, int, string) (string, bool) { return "", false }

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
