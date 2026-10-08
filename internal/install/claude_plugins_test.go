package install_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/install"
)

func writeClaudePlugins(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeClaudeSettings(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkPluginDir(t *testing.T, home, name string) string {
	t.Helper()
	p := filepath.Join(home, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeClaudePluginsDoc(t *testing.T, home string, plugins map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": 2, "plugins": plugins})
	if err != nil {
		t.Fatal(err)
	}
	writeClaudePlugins(t, home, string(body))
}

func TestClaudePluginDirsReturnsQualifyingUserScopePlugin(t *testing.T) {
	home := t.TempDir()
	dir := mkPluginDir(t, home, "sp")
	writeClaudePluginsDoc(t, home, map[string]any{
		"superpowers@superpowers-marketplace": []any{
			map[string]any{"scope": "user", "installPath": dir},
		},
	})
	writeClaudeSettings(t, home, `{"enabledPlugins":{"superpowers@superpowers-marketplace":true}}`)
	want := []string{dir}
	if got := install.ClaudePluginDirs(home); !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudePluginDirs() = %q, want %q", got, want)
	}
}

func TestClaudePluginDirsExcludesOtherMarketplaces(t *testing.T) {
	home := t.TempDir()
	sp := mkPluginDir(t, home, "sp")
	cx := mkPluginDir(t, home, "cx")
	writeClaudePluginsDoc(t, home, map[string]any{
		"superpowers@superpowers-marketplace": []any{
			map[string]any{"scope": "user", "installPath": sp},
		},
		"context7@claude-plugins-official": []any{
			map[string]any{"scope": "user", "installPath": cx},
		},
	})
	got := install.ClaudePluginDirs(home)
	want := []string{sp}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudePluginDirs() = %q, want %q (other marketplaces out)", got, want)
	}
}

func TestClaudePluginDirsExcludesSkipAlways(t *testing.T) {
	home := t.TempDir()
	dev := mkPluginDir(t, home, "dev")
	sp := mkPluginDir(t, home, "sp")
	writeClaudePluginsDoc(t, home, map[string]any{
		"superpowers-dev@superpowers-marketplace": []any{
			map[string]any{"scope": "user", "installPath": dev},
		},
		"superpowers@superpowers-marketplace": []any{
			map[string]any{"scope": "user", "installPath": sp},
		},
	})
	got := install.ClaudePluginDirs(home)
	want := []string{sp}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudePluginDirs() = %q, want %q (SkipAlways out)", got, want)
	}
}

func TestClaudePluginDirsExcludesExplicitlyDisabledPlugin(t *testing.T) {
	home := t.TempDir()
	sp := mkPluginDir(t, home, "sp")
	writeClaudePluginsDoc(t, home, map[string]any{
		"superpowers@superpowers-marketplace": []any{
			map[string]any{"scope": "user", "installPath": sp},
		},
	})
	writeClaudeSettings(t, home, `{"enabledPlugins":{"superpowers@superpowers-marketplace":false}}`)
	if got := install.ClaudePluginDirs(home); len(got) != 0 {
		t.Fatalf("ClaudePluginDirs() = %q, want empty (explicitly disabled)", got)
	}
}

func TestClaudePluginDirsExcludesNonUserScope(t *testing.T) {
	home := t.TempDir()
	sp := mkPluginDir(t, home, "sp")
	writeClaudePluginsDoc(t, home, map[string]any{
		"superpowers@superpowers-marketplace": []any{
			map[string]any{"scope": "project", "installPath": sp},
		},
	})
	if got := install.ClaudePluginDirs(home); len(got) != 0 {
		t.Fatalf("ClaudePluginDirs() = %q, want empty (non-user scope)", got)
	}
}

func TestClaudePluginDirsMissingFilesAreEmpty(t *testing.T) {
	if got := install.ClaudePluginDirs(t.TempDir()); len(got) != 0 {
		t.Fatalf("ClaudePluginDirs() = %q, want empty (missing files)", got)
	}
}

func TestClaudePluginDirsMalformedFileIsEmpty(t *testing.T) {
	home := t.TempDir()
	writeClaudePlugins(t, home, `{"version":2,"plugins":`)
	if got := install.ClaudePluginDirs(home); len(got) != 0 {
		t.Fatalf("ClaudePluginDirs() = %q, want empty (malformed file)", got)
	}
}

func TestClaudePluginDirsKeepsOnlyAllKindPlugins(t *testing.T) {
	home := t.TempDir()
	sp := mkPluginDir(t, home, "sp")
	es := mkPluginDir(t, home, "es")
	dsl := mkPluginDir(t, home, "dsl")
	em := mkPluginDir(t, home, "em")
	writeClaudePluginsDoc(t, home, map[string]any{
		"superpowers@superpowers-marketplace": []any{
			map[string]any{"scope": "user", "installPath": sp},
		},
		"elements-of-style@superpowers-marketplace": []any{
			map[string]any{"scope": "user", "installPath": es},
		},
		"double-shot-latte@superpowers-marketplace": []any{
			map[string]any{"scope": "user", "installPath": dsl},
		},
		"episodic-memory@superpowers-marketplace": []any{
			map[string]any{"scope": "user", "installPath": em},
		},
	})
	got := install.ClaudePluginDirs(home)
	// want is sorted (ClaudePluginDirs sorts output)
	want := []string{es, sp}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudePluginDirs() = %q, want %q (only all-kind plugins)", got, want)
	}
}

func TestClaudePluginDirsSkipsMissingInstallPath(t *testing.T) {
	home := t.TempDir()
	valid := filepath.Join(home, "present")
	if err := os.MkdirAll(valid, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(home, "file")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(home, "absent")
	doc := map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"superpowers@superpowers-marketplace": []any{
				map[string]any{"scope": "user", "installPath": valid},
				map[string]any{"scope": "user", "installPath": missing},
				map[string]any{"scope": "user", "installPath": filePath},
			},
		},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeClaudePlugins(t, home, string(body))
	got := install.ClaudePluginDirs(home)
	want := []string{valid}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudePluginDirs() = %q, want %q (missing dir and file out)", got, want)
	}
}
