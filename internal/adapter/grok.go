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

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

// Grok drives the grok CLI (Grok Build). Every Swarm grok process runs with its
// own GROK_HOME under the Swarm home and every foreign-agent compat surface off
// (internal/grokhome), so launch, catalog and install all build that env in one
// place.
type Grok struct{ base }

func newGrok(d Deps) *Grok { return &Grok{base{d: d, kind: kinds.Grok}} }

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
// text), and no --leader yet: the wake package adds it once the leader runs.
func (g *Grok) flags(s Spec, sessionFlag, sessionID string) []string {
	a := []string{"grok", "--model", s.Model}
	if s.Effort != "" {
		a = append(a, "--reasoning-effort", s.Effort)
	}
	a = append(a, "--always-approve", "--trust", "--no-subagents", sessionFlag, sessionID)
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
	return Launch{Argv: g.flags(s, "--session-id", newUUIDv4()), Env: g.launchEnv(s)}, nil
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

// ---- filled in by the pane-rules unit and the hook, wake packages ----

var grokNever = regexp.MustCompile(`\A\z.\A`)

func (g *Grok) ProcessNames() []*regexp.Regexp                  { return nil }
func (g *Grok) IdlePrompt() *regexp.Regexp                      { return grokNever }
func (g *Grok) Busy() *regexp.Regexp                            { return nil }
func (g *Grok) Idle(capture string) bool                        { return idle(g, capture) }
func (g *Grok) StartupDialogs() []Dialog                        { return nil }
func (g *Grok) PromptPatterns() []PromptMatcher                 { return nil }
func (g *Grok) InterruptKeys() []string                         { return []string{"C-c"} }
func (g *Grok) Wake(context.Context, WakeTarget) (bool, error)  { return false, nil }
func (g *Grok) HookOutput(string, HookDecision) ([]byte, error) { return nil, nil }
func (g *Grok) ParseHook(string, []byte) (HookInput, error)     { return HookInput{}, nil }
func (g *Grok) DiscoverSession(context.Context, int, string) (string, bool) {
	return "", false
}
