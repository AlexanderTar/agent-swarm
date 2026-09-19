// Package adapter turns a Swarm session into one agent CLI invocation and back
// (spec §6.3, §11). Every command line here was validated in Phase 0; never
// invent a flag the probes did not run.
package adapter

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

// Spec is what a caller assembles to launch or resume one agent session.
type Spec struct {
	AgentName, SessionID, Token, DaemonURL string
	Model, Effort, Cwd                     string
	ProviderSessionID                      string
	Kickoff                                string
	SettingsDir                            string // per-launch JSON files: <home>/run/launch/<session id>
	Bin                                    string // absolute path to the swarm binary
	PluginDirs                             []string
	AdvisorModel                           string
	Env                                    map[string]string
}

// Launch is what the caller feeds to the spawner.
type Launch struct {
	Argv   []string
	Env    map[string]string
	PreRun [][]string
}

// Dialog is a startup prompt the adapter must answer (§11.5).
type Dialog struct {
	Match   *regexp.Regexp
	Keys    []string
	Fail    bool
	Require *regexp.Regexp
}

// HookDecision is what the daemon's hook handler decided; HookOutput renders it
// into the agent's own hook JSON shape.
type HookDecision struct {
	Context string
	Block   bool
	Reason  string
}

// HookInput is what ParseHook extracts from an agent's hook stdin.
type HookInput struct {
	ProviderSessionID string
	Event             string
	ToolName          string
	Command           string
	IsSwarmTool       bool
	Source            string
	TranscriptPath    string
	Cwd               string
}

// WakeTarget is what Wake needs to deliver a native or pasted notice.
type WakeTarget struct {
	SessionID, ProviderSessionID, TmuxName, Notice string
}

// Deps are the seams every adapter is built from; nothing here touches the
// network, the keychain or a real process without going through Run/Start.
type Deps struct {
	Home, UserHome, Bin string
	Run                 execx.Runner
	Start               execx.Starter
	Now                 func() time.Time
	Log                 func(format string, args ...any)
	PublishWake         func(ctx context.Context, sessionID, notice string) error
}

// Adapter is the per-agent-kind seam between the daemon and one CLI.
type Adapter interface {
	Kind() kinds.AgentKind
	Installed(ctx context.Context) (version string, ok bool)
	AuthOK(ctx context.Context) error
	Models(ctx context.Context) ([]catalog.CatalogModel, error)
	SuperpowersInstalled() bool
	Launch(s Spec) (Launch, error)
	Resume(s Spec) (Launch, error)
	ProcessNames() []*regexp.Regexp
	IdlePrompt() *regexp.Regexp
	Busy() *regexp.Regexp
	StartupDialogs() []Dialog
	InterruptKeys() []string
	Wake(ctx context.Context, sess WakeTarget) (delivered bool, err error)
	HookOutput(event string, d HookDecision) ([]byte, error)
	ParseHook(event string, stdin []byte) (HookInput, error)
	Idle(capture string) bool
	TrustFolder(ctx context.Context, path string) error
	ForgetFolder(ctx context.Context, path string) error
}

// base carries what every adapter needs and supplies the shared Idle rule.
type base struct {
	d    Deps
	kind kinds.AgentKind
}

func (b base) Kind() kinds.AgentKind { return b.kind }

// ansiEscape strips terminal escape sequences before a Busy regex sees the
// capture. Live incident (2026-09-19): the spinner line renders with a
// leading SGR color escape (e.g. "\x1b[38;5;174m·..."), so a plain Busy
// pattern anchored at line-start never matched it and a genuinely busy
// session got reported idle/waiting. IdlePrompt is deliberately NOT run
// through this: it relies on the raw, unstripped capture to tell an empty
// prompt's dim placeholder text (wrapped in its own escape codes) apart from
// a real human draft (plain, unescaped) -- stripping first would erase that
// distinction.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// StripANSI removes terminal escape sequences from a tmux capture.
func StripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

// idle is §11.3: the idle prompt matches and no line matches Busy. RE2 has no
// lookahead, so the two checks are separate.
func idle(a Adapter, capture string) bool {
	if !a.IdlePrompt().MatchString(capture) {
		return false
	}
	if bz := a.Busy(); bz != nil && bz.MatchString(StripANSI(capture)) {
		return false
	}
	return true
}

func (b base) TrustFolder(context.Context, string) error  { return nil }
func (b base) ForgetFolder(context.Context, string) error { return nil }

// errNotImplemented is what Models returns from every real adapter: P1's
// internal/catalog already owns model fetching, and duplicating it here would be
// two implementations of one thing. Models stays on the interface because the
// catalog's per-kind fetcher selection reads it; see the note in the Type ledger.
var errNotImplemented = errors.New("not implemented")

// newUUIDv4 is a v4 UUID for `claude --session-id`. Task 6 is the only caller
// today; it lives here (R1) because no task after this one may edit adapter.go.
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand cannot fail on darwin; a silent bad id would be worse
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// writeFileAtomic writes to a temp file in the same folder and renames over the
// target, keeping the existing mode when the target exists. Tasks 7, 8 and 9
// rewrite the agents' own config files with it; a half-written TOML or JSON would
// make an agent unstartable. Lives here (R1) for the same reason as newUUIDv4.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// registry is filled by each adapter's own file through register() in an init
// function. It exists so each adapter task adds exactly one file and never edits
// this one. Two implementers staging explicit paths into one shared file is how
// work gets lost, and --amend is forbidden in a shared worktree.
var registry = map[kinds.AgentKind]func(Deps) Adapter{}

func register(k kinds.AgentKind, make func(Deps) Adapter) { registry[k] = make }

func init() { register(kinds.Fake, func(d Deps) Adapter { return NewFake(d) }) }

func New(kind kinds.AgentKind, d Deps) (Adapter, error) {
	make, ok := registry[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported agent kind %q", kind)
	}
	return make(d), nil
}

// All returns every registered adapter. Until Tasks 6-9 land it holds only the
// fake one, which is what Task 3's test asserts; Task 9 tightens it to all five.
func All(d Deps) map[kinds.AgentKind]Adapter {
	out := make(map[kinds.AgentKind]Adapter, len(registry))
	for k, make := range registry {
		out[k] = make(d)
	}
	return out
}

// launchDir is the per-launch settings folder: <home>/run/launch/<session id>.
// It is created 0700 and holds the JSON files claude and codex read.
func (d Deps) launchDir(sessionID string) string {
	return filepath.Join(d.Home, "run", "launch", sessionID)
}

// writeLaunchFile writes one per-launch config file (0600) and returns its path.
func (d Deps) writeLaunchFile(sessionID, name string, body []byte) (string, error) {
	dir := d.launchDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	return p, os.WriteFile(p, body, 0o600)
}
