package install_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// seedV1Home builds the v1 layout this machine actually had on 2026-09-18, inside
// the fake home: app/{current,releases}, bin/{swarm,swarmd-start.sh,swarm-update.sh}.
func seedV1Home(t *testing.T, c install.Config) {
	t.Helper()
	for _, dir := range []string{
		filepath.Join(c.Home, "app", "releases", "0.1.0", "plugin"),
		filepath.Join(c.Home, "bin"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(c.Home, "app", "releases", "0.1.0"), filepath.Join(c.Home, "app", "current")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"swarm", "swarmd-start.sh", "swarm-update.sh"} {
		if err := os.WriteFile(filepath.Join(c.Home, "bin", f), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func alwaysYes(string) bool { return true }
func alwaysNo(string) bool  { return false }

func TestRemoveLegacySharedBootsOutTheUpdaterAndRemovesTheReleaseFolders(t *testing.T) {
	c := fakeHome(t)
	seedV1Home(t, c)
	target := "gui/501/dev.swarm.updater"
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout " + target: {Out: ""},
	}}
	changed, err := install.RemoveLegacyShared(context.Background(), c, f.Runner(), alwaysYes)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); len(got) != 1 || got[0] != "launchctl bootout "+target {
		t.Fatalf("calls = %v", got)
	}
	if _, err := os.Stat(filepath.Join(c.Home, "app")); !os.IsNotExist(err) {
		t.Error("~/.swarm/app survived")
	}
	for _, f := range []string{"swarmd-start.sh", "swarm-update.sh"} {
		if _, err := os.Stat(filepath.Join(c.Home, "bin", f)); !os.IsNotExist(err) {
			t.Errorf("%s survived", f)
		}
	}
	// The v1 binary itself stays: ~/.local/bin/swarm may still point at it until
	// LinkBinary repoints the link.
	if _, err := os.Stat(filepath.Join(c.Home, "bin", "swarm")); err != nil {
		t.Error("~/.swarm/bin/swarm must be left in place")
	}
	if len(changed) == 0 {
		t.Error("changed is empty")
	}

	// Idempotence (Global Constraint): a second run on the now-clean home reports
	// no change and never re-prompts, even though the bootout is still attempted
	// (it is unconditional and tolerant, per §21.3).
	changed, err = install.RemoveLegacyShared(context.Background(), c, f.Runner(), func(string) bool {
		t.Fatal("confirm called on an already-clean home")
		return false
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("second run changed = %v, want none", changed)
	}
	if got := f.Calls(); len(got) != 2 {
		t.Fatalf("calls after second run = %v, want 2 bootouts", got)
	}
}

// P1's notLoaded: a bootout for a job that was never loaded is not an error.
func TestRemoveLegacySharedToleratesAnUnloadedUpdater(t *testing.T) {
	c := fakeHome(t)
	seedV1Home(t, c)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.updater": {Err: errors.New("Could not find service \"dev.swarm.updater\"")},
	}}
	if _, err := install.RemoveLegacyShared(context.Background(), c, f.Runner(), alwaysYes); err != nil {
		t.Fatalf("a not-loaded updater must not fail the install: %v", err)
	}
}

func TestRemoveLegacySharedLeavesTheFoldersWhenTheUserDeclines(t *testing.T) {
	c := fakeHome(t)
	seedV1Home(t, c)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.updater": {},
	}}
	changed, err := install.RemoveLegacyShared(context.Background(), c, f.Runner(), alwaysNo)
	if err != nil {
		t.Fatalf("declining must not be an error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Home, "app")); err != nil {
		t.Error("~/.swarm/app was removed without confirmation")
	}
	for _, name := range []string{"swarmd-start.sh", "swarm-update.sh"} {
		if _, err := os.Stat(filepath.Join(c.Home, "bin", name)); err != nil {
			t.Errorf("%s was removed without confirmation", name)
		}
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want none; declining removed nothing", changed)
	}
}

func TestLinkBinaryReplacesAStaleLinkAndRefusesARealFile(t *testing.T) {
	c := fakeHome(t)
	link := c.LocalBin()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	// old must differ from c.Bin: fakeHome puts Bin at ~/.swarm/bin/swarm, so a
	// link already pointing there is already correct and LinkBinary rightly
	// leaves it alone (see the no-op assertion below).
	old := filepath.Join(t.TempDir(), "old-swarm")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}

	wrote, err := install.LinkBinary(c)
	if err != nil || !wrote {
		t.Fatalf("= %v, %v", wrote, err)
	}
	got, err := os.Readlink(link)
	if err != nil || got != c.Bin {
		t.Fatalf("readlink = %q, %v, want %q", got, err, c.Bin)
	}
	wrote, err = install.LinkBinary(c)
	if err != nil || wrote {
		t.Fatalf("second LinkBinary = %v, %v; an up-to-date link must be a no-op", wrote, err)
	}

	// A real file there is the user's own binary; never clobber it.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := install.LinkBinary(c); err == nil {
		t.Fatal("want an error: a real file at ~/.local/bin/swarm must not be replaced")
	}
	body, _ := os.ReadFile(link)
	if string(body) != "mine" {
		t.Errorf("the user's file was overwritten: %q", body)
	}
}

// §25 / doctor --legacy: every v1 location §20 lists is reported while it exists.
func TestLeftoversReportsEveryV1LocationAndNothingOnACleanHome(t *testing.T) {
	c := fakeHome(t)
	if got := install.Leftovers(c); len(got) != 0 {
		t.Fatalf("clean home = %+v", got)
	}

	plugin := filepath.Join(c.Home, "app", "current", "plugin")
	if err := os.MkdirAll(plugin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{
		c.Claude("skills", "swarm"),
		c.Cursor("plugins", "local", "swarm"),
		c.Gemini("config", "plugins", "swarm"),
	} {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(plugin, link); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(c.Codex(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Codex("config.toml"), []byte("[mcp_servers.swarm]\ncommand = \"node\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Gemini(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Gemini("GEMINI.md"),
		[]byte("a\n<!-- swarm:start -->\nb\n<!-- swarm:end -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Gemini("antigravity"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Gemini("antigravity", "mcp_config.json"),
		[]byte(`{"mcpServers":{"swarm":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(c.Home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Home, "bin", "swarm-update.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Home, "config.json"), []byte(`{"port":7777}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := install.Leftovers(c)
	byPath := map[string]string{}
	for _, l := range got {
		byPath[l.Path] = l.What
	}
	for _, want := range []string{
		c.Claude("skills", "swarm"),
		c.Cursor("plugins", "local", "swarm"),
		c.Gemini("config", "plugins", "swarm"),
		c.Codex("config.toml"),
		c.Gemini("GEMINI.md"),
		c.Gemini("antigravity", "mcp_config.json"),
		filepath.Join(c.Home, "app"),
		filepath.Join(c.Home, "bin", "swarm-update.sh"),
		filepath.Join(c.Home, "config.json"),
	} {
		if _, ok := byPath[want]; !ok {
			t.Errorf("Leftovers did not report %s; got %+v", want, got)
		}
	}
}
