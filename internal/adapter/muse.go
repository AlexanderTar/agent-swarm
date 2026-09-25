package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/install"
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
// until startSession mints them). SWARM_TOKEN_FILE comes from Spec.TokenFile
// (runtime/agents.go's startSession threads through the exact tokPath it
// wrote the token to) rather than being recomputed here, so the two can
// never drift out of sync.
func museMCPEnv(s Spec) map[string]any {
	return map[string]any{
		"SWARM_URL":        s.DaemonURL,
		"SWARM_SESSION":    s.SessionID,
		"SWARM_TOKEN_FILE": s.TokenFile,
		"SWARM_AGENT_KIND": string(kinds.Muse),
	}
}

// setupEnv writes custom instructions to the workspace AGENTS.md (cursor
// pattern §11.1: workspace trust, --trust-workspace, is what loads them),
// isolates muse's HOME (spec A8: closes the $HOME/.claude|.codex|.agents
// "foreign personal" skill+rules leak -- see the museHomeDenylist doc), and
// isolates XDG_CONFIG_HOME to a per-launch copy of the real settings.json
// with mcpServers replaced by swarm-only and the swarm server's env patched
// in -- the only place the four SWARM_* vars can reach the swarm MCP
// subprocess muse spawns (see museMCPEnv). The real settings.json is cloned
// first only so unrelated settings (trust hashes, tui flags) and
// schema_version survive -- every operator MCP server is dropped, not kept
// (Q1, controller ruling: matches codex's precedent). auth.json is
// symlinked in so provider login still works. trust.json is not needed:
// --yolo already trusts the workspace for this run without saving it.
//
// XDG_CONFIG_HOME is process-wide, not muse-specific (muse has no dedicated
// override var -- confirmed by strings-dumping the binary, no MUSE_CONFIG/
// MUSE_HOME/MUSE_SETTINGS hit), and it lands on the whole tmux pane, so any
// other XDG_CONFIG_HOME-rooted tool muse's shell tool runs (git, gh, ...)
// would otherwise see an empty config. Every real ~/.config sibling except
// muse/ is symlinked into the isolated dir so they keep resolving (agy.go's
// pattern for the same reason, one level up). HOME/.config itself is then
// symlinked to that isolated XDG_CONFIG_HOME dir (fix round 1, important 2),
// so a tool that hardcodes ~/.config/<name> instead of honouring
// $XDG_CONFIG_HOME (gcloud, solana) still resolves through the same sibling
// links, and ~/.config/muse still resolves to the isolated copy either way.

// museHomeDenylist is every real `~` entry setupEnv must NOT symlink into an
// isolated HOME: other coding agents' personal roots (the actual leak --
// docs/plans/2026-09-25-muse-isolation-probe.md, Finding 2/2b: muse scans
// these directly off $HOME regardless of any XDG_CONFIG_HOME isolation),
// `.config` (already isolated separately, below, matching muse's own
// XDG_CONFIG_HOME override), and `.muse` (would shadow the isolated
// XDG_CONFIG_HOME's own muse/ dir if HOME's fallback were ever consulted).
// Everything else (`.gitconfig`, `.ssh`, toolchains, ...) is symlinked
// through unchanged -- a denylist, not an allowlist, because swarm agents
// commit and push per unit and an unprobed allowlist is a strong risk of
// production breakage.
var museHomeDenylist = map[string]bool{
	".claude": true, ".claude.json": true, ".codex": true, ".cursor": true,
	".agents": true, ".gemini": true, ".config": true, ".muse": true,
}

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

	homeDir := filepath.Join(m.d.launchDir(s.SessionID), "muse-home")
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return nil, err
	}
	// xdgConfigHome's path is needed below, before it's built, to link
	// HOME/.config to it (fix round 1, important 2).
	xdgConfigHome := filepath.Join(m.d.launchDir(s.SessionID), "muse-config")
	// The swarm home itself (m.d.Home, ~/.swarm in production) is excluded by
	// absolute path, not by a fixed name in museHomeDenylist: its name isn't
	// a constant (Config.Home/--home/SWARM_HOME can point anywhere), and
	// unlike the fixed dotfile names above, a name-based exclusion could
	// either miss a custom home or wrongly exclude an unrelated real
	// dotfile that happens to be named ".swarm". Fix round 1, important 1:
	// without this, a swarm home nested under the real UserHome (the
	// production shape) gets symlinked into the isolated HOME, and since
	// that isolated HOME itself lives under
	// <swarm home>/run/launch/<sid>/muse-home/, that is a symlink cycle for
	// any recursive walk (find -L, grep -R) a spawned agent's shell tool
	// runs from $HOME. This does NOT close absolute-path token exposure: a
	// shell tool that already knows or guesses the real swarm home's
	// absolute path (e.g. from s.Bin's own path) can still read
	// ~/.swarm/run/tokens/* directly -- HOME isolation only controls what a
	// relative/$HOME-rooted lookup finds.
	// ponytail: only excludes m.d.Home when it sits directly under
	// UserHome (today's production shape); a swarm home nested deeper
	// (e.g. ~/foo/.swarm) still cycles via its own parent directory, which
	// this single-level check can't see. Widen to a full ancestor walk if
	// that configuration is ever supported.
	swarmHome := filepath.Clean(m.d.Home)
	if entries, err := os.ReadDir(m.d.UserHome); err == nil {
		for _, e := range entries {
			if museHomeDenylist[e.Name()] {
				continue
			}
			if filepath.Join(m.d.UserHome, e.Name()) == swarmHome {
				continue
			}
			if err := symlinkIfExists(filepath.Join(m.d.UserHome, e.Name()),
				filepath.Join(homeDir, e.Name())); err != nil {
				return nil, err
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// HOME/.config resolves to the same isolated XDG_CONFIG_HOME dir (not
	// just excluded/absent): a tool that hardcodes ~/.config/<name> instead
	// of honouring $XDG_CONFIG_HOME (gcloud, solana) still needs it to
	// resolve, through the same real-sibling links built below. Removed
	// first if already present (idempotent, matching symlinkIfExists/
	// applyLink's own pattern): Launch and Resume share one launchDir per
	// session, so a second setupEnv call for the same session must replace
	// rather than fail on the existing link.
	homeConfigLink := filepath.Join(homeDir, ".config")
	if _, err := os.Lstat(homeConfigLink); err == nil {
		if err := os.Remove(homeConfigLink); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.Symlink(xdgConfigHome, homeConfigLink); err != nil {
		return nil, err
	}

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
	// Q1 (controller ruling): every operator MCP server is dropped, not just
	// added to -- codex's precedent (its config.toml/MCP servers are never
	// read at all). A spawned muse gets swarm and nothing else the operator
	// configured for their own interactive use (context7/neon/notion/
	// railway/revenuecat/vercel and their live credentials, confirmed
	// present in this operator's real settings.json -- probe Finding 1).
	settings["mcpServers"] = map[string]any{
		"swarm": map[string]any{
			"mode":    "optional",
			"command": s.Bin,
			"args":    []string{"mcp"},
			"env":     museMCPEnv(s),
		},
	}
	// Q3/Finding 2b: suppresses muse's own $HOME/.claude and $HOME/.codex
	// "foreign personal" skill+rules import (confirmed live settings.json
	// keys). Merged into any existing context map so an operator's other
	// context settings, if any, survive.
	ctx, _ := settings["context"].(map[string]any)
	if ctx == nil {
		ctx = map[string]any{}
	}
	ctx["foreign_personal_skills"] = false
	ctx["foreign_personal_rules"] = false
	settings["context"] = ctx
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
	// Only swarm-managed skills go in (Finding 2 hygiene, though the real
	// $HOME/.claude|.codex|.agents leak PM.2 fixes was the actual bug): the
	// whole real ~/.config/muse/skills dir is never symlinked wholesale, so
	// any personal skill a user someday installs directly there does not
	// reach a spawned muse either. Matches claude.go's writeProjectSwarmConfig.
	skillsHome, err := install.SkillsHome(m.d.Home)
	if err != nil {
		return nil, err
	}
	if _, err := install.LinkSkills(filepath.Join(museDir, "skills"), skillsHome,
		install.SkillLinkMode(install.KindMuse)); err != nil {
		return nil, err
	}

	// XDG_DATA_HOME/STATE/CACHE must be pinned to the real, UserHome-rooted
	// paths explicitly, not left unset: muse's own fallback for each is
	// $HOME/.local/share etc. (binary's embedded docs strings), and HOME is
	// now isolated above, so an unset value would silently move muse's
	// plugin store and session registry off the real one. The plugin
	// store's own integrity/ownership check rejects every partial
	// reconstruction under an isolated data dir we tried (probe Finding 5:
	// a symlinked store root, a symlinked installed.json, and a copied
	// installed.json + symlinked cache/marketplaces + copied .installed.lock
	// were all rejected) -- sharing the real one is the only proven-working
	// way to keep the superpowers plugin (locked decision 7) and
	// DiscoverSession's real session-registry read working.
	return map[string]string{
		"HOME":            homeDir,
		"XDG_CONFIG_HOME": xdgConfigHome,
		"XDG_DATA_HOME":   filepath.Join(m.d.UserHome, ".local", "share"),
		"XDG_STATE_HOME":  filepath.Join(m.d.UserHome, ".local", "state"),
		"XDG_CACHE_HOME":  filepath.Join(m.d.UserHome, ".cache"),
	}, nil
}

// Launch is §11.1. `muse [OPTIONS] [PROMPT]` takes the kickoff as a bare
// positional (confirmed live 2026-09-23 against `muse --help`, v1.3.0; there
// is no -i flag -- an earlier version of this comment claimed one was probed,
// but muse's own --help lists no such flag, and the probe test that should
// have caught this only asserted `strings.Contains(help, "-i")`, which
// "--image" also satisfies). --model takes the raw Spark slug,
// --reasoning-effort the tier ladder, and --yolo --trust-workspace is the
// always-yolo posture other agents get from --dangerously-skip-permissions.
// HOME and XDG_CONFIG_HOME are both isolated per launch (see setupEnv) --
// HOME to keep other agents' personal skills/rules out, XDG_CONFIG_HOME for
// the swarm MCP server's env; workspace trust still loads the workspace
// skills and AGENTS.md.
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

// StartupDialogs is empty: --yolo disables approval prompts. A fresh,
// isolated HOME (spec A8) was confirmed live not to trigger any blocking
// first-run/foreign-context dialog -- see PM.1's live TUI probe,
// docs/plans/2026-09-25-muse-isolation-probe.md.
func (m *Muse) StartupDialogs() []Dialog { return nil }
func (m *Muse) PromptPatterns() []PromptMatcher {
	return nil // TODO(probe): record muse's permission/continue prompts live
}
func (m *Muse) InterruptKeys() []string { return []string{"C-c"} }

// HookOutput/ParseHook are lenient no-ops until Task 5 probes muse's hook
// surface (plugin hook fixture). They must not fail closed: an unknown hook
// shape is dropped, never a daemon error.
//
// Task 4 live probe (2026-09-25/26, isolated scratch project, no Swarm
// involvement): a plugin is a workspace-local `.muse-plugin/plugin.json`
// (native-plugin-contract.md), validated with `muse plugins validate .` and
// installed with `muse plugins install .` -- installing writes into the
// REAL, shared `XDG_DATA_HOME` plugin store (`~/.local/share/muse/plugins/...`),
// confirming this file's existing XDG_DATA_HOME-pinning comment above: a
// per-launch plugin cannot be sandboxed into an isolated data dir. First
// launch after install/change shows a "Review plugin hooks" trust dialog
// (matches testdata/muse/pane-*), which must be accepted the same way codex's
// and agy's hook-trust dialogs are. `PreToolUse`/`PostToolUse` stdin is
// `{hook_event_name, tool_name, tool_input, tool_use_id, session_id, turn_id,
// cwd, transcript_path, model, permission_mode, model_provider}`, plus
// `tool_response` (a string) on PostToolUse; `UserPromptSubmit` stdin is
// `{hook_event_name, prompt, session_id, turn_id, cwd, transcript_path,
// model, permission_mode, model_provider}` (testdata/muse-hook-userpromptsubmit.json).
//
// Items 3 and 4 (deny, tool name as the hook sees it): **muse's own
// `request_user_input` tool never dispatches `PreToolUse`/`PostToolUse` at
// all.** Two independent live turns asked the model to call
// `request_user_input`; in both, the dialog rendered, was answered, and the
// TUI printed "Structured user input answered" (not the `●
// ToolName(...)` chrome muse uses for a real tool call) -- while, in the
// same turn, the plugin's `matcher: null` (all tool names) PreToolUse/
// PostToolUse hooks DID fire correctly for `submit_reminder_decision` and
// `read_skill`, the model's other tool calls (testdata/muse-hook-pretooluse.json,
// muse-hook-posttooluse.json capture that *sibling* firing, not
// `request_user_input` -- the honest fixture, since no `request_user_input`
// hook payload exists to capture). So: **cannot deny** (no hook ever runs to
// deny it), and the tool name question is moot -- there is no hook event to
// carry it. This triggers the plan's Task 4 stop-and-report clause: Task 9
// must not refuse `swarm_ask kind:"question"` for muse without a fallback
// (spec section 10, new OQ).
func (m *Muse) HookOutput(string, HookDecision) ([]byte, error) { return nil, nil }

func (m *Muse) ParseHook(event string, stdin []byte) (HookInput, error) {
	var raw struct {
		SessionID    string          `json:"session_id"`
		ToolName     string          `json:"tool_name"`
		Cwd          string          `json:"cwd"`
		ToolInput    json.RawMessage `json:"tool_input"`
		ToolResponse json.RawMessage `json:"tool_response"`
		Prompt       string          `json:"prompt"`
	}
	if len(stdin) > 0 {
		if err := json.Unmarshal(stdin, &raw); err != nil {
			return HookInput{}, err
		}
	}
	return HookInput{
		ProviderSessionID: raw.SessionID,
		Event:             event,
		ToolName:          raw.ToolName,
		Cwd:               raw.Cwd,
		RawToolInput:      raw.ToolInput,
		ToolResponse:      raw.ToolResponse,
		Prompt:            raw.Prompt,
	}, nil
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
