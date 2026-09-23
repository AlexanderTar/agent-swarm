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
	return []string{"muse", "--model", s.Model,
		"--reasoning-effort", museEffort(s.Effort), "--yolo", "--trust-workspace", s.Kickoff}
}

// museMCPEnv is the literal env block the isolated settings.json's swarm
// entry needs for this one launch (P0-1). Confirmed live 2026-09-23 by
// strings-dumping the installed muse-bin-1.3.0-R3401.1 binary: its own
// embedded "migrate" skill doc states "Muse does not expand ${VAR} and
// starts stdio servers with only a small fixed allowlist of the user's
// environment (HOME, PATH, USER, LANG, TERM and similar) plus the literal
// `env` map" -- so neither codex's env_vars-by-name nor cursor's
// ${env:NAME} templates work here; only baked-in literal values do. Verified
// empirically too: a settings.json env block with literal (non-${VAR})
// values, read from an XDG_CONFIG_HOME-isolated dir, reached a spawned stdio
// MCP subprocess's own environment even with those same vars unset in
// muse's process env -- see TestMuseSetupEnvReachesRealMCPSubprocess
// (muse_wake_probe_test.go, MUSE_LIVE_PROBE=1) for the exact repro.
// mcpServers.swarm.env therefore must be written fresh per launch (WriteMuse,
// at install time, cannot: SWARM_SESSION and SWARM_TOKEN_FILE do not exist
// until startSession mints them).
//
// SWARM_TOKEN_FILE mirrors runtime/agents.go:936's tokPath formula exactly
// (<home>/run/tokens/<session id>); the two must be kept in sync if that
// formula ever moves.
func museMCPEnv(d Deps, s Spec) map[string]any {
	return map[string]any{
		"SWARM_URL":        s.DaemonURL,
		"SWARM_SESSION":    s.SessionID,
		"SWARM_TOKEN_FILE": filepath.Join(d.Home, "run", "tokens", s.SessionID),
		"SWARM_AGENT_KIND": string(kinds.Muse),
	}
}

// setupEnv writes custom instructions to the workspace AGENTS.md (cursor
// pattern §11.1: workspace trust, --trust-workspace, is what loads them) and
// isolates muse's XDG_CONFIG_HOME to a per-launch copy of the real
// settings.json with the swarm server's env patched in -- the only place
// the four SWARM_* vars can reach the swarm MCP subprocess muse spawns (see
// museMCPEnv). The real settings.json is cloned first so every other MCP
// server and setting the operator configured keeps working; auth.json is
// symlinked in so provider login still works. trust.json is not needed:
// --yolo already trusts the workspace for this run without saving it.
//
// XDG_CONFIG_HOME is process-wide, not muse-specific (muse has no dedicated
// override var -- confirmed by strings-dumping the binary, no MUSE_CONFIG/
// MUSE_HOME/MUSE_SETTINGS hit), and it lands on the whole tmux pane, so any
// other XDG_CONFIG_HOME-rooted tool muse's shell tool runs (git, gh, ...)
// would otherwise see an empty config. Every real ~/.config sibling except
// muse/ is symlinked into the isolated dir so they keep resolving (agy.go's
// pattern for the same reason, one level up).
func (m *Muse) setupEnv(s Spec) (map[string]string, error) {
	if s.Instructions != "" && s.Cwd != "" {
		if err := os.MkdirAll(s.Cwd, 0o755); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(filepath.Join(s.Cwd, "AGENTS.md"),
			[]byte(s.Instructions), 0o644); err != nil {
			return nil, err
		}
	}

	xdgConfigHome := filepath.Join(m.d.launchDir(s.SessionID), "muse-config")
	museDir := filepath.Join(xdgConfigHome, "muse")
	if err := os.MkdirAll(museDir, 0o700); err != nil {
		return nil, err
	}
	realConfigHome := filepath.Join(m.d.UserHome, ".config")
	if entries, err := os.ReadDir(realConfigHome); err == nil {
		for _, e := range entries {
			if e.Name() == "muse" {
				continue // isolated below, not symlinked whole
			}
			if err := symlinkIfExists(filepath.Join(realConfigHome, e.Name()),
				filepath.Join(xdgConfigHome, e.Name())); err != nil {
				return nil, err
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	settings := map[string]any{}
	realSettings := filepath.Join(m.d.UserHome, ".config", "muse", "settings.json")
	if raw, err := os.ReadFile(realSettings); err == nil {
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &settings); err != nil {
				return nil, fmt.Errorf("%s: %w", realSettings, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if _, ok := settings["schema_version"]; !ok {
		// A real settings.json always carries this (muse writes it on first
		// run); a missing/fresh one needs it too -- muse's loader rejects a
		// settings file without it (confirmed live: "missing field
		// `schema_version`").
		settings["schema_version"] = 1
	}
	servers, _ := settings["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["swarm"] = map[string]any{
		"mode":    "optional",
		"command": s.Bin,
		"args":    []string{"mcp"},
		"env":     museMCPEnv(m.d, s),
	}
	settings["mcpServers"] = servers
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(museDir, "settings.json"), body, 0o600); err != nil {
		return nil, err
	}

	if err := symlinkIfExists(filepath.Join(m.d.UserHome, ".config", "muse", "auth.json"),
		filepath.Join(museDir, "auth.json")); err != nil {
		return nil, err
	}
	if err := symlinkIfExists(filepath.Join(m.d.UserHome, ".config", "muse", "skills"),
		filepath.Join(museDir, "skills")); err != nil {
		return nil, err
	}

	return map[string]string{"XDG_CONFIG_HOME": xdgConfigHome}, nil
}

// Launch is §11.1. `muse [OPTIONS] [PROMPT]` takes the kickoff as a bare
// positional (confirmed live 2026-09-23 against `muse --help`, v1.3.0; there
// is no -i flag -- an earlier version of this comment claimed one was probed,
// but muse's own --help lists no such flag, and the probe test that should
// have caught this only asserted `strings.Contains(help, "-i")`, which
// "--image" also satisfies). --model takes the raw Spark slug,
// --reasoning-effort the tier ladder, and --yolo --trust-workspace is the
// always-yolo posture other agents get from --dangerously-skip-permissions.
// XDG_CONFIG_HOME is isolated per launch (see setupEnv) for the swarm MCP
// server's env; workspace trust still loads the workspace skills and AGENTS.md.
func (m *Muse) Launch(s Spec) (Launch, error) {
	env, err := m.setupEnv(s)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Argv: m.argv(s), Env: env}, nil
}

// Resume reattaches the muse session by its UUID. No kickoff is passed:
// `muse resume <sid>` takes exactly one positional, the session ref itself,
// with no room for a trailing prompt (confirmed live 2026-09-23 -- a second
// positional is parsed as an invalid session name, not a prompt; `--last`
// is the only other accepted form). Resume only ever runs from an explicit
// swarm_control resume of a paused/interrupted session (runtime/pause.go),
// never from daemon-restart recovery (Reconcile reattaches to still-live
// tmux panes without calling Resume), so there is no "every daemon restart"
// risk to invent a kickoff against. The reminder to call swarm_sync still
// reaches the agent -- runtime/pause.go's Resume queues it a message, and
// WakeDue's idle-paste fallback (the only wake path muse supports, see
// Wake below) delivers it once the reattached pane goes idle.
func (m *Muse) Resume(s Spec) (Launch, error) {
	env, err := m.setupEnv(s)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Argv: []string{"muse", "--model", s.Model,
		"--reasoning-effort", museEffort(s.Effort),
		"--yolo", "--trust-workspace", "resume", s.ProviderSessionID},
		Env: env}, nil
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

// museSessionFile is one entry in muse's own live-session registry
// (~/.local/share/muse/runtime/muse/sessions/<uuid>.json, confirmed live
// 2026-09-23), one file per currently-running muse TUI process.
type museSessionFile struct {
	SessionID             string `json:"session_id"`
	ProcessGenerationHint string `json:"process_generation_hint"`
}

// DiscoverSession recovers muse's own provider session id after launch: muse
// has no hook surface (ParseHook is unreachable, see the package doc above),
// so instead of a hook writing ProviderSessionID, the caller matches the live
// tmux pane's OS pid against process_generation_hint ("pid=<pid>") in muse's
// session registry. No match (empty dir, file not written yet, wrong pid) is
// a normal, expected outcome -- e.g. called before muse has written its
// registry file -- not an error, so it returns false rather than an error.
func (m *Muse) DiscoverSession(ctx context.Context, pid int, workspaceRoot string) (string, bool) {
	_ = ctx
	_ = workspaceRoot // secondary sanity check only; pid is the primary, sufficient key
	dir := filepath.Join(m.d.UserHome, ".local", "share", "muse", "runtime", "muse", "sessions")
	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	want := fmt.Sprintf("pid=%d", pid)
	for _, p := range matches {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f museSessionFile
		if err := json.Unmarshal(raw, &f); err != nil {
			continue // a session mid-write is not a bug, just skip it
		}
		if f.ProcessGenerationHint == want && f.SessionID != "" {
			return f.SessionID, true
		}
	}
	return "", false
}
