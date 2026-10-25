package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

// Grok drives the grok CLI (Grok Build). Every Swarm grok process runs with its
// own GROK_HOME under the Swarm home and every foreign-agent compat surface off
// (internal/grokhome), so launch, catalog and install all build that env in one
// place.
type Grok struct {
	base
	mu     sync.Mutex
	minted map[string]string // workspace root -> the --session-id Launch minted
	// leader holds what the leaders push (grokleader_state.go), by Swarm session id.
	leader leaderState
}

func newGrok(d Deps) *Grok {
	return &Grok{base: base{d: d, kind: kinds.Grok}, minted: map[string]string{}}
}

func init() { register(kinds.Grok, func(d Deps) Adapter { return newGrok(d) }) }

// grokContextWindow is the window of every model in grok's catalog today
// (models_cache.json context_window); it turns the low-token cap in tokens into
// the percentage grok's compaction knob takes.
const grokContextWindow = 256000

// flags is every argv override Launch and Resume pass to `grok`, each proven by
// a recorded probe (never invent one):
//
//   - --model / --reasoning-effort: the agent's model and effort (effort is
//     omitted when unset, leaving grok's per-model default).
//   - --always-approve: Swarm supervises tool use through hooks; grok's own
//     approval prompts would block a pane nobody watches.
//   - --trust: pre-trusts the cwd in the Swarm grok home's trusted_folders.toml.
//   - --no-subagents: no child agents outside Swarm's own tree.
//   - --session-id <uuid> (Launch): Swarm mints the id so the transcript starts
//     under a known name. Never with --resume (grok needs --fork-session then,
//     and Swarm never forks).
//   - --resume <id> (Resume): reattach; grok submits the positional kickoff.
//   - --rules <text>: the role's instructions appended to the system prompt.
//
// Never --disallowed-tools (it silently turns leader mode off), never --debug
// or --debug-file (they write bearer tokens and the account email in plain
// text). --leader --leader-socket <per-session path> makes the TUI start its own
// leader for wake (grokleader_life.go); it is left off when the path is too long.
func (g *Grok) flags(s Spec, sessionFlag, sessionID string) []string {
	a := []string{"grok", "--model", s.Model}
	if s.Effort != "" {
		a = append(a, "--reasoning-effort", s.Effort)
	}
	a = append(a, "--always-approve", "--trust", "--no-subagents", sessionFlag, sessionID)
	a = append(a, g.leaderArgs(s.SessionID)...)
	if s.Instructions != "" {
		a = append(a, "--rules", s.Instructions)
	}
	return append(a, s.Kickoff)
}

// launchEnv is the grok home env plus the identity the swarm MCP server and the
// hooks read, and the compaction threshold for a low-token orchestrator.
func (g *Grok) launchEnv(s Spec) map[string]string {
	env := grokhome.Env(g.d.Home)
	env["SWARM_URL"], env["SWARM_SESSION"] = s.DaemonURL, s.SessionID
	env["SWARM_TOKEN_FILE"], env["SWARM_AGENT_KIND"] = s.TokenFile, string(kinds.Grok)
	if s.LowTokenCap > 0 {
		pct := min(100, max(1, s.LowTokenCap*100/grokContextWindow))
		env["GROK_AUTO_COMPACT_THRESHOLD_PERCENT"] = strconv.Itoa(pct)
	}
	return env
}

// refreshPlugins disables, in the Swarm grok home, any Claude plugin installed
// since `swarm install`, before grok starts and would load it.
func (g *Grok) refreshPlugins() error {
	if _, err := install.RefreshGrokPlugins(install.Config{Home: g.d.Home, UserHome: g.d.UserHome}); err != nil {
		return fmt.Errorf("grok: refresh disabled plugins: %w", err)
	}
	return nil
}

func (g *Grok) Launch(s Spec) (Launch, error) {
	if err := g.refreshPlugins(); err != nil {
		return Launch{}, err
	}
	id := newUUIDv4()
	g.mu.Lock()
	g.minted[s.Cwd] = id
	g.mu.Unlock()
	return Launch{Argv: g.flags(s, "--session-id", id), Env: g.launchEnv(s)}, nil
}

// Resume keeps the kickoff: grok accepts a positional prompt with --resume and
// submits it (probed 2026-10-16), which is how the agent is told to sync again.
func (g *Grok) Resume(s Spec) (Launch, error) {
	if s.ProviderSessionID == "" {
		return Launch{}, errors.New("grok: no provider session to resume")
	}
	if err := g.refreshPlugins(); err != nil {
		return Launch{}, err
	}
	return Launch{Argv: g.flags(s, "--resume", s.ProviderSessionID), Env: g.launchEnv(s)}, nil
}

func (g *Grok) Installed(ctx context.Context) (string, bool) {
	if g.d.RunEnv == nil {
		return "", false
	}
	out, err := g.d.RunEnv(ctx, grokhome.Env(g.d.Home), grokhome.Bin, "--version")
	if err != nil {
		return "", false
	}
	v := museVersionRe.FindString(string(out))
	return v, v != ""
}

const grokSignedOut = "grok isn't signed in. Run `grok login` in a terminal."

// AuthOK passes when XAI_API_KEY is set or the Swarm grok home's auth.json (a
// link to ~/.grok/auth.json) holds an entry with a non-empty key and
// refresh_token. An expired access token is fine: grok refreshes it. Credential
// values are never read into an error.
func (g *Grok) AuthOK(context.Context) error {
	if os.Getenv("XAI_API_KEY") != "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(grokhome.Dir(g.d.Home), "auth.json"))
	if err != nil {
		return errors.New(grokSignedOut)
	}
	var entries map[string]struct {
		Key          string `json:"key"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(raw, &entries) != nil {
		return errors.New(grokSignedOut)
	}
	for _, e := range entries {
		if e.Key != "" && e.RefreshToken != "" {
			return nil
		}
	}
	return errors.New(grokSignedOut)
}

func (g *Grok) Models(context.Context) ([]catalog.CatalogModel, error) {
	return nil, fmt.Errorf("use catalog.GrokFetcher: %w", errNotImplemented)
}

// SuperpowersInstalled is the catalog's own check, so doctor, the catalog entry
// and the orchestrator gate cannot disagree.
func (g *Grok) SuperpowersInstalled() bool {
	return catalog.SuperpowersInstalled(g.d.UserHome, g.d.Home, kinds.Grok)
}

var (
	// Pinned against 14 live tmux captures (grok 1.0.60, fixtures in
	// testdata/grok/). The input box is drawn in every state, so a muse-style
	// bare-prompt rule would be wrong: idleness is the footer at rest, and a
	// draft or queued message changes the footer. Each footer segment is wrapped
	// in its own SGR codes, so the rules run on stripped text (Idle strips first).
	//
	// pane_current_command is the binary name cut to MAXCOMLEN: "grok-1.0.60-mac".
	grokProcess = []*regexp.Regexp{regexp.MustCompile(`^grok(-\d.*)?$`)}
	grokIdle    = regexp.MustCompile(`(?m)^\s*Shift\+Tab:mode\s+│\s+Ctrl\+x:shortcuts\s*$`)
	grokBusy    = regexp.MustCompile(`(?m)(\[stop\]\s*$|│\s+Ctrl\+c:cancel\s+│)`)
	// grokCard is a modal card (an upgrade or usage-limit offer, or a question):
	// it swallows typed text, and only a person (or Shift+x) clears it.
	grokCard = regexp.MustCompile(`Tab:next answer\s+│\s+Esc:scrollback`)
	// grokQuestionCard is the ask_user_question card. It shares grokCard's footer,
	// so the paste guard (PasteReady) keeps matching it, but it is no stall: a
	// bound approval leaves it on screen and the bound question row is its
	// Needs-you entry, so the dialog matchers exclude it.
	grokQuestionCard = regexp.MustCompile(`Type your answer here|Waiting on answers for`)
	// grokUsageLimit is the card title when the plan's allowance is spent. The
	// card draws it in its left gutter (┃), so anchoring to a line that starts
	// with the gutter keeps source, fixtures and comments showing the same words
	// from matching; ObserveCapture also requires the card footer.
	grokUsageLimit = regexp.MustCompile(`(?m)^\s*┃\s+You hit your .*usage limit`)
)

func (g *Grok) ProcessNames() []*regexp.Regexp { return grokProcess }
func (g *Grok) IdlePrompt() *regexp.Regexp     { return grokIdle }
func (g *Grok) Busy() *regexp.Regexp           { return grokBusy }

// Idle reads the stripped pane: IdlePrompt spans several colour-coded segments.
func (g *Grok) Idle(capture string) bool { return idle(g, StripANSI(capture)) }

const grokCardTitle = "Grok is showing a card"

// StartupDialogs and PromptPatterns are detect-only (no keys): --trust leaves no
// startup dialog, so the only thing that can stall the pane is a card, and that
// needs a person.
func (g *Grok) StartupDialogs() []Dialog {
	return []Dialog{{Match: grokCard, Exclude: grokQuestionCard, Title: grokCardTitle}}
}
func (g *Grok) PromptPatterns() []PromptMatcher {
	return []PromptMatcher{{Match: grokCard, Exclude: grokQuestionCard, Title: grokCardTitle}}
}

// InterruptKeys is one C-c: mid-turn it cancels the turn, at idle it arms quit,
// so the runtime must never send it twice.
func (g *Grok) InterruptKeys() []string { return []string{"C-c"} }

func (g *Grok) RenameCommand(name string) (string, bool) { return "/rename " + name, true }

// DiscoverSession returns the id Launch minted for this workspace: grok takes
// Swarm's own --session-id, so there is nothing to scan for. Discovery runs in
// the daemon that launched the session; a workspace this adapter never
// launched (a resumed one already has its id) reports false.
func (g *Grok) DiscoverSession(_ context.Context, _ int, workspaceRoot string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.minted[workspaceRoot]
	return id, ok
}

// ForgetFolder drops one folder's trust from the Swarm grok home's
// trusted_folders.toml (written by --trust) and keeps every other entry. The
// edit is skipped when the file changed since it was read, and an unparseable
// file is left alone.
func (g *Grok) ForgetFolder(_ context.Context, path string) error {
	p := filepath.Join(grokhome.Dir(g.d.Home), "trusted_folders.toml")
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		g.d.Log("grok: trusted_folders.toml does not parse, leaving it alone: %v", err)
		return nil
	}
	folders, _ := doc["folders"].(map[string]any)
	if _, ok := folders[path]; !ok {
		return nil
	}
	delete(folders, path)
	out, err := toml.Marshal(doc)
	if err != nil {
		return err
	}
	if now, err := os.ReadFile(p); err != nil || string(now) != string(raw) {
		g.d.Log("grok: trusted_folders.toml changed while editing, skipping the trust cleanup for %s", path)
		return nil
	}
	return writeFileAtomic(p, out, 0o600)
}

// grokContext reads signals.json from the session dir transcriptPath lives in
// (hooks hand over updates.jsonl). ok is false for a missing or unreadable
// file and for a session that has used no context yet.
func grokContext(transcriptPath string) (ContextSample, bool) {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(transcriptPath), "signals.json"))
	if err != nil {
		return ContextSample{}, false
	}
	var sig struct {
		Used    int    `json:"contextTokensUsed"`
		Window  int    `json:"contextWindowTokens"`
		Primary string `json:"primaryModelId"`
	}
	if json.Unmarshal(raw, &sig) != nil || sig.Used <= 0 {
		return ContextSample{}, false
	}
	s := ContextSample{Tokens: sig.Used, Model: sig.Primary}
	if sig.Window > 0 {
		s.Window = &sig.Window
	}
	return s, true
}

// ObserveModel reports the model and effort in the session's summary.json, which
// grok rewrites when a person runs /model or /effort. transcriptPath (a hook's
// updates.jsonl) names the session dir; without it the dir is found by the
// provider session id, unique across every cwd folder.
func (g *Grok) ObserveModel(transcriptPath, providerSessionID string) (string, string, bool) {
	dir := ""
	switch {
	case transcriptPath != "":
		dir = filepath.Dir(transcriptPath)
	case providerSessionID != "":
		hits, _ := filepath.Glob(filepath.Join(grokhome.Dir(g.d.Home), "sessions", "*", providerSessionID, "summary.json"))
		if len(hits) != 1 {
			return "", "", false
		}
		dir = filepath.Dir(hits[0])
	default:
		return "", "", false
	}
	raw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return "", "", false
	}
	var sum struct {
		Model  string `json:"current_model_id"`
		Effort string `json:"reasoning_effort"`
	}
	if json.Unmarshal(raw, &sum) != nil || sum.Model == "" {
		return "", "", false
	}
	return sum.Model, sum.Effort, true
}

// ---- filled in by the wake package ----

// PasteReady is the wake-paste gate (runtime tryPaste): a paste plus one Enter
// mid-turn queues the message (TASK-1126), so only a card, which swallows typed
// text, blocks it.
func (g *Grok) PasteReady(capture string) bool { return !grokCard.MatchString(StripANSI(capture)) }

// AssistantTextSinceLastTurn lets the Stop hook judge the reply for the
// approval chat block: grok's Stop carries lastAssistantMessage, which the
// handler prefers, and updates.jsonl is not parsed, so an empty message fails
// open (the ask is sent) rather than trusting an unreadable transcript.
func (g *Grok) AssistantTextSinceLastTurn(string) (string, bool) { return "", false }

// grokContextCap keeps additionalContext under grok's 10,000-character limit
// (bytes, to be safe for multi-byte text).
const grokContextCap = 9500

// HookOutput speaks Claude's JSON, which grok honours (TASK-1125 Q2/Q3): a deny
// on PreToolUse, a block on Stop, and additionalContext on the two tool hooks.
// SessionStart and UserPromptSubmit context never reaches the model, and grok
// never rewrites tool input, so those print nothing: the handler routes such
// notices to the kickoff and the tool hooks instead.
func (g *Grok) HookOutput(event string, d HookDecision) ([]byte, error) {
	if d.Block {
		return (&Claude{}).HookOutput(event, HookDecision{Block: true, Reason: d.Reason})
	}
	if (event != "PreToolUse" && event != "PostToolUse") || d.Context == "" {
		return nil, nil
	}
	ctx := d.Context
	if len(ctx) > grokContextCap {
		ctx = strings.ToValidUTF8(ctx[:grokContextCap], "") + "…"
	}
	return (&Claude{}).HookOutput(event, HookDecision{Context: ctx})
}

// ParseHook is Claude's parser plus grok's deltas: qualified MCP names
// (swarm__<tool>, with the tool's own arguments wrapped in
// tool_input.tool_input), and the Stop/Notification fields the handler gates on.
func (g *Grok) ParseHook(event string, stdin []byte) (HookInput, error) {
	in, err := parseClaudeHook(event, stdin)
	if err != nil || len(stdin) == 0 {
		return in, err
	}
	var extra struct {
		Reason           string `json:"reason"`
		StopHookActive   bool   `json:"stop_hook_active"`
		NotificationType string `json:"notification_type"`
		Error            string `json:"error"`
		ErrorDetails     string `json:"error_details"`
		Message          string `json:"message"`
	}
	_ = json.Unmarshal(stdin, &extra)
	in.Reason, in.StopHookActive, in.NotificationType = extra.Reason, extra.StopHookActive, extra.NotificationType
	in.Error, in.ErrorDetails, in.Message = extra.Error, extra.ErrorDetails, extra.Message
	in.IsSwarmTool = strings.HasPrefix(in.ToolName, "swarm__") || strings.HasPrefix(in.ToolName, "mcp__swarm__")
	if strings.HasPrefix(in.ToolName, "swarm__") {
		var wrapped struct {
			Inner json.RawMessage `json:"tool_input"`
		}
		if json.Unmarshal(in.RawToolInput, &wrapped) == nil && len(wrapped.Inner) > 0 && string(wrapped.Inner) != "null" {
			in.RawToolInput = wrapped.Inner
		}
	}
	return in, nil
}

// ObserveCapture is told of every pane capture reconcile reads. The usage-limit
// card marks grok exhausted for as long as it shows, a source that needs no
// billing meter. Only an open card counts: its title in the gutter and its
// footer both present.
func (g *Grok) ObserveCapture(_, capture string) {
	plain := StripANSI(capture)
	line := grokUsageLimit.FindString(plain)
	if line == "" || !grokCard.MatchString(plain) {
		return
	}
	line = strings.TrimSpace(line)
	g.leader.mu.Lock()
	sink := g.leader.onExhausted
	g.leader.mu.Unlock()
	if sink != nil {
		sink(line)
	}
}
