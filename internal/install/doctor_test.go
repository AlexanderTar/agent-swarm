package install

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

type doctorEnv struct {
	d         Doctor
	fake      *execx.Fake
	daemon    *httptest.Server
	found     map[string]bool
	ollamaErr error
	healthy   bool
}

func newDoctorEnv(t *testing.T) *doctorEnv {
	e := &doctorEnv{found: map[string]bool{"claude": true, "codex": true}, healthy: true}
	e.fake = &execx.Fake{Responses: map[string]execx.Result{
		"tmux -V":                            {Out: "tmux 3.7c\n"},
		"git config --global commit.gpgsign": {Out: "true\n"},
	}}
	e.daemon = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !e.healthy || r.URL.Path != "/api/health" {
			http.Error(w, "down", 503)
			return
		}
		w.Write([]byte(`{"ok":true,"version":"dev","schema":1}`))
	}))
	t.Cleanup(e.daemon.Close)
	root := t.TempDir()
	apps := filepath.Join(root, "Applications")
	os.MkdirAll(filepath.Join(apps, "Ghostty.app"), 0o755)
	c := Config{Bin: "/bin/swarm", Home: filepath.Join(root, "home"), LaunchAgentsDir: filepath.Join(root, "LA"), Path: "/usr/bin", UID: 501}
	os.MkdirAll(c.LaunchAgentsDir, 0o755)
	os.WriteFile(PlistPath(c), Plist(c), 0o644)
	e.d = Doctor{Run: e.fake.Runner(), Home: c.Home, UserHome: root, LaunchAgentsDir: c.LaunchAgentsDir,
		DaemonURL: e.daemon.URL, OllamaCheck: func(context.Context) error { return e.ollamaErr }, GhosttyApps: []string{"/nonexistent/Ghostty.app", filepath.Join(apps, "Ghostty.app")},
		HTTP: http.DefaultClient,
		LookPath: func(name string) (string, error) {
			if e.found[name] {
				return "/usr/local/bin/" + name, nil
			}
			return "", errors.New("not found")
		}}
	return e
}

func byName(t *testing.T, checks []Check) map[string]Check {
	t.Helper()
	m := map[string]Check{}
	for _, c := range checks {
		m[c.Name] = c
	}
	if len(m) != 8 {
		t.Fatalf("checks = %+v", checks)
	}
	return m
}

func TestDoctorAllGood(t *testing.T) {
	e := newDoctorEnv(t)
	want := map[string]string{
		"tmux":           "tmux 3.7c",
		"Ghostty":        filepath.Join(e.d.UserHome, "Applications", "Ghostty.app"),
		"Ollama":         "The embedding model is ready.",
		"Agents":         "claude, codex",
		"Commit signing": "commit.gpgsign is true.",
		"Launch agent":   PlistPath(Config{LaunchAgentsDir: e.d.LaunchAgentsDir}),
		"Daemon":         "Running (version dev, schema 1).",
		"Data":           "No Agent Swarm 1.x data.",
	}
	for name, c := range byName(t, e.d.Checks(bg)) {
		if !c.OK || c.Detail != want[name] {
			t.Errorf("%s = %+v, want detail %q", name, c, want[name])
		}
	}
}

func TestDoctorFailures(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(e *doctorEnv)
		detail string
	}{
		{"tmux", func(e *doctorEnv) { e.fake.Responses["tmux -V"] = execx.Result{Out: "tmux 3.4\n"} }, "tmux 3.4 is too old. Install tmux 3.6 or later: brew install tmux"},
		{"tmux", func(e *doctorEnv) { e.fake.Responses["tmux -V"] = execx.Result{Err: errors.New("not found")} }, "tmux isn't installed. Install it: brew install tmux"},
		{"Ghostty", func(e *doctorEnv) { e.d.GhosttyApps = []string{"/nonexistent/Ghostty.app"} }, "Ghostty isn't installed. Install it: brew install --cask ghostty"},
		{"Ollama", func(e *doctorEnv) {
			e.ollamaErr = errors.New("Search unavailable: run `ollama pull qwen3-embedding:0.6b`")
		}, "Search unavailable: run `ollama pull qwen3-embedding:0.6b`"},
		{"Agents", func(e *doctorEnv) { e.found = map[string]bool{} }, "Install at least one agent CLI: claude, codex, agy or cursor-agent."},
		{"Commit signing", func(e *doctorEnv) {
			e.fake.Responses["git config --global commit.gpgsign"] = execx.Result{Out: "false\n"}
		}, "Commit signing is off. Enable it in git config."},
		{"Commit signing", func(e *doctorEnv) {
			e.fake.Responses["git config --global commit.gpgsign"] = execx.Result{Err: errors.New("exit 1")}
		}, "Commit signing is off. Enable it in git config."},
		{"Launch agent", func(e *doctorEnv) { os.Remove(filepath.Join(e.d.LaunchAgentsDir, "dev.swarm.daemon.plist")) }, "Not installed. Run swarm install."},
		{"Launch agent", func(e *doctorEnv) {
			os.WriteFile(filepath.Join(e.d.LaunchAgentsDir, "dev.swarm.daemon.plist"), []byte("<plist><string>/x/swarmd-start.sh</string></plist>"), 0o644)
		}, "The launch agent is from Agent Swarm 1.x. Run swarm install."},
		{"Daemon", func(e *doctorEnv) { e.healthy = false }, "The daemon isn't running. Run swarm install."},
		{"Data", func(e *doctorEnv) { legacyDB(t, e.d.Home) }, "Agent Swarm 1.x data found. Run `swarm migrate` first."},
	}
	for _, c := range cases {
		e := newDoctorEnv(t)
		c.break_(e)
		got := byName(t, e.d.Checks(bg))[c.name]
		if got.OK || got.Detail != c.detail {
			t.Errorf("%s: %+v, want %q", c.name, got, c.detail)
		}
	}
}
