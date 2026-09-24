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

// A1: every installed kind gets its own skills check, not only Claude.
func TestDoctorChecksSkillsForEveryKind(t *testing.T) {
	c := fakeHome(t)
	for _, k := range install.Kinds {
		if _, _, err := install.WriteSkills(c, k); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	d := newTestDoctor(t, c, install.Kinds...)
	seen := map[install.Kind]bool{}
	for _, ch := range d.Checks(context.Background()) {
		for _, k := range install.Kinds {
			if ch.Name == k.Display()+" skills" {
				seen[k] = true
				if !ch.OK {
					t.Errorf("%s skills check failed after WriteSkills: %+v", k, ch)
				}
			}
		}
	}
	for _, k := range install.Kinds {
		if !seen[k] {
			t.Errorf("no skills check for %s", k)
		}
	}
}

// Review round 1, Minor 9: the exact user-owned wording A1 promises, not just
// "OK and mentions the skill somehow".
func TestCheckSkillsReportsTheExactUserOwnedDetail(t *testing.T) {
	c := fakeHome(t)
	for _, name := range install.SkillNames() {
		if name == "swarm" {
			continue
		}
		p := filepath.Join(c.SkillsDir(install.KindCodex), name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, install.SkillBody(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A real user directory, no marker, content that is deliberately not a
	// valid pre-A1 swarm skill body -- genuinely user-owned, not adopted.
	own := filepath.Join(c.SkillsDir(install.KindCodex), "swarm")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "SKILL.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ch := install.CheckSkills(c, install.KindCodex)
	if !ch.OK {
		t.Fatalf("a user-owned skill must not fail the check: %+v", ch)
	}
	want := "skill swarm for Codex is user-owned; swarm's copy is not installed there"
	if !strings.Contains(ch.Detail, want) {
		t.Errorf("detail = %q, want it to contain %q", ch.Detail, want)
	}
}

// Review round 2, item 2: CheckSkills used adopt=false, so a marker-less
// pre-A1 install (this machine's real shape until the next `swarm install` --
// see the P1 fix report) was reported "user-owned" even though it is
// swarm's own. Doctor is read-only, so there is no adoption risk here the
// way there is for the daemon's automatic refresh: adopt=true lets it agree
// with what an explicit `swarm install` would recognize as pre-A1.
func TestCheckSkillsRecognizesAPreA1InstallAsSwarmOwned(t *testing.T) {
	c := fakeHome(t)
	if _, _, err := install.WriteSkills(c, install.KindCodex); err != nil {
		t.Fatal(err)
	}
	// Replace the fresh "swarm" install with a marker-less pre-A1 shape.
	dst := filepath.Join(c.SkillsDir(install.KindCodex), "swarm")
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), readTestdata(t, "pre_a1_swarm_skill.md"), 0o644); err != nil {
		t.Fatal(err)
	}

	ch := install.CheckSkills(c, install.KindCodex)
	if !ch.OK {
		t.Fatalf("a pre-A1 install must not fail the check: %+v", ch)
	}
	if strings.Contains(ch.Detail, "user-owned") {
		t.Errorf("a pre-A1 install was reported user-owned: %+v", ch)
	}
}

// A1: ui-ux-pro-max's search script needs python3, but its absence must warn,
// not fail doctor (§ many machines run swarm without it and still work fine
// otherwise).
func TestDoctorWarnsWithoutPython3(t *testing.T) {
	c := fakeHome(t)
	d := newTestDoctor(t, c) // LookPath always errors, so python3 "isn't found"
	ch := findCheck(t, d.Checks(context.Background()), "python3")
	if !ch.OK {
		t.Errorf("python3 must warn, not fail doctor: %+v", ch)
	}
	if !strings.Contains(ch.Detail, "python3") {
		t.Errorf("detail should mention python3: %+v", ch)
	}
}
