package install_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// agentsOpts wires a fully fake environment: fake home, fake commands, fake
// marketplace. S-5 and S-6 both.
func agentsOpts(t *testing.T, c install.Config, f *execx.Fake, kinds ...install.Kind) install.AgentsOpts {
	t.Helper()
	srv := marketplaceServer(t)
	return install.AgentsOpts{
		Cfg: c, Run: f.Runner(), HTTP: srv.Client(), MarketplaceURL: srv.URL,
		Installed: func(context.Context) []install.Kind { return kinds },
		Confirm:   alwaysYes,
		Out:       &bytes.Buffer{},
	}
}

// §20: removal runs before any writer registers the name `swarm`.
func TestAgentsRemovesTheV1IntegrationsBeforeWritingTheNewOnes(t *testing.T) {
	c := fakeHome(t)
	seedV1Home(t, c)
	seedFile(t, filepath.Join("codex", "config-v1.toml"), c.Codex("config.toml"))
	seedFile(t, filepath.Join("agy", "GEMINI-v1.md"), c.Gemini("GEMINI.md"))
	plugin := filepath.Join(c.Home, "app", "current", "plugin")
	for _, link := range []string{c.Claude("skills", "swarm"), c.Cursor("plugins", "local", "swarm")} {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(plugin, link); err != nil {
			t.Fatal(err)
		}
	}
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.updater":                                                          {},
		"claude mcp remove swarm -s user":                                                                      {},
		"claude mcp add swarm -s user -- " + c.Bin + " mcp":                                                    {},
		"claude plugin marketplace list":                                                                       {Out: install.MarketplaceName},
		"claude plugin list --json":                                                                            {Out: `{"plugins":[]}`},
		"claude plugin install superpowers@superpowers-marketplace --scope user -y":                            {Out: "ok"},
		"claude plugin install elements-of-style@superpowers-marketplace --scope user -y":                      {Out: "ok"},
		"claude plugin install episodic-memory@superpowers-marketplace --scope user -y":                        {Out: "ok"},
		"claude plugin install superpowers-chrome@superpowers-marketplace --scope user -y":                     {Out: "ok"},
		"claude plugin install superpowers-lab@superpowers-marketplace --scope user -y":                        {Out: "ok"},
		"claude plugin install superpowers-developing-for-claude-code@superpowers-marketplace --scope user -y": {Out: "ok"},
		"claude plugin install claude-session-driver@superpowers-marketplace --scope user -y":                  {Out: "ok"},
		"claude plugin install double-shot-latte@superpowers-marketplace --scope user -y":                      {Out: "ok"},
		"claude plugin install private-journal-mcp@superpowers-marketplace --scope user -y":                    {Out: "ok"},
		"claude plugin install brand-new-plugin@superpowers-marketplace --scope user -y":                       {Out: "ok"},
	}}
	out := &bytes.Buffer{}
	o := agentsOpts(t, c, f, install.KindClaude)
	o.Out = out
	if err := install.Agents(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	// §20 ordering: every "removed ..." line must come before every "wrote ..."
	// line. Claude alone can't tell "removal ran first" from "the writer's own
	// leftover-symlink cleanup papered over running out of order" (T6's WriteClaude
	// already removes a stale v1 symlink itself), so this checks the report log
	// Agents itself produces, not just the end state on disk.
	if s := out.String(); strings.Index(s, "wrote ") >= 0 && strings.LastIndex(s, "removed ") > strings.Index(s, "wrote ") {
		t.Errorf("a writer ran before removal finished:\n%s", s)
	}
	// Removal happened.
	for _, link := range []string{c.Claude("skills", "swarm"), c.Cursor("plugins", "local", "swarm")} {
		if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s is still a v1 symlink", link)
		}
	}
	toml, _ := os.ReadFile(c.Codex("config.toml"))
	if strings.Contains(string(toml), "mcp_servers.swarm") {
		t.Error("the v1 codex MCP table survived")
	}
	md, _ := os.ReadFile(c.Gemini("GEMINI.md"))
	if strings.Contains(string(md), "swarm:start") {
		t.Error("the v1 GEMINI.md block survived")
	}
	// Writers ran for claude only, and the binary link exists.
	if _, err := os.Stat(c.Claude("skills", "swarm", "SKILL.md")); err != nil {
		t.Error(err)
	}
	if got, err := os.Readlink(c.LocalBin()); err != nil || got != c.Bin {
		t.Errorf("~/.local/bin/swarm = %q, %v", got, err)
	}
	// No codex or cursor writer ran: they are not installed.
	if _, err := os.Stat(c.Codex("hooks.json")); !os.IsNotExist(err) {
		t.Error("codex hooks were written although codex is not installed")
	}
	if len(install.Leftovers(c)) != 0 {
		t.Errorf("leftovers after install: %+v", install.Leftovers(c))
	}
}

func TestAgentsIsIdempotent(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.updater":      {},
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {},
		"agy plugin list": {Out: `{"imports":[{"name":"superpowers"},{"name":"elements-of-style"}]}`},
		"agy plugin install https://github.com/obra/superpowers":       {Out: "ok"},
		"agy plugin install https://github.com/obra/elements-of-style": {Out: "ok"},
	}}
	o := agentsOpts(t, c, f, install.KindAgy)
	for i := 0; i < 2; i++ {
		if err := install.Agents(context.Background(), o); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(c.Gemini("config", "hooks.json")); err != nil {
		t.Fatal(err)
	}
}

// §12.4: a plugin failure is reported but does not fail the install.
func TestAgentsReportsPluginFailuresWithoutFailing(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.updater":       {},
		"claude mcp remove swarm -s user":                   {},
		"claude mcp add swarm -s user -- " + c.Bin + " mcp": {},
		"claude plugin marketplace list":                    {Out: install.MarketplaceName},
		"claude plugin list --json":                         {Out: `{"plugins":[]}`},
	}} // every install command is missing → every one fails
	out := &bytes.Buffer{}
	o := agentsOpts(t, c, f, install.KindClaude)
	o.Out = out
	if err := install.Agents(context.Background(), o); err != nil {
		t.Fatalf("plugin failures must not fail swarm install (§12.4): %v", err)
	}
	if !strings.Contains(out.String(), "✗ claude:") {
		t.Errorf("no failure line in the output:\n%s", out)
	}
}

// swarm install --plugins touches no config file.
func TestAgentsWithPluginsOnlyWritesNoConfiguration(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"claude plugin marketplace list": {Out: install.MarketplaceName},
		"claude plugin list --json":      {Out: `{"plugins":[]}`},
	}}
	o := agentsOpts(t, c, f, install.KindClaude)
	o.PluginsOnly = true
	if err := install.Agents(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Claude("skills", "swarm", "SKILL.md")); !os.IsNotExist(err) {
		t.Error("--plugins must not install skills")
	}
	for _, call := range f.Calls() {
		if strings.HasPrefix(call, "launchctl") {
			t.Errorf("--plugins must not touch launchd; calls = %v", f.Calls())
		}
	}
}

// A writer error is fatal: a half-written config would launch agents wrong.
func TestAgentsFailsWhenAWriterFails(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.updater":      {},
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {Err: errors.New("agy: not signed in")},
	}}
	o := agentsOpts(t, c, f, install.KindAgy)
	if err := install.Agents(context.Background(), o); err == nil {
		t.Fatal("want an error when `agy mcp add` fails")
	}
}

// S-5: InstalledKinds uses the injected LookPath, never a real one in tests.
func TestInstalledKindsUsesTheInjectedLookPath(t *testing.T) {
	found := map[string]bool{"codex": true, "cursor-agent": true}
	kinds := install.InstalledKinds(func(bin string) (string, error) {
		if found[bin] {
			return "/fake/bin/" + bin, nil
		}
		return "", errors.New("not found")
	})(context.Background())
	if len(kinds) != 2 || kinds[0] != install.KindCodex || kinds[1] != install.KindCursor {
		t.Fatalf("kinds = %v, want [codex cursor]", kinds)
	}
}
