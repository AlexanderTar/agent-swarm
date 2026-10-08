package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedClaudePlugin(t *testing.T, userHome, installPath string, enabled *bool) {
	t.Helper()
	doc := map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"superpowers@superpowers-marketplace": []any{
				map[string]any{"scope": "user", "installPath": installPath},
			},
		},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(userHome, ".claude", "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if enabled != nil {
		sbody, err := json.Marshal(map[string]any{
			"enabledPlugins": map[string]any{"superpowers@superpowers-marketplace": *enabled},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(userHome, ".claude", "settings.json"), sbody, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func pluginDirFlags(argv []string) []string {
	var out []string
	for i, v := range argv {
		if v == "--plugin-dir" && i+1 < len(argv) {
			out = append(out, argv[i+1])
		}
	}
	return out
}

func settingSources(argv []string) (string, bool) {
	for i, v := range argv {
		if v == "--setting-sources" && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

func TestClaudeLaunchIncludesPluginDirPerResolvedDir(t *testing.T) {
	d := testDeps(t)
	pluginDir := t.TempDir()
	yes := true
	seedClaudePlugin(t, d.UserHome, pluginDir, &yes)
	l, err := newClaude(d).Launch(claudeSpec(t, d))
	if err != nil {
		t.Fatal(err)
	}
	if got := pluginDirFlags(l.Argv); len(got) != 1 || got[0] != pluginDir {
		t.Fatalf("--plugin-dir flags = %q, want [%q]\nargv: %s", got, pluginDir, strings.Join(l.Argv, " "))
	}
	if v, ok := settingSources(l.Argv); !ok || v != "project,local" {
		t.Fatalf("--setting-sources = %q, %v, want project,local", v, ok)
	}
}

func TestClaudeResumeIncludesPluginDirPerResolvedDir(t *testing.T) {
	d := testDeps(t)
	pluginDir := t.TempDir()
	yes := true
	seedClaudePlugin(t, d.UserHome, pluginDir, &yes)
	s := claudeSpec(t, d)
	s.ProviderSessionID = "11111111-2222-4333-8444-555555555555"
	l, err := newClaude(d).Resume(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := pluginDirFlags(l.Argv); len(got) != 1 || got[0] != pluginDir {
		t.Fatalf("resume --plugin-dir flags = %q, want [%q]\nargv: %s", got, pluginDir, strings.Join(l.Argv, " "))
	}
	if v, ok := settingSources(l.Argv); !ok || v != "project,local" {
		t.Fatalf("resume --setting-sources = %q, %v, want project,local", v, ok)
	}
}

func TestClaudeLaunchOmitsPluginDirWhenDisabled(t *testing.T) {
	d := testDeps(t)
	no := false
	seedClaudePlugin(t, d.UserHome, t.TempDir(), &no)
	l, err := newClaude(d).Launch(claudeSpec(t, d))
	if err != nil {
		t.Fatal(err)
	}
	if got := pluginDirFlags(l.Argv); len(got) != 0 {
		t.Fatalf("--plugin-dir flags = %q, want none (disabled)\nargv: %s", got, strings.Join(l.Argv, " "))
	}
}

func TestClaudeLaunchOmitsPluginDirWhenFilesMissing(t *testing.T) {
	d := testDeps(t)
	l, err := newClaude(d).Launch(claudeSpec(t, d))
	if err != nil {
		t.Fatal(err)
	}
	if got := pluginDirFlags(l.Argv); len(got) != 0 {
		t.Fatalf("--plugin-dir flags = %q, want none (missing files)\nargv: %s", got, strings.Join(l.Argv, " "))
	}
}
