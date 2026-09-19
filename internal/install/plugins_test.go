package install_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

func marketplaceServer(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestParseMarketplaceReadsTheNamesAndSources(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := install.ParseMarketplace(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 11 {
		t.Fatalf("plugins = %d, want 11", len(got))
	}
	if got[0].Name != "superpowers" || got[0].Source.URL != "https://github.com/obra/superpowers.git" {
		t.Errorf("first = %+v", got[0])
	}
}

// §12.4's matrix, pinned. superpowers-dev is never installed.
func TestSupportedByFollowsTheSpecMatrix(t *testing.T) {
	for _, tc := range []struct {
		plugin string
		kind   install.Kind
		want   bool
	}{
		{"superpowers", install.KindClaude, true},
		{"superpowers", install.KindCodex, true},
		{"superpowers", install.KindAgy, true},
		{"superpowers", install.KindCursor, true},
		{"elements-of-style", install.KindCursor, true},
		{"episodic-memory", install.KindCodex, true},
		{"episodic-memory", install.KindAgy, false},
		{"episodic-memory", install.KindCursor, false},
		{"superpowers-chrome", install.KindClaude, true},
		{"superpowers-chrome", install.KindCodex, false},
		{"claude-session-driver", install.KindAgy, false},
		{"private-journal-mcp", install.KindClaude, true},
		{"private-journal-mcp", install.KindCursor, false},
		// Not in the pinned matrix: claude only (see the task's rationale).
		{"brand-new-plugin", install.KindClaude, true},
		{"brand-new-plugin", install.KindCodex, false},
		// Never, for anyone.
		{"superpowers-dev", install.KindClaude, false},
	} {
		if got := install.SupportedBy(install.MarketplacePlugin{Name: tc.plugin}, tc.kind); got != tc.want {
			t.Errorf("SupportedBy(%s, %s) = %v, want %v", tc.plugin, tc.kind, got, tc.want)
		}
	}
}

func TestSyncRunsTheExactPerAgentCommands(t *testing.T) {
	c := fakeHome(t)
	srv := marketplaceServer(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"claude plugin marketplace list":                                            {Out: "no marketplaces\n"},
		"claude plugin marketplace add obra/superpowers-marketplace":                {Out: "added"},
		"claude plugin install superpowers@superpowers-marketplace --scope user -y": {Out: "ok"},
		"claude plugin list --json":                                                 {Out: `{"plugins":[]}`},
	}}
	var log bytes.Buffer
	p := install.Plugins{Cfg: c, Run: f.Runner(), HTTP: srv.Client(), MarketplaceURL: srv.URL, Log: &log}
	got := p.Sync(context.Background(), []install.Kind{install.KindClaude})

	calls := strings.Join(f.Calls(), "\n")
	for _, want := range []string{
		"claude plugin marketplace list",
		"claude plugin marketplace add obra/superpowers-marketplace",
		"claude plugin install superpowers@superpowers-marketplace --scope user -y",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing call %q; calls =\n%s", want, calls)
		}
	}
	// The "@" anchors this to the actual install command: a bare "superpowers-dev"
	// substring false-positives on "superpowers-developing-for-claude-code", which
	// the fixture also lists and which IS legitimately installed for claude.
	if strings.Contains(calls, "install superpowers-dev@") {
		t.Error("superpowers-dev must never be installed (§12.4)")
	}
	var installed int
	for _, r := range got {
		if r.Err == nil && r.Action == "install" {
			installed++
		}
	}
	if installed == 0 {
		t.Errorf("no successful installs: %+v", got)
	}
	if log.Len() == 0 {
		t.Error("nothing was written to the install log (§12.4)")
	}
}

// §12.4 I17: superpowers-dev present means superpowers is NOT installed alongside it.
func TestSyncSkipsSuperpowersWhenSuperpowersDevIsAlreadyInstalled(t *testing.T) {
	c := fakeHome(t)
	srv := marketplaceServer(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"claude plugin marketplace list":            {Out: "superpowers-marketplace\n"},
		"claude plugin list --json":                 {Out: `{"plugins":[{"name":"superpowers-dev"}]}`},
		"claude plugin update elements-of-style -y": {Out: "ok"},
		"claude plugin install elements-of-style@superpowers-marketplace --scope user -y": {Out: "ok"},
	}}
	p := install.Plugins{Cfg: c, Run: f.Runner(), HTTP: srv.Client(), MarketplaceURL: srv.URL, Log: &bytes.Buffer{}}
	got := p.Sync(context.Background(), []install.Kind{install.KindClaude})
	for _, call := range f.Calls() {
		if strings.Contains(call, "install superpowers@") {
			t.Errorf("superpowers was installed next to superpowers-dev (I17); calls = %v", f.Calls())
		}
	}
	if strings.Contains(strings.Join(f.Calls(), " "), "marketplace add") {
		t.Error("the marketplace is already present; add must be skipped (§12.4)")
	}
	var skipped bool
	for _, r := range got {
		if r.Plugin == "superpowers" && r.Action == "skip" {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("superpowers was not reported as skipped: %+v", got)
	}
}

// §12.4 cursor row + P0-8: clone into vendor, then copy as a REAL folder.
func TestSyncVendorsCursorPluginsAsRealFoldersNotSymlinks(t *testing.T) {
	c := fakeHome(t)
	srv := marketplaceServer(t)
	vendor := c.Vendor("superpowers")
	// The fake `git clone` cannot create files, so seed what a clone would leave.
	seedClone := func() {
		if err := os.MkdirAll(filepath.Join(vendor, ".cursor-plugin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(vendor, ".cursor-plugin", "plugin.json"), []byte(`{"skills":"./skills/"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(vendor, "skills", "brainstorming"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(vendor, "skills", "brainstorming", "SKILL.md"), []byte("# b"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &execx.Fake{Responses: map[string]execx.Result{
		"cursor-agent plugin marketplace add https://github.com/obra/superpowers-marketplace":                {Out: "added"},
		"git clone --depth 1 https://github.com/obra/superpowers.git " + vendor:                              {Out: "cloned"},
		"git clone --depth 1 https://github.com/obra/elements-of-style.git " + c.Vendor("elements-of-style"): {Out: "cloned"},
	}}
	seedClone()
	p := install.Plugins{Cfg: c, Run: f.Runner(), HTTP: srv.Client(), MarketplaceURL: srv.URL, Log: &bytes.Buffer{}}
	p.Sync(context.Background(), []install.Kind{install.KindCursor})

	local := c.Cursor("plugins", "local", "superpowers")
	fi, err := os.Lstat(local)
	if err != nil {
		t.Fatalf("%s: %v", local, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the local plugin is a symlink; cursor ignores those (P0-8)")
	}
	if _, err := os.Stat(filepath.Join(local, "skills", "brainstorming", "SKILL.md")); err != nil {
		t.Errorf("the copy is incomplete: %v", err)
	}
}

// §12.4: a failure for one agent does not stop the rest.
func TestSyncKeepsGoingAfterAFailureAndReportsIt(t *testing.T) {
	c := fakeHome(t)
	srv := marketplaceServer(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"claude plugin marketplace list": {Out: "superpowers-marketplace"},
		"claude plugin list --json":      {Out: `{"plugins":[]}`},
		"claude plugin install superpowers@superpowers-marketplace --scope user -y":       {Err: os.ErrPermission},
		"claude plugin install elements-of-style@superpowers-marketplace --scope user -y": {Out: "ok"},
	}}
	p := install.Plugins{Cfg: c, Run: f.Runner(), HTTP: srv.Client(), MarketplaceURL: srv.URL, Log: &bytes.Buffer{}}
	got := p.Sync(context.Background(), []install.Kind{install.KindClaude})
	var failed, ok int
	for _, r := range got {
		if r.Err != nil {
			failed++
		} else if r.Action == "install" {
			ok++
		}
	}
	if failed == 0 || ok == 0 {
		t.Fatalf("want at least one failure and one success: %+v", got)
	}
}

func TestSyncFailsSoftlyWhenTheMarketplaceIsUnreachable(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	p := install.Plugins{Cfg: c, Run: f.Runner(), HTTP: &http.Client{}, MarketplaceURL: "http://127.0.0.1:1/x.json", Log: &bytes.Buffer{}}
	got := p.Sync(context.Background(), []install.Kind{install.KindClaude})
	if len(got) != 1 || got[0].Err == nil {
		t.Fatalf("= %+v, want one failure", got)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("no command should run without a plugin list; calls = %v", f.Calls())
	}
}

// §12.4's usability check.
func TestSuperpowersOKChecksTheListedFilePerAgent(t *testing.T) {
	c := fakeHome(t)
	for _, tc := range []struct {
		kind install.Kind
		path string
	}{
		{install.KindClaude, c.Claude("plugins", "cache", "m1", "superpowers", "v1", "skills", "brainstorming", "SKILL.md")},
		{install.KindCodex, c.Codex("plugins", "cache", "m1", "superpowers", "v1", "skills", "brainstorming", "SKILL.md")},
		{install.KindAgy, c.Gemini("config", "plugins", "superpowers", "skills", "brainstorming", "SKILL.md")},
		{install.KindCursor, c.Cursor("plugins", "local", "superpowers", "skills", "brainstorming", "SKILL.md")},
	} {
		if ok, _ := install.SuperpowersOK(c, tc.kind); ok {
			t.Errorf("%s: ok before anything is installed", tc.kind)
		}
		if err := os.MkdirAll(filepath.Dir(tc.path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tc.path, []byte("# brainstorming"), 0o644); err != nil {
			t.Fatal(err)
		}
		if tc.kind == install.KindClaude {
			// claude also needs the TDD skill (§21.4 "the file check also needs the TDD skill").
			if ok, detail := install.SuperpowersOK(c, tc.kind); ok {
				t.Errorf("claude must also require test-driven-development: %q", detail)
			}
			tdd := filepath.Join(filepath.Dir(filepath.Dir(tc.path)), "test-driven-development", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(tdd), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tdd, []byte("# tdd"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if ok, detail := install.SuperpowersOK(c, tc.kind); !ok {
			t.Errorf("%s: %q", tc.kind, detail)
		}
	}
}
