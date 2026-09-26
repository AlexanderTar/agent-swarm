package install_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

func claudeMCPFake(bin string) *execx.Fake {
	return &execx.Fake{Responses: map[string]execx.Result{
		"claude mcp remove swarm -s user":                 {Out: "removed"},
		"claude mcp add swarm -s user -- " + bin + " mcp": {Out: "added"},
	}}
}

func TestWriteClaudeInstallsSkillsAndTouchesNoLocalFile(t *testing.T) {
	c := fakeHome(t)
	run := claudeMCPFake(c.Bin).Runner()
	changed, err := install.WriteClaude(context.Background(), c, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != len(install.SkillNames()) {
		t.Fatalf("changed = %v, want one skill file per registered skill", changed)
	}
	for _, name := range install.SkillNames() {
		if _, err := os.Stat(c.Claude("skills", name, "SKILL.md")); err != nil {
			t.Error(err)
		}
	}
	// §11.5: trust lives only in ~/.claude.json, which Swarm never edits directly
	// (the global MCP registration goes through the claude CLI itself, never a
	// hand-written edit to the file).
	if _, err := os.Stat(filepath.Join(c.UserHome, ".claude.json")); !os.IsNotExist(err) {
		t.Error("~/.claude.json must never be created or edited directly by Swarm (§11.5)")
	}
	// §11.1: a spawned session's MCP and hooks are still built per launch, so
	// install writes no local global file for those.
	for _, p := range []string{c.Claude("mcp.json"), c.Claude("settings.json"), c.Claude("hooks.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("install must not write %s; a spawned claude session is configured per launch (§11.1)", p)
		}
	}
	again, err := install.WriteClaude(context.Background(), c, run)
	if err != nil || len(again) != 0 {
		t.Fatalf("second WriteClaude changed %v, %v", again, err)
	}
}

// §11.1, M3: the global swarm MCP server is registered via the CLI (remove-then-add,
// so a stale binary path from an older build is replaced, not left alongside a new one).
func TestWriteClaudeRegistersTheGlobalMCPServer(t *testing.T) {
	c := fakeHome(t)
	f := claudeMCPFake(c.Bin)
	if _, err := install.WriteClaude(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(f.Calls(), "\n")
	for _, want := range []string{
		"claude mcp remove swarm -s user",
		"claude mcp add swarm -s user -- " + c.Bin + " mcp",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing call %q; calls =\n%s", want, calls)
		}
	}
}

// WriteClaude must replace a leftover v1 symlink with its own (A1): the two
// occupy the same path, and a stale v1 target must never be mistaken for v2's
// own link into ~/.swarm/skills. Intentional behavior change from pre-A1: v2
// used to land a real directory here; now Claude is symlink-mode (unit 1.2),
// so the replacement is v2's own symlink, not a directory.
func TestWriteClaudeRemovesALeftoverV1LinkBeforeWriting(t *testing.T) {
	c := fakeHome(t)
	target := filepath.Join(c.Home, "app", "current", "plugin", "skills")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(target, "canary")
	if err := os.WriteFile(canary, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := c.Claude("skills", "swarm")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := install.WriteClaude(context.Background(), c, claudeMCPFake(c.Bin).Runner()); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("skills/swarm is not a symlink; v2's own link (A1) was not written")
	}
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(c.Home, "skills", "swarm"); got != want {
		t.Errorf("skills/swarm -> %s, want %s (still pointing at the v1 target, or somewhere else)", got, want)
	}
	if _, err := os.Stat(filepath.Join(link, "SKILL.md")); err != nil {
		t.Error(err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "canary" {
		t.Errorf("v1 target dir = %v, want only the canary file untouched", entries)
	}
	again, err := install.RemoveLegacyClaude(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("RemoveLegacyClaude changed %v after WriteClaude, want none left to remove", again)
	}
}

// §20: the v1 link is removed as a link. Cursor reads this folder too, so a stale
// swarm-status skill here would still be loaded.
func TestRemoveLegacyClaudeRemovesTheV1SkillLinkAsALink(t *testing.T) {
	c := fakeHome(t)
	target := filepath.Join(c.Home, "app", "current", "plugin")
	if err := os.MkdirAll(filepath.Join(target, "skills", "swarm-status"), 0o755); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(target, "skills", "swarm-status", "SKILL.md")
	if err := os.WriteFile(canary, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := c.Claude("skills", "swarm")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	changed, err := install.RemoveLegacyClaude(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != link {
		t.Fatalf("changed = %v, want [%s]", changed, link)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("the link survived")
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatal("the symlink target was followed and deleted (§20)")
	}
}

// After WriteClaude the real skill directory exists; removal must not delete it.
func TestRemoveLegacyClaudeLeavesTheNewSkillDirectoryAlone(t *testing.T) {
	c := fakeHome(t)
	if _, err := install.WriteClaude(context.Background(), c, claudeMCPFake(c.Bin).Runner()); err != nil {
		t.Fatal(err)
	}
	changed, err := install.RemoveLegacyClaude(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed = %v; a real skills/swarm folder written by v2 is not legacy", changed)
	}
	if _, err := os.Stat(c.Claude("skills", "swarm", "SKILL.md")); err != nil {
		t.Fatal("the v2 skill was deleted")
	}
}

// D3 (dialog-needs-you spec): a missing or unwritable ~/.claude.json gets a
// warning line, but install never fails over it.
func TestInstallWarnsWhenClaudeJSONIsMissingOrNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("every file looks writable as root")
	}
	c := fakeHome(t)
	run := (&execx.Fake{Responses: map[string]execx.Result{
		"claude --version": {Out: "2.1.283 (Claude Code)\n"},
	}}).Runner()
	// A parent directory that cannot be created in (unwritable UserHome)
	// means ~/.claude.json can never be created either.
	if err := os.Chmod(c.UserHome, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(c.UserHome, 0o755)
	lines := install.CheckAndPruneClaudeTrust(context.Background(), c, run, nil)
	if len(lines) == 0 || !strings.Contains(lines[0], "missing or not writable") {
		t.Fatalf("lines = %v, want a missing/not-writable warning", lines)
	}
}

// Review round 2, finding 3: a plain-missing ~/.claude.json (writable
// parent, file just isn't there -- no chmod trickery) must warn on install
// and FAIL on doctor too, not only an unwritable parent. Pre-trust no
// longer creates the file (see the adapter's
// TestClaudePreTrustSkipsAMissingClaudeJSON), so claudeJSONWritable can no
// longer treat "doesn't exist yet, will be created on demand" as writable.
func TestInstallWarnsWhenClaudeJSONIsSimplyMissing(t *testing.T) {
	c := fakeHome(t)
	if _, err := os.Stat(install.ClaudeJSONPath(c)); !os.IsNotExist(err) {
		t.Fatalf("test setup: claude.json must not already exist: %v", err)
	}
	run := (&execx.Fake{Responses: map[string]execx.Result{
		"claude --version": {Out: "2.1.283 (Claude Code)\n"},
	}}).Runner()
	lines := install.CheckAndPruneClaudeTrust(context.Background(), c, run, nil)
	if len(lines) == 0 || !strings.Contains(lines[0], "missing or not writable") {
		t.Fatalf("lines = %v, want a missing/not-writable warning", lines)
	}
	ch := install.CheckClaudeTrust(context.Background(), c, run, nil)
	if ch.Name != "Claude trust" || ch.OK {
		t.Fatalf("check = %+v, want a FAIL named \"Claude trust\" for a missing file", ch)
	}
}

// D3: an installed Claude older than the verified version gets a warning
// line with the exact wording, and install still succeeds.
func TestInstallWarnsOnAnUntestedClaudeVersion(t *testing.T) {
	c := fakeHome(t)
	// Review round 2, finding 3: claudeJSONWritable now FAILs a missing
	// file, so seed one -- this test is about the version gate, not the
	// missing-file path (covered by TestInstallWarnsWhenClaudeJSONIsSimplyMissing).
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := (&execx.Fake{Responses: map[string]execx.Result{
		"claude --version": {Out: "2.1.200 (Claude Code)\n"},
	}}).Runner()
	lines := install.CheckAndPruneClaudeTrust(context.Background(), c, run, nil)
	want := "Claude 2.1.200 is older than 2.1.283: the trust-dialog key was verified on 2.1.283+. Continuing, but sessions may still hit the trust dialog."
	if len(lines) == 0 || lines[0] != want {
		t.Fatalf("lines = %v, want [%q]", lines, want)
	}
}

// D3: pruning removes only Swarm-owned entries whose workspace no longer
// exists; a Swarm-owned entry that still exists, and any non-Swarm-owned
// entry (the user's own project, even one pointing nowhere), survive.
func TestInstallPrunesOnlyStaleSwarmOwnedEntries(t *testing.T) {
	c := fakeHome(t)
	staleOwned := filepath.Join(c.Home, "work", "3")     // Swarm-owned, gone
	liveOwned := filepath.Join(c.Home, "work", "4")      // Swarm-owned, still there
	userEntry := filepath.Join(c.UserHome, "my-project") // not Swarm-owned, also gone
	if err := os.MkdirAll(liveOwned, 0o700); err != nil {
		t.Fatal(err)
	}
	seed := fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true},%q:{"hasTrustDialogAccepted":true},%q:{"hasTrustDialogAccepted":true}}}`,
		staleOwned, liveOwned, userEntry)
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := install.PruneStaleClaudeTrustEntries(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	b, _ := os.ReadFile(install.ClaudeJSONPath(c))
	var doc struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Projects[staleOwned]; ok {
		t.Error("the stale Swarm-owned entry must be removed")
	}
	if _, ok := doc.Projects[liveOwned]; !ok {
		t.Error("a Swarm-owned entry whose workspace still exists must survive")
	}
	if _, ok := doc.Projects[userEntry]; !ok {
		t.Error("a non-Swarm-owned entry must never be touched, even if it points nowhere")
	}
}

// Review round 2, finding 2: a live Claude session (or anything else)
// holding the mkdir lock around ~/.claude.json means the prune must skip
// its write rather than racing it -- the same best-effort skip the
// adapter's own trustClaudeWorkspace/ForgetFolder use, now shared via
// WithClaudeConfigLock. Previously PruneStaleClaudeTrustEntries took no
// lock at all and would have clobbered a concurrent write.
func TestPruneSkipsTheWriteWhileClaudeConfigLockIsHeld(t *testing.T) {
	c := fakeHome(t)
	staleOwned := filepath.Join(c.Home, "work", "3") // Swarm-owned, gone
	seed := fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, staleOwned)
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	lock := install.ClaudeJSONPath(c) + ".lock"
	if err := os.Mkdir(lock, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lock)

	// Review round 3, item 3: busy is ErrClaudeConfigBusy now, not a
	// silent nil, so the caller can say the prune was skipped.
	n, err := install.PruneStaleClaudeTrustEntries(c, nil)
	if !errors.Is(err, install.ErrClaudeConfigBusy) {
		t.Fatalf("err = %v, want ErrClaudeConfigBusy", err)
	}
	if n != 0 {
		t.Fatalf("pruned %d while the lock was held, want 0 (best-effort skip)", n)
	}
	b, _ := os.ReadFile(install.ClaudeJSONPath(c))
	if string(b) != seed {
		t.Fatalf("file was rewritten while the lock was held: %s", b)
	}
}

// D4 (dialog-needs-you spec): doctor's "Claude trust" check FAILs when
// ~/.claude.json isn't writable.
func TestDoctorFailsWhenClaudeJSONNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("every file looks writable as root")
	}
	c := fakeHome(t)
	run := (&execx.Fake{Responses: map[string]execx.Result{
		"claude --version": {Out: "2.1.283 (Claude Code)\n"},
	}}).Runner()
	if err := os.Chmod(c.UserHome, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(c.UserHome, 0o755)
	ch := install.CheckClaudeTrust(context.Background(), c, run, nil)
	if ch.Name != "Claude trust" || ch.OK {
		t.Fatalf("check = %+v, want a FAIL named \"Claude trust\"", ch)
	}
}

// D4: FAILs on a Claude version older than the one the trust key was
// verified on, or when the version can't be determined at all.
func TestDoctorFailsOnAnUntestedClaudeVersion(t *testing.T) {
	c := fakeHome(t)
	old := (&execx.Fake{Responses: map[string]execx.Result{
		"claude --version": {Out: "2.1.200 (Claude Code)\n"},
	}}).Runner()
	if ch := install.CheckClaudeTrust(context.Background(), c, old, nil); ch.OK {
		t.Fatalf("check = %+v, want FAIL on an old version", ch)
	}
	none := (&execx.Fake{Responses: map[string]execx.Result{}}).Runner()
	if ch := install.CheckClaudeTrust(context.Background(), c, none, nil); ch.OK {
		t.Fatalf("check = %+v, want FAIL when the version can't be read", ch)
	}
}

// D4: WARNs (OK=true, with a count) on stale Swarm-owned entries; doctor
// only ever reports, it never prunes (install does that, D3).
func TestDoctorWarnsOnStaleSwarmOwnedEntriesWithCount(t *testing.T) {
	c := fakeHome(t)
	run := (&execx.Fake{Responses: map[string]execx.Result{
		"claude --version": {Out: "2.1.283 (Claude Code)\n"},
	}}).Runner()
	staleOwned := filepath.Join(c.Home, "work", "3")
	seed := fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, staleOwned)
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	ch := install.CheckClaudeTrust(context.Background(), c, run, nil)
	if !ch.OK || !strings.Contains(ch.Detail, "1 stale") {
		t.Fatalf("check = %+v, want a WARN naming 1 stale entry", ch)
	}
	// doctor must never have pruned it
	b, _ := os.ReadFile(install.ClaudeJSONPath(c))
	if !strings.Contains(string(b), staleOwned) {
		t.Fatal("doctor must never prune; the stale entry must still be there")
	}
}

// D4: PASSes with a clean, up-to-date, writable state.
func TestDoctorPassesWithCleanState(t *testing.T) {
	c := fakeHome(t)
	// Review round 2, finding 3: claudeJSONWritable now FAILs a missing
	// file; seed one so this test exercises the clean-state PASS path.
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := (&execx.Fake{Responses: map[string]execx.Result{
		"claude --version": {Out: "2.1.283 (Claude Code)\n"},
	}}).Runner()
	ch := install.CheckClaudeTrust(context.Background(), c, run, nil)
	if !ch.OK || !strings.Contains(ch.Detail, "2.1.283") {
		t.Fatalf("check = %+v, want a PASS naming the version", ch)
	}
}

func TestCheckClaudeReportsTheSkillsAndTheLeftoverLink(t *testing.T) {
	c := fakeHome(t)
	run := (&execx.Fake{Responses: map[string]execx.Result{}}).Runner()
	if ch := findCheck(t, install.CheckClaude(context.Background(), c, run, nil), "Claude skills"); ch.OK {
		t.Error("Claude skills must fail before install")
	}
	if _, err := install.WriteClaude(context.Background(), c, claudeMCPFake(c.Bin).Runner()); err != nil {
		t.Fatal(err)
	}
	ch := findCheck(t, install.CheckClaude(context.Background(), c, run, nil), "Claude skills")
	if !ch.OK || !strings.Contains(ch.Detail, "skills") {
		t.Errorf("Claude skills = %+v", ch)
	}
}

// Review round 3, item 1 (data loss): the install-time prune shares the
// adapter's refusal -- a 0-byte, whitespace-only, `null` or malformed
// ~/.claude.json is never rewritten, and the refusal is an error the caller
// can print, not a silent (0, nil).
func TestPruneNeverRewritesAnEmptyNullOrMalformedClaudeJSON(t *testing.T) {
	for _, seed := range []string{"", " \n\t ", "null", "{bad"} {
		c := fakeHome(t)
		if err := os.WriteFile(install.ClaudeJSONPath(c), []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
		n, err := install.PruneStaleClaudeTrustEntries(c, nil)
		if err == nil || n != 0 {
			t.Errorf("seed %q: prune = (%d, %v), want (0, an error)", seed, n, err)
		}
		if b, _ := os.ReadFile(install.ClaudeJSONPath(c)); string(b) != seed {
			t.Errorf("seed %q: prune rewrote the file to %q", seed, b)
		}
	}
}

// Review round 3, item 2: a symlinked ~/.claude.json stays a symlink after
// the prune; the target is what gets rewritten.
func TestPruneWritesThroughASymlinkedClaudeJSON(t *testing.T) {
	c := fakeHome(t)
	staleOwned := filepath.Join(c.Home, "work", "3")
	target := filepath.Join(t.TempDir(), "claude.json")
	seed := fmt.Sprintf(`{"oauthAccount":{"id":"x"},"projects":{%q:{"hasTrustDialogAccepted":true}}}`, staleOwned)
	if err := os.WriteFile(target, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, install.ClaudeJSONPath(c)); err != nil {
		t.Fatal(err)
	}
	n, err := install.PruneStaleClaudeTrustEntries(c, nil)
	if err != nil || n != 1 {
		t.Fatalf("prune = (%d, %v), want (1, nil)", n, err)
	}
	if fi, _ := os.Lstat(install.ClaudeJSONPath(c)); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("~/.claude.json was replaced by a regular file")
	}
	b, _ := os.ReadFile(target)
	if strings.Contains(string(b), staleOwned) || !strings.Contains(string(b), "oauthAccount") {
		t.Fatalf("target = %s, want the stale entry gone and oauthAccount kept", b)
	}
}

// Review round 3, item 5: CheckAndPruneClaudeTrust prints a busy skip and a
// prune error instead of dropping them.
func TestInstallPrintsAPruneBusySkipAndAPruneError(t *testing.T) {
	run := (&execx.Fake{Responses: map[string]execx.Result{
		"claude --version": {Out: "2.1.283 (Claude Code)\n"},
	}}).Runner()

	c := fakeHome(t)
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(install.ClaudeJSONPath(c)+".lock", 0o755); err != nil {
		t.Fatal(err)
	}
	lines := install.CheckAndPruneClaudeTrust(context.Background(), c, run, nil)
	want := "Skipped pruning ~/.claude.json: another process holds its lock. Run swarm install again."
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("busy: lines = %q, want [%q]", lines, want)
	}

	c = fakeHome(t)
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines = install.CheckAndPruneClaudeTrust(context.Background(), c, run, nil)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "Couldn't prune ~/.claude.json: claude.json does not parse") {
		t.Errorf("parse error: lines = %q, want one \"Couldn't prune\" line", lines)
	}
}

// Review round 3, item 5: only ENOENT means stale. A Swarm-owned entry
// whose stat fails any other way (EACCES here) is kept by the prune and not
// counted by doctor.
func TestPruneTreatsOnlyENOENTAsStale(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can stat through a 000 dir")
	}
	c := fakeHome(t)
	locked := filepath.Join(c.Home, "work", "locked")
	entry := filepath.Join(locked, "3")
	if err := os.MkdirAll(entry, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o700)
	seed := fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, entry)
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := install.PruneStaleClaudeTrustEntries(c, nil); err != nil || n != 0 {
		t.Fatalf("prune = (%d, %v), want (0, nil) for an EACCES stat", n, err)
	}
	run := (&execx.Fake{Responses: map[string]execx.Result{
		"claude --version": {Out: "2.1.283 (Claude Code)\n"},
	}}).Runner()
	if ch := install.CheckClaudeTrust(context.Background(), c, run, nil); strings.Contains(ch.Detail, "stale") {
		t.Fatalf("doctor = %+v, want no stale count for an EACCES stat", ch)
	}
}

func trustRun() execx.Runner {
	return (&execx.Fake{Responses: map[string]execx.Result{
		"claude --version": {Out: "2.1.283 (Claude Code)\n"},
	}}).Runner()
}

func sessionsStub(s install.ClaudeSessions, err error) install.ClaudeSessionsFunc {
	return func(context.Context) (install.ClaudeSessions, error) { return s, err }
}

func mkdirs(t *testing.T, ps ...string) {
	t.Helper()
	for _, p := range ps {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// D3 (locked wording, implemented review round 3): swarm install also
// prunes the trust entry of every finished session's work dir the daemon
// reports, even though the dir still exists on disk -- Swarm-owned paths
// only. A live session's entry, and the user's own project, survive.
func TestInstallPrunesFinishedSessionsTrustEntries(t *testing.T) {
	c := fakeHome(t)
	finished := filepath.Join(c.Home, "work", "3")
	live := filepath.Join(c.Home, "work", "4")
	user := filepath.Join(c.UserHome, "my-project")
	mkdirs(t, finished, live, user)
	seed := fmt.Sprintf(`{"oauthAccount":{"id":"x"},"projects":{%q:{"hasTrustDialogAccepted":true},%q:{"hasTrustDialogAccepted":true},%q:{"hasTrustDialogAccepted":true}}}`,
		finished, live, user)
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := install.CheckAndPruneClaudeTrust(context.Background(), c, trustRun(), sessionsStub(install.ClaudeSessions{
		Live:     []install.LiveClaudeSession{{Agent: "coder-4", Cwd: live}},
		Finished: []string{finished, user},
	}, nil))
	if want := "Removed 1 stale Swarm-owned entries from ~/.claude.json."; len(lines) != 1 || lines[0] != want {
		t.Fatalf("lines = %q, want [%q]", lines, want)
	}
	b, _ := os.ReadFile(install.ClaudeJSONPath(c))
	var doc struct {
		OAuth    json.RawMessage            `json:"oauthAccount"`
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Projects[finished]; ok {
		t.Error("a finished session's Swarm-owned entry must be pruned")
	}
	if _, ok := doc.Projects[live]; !ok {
		t.Error("a live session's entry must survive")
	}
	if _, ok := doc.Projects[user]; !ok {
		t.Error("a non-Swarm-owned entry must never be pruned, even if the daemon reports it finished")
	}
	if len(doc.OAuth) == 0 {
		t.Error("oauthAccount must survive")
	}
}

// D3/D4: with the daemon offline the session-based part is skipped with a
// one-line note, never an error or a failed check; everything else still runs.
func TestClaudeTrustNotesAnOfflineDaemon(t *testing.T) {
	c := fakeHome(t)
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	offline := sessionsStub(install.ClaudeSessions{}, errors.New("connection refused"))
	lines := install.CheckAndPruneClaudeTrust(context.Background(), c, trustRun(), offline)
	want := "Skipped pruning finished sessions' Claude trust entries: the daemon isn't running."
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("install lines = %q, want [%q]", lines, want)
	}
	ch := install.CheckClaudeTrust(context.Background(), c, trustRun(), offline)
	if !ch.OK || !strings.HasSuffix(ch.Detail, " Live-session check skipped: the daemon isn't running.") {
		t.Fatalf("doctor = %+v, want a PASS with the skip note", ch)
	}
}

// D4 (locked wording, implemented review round 3): doctor WARNs, naming the
// agent, when a live Claude session's workspace has no projects entry under
// either its literal or realpath key. doctor still never writes.
func TestDoctorWarnsWhenALiveClaudeSessionHasNoTrustEntry(t *testing.T) {
	c := fakeHome(t)
	trusted := filepath.Join(c.Home, "work", "1")
	missing := filepath.Join(c.Home, "work", "2")
	mkdirs(t, trusted, missing)
	realTrusted, _ := filepath.EvalSymlinks(trusted) // D1 writes both keys; either counts
	seed := fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, realTrusted)
	if err := os.WriteFile(install.ClaudeJSONPath(c), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions := sessionsStub(install.ClaudeSessions{Live: []install.LiveClaudeSession{
		{Agent: "coder-1", Cwd: trusted}, {Agent: "coder-2", Cwd: missing},
	}}, nil)
	ch := install.CheckClaudeTrust(context.Background(), c, trustRun(), sessions)
	want := "coder-2 is running but its workspace isn't trusted in ~/.claude.json yet."
	if !ch.OK || ch.Detail != want {
		t.Fatalf("doctor = %+v, want WARN %q", ch, want)
	}
	if b, _ := os.ReadFile(install.ClaudeJSONPath(c)); string(b) != seed {
		t.Fatal("doctor must never write")
	}
}
