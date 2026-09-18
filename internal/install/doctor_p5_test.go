package install_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// newTestDoctor builds a Doctor whose every seam is fake (S-5, S-6): no real
// command, no real HTTP, no real home.
func newTestDoctor(t *testing.T, c install.Config, installed ...install.Kind) install.Doctor {
	t.Helper()
	f := &execx.Fake{Responses: map[string]execx.Result{
		"tmux -V":                            {Out: "tmux 3.7c\n"},
		"git config --global commit.gpgsign": {Out: "true\n"},
	}}
	return install.Doctor{
		Run: f.Runner(), Home: c.Home, UserHome: c.UserHome, LaunchAgentsDir: c.LaunchAgentsDir,
		Cfg: c, Installed: func(context.Context) []install.Kind { return installed },
		DaemonURL:   "http://127.0.0.1:1",
		OllamaCheck: func(context.Context) error { return nil },
		GhosttyApps: []string{filepath.Join(c.UserHome, "Applications", "Ghostty.app")},
		LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
		HTTP:        &http.Client{Timeout: time.Millisecond},
	}
}

// §11.1: plain doctor fails on a leftover codex MCP table, and on nothing else
// from the legacy list — an operator who kept ~/.swarm/app must still see green.
func TestChecksFailOnlyOnTheCodexMCPTableFromTheLegacyList(t *testing.T) {
	c := fakeHome(t)
	seedFile(t, filepath.Join("codex", "config-v1.toml"), c.Codex("config.toml"))
	if err := os.MkdirAll(filepath.Join(c.Home, "app", "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newTestDoctor(t, c, install.KindCodex)
	names := map[string]bool{}
	for _, ch := range d.Checks(context.Background()) {
		if !ch.OK {
			names[ch.Name] = true
		}
	}
	if !names["Codex MCP"] {
		t.Error("Codex MCP must fail while [mcp_servers.swarm] exists (§11.1)")
	}
	if names["Agent Swarm 1.x"] {
		t.Error("the release folders must not fail plain doctor; they are --legacy only")
	}
}

// §25: doctor --legacy reports every leftover.
func TestLegacyChecksListEveryLeftoverAndPassWhenClean(t *testing.T) {
	c := fakeHome(t)
	d := newTestDoctor(t, c)
	got := d.LegacyChecks(context.Background())
	if len(got) != 1 || !got[0].OK || got[0].Name != "Agent Swarm 1.x" {
		t.Fatalf("clean = %+v", got)
	}
	if err := os.MkdirAll(filepath.Join(c.Home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Home, "bin", "swarm-update.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got = d.LegacyChecks(context.Background())
	if len(got) != 1 || got[0].OK {
		t.Fatalf("with a leftover = %+v", got)
	}
	if !strings.Contains(got[0].Detail, "launcher script") {
		t.Errorf("detail = %q", got[0].Detail)
	}
}

// §12.4: doctor warns when both superpowers variants are present.
func TestSuperpowersCheckFailsWhenBothVariantsAreInstalled(t *testing.T) {
	c := fakeHome(t)
	for _, name := range []string{"superpowers", "superpowers-dev"} {
		for _, skill := range []string{"brainstorming", "test-driven-development"} {
			p := c.Claude("plugins", "cache", "m1", name, "v1", "skills", skill, "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("#"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	d := newTestDoctor(t, c, install.KindClaude)
	var found bool
	for _, ch := range d.Checks(context.Background()) {
		if ch.Name != "Claude superpowers" {
			continue
		}
		found = true
		if ch.OK {
			t.Error("want a failure when both variants are installed (§12.4)")
		}
		if want := "Both superpowers and superpowers-dev are installed for Claude. Remove one."; ch.Detail != want {
			t.Errorf("detail = %q, want %q", ch.Detail, want)
		}
	}
	if !found {
		t.Fatal("no Claude superpowers check")
	}
}

// P1's eight checks stay first, in their original order.
func TestChecksKeepsThePhaseOneOrderFirst(t *testing.T) {
	c := fakeHome(t)
	d := newTestDoctor(t, c)
	got := d.Checks(context.Background())
	want := []string{"tmux", "Ghostty", "Ollama", "Agents", "Commit signing", "Launch agent", "Daemon", "Data"}
	if len(got) < len(want) {
		t.Fatalf("got %d checks", len(got))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("check %d = %q, want %q", i, got[i].Name, name)
		}
	}
}
