package install_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// isolatedHome is a fake user home whose swarm home is a scratch folder, not
// ~/.swarm: the shape of `SWARM_HOME=<scratch> swarm install` (BUG-100).
func isolatedHome(t *testing.T) install.Config {
	t.Helper()
	c := fakeHome(t)
	c.Home = filepath.Join(t.TempDir(), "scratch-swarm")
	c.Isolated = true
	return c
}

// launchctlCalls returns every call the shim saw that touches launchd.
func launchctlCalls(f *execx.Fake) []string {
	var out []string
	for _, call := range f.Calls() {
		if strings.HasPrefix(call, "launchctl ") {
			out = append(out, call)
		}
	}
	return out
}

func TestIsolatedInstallNeverTouchesLaunchd(t *testing.T) {
	for _, dry := range []bool{false, true} {
		c := isolatedHome(t)
		f := &execx.Fake{}
		var out bytes.Buffer
		if err := install.Install(context.Background(), c, f.Runner(), dry, &out); err != nil {
			t.Fatal(err)
		}
		if got := launchctlCalls(f); len(got) != 0 {
			t.Errorf("dry=%v: isolated install ran %v", dry, got)
		}
		if _, err := os.Stat(c.LaunchAgentsDir); !os.IsNotExist(err) {
			t.Errorf("dry=%v: isolated install created %s", dry, c.LaunchAgentsDir)
		}
		if !strings.Contains(out.String(), "--live") {
			t.Errorf("dry=%v: output does not say how to opt in:\n%s", dry, out.String())
		}
	}
}

func TestIsolatedUninstallNeverTouchesLaunchd(t *testing.T) {
	c := isolatedHome(t)
	if err := os.MkdirAll(c.LaunchAgentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	live := install.PlistPath(c)
	if err := os.WriteFile(live, []byte("live plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	if err := install.Uninstall(context.Background(), agentsOpts(t, c, f)); err != nil {
		t.Fatal(err)
	}
	if got := launchctlCalls(f); len(got) != 0 {
		t.Errorf("isolated uninstall ran %v", got)
	}
	if b, err := os.ReadFile(live); err != nil || string(b) != "live plist" {
		t.Errorf("isolated uninstall changed the live plist: %q %v", b, err)
	}
}

func TestIsolatedAgentsNeverBootsOutTheUpdater(t *testing.T) {
	c := isolatedHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	if err := install.Agents(context.Background(), agentsOpts(t, c, f)); err != nil {
		t.Fatal(err)
	}
	if got := launchctlCalls(f); len(got) != 0 {
		t.Errorf("isolated agents ran %v", got)
	}
}

// Default-home behaviour is unchanged: not Isolated still boots out and bootstraps.
func TestNonIsolatedInstallStillTouchesLaunchd(t *testing.T) {
	c := isolatedHome(t)
	c.Isolated = false
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.daemon":          {},
		"launchctl bootstrap gui/501 " + install.PlistPath(c): {},
	}}
	if err := install.Install(context.Background(), c, f.Runner(), false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := launchctlCalls(f); len(got) != 2 {
		t.Errorf("launchctl calls = %v, want bootout + bootstrap", got)
	}
}

// snapshot maps every path under root to its mode and content (links by target).
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			dst, _ := os.Readlink(p)
			out[rel] = "link " + dst
		case fi.IsDir():
			out[rel] = "dir"
		default:
			b, _ := os.ReadFile(p)
			out[rel] = fi.Mode().String() + " " + string(b)
		}
		return nil
	})
	return out
}

// seedUserConfigs puts files the user owns in the fake home, where an install
// would otherwise add or edit Swarm entries.
func seedUserConfigs(t *testing.T, c install.Config) {
	t.Helper()
	for p, body := range map[string]string{
		c.Claude("settings.json"):                           `{"theme":"dark"}`,
		c.Codex("config.toml"):                              "model = \"x\"\n",
		c.Cursor("mcp.json"):                                `{"mcpServers":{"swarm":{"command":"x"}}}`,
		c.Gemini("GEMINI.md"):                               "# mine\n",
		c.UserGrok("config.toml"):                           "[ui]\n",
		filepath.Join(c.UserHome, ".local", "bin", "other"): "mine",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIsolatedInstallAndUninstallLeaveUserAgentConfigsAlone(t *testing.T) {
	for _, op := range []struct {
		name string
		run  func(install.Config, *execx.Fake) error
	}{
		{"agents", func(c install.Config, f *execx.Fake) error {
			return install.Agents(context.Background(), agentsOpts(t, c, f, install.Kinds...))
		}},
		{"plugins", func(c install.Config, f *execx.Fake) error {
			o := agentsOpts(t, c, f, install.Kinds...)
			o.PluginsOnly = true
			return install.Agents(context.Background(), o)
		}},
		{"uninstall", func(c install.Config, f *execx.Fake) error {
			return install.Uninstall(context.Background(), agentsOpts(t, c, f, install.Kinds...))
		}},
	} {
		t.Run(op.name, func(t *testing.T) {
			c := isolatedHome(t)
			seedUserConfigs(t, c)
			before := snapshot(t, c.UserHome)
			f := &execx.Fake{Responses: map[string]execx.Result{}}
			if err := op.run(c, f); err != nil {
				t.Fatal(err)
			}
			after := snapshot(t, c.UserHome)
			for p, want := range before {
				if after[p] != want {
					t.Errorf("%s changed: %q -> %q", p, want, after[p])
				}
			}
			for p := range after {
				if _, ok := before[p]; !ok {
					t.Errorf("%s was created", p)
				}
			}
			if calls := f.Calls(); len(calls) != 0 {
				t.Errorf("ran %v", calls)
			}
		})
	}
}
