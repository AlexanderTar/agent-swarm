package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

type Muse struct{ base }

func newMuse(d Deps) *Muse { return &Muse{base{d: d, kind: kinds.Muse}} }

func init() { register(kinds.Muse, func(d Deps) Adapter { return newMuse(d) }) }

// museEffort is the --reasoning-effort value: "" means the CLI default (high).
func museEffort(effort string) string {
	if effort == "" {
		return "high"
	}
	return effort
}

func (m *Muse) argv(s Spec) []string {
	return []string{"muse", "-i", s.Kickoff, "--model", s.Model,
		"--reasoning-effort", museEffort(s.Effort), "--yolo", "--trust-workspace"}
}

// setupEnv writes custom instructions to the workspace AGENTS.md (cursor
// pattern §11.1): workspace trust (--trust-workspace) is what loads them, and
// there is no isolated HOME to carry them instead. Empty instructions touch
// nothing, so repos without custom instructions are never modified.
func (m *Muse) setupEnv(s Spec) error {
	if s.Instructions == "" || s.Cwd == "" {
		return nil
	}
	if err := os.MkdirAll(s.Cwd, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.Cwd, "AGENTS.md"), []byte(s.Instructions), 0o644)
}

// Launch is §11.1. Every flag was probed 2026-09-23: -i takes the kickoff,
// --model takes the raw Spark slug, --reasoning-effort the tier ladder, and
// --yolo --trust-workspace is the always-yolo posture other agents get from
// --dangerously-skip-permissions. No isolated HOME: workspace trust loads the
// workspace skills and AGENTS.md.
func (m *Muse) Launch(s Spec) (Launch, error) {
	if err := m.setupEnv(s); err != nil {
		return Launch{}, err
	}
	return Launch{Argv: m.argv(s), Env: map[string]string{}}, nil
}

// Resume reattaches the muse session by its UUID. No kickoff is passed: `muse
// resume` takes no prompt argument (probed 2026-09-23), and inventing one
// would start a new turn on every daemon restart.
func (m *Muse) Resume(s Spec) (Launch, error) {
	if err := m.setupEnv(s); err != nil {
		return Launch{}, err
	}
	return Launch{Argv: []string{"muse", "--model", s.Model,
		"--reasoning-effort", museEffort(s.Effort),
		"--yolo", "--trust-workspace", "resume", s.ProviderSessionID},
		Env: map[string]string{}}, nil
}

var (
	// Pinned against live tmux captures 2026-09-23 (v1.3.0, fixtures in
	// testdata/muse/). Idle is a bare ❯ line; a submitted prompt puts text
	// after ❯ so it no longer matches. Busy runs against StripANSI (see
	// idle()), which is what makes the per-letter truecolor escapes in the
	// spinner line harmless: ◈/◇ + "(Ns · esc to interrupt)".
	// P0-4: `muse` is a launcher script whose last line `exec`s the real
	// binary, muse-bin-<version> (e.g. muse-bin-1.3.0-R3401.1). exec keeps the
	// PID but replaces the process image, so tmux's pane_current_command
	// becomes the binary's name, not "muse" — and macOS truncates that to
	// MAXCOMLEN, so "muse-bin-1.3.0-R3401.1" is seen live as
	// "muse-bin-1.3.0-". `.*` matches the empty tail, so this also covers the
	// truncated form. Confirmed live 2026-09-23.
	museProcess = []*regexp.Regexp{regexp.MustCompile(`^muse(-bin.*)?$`)}
	museIdle    = regexp.MustCompile("(?m)^(?:\x1b\\[[0-9;?]*[A-Za-z]|\\s)*❯(?:\x1b\\[[0-9;?]*[A-Za-z]|\\s)*$")
	museBusy    = regexp.MustCompile(`(?m)^[^\n]*[◈◇][^\n]*interrupt[^\n]*$`)
)

func (m *Muse) ProcessNames() []*regexp.Regexp { return museProcess }
func (m *Muse) IdlePrompt() *regexp.Regexp     { return museIdle }
func (m *Muse) Busy() *regexp.Regexp           { return museBusy }
func (m *Muse) Idle(capture string) bool       { return idle(m, capture) }

// StartupDialogs is empty: --yolo disables approval prompts.
// TODO(probe): confirm no first-run onboarding wizard blocks a fresh HOME.
func (m *Muse) StartupDialogs() []Dialog { return nil }
func (m *Muse) PromptPatterns() []PromptMatcher {
	return nil // TODO(probe): record muse's permission/continue prompts live
}
func (m *Muse) InterruptKeys() []string { return []string{"C-c"} }

// HookOutput/ParseHook are lenient no-ops until Task 5 probes muse's hook
// surface (plugin hook fixture). They must not fail closed: an unknown hook
// shape is dropped, never a daemon error.
func (m *Muse) HookOutput(string, HookDecision) ([]byte, error) { return nil, nil }

func (m *Muse) ParseHook(event string, stdin []byte) (HookInput, error) {
	var raw struct {
		SessionID string `json:"session_id"`
	}
	if len(stdin) > 0 {
		if err := json.Unmarshal(stdin, &raw); err != nil {
			return HookInput{}, err
		}
	}
	return HookInput{ProviderSessionID: raw.SessionID, Event: event}, nil
}

var museVersionRe = regexp.MustCompile(`\d+(\.\d+)+`)

func (m *Muse) Installed(ctx context.Context) (string, bool) {
	out, err := m.d.Run(ctx, "muse", "--version")
	if err != nil {
		return "", false
	}
	// "Muse Code 1.3.0 (1.3.0-R3401.1)": the bare version, not the build tag.
	v := museVersionRe.FindString(string(out))
	if v == "" {
		return "", false
	}
	return v, true
}

// AuthOK reads the CLI's own auth file: providers is keyed by provider id
// (probed live 2026-09-23: {"schema_version":1,"providers":{"meta":{...}}}).
// META_API_KEY overrides also count: an env-bearing launch authenticates
// without the file.
func (m *Muse) AuthOK(ctx context.Context) error {
	_ = ctx
	if os.Getenv("META_API_KEY") != "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(m.d.UserHome, ".config", "muse", "auth.json"))
	if err != nil {
		return fmt.Errorf("muse isn't signed in. Run `muse login` in a terminal.")
	}
	var auth struct {
		Providers map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(raw, &auth); err != nil || len(auth.Providers) == 0 {
		return fmt.Errorf("muse isn't signed in. Run `muse login` in a terminal.")
	}
	return nil
}

func (m *Muse) Models(ctx context.Context) ([]catalog.CatalogModel, error) {
	return nil, fmt.Errorf("use catalog.MuseFetcher: %w", errNotImplemented)
}

func (m *Muse) SuperpowersInstalled() bool {
	m1, _ := filepath.Glob(filepath.Join(m.d.UserHome, ".local", "share", "muse",
		"plugins", "cache", "*", "superpowers", "*", "skills", "brainstorming", "SKILL.md"))
	if len(m1) > 0 {
		return true
	}
	m2, _ := filepath.Glob(filepath.Join(m.d.UserHome, ".local", "share", "muse",
		"plugins", "cache", "*", "superpowers", "*", "package", "skills", "brainstorming", "SKILL.md"))
	return len(m2) > 0
}

// Wake stays on the tmux-paste fallback (false) until Task 5's probe proves
// `session-message send` delivers into a live TUI session without corrupting
// history. Returning an error would read as "wake unsupported"; false reads as
// "use the fallback", which is the cursor precedent.
func (m *Muse) Wake(context.Context, WakeTarget) (bool, error) { return false, nil }
