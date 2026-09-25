package install_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// §11.1 + §21.4 row 1: flat commands for Pre/PostInvocation and Stop, a matcher
// wrapper for the two tool events, no PLUGIN_ROOT, and five keys (agy has no
// SessionStart and no compaction event).
func TestAgyHooksUsesFlatHandlersForInvocationAndStopAndMatchersForTools(t *testing.T) {
	c := fakeHome(t)
	var f map[string]map[string]json.RawMessage
	if err := json.Unmarshal(install.AgyHooks(c), &f); err != nil {
		t.Fatal(err)
	}
	named, ok := f["swarm"]
	if !ok {
		t.Fatalf("hooks.json is not keyed by the hook name `swarm`: %v", f)
	}
	if len(named) != 5 {
		t.Errorf("events = %d, want 5: %v", len(named), named)
	}
	for _, ev := range []string{"PreInvocation", "PostInvocation", "Stop"} {
		var flat []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
			Matcher string `json:"matcher"`
			Hooks   []any  `json:"hooks"`
		}
		if err := json.Unmarshal(named[ev], &flat); err != nil {
			t.Fatalf("%s: %v", ev, err)
		}
		if len(flat) != 1 {
			t.Fatalf("%s = %+v", ev, flat)
		}
		if flat[0].Command != c.Bin+" hook agy "+ev {
			t.Errorf("%s command = %q", ev, flat[0].Command)
		}
		if flat[0].Timeout != 3 {
			t.Errorf("%s timeout = %d, want 3", ev, flat[0].Timeout)
		}
		if flat[0].Matcher != "" || len(flat[0].Hooks) != 0 {
			t.Errorf("%s must be a FLAT {type,command,timeout} handler; a nested one fails to parse in agy: %+v", ev, flat[0])
		}
	}
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		var wrapped []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(named[ev], &wrapped); err != nil {
			t.Fatalf("%s: %v", ev, err)
		}
		if len(wrapped) != 1 || wrapped[0].Matcher != "*" || len(wrapped[0].Hooks) != 1 {
			t.Fatalf("%s = %+v, want one matcher \"*\" with one hook", ev, wrapped)
		}
		if wrapped[0].Hooks[0].Command != c.Bin+" hook agy "+ev {
			t.Errorf("%s command = %q", ev, wrapped[0].Hooks[0].Command)
		}
	}
	if _, bad := named["SessionStart"]; bad {
		t.Error("agy has no SessionStart hook (§11.1)")
	}
	if strings.Contains(string(install.AgyHooks(c)), "PLUGIN_ROOT") {
		t.Error("PLUGIN_ROOT must never appear in a generated command (§21.4)")
	}
}

func TestWriteAgyRegistersTheMCPServerAndKeepsOtherHookNames(t *testing.T) {
	c := fakeHome(t)
	p := c.Gemini("config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// The file merges with plugin hooks.json files, so another name must survive.
	if err := os.WriteFile(p, []byte(`{"ponytail":{"Stop":[{"type":"command","command":"/usr/bin/true"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	add := c.Bin + " mcp"
	f := &execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + add: {Out: ""},
	}}
	if _, err := install.WriteAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if !strings.Contains(string(body), "ponytail") {
		t.Errorf("another plugin's hook name was dropped:\n%s", body)
	}
	if !strings.Contains(string(body), c.Bin+" hook agy Stop") {
		t.Errorf("swarm's Stop hook was not added:\n%s", body)
	}
	var sawAdd bool
	for _, call := range f.Calls() {
		if strings.HasPrefix(call, "agy mcp add ") {
			sawAdd = true
			if call != "agy mcp add --type stdio swarm "+add {
				t.Errorf("argv = %q; §11.1 gives `agy mcp add --type stdio swarm $BIN mcp`", call)
			}
		}
	}
	if !sawAdd {
		t.Errorf("agy mcp add was not run; calls = %v", f.Calls())
	}
	again, err := install.WriteAgy(context.Background(), c, f.Runner())
	if err != nil || len(again) != 0 {
		t.Fatalf("second WriteAgy changed %v, %v; must be a no-op", again, err)
	}
}

// §20 step 8: only the marked span goes. The operator's own heading stays.
func TestRemoveLegacyAgyTakesOnlyTheMarkedGeminiSpan(t *testing.T) {
	c := fakeHome(t)
	seedFile(t, filepath.Join("agy", "GEMINI-v1.md"), c.Gemini("GEMINI.md"))
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	if _, err := install.RemoveLegacyAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(c.Gemini("GEMINI.md"))
	s := string(body)
	if !strings.Contains(s, "### Agent Swarm Coordination and Task Hygiene") {
		t.Error("the operator's own heading was removed; it is not v1-installed")
	}
	for _, keep := range []string{"# Fake operator instructions", "## Something else", "## Tail section", "`swarm_handoff`"} {
		if !strings.Contains(s, keep) {
			t.Errorf("lost %q:\n%s", keep, s)
		}
	}
	for _, gone := range []string{"swarm:start", "swarm:end", "board at http://127.0.0.1:7777"} {
		if strings.Contains(s, gone) {
			t.Errorf("kept %q:\n%s", gone, s)
		}
	}
}

// Verified 2026-09-18: on this machine the v1 agy plugin is a SYMLINK.
func TestRemoveLegacyAgyRemovesTheV1PluginSymlinkWithoutCallingAgy(t *testing.T) {
	c := fakeHome(t)
	target := filepath.Join(c.Home, "app", "current", "plugin")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "plugin.json")
	if err := os.WriteFile(keep, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := c.Gemini("config", "plugins", "swarm")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	if _, err := install.RemoveLegacyAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("the symlink was not removed")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("the symlink target was followed and deleted (§20: removed as links, never followed)")
	}
	for _, call := range f.Calls() {
		if strings.Contains(call, "plugin uninstall") {
			t.Errorf("agy plugin uninstall must not run for a symlink; calls = %v", f.Calls())
		}
	}
}

func TestRemoveLegacyAgyUsesAgyThenFallsBackForARealPluginFolder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		uninstall execx.Result
		wantGone  bool
	}{
		{"agy uninstall succeeds", execx.Result{Out: "removed"}, true},
		{"agy uninstall fails, folder removed directly", execx.Result{Err: os.ErrPermission}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeHome(t)
			dir := c.Gemini("config", "plugins", "swarm")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
			f := &execx.Fake{Responses: map[string]execx.Result{"agy plugin uninstall swarm": tc.uninstall}}
			if _, err := install.RemoveLegacyAgy(context.Background(), c, f.Runner()); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(dir)
			if tc.wantGone && !os.IsNotExist(err) {
				t.Errorf("the plugin folder survived: %v", err)
			}
			var called bool
			for _, call := range f.Calls() {
				called = called || call == "agy plugin uninstall swarm"
			}
			if !called {
				t.Errorf("agy plugin uninstall was not tried; calls = %v", f.Calls())
			}
		})
	}
}

func TestRemoveLegacyAgyDropsTheMCPConfigEntryAndKeepsOthers(t *testing.T) {
	c := fakeHome(t)
	p := c.Gemini("antigravity", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"mcpServers":{"swarm":{"command":"node","args":["/x/swarm-mcp.mjs"]},"other":{"command":"y"}}}`
	if err := os.WriteFile(p, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	if _, err := install.RemoveLegacyAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	var m struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	body, _ := os.ReadFile(p)
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if _, bad := m.MCPServers["swarm"]; bad {
		t.Error("the v1 swarm entry survived")
	}
	if _, ok := m.MCPServers["other"]; !ok {
		t.Error("another MCP server was dropped")
	}
}

func TestRemoveLegacyAgyOnACleanHomeIsANoOp(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	changed, err := install.RemoveLegacyAgy(context.Background(), c, f.Runner())
	if err != nil || len(changed) != 0 {
		t.Fatalf("= %v, %v", changed, err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("nothing should be executed on a clean home; calls = %v", f.Calls())
	}
	if _, err := os.Stat(c.Gemini("GEMINI.md")); !os.IsNotExist(err) {
		t.Error("removal created GEMINI.md")
	}
}

func TestCheckAgyFailsOnANestedPreInvocationAndOnAMissingBinary(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	p := c.Gemini("config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// §21.4 row 1: a nested PreInvocation must fail the check.
	nested := `{"swarm":{"PreInvocation":[{"matcher":"*","hooks":[{"type":"command","command":"` + c.Bin + ` hook agy PreInvocation","timeout":3}]}]}}`
	if err := os.WriteFile(p, []byte(nested), 0o644); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy hooks"); ch.OK {
		t.Error("a nested PreInvocation must fail (§21.4 row 1)")
	}
	// PLUGIN_ROOT must fail too.
	if err := os.WriteFile(p, []byte(`{"swarm":{"PreInvocation":[{"type":"command","command":"node ${PLUGIN_ROOT}/x.mjs"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy hooks"); ch.OK {
		t.Error("${PLUGIN_ROOT} must fail (§21.4 row 1)")
	}
	// The correct file passes, and then fails once the binary is gone.
	if _, err := install.WriteAgy(context.Background(), c, (&execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {},
	}}).Runner()); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy hooks"); !ch.OK {
		t.Errorf("agy hooks = %+v after WriteAgy", ch)
	}
	if err := os.Remove(c.Bin); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy hooks"); ch.OK {
		t.Error("a missing hook binary must fail (§21.4 row 1)")
	}
}

// A7 decision 3: an old ~/.gemini/antigravity-cli/skills symlink chaining
// into a swarm session's launch folder (the exact shape a spawned agy's own
// first-run migration used to leave behind, pre-PA) must be repaired by an
// explicit `swarm install`: salvage what's there into the new root, then
// repoint the old path at the new root -- agy's own post-migration shape.
// Never delete anything under run/launch, and never clobber an entry that
// already exists at the new root and is user-owned.
func TestWriteAgyRepairsALegacySkillsChainIntoRunLaunch(t *testing.T) {
	c := fakeHome(t)
	launchSkills := filepath.Join(c.Home, "run", "launch", "ses_x", "agy-home", ".gemini", "config", "skills")
	// A name that also exists (user-owned) at the new root already: must
	// survive there untouched, never overwritten from the legacy copy.
	if err := os.MkdirAll(filepath.Join(launchSkills, "custom-user-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launchSkills, "custom-user-skill", "SKILL.md"), []byte("# mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A legacy-only name, no conflict at the new root at all: must actually
	// land there (fix round 1, finding 5 -- the original version of this
	// test only proved survival at the SOURCE, never that salvage lands
	// content at the destination).
	if err := os.MkdirAll(filepath.Join(launchSkills, "legacy-only-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launchSkills, "legacy-only-skill", "SKILL.md"), []byte("# salvage me"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A registered name too, so the test can also prove run/launch survives.
	if err := os.MkdirAll(filepath.Join(launchSkills, "swarm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launchSkills, "swarm", "SKILL.md"), []byte("# old swarm"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldPath := c.Gemini("antigravity-cli", "skills")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(launchSkills, oldPath); err != nil {
		t.Fatal(err)
	}

	// A pre-existing user-owned entry at the NEW root with the same name as a
	// legacy one: repair must never clobber it.
	newRoot := c.Gemini("config", "skills")
	if err := os.MkdirAll(filepath.Join(newRoot, "custom-user-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newRoot, "custom-user-skill", "SKILL.md"), []byte("# theirs, keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := &execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {},
	}}
	if _, err := install.WriteAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(oldPath)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected antigravity-cli/skills to be a symlink after repair: %v", err)
	}
	if target, err := os.Readlink(oldPath); err != nil || target != newRoot {
		t.Errorf("antigravity-cli/skills points at %q, %v, want %q (agy's own post-migration shape)", target, err, newRoot)
	}

	kept, err := os.ReadFile(filepath.Join(newRoot, "custom-user-skill", "SKILL.md"))
	if err != nil || string(kept) != "# theirs, keep me" {
		t.Errorf("a user-owned entry at the new root was overwritten: %q, %v", kept, err)
	}

	if _, err := os.Stat(filepath.Join(launchSkills, "swarm", "SKILL.md")); err != nil {
		t.Errorf("run/launch content was deleted; PA.3 must never delete under run/launch: %v", err)
	}

	salvaged, err := os.ReadFile(filepath.Join(newRoot, "legacy-only-skill", "SKILL.md"))
	if err != nil || string(salvaged) != "# salvage me" {
		t.Errorf("legacy-only entry was not salvaged into the new root: %q, %v", salvaged, err)
	}
}

// Fix round 1, finding 1: a legacy dir holding a symlinked entry (e.g. a
// vendored skill some previous swarm install itself linked in) used to abort
// the whole repair -- copyTree's filepath.WalkDir follows the symlink, then
// tries to os.ReadFile what turns out to be a directory, erroring the entire
// `swarm install` for agy before hooks/MCP/WriteSkills ever run. The fix
// recreates the link itself at the destination instead of walking through it.
func TestWriteAgyRepairSalvagesASymlinkedLegacyEntry(t *testing.T) {
	c := fakeHome(t)
	launchSkills := filepath.Join(c.Home, "run", "launch", "ses_x", "agy-home", ".gemini", "config", "skills")
	if err := os.MkdirAll(launchSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	// The symlink's target: some OTHER real location entirely, outside both
	// the legacy chain and ~/.swarm/skills (skillsHome) -- deliberately not
	// the shared skills copy, so this pins that repair recreates the link
	// as-is (an unrelated, user-owned target) rather than only working by
	// accident for links that happen to already resolve into skillsHome.
	linkTarget := filepath.Join(t.TempDir(), "vendored-thing")
	if err := os.MkdirAll(linkTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linkTarget, "SKILL.md"), []byte("# vendored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkTarget, filepath.Join(launchSkills, "linked")); err != nil {
		t.Fatal(err)
	}

	oldPath := c.Gemini("antigravity-cli", "skills")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(launchSkills, oldPath); err != nil {
		t.Fatal(err)
	}

	f := &execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {},
	}}
	if _, err := install.WriteAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatalf("WriteAgy must not abort on a symlinked legacy entry: %v", err)
	}

	newRoot := c.Gemini("config", "skills")
	linkDst := filepath.Join(newRoot, "linked")
	fi, err := os.Lstat(linkDst)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to be a symlink (recreated, not copied through): %v", linkDst, err)
	}
	if got, err := os.Readlink(linkDst); err != nil || got != linkTarget {
		t.Errorf("linked entry target = %q, %v, want %q", got, err, linkTarget)
	}
}

// A plain, healthy skills root (already at the new location, or nothing
// there yet) must never be treated as a legacy chain.
func TestWriteAgyLeavesAHealthySkillsRootAlone(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {},
	}}
	if _, err := install.WriteAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	oldPath := c.Gemini("antigravity-cli", "skills")
	if _, err := os.Lstat(oldPath); err == nil {
		t.Errorf("no legacy chain existed; repair must not create %s", oldPath)
	}
}

// Fix round 1, finding 5: the old path being a REAL directory (never the
// legacy symlink-into-run/launch shape) must be left untouched by repair.
func TestWriteAgyLeavesARealAntigravityCliSkillsDirAlone(t *testing.T) {
	c := fakeHome(t)
	oldPath := c.Gemini("antigravity-cli", "skills")
	if err := os.MkdirAll(filepath.Join(oldPath, "builtin-thing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldPath, "builtin-thing", "SKILL.md"), []byte("# builtin"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {},
	}}
	if _, err := install.WriteAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(oldPath)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		t.Fatalf("a real antigravity-cli/skills dir must survive untouched: %v, %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(oldPath, "builtin-thing", "SKILL.md")); err != nil {
		t.Errorf("content under the real dir was lost: %v", err)
	}
}

// Fix round 1, finding 5: the old path already pointing straight at the new
// root (the healthy, post-repair or fresh-install shape) is a no-op.
func TestWriteAgyLeavesAnAlreadyHealthySymlinkAlone(t *testing.T) {
	c := fakeHome(t)
	newRoot := c.Gemini("config", "skills")
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := c.Gemini("antigravity-cli", "skills")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newRoot, oldPath); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {},
	}}
	if _, err := install.WriteAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(oldPath)
	if err != nil || target != newRoot {
		t.Errorf("an already-healthy symlink must be left alone: %q, %v", target, err)
	}
}

// Fix round 1, finding 4: a first hop into run/launch that DANGLES further
// down the chain (e.g. a reaped intermediate session) must still be treated
// as the legacy shape -- the fully-resolved check alone missed this, since
// filepath.EvalSymlinks errors on a dangling target and the old code bailed
// out entirely instead of still repointing the (unsalvageable) old path.
func TestWriteAgyRepairsADanglingLegacyChain(t *testing.T) {
	c := fakeHome(t)
	oldPath := c.Gemini("antigravity-cli", "skills")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	danglingTarget := filepath.Join(c.Home, "run", "launch", "ses_reaped", "agy-home", ".gemini", "config", "skills")
	if err := os.Symlink(danglingTarget, oldPath); err != nil { // target never created: dangling
		t.Fatal(err)
	}

	f := &execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {},
	}}
	if _, err := install.WriteAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatalf("WriteAgy must not abort on a dangling legacy chain: %v", err)
	}
	newRoot := c.Gemini("config", "skills")
	target, err := os.Readlink(oldPath)
	if err != nil || target != newRoot {
		t.Errorf("a dangling legacy chain must still be repointed at the new root: %q, %v", target, err)
	}
}

// Fix round 1, finding 4: a first hop into run/launch whose chain resolves
// all the way back to the healthy new root itself (possible once a spawn's
// own agy-home/.gemini/config/skills is itself a symlink to the real
// config/skills, per PA.2's setupEnv) must still be detected and repointed,
// with nothing to copy (the content is already the new root's own).
func TestWriteAgyRepairsAChainThatResolvesBackToTheNewRoot(t *testing.T) {
	c := fakeHome(t)
	newRoot := c.Gemini("config", "skills")
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newRoot, "already-healthy.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A spawn's own agy-home config/skills, itself a symlink to the real new
	// root (exactly what PA.2's setupEnv produces).
	agyHomeConfigSkills := filepath.Join(c.Home, "run", "launch", "ses_x", "agy-home", ".gemini", "config", "skills")
	if err := os.MkdirAll(filepath.Dir(agyHomeConfigSkills), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newRoot, agyHomeConfigSkills); err != nil {
		t.Fatal(err)
	}
	oldPath := c.Gemini("antigravity-cli", "skills")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(agyHomeConfigSkills, oldPath); err != nil {
		t.Fatal(err)
	}

	f := &execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {},
	}}
	if _, err := install.WriteAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(oldPath)
	if err != nil || target != newRoot {
		t.Errorf("must be repointed straight at the new root: %q, %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(newRoot, "already-healthy.txt")); err != nil {
		t.Errorf("the new root's own content must survive: %v", err)
	}
}

// A7 decision 4: doctor warns (does not fail) when the legacy
// ~/.gemini/antigravity-cli/skills path resolves into a swarm session's
// launch folder, since that state can exist before `swarm install` has had a
// chance to repair it.
func TestCheckAgyWarnsWhenSkillsRootIsInsideARunLaunchSession(t *testing.T) {
	c := fakeHome(t)
	launchSkills := filepath.Join(c.Home, "run", "launch", "ses_x", "agy-home", ".gemini", "config", "skills")
	if err := os.MkdirAll(launchSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := c.Gemini("antigravity-cli", "skills")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(launchSkills, oldPath); err != nil {
		t.Fatal(err)
	}

	f := &execx.Fake{Responses: map[string]execx.Result{}}
	ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy skills root")
	if !ch.OK || !strings.Contains(ch.Detail, "swarm session folder") {
		t.Errorf("check = %+v, want OK with a swarm-session-folder warning", ch)
	}

	// Once repointed at the new root (what repair does), no warning.
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(c.Gemini("config", "skills"), oldPath); err != nil {
		t.Fatal(err)
	}
	ch2 := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy skills root")
	if !ch2.OK || strings.Contains(ch2.Detail, "swarm session folder") {
		t.Errorf("check after repointing = %+v", ch2)
	}
}

// Fix round 1, finding 3: a dangling old link whose first hop is still
// inside run/launch (e.g. a reaped intermediate session) must get the same
// warning as a live chain, not the generic "is not inside a session folder"
// (which reads as "this is fine" when it plainly is not).
func TestCheckAgyWarnsOnADanglingLegacyChainIntoRunLaunch(t *testing.T) {
	c := fakeHome(t)
	oldPath := c.Gemini("antigravity-cli", "skills")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(c.Home, "run", "launch", "ses_reaped", "agy-home", ".gemini", "config", "skills")
	if err := os.Symlink(dangling, oldPath); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy skills root")
	if !ch.OK || !strings.Contains(ch.Detail, "swarm session folder") {
		t.Errorf("dangling chain into run/launch = %+v, want the same warning as a live chain", ch)
	}
}

// Fix round 1, finding 3: nothing at all at the old path (never installed,
// or already cleaned up some other way) must not print "is not inside a
// session folder" -- there is nothing to say either way.
func TestCheckAgyReportsNothingWhenTheOldPathDoesNotExist(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy skills root")
	if !ch.OK || strings.Contains(ch.Detail, "is not inside a session folder") {
		t.Errorf("missing old path = %+v, want a neutral OK, not the session-folder phrasing", ch)
	}
}

// findCheck is shared by the four agent test files.
func findCheck(t *testing.T, cs []install.Check, name string) install.Check {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, cs)
	return install.Check{}
}
