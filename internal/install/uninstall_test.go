package install_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

func TestUninstallRemovesSwarmsOwnEntriesAndKeepsEverythingElse(t *testing.T) {
	c := fakeHome(t)
	// Install first, for all four agents, so uninstall has something to remove.
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.updater":               {},
		"launchctl bootout gui/501/dev.swarm.daemon":                {},
		"agy mcp add --type stdio swarm " + c.Bin + " mcp":          {},
		"agy mcp remove swarm":                                      {},
		"agy plugin list":                                           {Out: `{"imports":[]}`},
		"claude plugin marketplace list":                            {Out: install.MarketplaceName},
		"claude plugin list --json":                                 {Out: `{"plugins":[]}`},
		"codex plugin marketplace add obra/superpowers-marketplace": {},
		"codex plugin list":                                         {Out: ""},
		"cursor-agent plugin marketplace add https://github.com/obra/superpowers-marketplace": {},
	}}
	o := agentsOpts(t, c, f, install.Kinds...)
	o.PluginsOnly = true // skip the plugin installs; this test is about config files
	// Write the configuration directly, so the test does not depend on plugin argv.
	for _, w := range []func() ([]string, error){
		func() ([]string, error) { return install.WriteClaude(c) },
		func() ([]string, error) { return install.WriteCodex(c) },
		func() ([]string, error) { return install.WriteCursor(c) },
		func() ([]string, error) { return install.WriteAgy(context.Background(), c, f.Runner()) },
	} {
		if _, err := w(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := install.LinkBinary(c); err != nil {
		t.Fatal(err)
	}
	// A plist and a vendored plugin, both of which uninstall must treat differently.
	if err := os.MkdirAll(c.LaunchAgentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(install.PlistPath(c), install.Plist(c), 0o644); err != nil {
		t.Fatal(err)
	}
	vendored := c.Cursor("plugins", "local", "superpowers")
	if err := os.MkdirAll(vendored, 0o755); err != nil {
		t.Fatal(err)
	}

	o.PluginsOnly = false
	o.Out = &bytes.Buffer{}
	if err := install.Uninstall(context.Background(), o); err != nil {
		t.Fatal(err)
	}

	// Gone: the plist, the skills, the hook and MCP entries, the binary link.
	if _, err := os.Stat(install.PlistPath(c)); !os.IsNotExist(err) {
		t.Error("the launch agent plist survived")
	}
	for _, k := range install.Kinds {
		for _, name := range install.SkillNames {
			if _, err := os.Stat(filepath.Join(c.SkillsDir(k), name, "SKILL.md")); !os.IsNotExist(err) {
				t.Errorf("%s/%s survived", k, name)
			}
		}
	}
	codexHooks, _ := os.ReadFile(c.Codex("hooks.json"))
	if strings.Contains(string(codexHooks), "hook codex") {
		t.Errorf("codex hooks survived:\n%s", codexHooks)
	}
	cursorMCP, _ := os.ReadFile(c.Cursor("mcp.json"))
	var m struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(cursorMCP, &m); err != nil {
		t.Fatal(err)
	}
	if _, bad := m.MCPServers["swarm"]; bad {
		t.Error("the cursor MCP entry survived")
	}
	agyHooks, _ := os.ReadFile(c.Gemini("config", "hooks.json"))
	if strings.Contains(string(agyHooks), "hook agy") {
		t.Errorf("agy hooks survived:\n%s", agyHooks)
	}
	if _, err := os.Lstat(c.LocalBin()); !os.IsNotExist(err) {
		t.Error("~/.local/bin/swarm survived")
	}
	// launchctl bootout for the daemon ran.
	if !strings.Contains(strings.Join(f.Calls(), "\n"), "launchctl bootout gui/501/dev.swarm.daemon") {
		t.Errorf("the daemon was not booted out; calls = %v", f.Calls())
	}

	// Kept: data, plugins, cursor attribution.
	if _, err := os.Stat(c.Home); err != nil {
		t.Error("~/.swarm was removed; uninstall keeps data (§19)")
	}
	if _, err := os.Stat(vendored); err != nil {
		t.Error("a vendored plugin was removed; uninstall leaves plugins (§12.4)")
	}
	cli, _ := os.ReadFile(c.Cursor("cli-config.json"))
	if !strings.Contains(string(cli), "attributeCommitsToAgent") {
		t.Error("the cursor attribution keys were removed; uninstall leaves them (L23)")
	}
}

// A ~/.local/bin/swarm that points somewhere else is the user's, not ours.
func TestUninstallLeavesALinkThatPointsElsewhere(t *testing.T) {
	c := fakeHome(t)
	other := filepath.Join(c.UserHome, "other-swarm")
	if err := os.WriteFile(other, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(c.LocalBin()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, c.LocalBin()); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{"launchctl bootout gui/501/dev.swarm.daemon": {}}}
	o := agentsOpts(t, c, f)
	if err := install.Uninstall(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(c.LocalBin()); err != nil || got != other {
		t.Errorf("the user's own link was removed: %q, %v", got, err)
	}
}

func TestUninstallOnACleanHomeSucceedsAndCreatesNothing(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		// A not-loaded bootout is not an error (matches launchd_test.go's notLoaded convention).
		"launchctl bootout gui/501/dev.swarm.daemon": {Err: errors.New("exit 3: no such process")},
	}}
	if err := install.Uninstall(context.Background(), agentsOpts(t, c, f)); err != nil {
		t.Fatalf("a clean home must uninstall cleanly: %v", err)
	}
	for _, p := range []string{c.Codex("hooks.json"), c.Cursor("mcp.json"), c.Gemini("config", "hooks.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("uninstall created %s", p)
		}
	}
}
