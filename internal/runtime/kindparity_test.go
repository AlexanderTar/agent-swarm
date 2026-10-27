package runtime_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/advisor"
	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/usage"
)

// The parity tables below are test-only on purpose: each row restates what a
// kind is supposed to do, so adding a kind to kinds.AgentKinds fails here until
// every seam has been decided (docs/adding-an-agent-kind.md, section 7). A kind
// that deliberately lacks a seam gets an explicit, commented entry, never a skip.

// loginCommands: the Go preflight hint, and what the web and menubar surfaces
// show. The surfaces launch claude and agy bare to sign in, so those two differ
// from the daemon's hint on purpose.
var loginCommands = map[kinds.AgentKind]struct{ daemon, surface string }{
	kinds.Claude: {"claude /login", "claude"},
	kinds.Codex:  {"codex login", "codex login"},
	kinds.Agy:    {"agy login", "agy"},
	kinds.Cursor: {"cursor-agent login", "cursor-agent login"},
	kinds.Muse:   {"muse login", "muse login"},
	kinds.Grok:   {"grok login", "grok login"},
}

// nativeCaps: NativeCapTokens per kind. agy has no native compaction cap.
var nativeCaps = map[kinds.AgentKind]int{
	kinds.Claude: 200000,
	kinds.Codex:  150000,
	kinds.Agy:    0,
	kinds.Cursor: 200000,
	kinds.Muse:   200000,
	kinds.Grok:   153600,
}

// questionHooked: kinds whose native question tool fires a hook Swarm
// intercepts. cursor and muse never dispatch one, so they use swarm_ask.
var questionHooked = map[kinds.AgentKind]bool{
	kinds.Claude: true, kinds.Codex: true, kinds.Agy: true, kinds.Grok: true,
	kinds.Cursor: false, kinds.Muse: false,
}

// advisorDisabled lists the kinds with a deliberate "no advisor". Every kind
// can advise today.
var advisorDisabled = map[kinds.AgentKind]string{}

// usageNone lists kinds with no usage source. Every kind has one today.
var usageNone = map[kinds.AgentKind]string{}

// usageLimitNone lists kinds whose adapter has no adapter.UsageLimiter, with why:
// a kind with a known hard-stop line in its pane implements the method, so a
// session showing it is ended and re-admitted on the usage fallback (BUG-110).
var usageLimitNone = map[kinds.AgentKind]string{
	kinds.Claude: "no hard-stop pane line captured yet; the poller's 100% headline gates it",
	kinds.Codex:  "its rate-limit picker offers a model switch, not a stop",
	kinds.Agy:    "no hard-stop pane line captured yet",
	kinds.Cursor: "no hard-stop pane line captured yet",
	kinds.Grok:   "its usage-limit card is a Needs-you row that marks grok exhausted (Grok.ObserveCapture)",
}

// superpowersFixture is a path (under the user home, or the Swarm home for
// grok) whose presence makes each kind's superpowers check pass.
var superpowersFixture = map[kinds.AgentKind]struct {
	underSwarmHome bool
	rel            []string
}{
	// install.SuperpowersOK also wants claude's test-driven-development skill.
	kinds.Claude: {false, []string{".claude/plugins/cache/m/superpowers/1/skills/brainstorming/SKILL.md", ".claude/plugins/cache/m/superpowers/1/skills/test-driven-development/SKILL.md"}},
	kinds.Codex:  {false, []string{".codex/plugins/cache/m/superpowers/1/skills/brainstorming/SKILL.md"}},
	kinds.Agy:    {false, []string{".gemini/config/plugins/superpowers/skills/brainstorming/SKILL.md"}},
	kinds.Cursor: {false, []string{".cursor/plugins/local/superpowers/skills/brainstorming/SKILL.md"}},
	kinds.Muse:   {false, []string{".local/share/muse/plugins/cache/m/superpowers/1/skills/brainstorming/SKILL.md"}},
	kinds.Grok:   {true, []string{"installed-plugins/superpowers-1/skills/brainstorming/SKILL.md"}},
}

func TestKindParity(t *testing.T) {
	root := filepath.Join("..", "..")
	webCopy := readFile(t, filepath.Join(root, "web/src/copy.ts"))
	swiftCopy := readFile(t, filepath.Join(root, "apps/menubar/Sources/SwarmBarKit/Copy.swift"))
	fetchers := map[kinds.AgentKind]bool{}
	for _, f := range catalog.DefaultFetchers("/h", "/h/.swarm", "u") {
		fetchers[f.Kind()] = true
	}
	sources := map[kinds.AgentKind]bool{}
	run := (&execx.Fake{}).Runner()
	for _, s := range usage.DefaultSources("/h", "u", nil, run, nil, nil, &usage.Grok{}) {
		sources[s.Agent] = true
	}

	for _, k := range kinds.AgentKinds {
		t.Run(string(k), func(t *testing.T) {
			// adapter
			a, err := adapter.New(k, adapter.Deps{})
			if err != nil || a.Kind() != k {
				t.Errorf("no registered adapter for %s: %v", k, err)
			}
			// usage-limit pane line
			_, hasLimit := a.(adapter.UsageLimiter)
			if why, none := usageLimitNone[k]; !hasLimit && !none {
				t.Error("adapter has no UsageLimit and usageLimitNone has no entry")
			} else if hasLimit && none {
				t.Errorf("usageLimitNone says %q but the adapter implements UsageLimit", why)
			}
			// catalog fetcher
			if !fetchers[k] {
				t.Error("catalog.DefaultFetchers has no fetcher")
			}
			// usage source
			if why, none := usageNone[k]; !sources[k] && !none {
				t.Error("usage.DefaultSources has no source and usageNone has no entry")
			} else if sources[k] && none {
				t.Errorf("usageNone says %q but a source exists", why)
			}
			// advisor command and parser
			_, cmdErr := advisor.AdvisorCommand(k, "m", "", "/d", "p")
			if why, off := advisorDisabled[k]; off {
				if cmdErr == nil {
					t.Errorf("advisor is enabled but advisorDisabled says %q: drop the entry", why)
				}
			} else if cmdErr != nil {
				t.Errorf("AdvisorCommand: %v", cmdErr)
			}
			if _, _, err := advisor.ParseAnswer(k, nil, nil, t.TempDir(), t.TempDir()); err != nil &&
				strings.Contains(err.Error(), "no advisor output parser") {
				t.Errorf("ParseAnswer has no arm: %v", err)
			}
			// superpowers check is not the default false
			fx := superpowersFixture[k]
			userHome, swarmHome := t.TempDir(), t.TempDir()
			if catalog.SuperpowersInstalled(userHome, swarmHome, k) {
				t.Error("superpowers reports installed in an empty home")
			}
			base := userHome
			if fx.underSwarmHome {
				base = grokhome.Dir(swarmHome)
			}
			for _, rel := range fx.rel {
				writeFile(t, filepath.Join(base, rel))
			}
			if !catalog.SuperpowersInstalled(userHome, swarmHome, k) {
				t.Errorf("catalog.SuperpowersInstalled has no arm for %s (fixture %v)", k, fx.rel)
			}
			if ok, _ := install.SuperpowersOK(install.Config{Home: swarmHome, UserHome: userHome}, install.Kind(k)); !ok {
				t.Error("install.SuperpowersOK disagrees with catalog.SuperpowersInstalled")
			}
			// login hint: daemon, web and menubar
			want, ok := loginCommands[k]
			if !ok {
				t.Fatal("loginCommands has no row")
			}
			if got := runtime.LoginCommand(k); got != want.daemon {
				t.Errorf("loginCommand = %q, want %q", got, want.daemon)
			}
			if !strings.Contains(webCopy, string(k)+`: "`+want.surface+`"`) {
				t.Errorf("web AGENT_LOGIN_CMD lacks %s: %q", k, want.surface)
			}
			if !strings.Contains(swiftCopy, `case .`+string(k)+`: return "`+want.surface+`"`) {
				t.Errorf("menubar loginCommand lacks %s: %q", k, want.surface)
			}
			// todos mirror text
			if runtime.TodosNext[k] == "" {
				t.Error("todosNext has no entry")
			}
			// native compaction cap
			capWant, ok := nativeCaps[k]
			if !ok {
				t.Fatal("nativeCaps has no row")
			}
			if got := runtime.NativeCapTokens(string(k)); got != capWant {
				t.Errorf("NativeCapTokens = %d, want %d", got, capWant)
			}
			// hooked or unhooked question class
			hooked, ok := questionHooked[k]
			if !ok {
				t.Fatal("questionHooked has no row")
			}
			if runtime.QuestionHookKinds[k] != hooked {
				t.Errorf("questionHookKinds[%s] = %v, want %v", k, !hooked, hooked)
			}
		})
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
