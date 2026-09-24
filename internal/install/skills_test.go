package install_test

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/install"
	"gopkg.in/yaml.v3"
)

// parseFrontmatter extracts and decodes the YAML between the first pair of
// "---" fence lines with a real YAML parser (yaml.v3, already a direct
// dependency: see internal/kb/doc.go). It is a test-local, from-scratch parse
// (not the production one), so a registry bug in Skills() can't hide behind a
// shared helper. Only string-valued top-level fields (name, description, ...)
// are surfaced; a skill's non-string fields (metadata, argument-hint lists,
// ...) aren't read by any test here.
//
// Review round 1, Important: the prior line-based version read only the first
// line after "key:", so a multi-line YAML scalar (a folded `description: >`
// or a plain scalar starting on the next line, both used upstream) decoded to
// "" or ">" instead of the real value -- and ">" being non-empty hid that from
// TestSkillsRegistryMatchesTree's "non-empty description" check. That bug
// nearly cost 4 vendored skills their byte-verbatim upstream frontmatter.
func parseFrontmatter(t *testing.T, body []byte) map[string]string {
	t.Helper()
	rest, ok := strings.CutPrefix(string(body), "---\n")
	if !ok {
		t.Fatalf("no frontmatter fence: %.40q", body)
	}
	front, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		// The fence may be the very last thing in the file (no body after).
		var ok2 bool
		front, ok2 = strings.CutSuffix(rest, "\n---")
		if !ok2 {
			t.Fatalf("frontmatter fence never closed: %.40q", body)
		}
	}
	var decoded map[string]any
	if err := yaml.Unmarshal([]byte(front), &decoded); err != nil {
		t.Fatalf("invalid frontmatter YAML: %v\n%s", err, front)
	}
	out := map[string]string{}
	for k, v := range decoded {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// P3 unit 3.2: swarm is rewritten as protocol only. Rule 11 (TDD) moved out
// to swarm-coder/swarm-debugger; swarm gained the workflow-step rule and
// `Workflow` in its disabled-tool list instead. This is the one existing
// expectation the P3 brief names as changing intentionally (unit 3.2), so the
// old "Reviewers: check that the tests..." assertion moves to the
// swarm-reviewer row of TestRoleSkillsReferenceTheirSkills below rather than
// being dropped.
func TestSkillBodyCarriesTheSpecFrontmatterAndLastRule(t *testing.T) {
	agent := string(install.SkillBody("swarm"))
	for _, want := range []string{
		"name: swarm\n",
		"description: Rules for any agent spawned by Agent Swarm (SWARM_SESSION is set).",
		"# Working as a Swarm agent",
		"Never add `Co-Authored-By`, \"Generated with\", session links, emoji signatures or agent names",
		"## Workflow",
		"`Workflow`",
	} {
		if !strings.Contains(agent, want) {
			t.Errorf("skills/swarm/SKILL.md is missing %q", want)
		}
	}
	orch := string(install.SkillBody("swarm-orchestrator"))
	for _, want := range []string{
		"name: swarm-orchestrator\n",
		"# Orchestrating with Swarm",
		"superpowers:brainstorming",
		"superpowers:systematic-debugging",
		"Then write `completed`, remove your worktrees with `swarm_worktree` `op: \"remove\"`, and stop.",
	} {
		if !strings.Contains(orch, want) {
			t.Errorf("skills/swarm-orchestrator/SKILL.md is missing %q", want)
		}
	}
	// The fence lines must not have been copied in with the body.
	for name, body := range map[string]string{"swarm": agent, "swarm-orchestrator": orch} {
		if strings.Contains(body, "~~~") {
			t.Errorf("%s still carries a spec fence line", name)
		}
		if !strings.HasPrefix(body, "---\n") {
			t.Errorf("%s does not start with frontmatter: %.20q", name, body)
		}
	}
}

// A1: the registry is derived from the embedded tree, not a hand-kept list. Every
// SKILL.md's frontmatter name must match its directory, every description must be
// non-empty, and the canonical trio must be present.
func TestSkillsRegistryMatchesTree(t *testing.T) {
	sk, err := install.Skills()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sk {
		if names[s.Name] {
			t.Fatalf("duplicate skill name %q", s.Name)
		}
		names[s.Name] = true
		body, err := fs.ReadFile(install.SkillFS(), path.Join(s.Dir, "SKILL.md"))
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		fm := parseFrontmatter(t, body)
		if fm["name"] != s.Name || path.Base(s.Dir) != s.Name {
			t.Errorf("%s: frontmatter/dir mismatch (frontmatter name %q, dir %q)", s.Dir, fm["name"], s.Dir)
		}
		if strings.TrimSpace(fm["description"]) == "" {
			t.Errorf("%s: empty description", s.Name)
		}
	}
	for _, want := range []string{"swarm", "swarm-orchestrator", "swarm-batching"} {
		if !names[want] {
			t.Errorf("missing %s", want)
		}
	}
}

// A1: SyncSkills extracts nested files (not just SKILL.md), marks each skill dir
// managed, and prunes files that are no longer part of the embed.
//
// Intentional plan erratum fix (review round 1): SyncSkills's `home` argument
// is the swarm home (Config.Home, e.g. ~/.swarm), not the user's home -- it
// writes to <home>/skills, not <home>/.swarm/skills. The plan's own snippet
// had it the other way; WriteSkills/CheckSkills/the claude adapter always
// derived skillsHome from Config.Home directly, so the mismatch broke a
// custom --home/SWARM_HOME (make dev, e2e): SyncSkills wrote under the real
// user's home while everything else looked under the custom one.
func TestSyncSkillsWritesNestedFilesAndPrunes(t *testing.T) {
	home := t.TempDir()
	if _, err := install.SyncSkills(home); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "skills", "swarm", install.ManagedMarker)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(home, "skills", "swarm", "SKILL.md"))
	if err != nil || !strings.HasPrefix(string(body), "---\n") {
		t.Fatalf("SKILL.md: body=%.20q err=%v", body, err)
	}
	stale := filepath.Join(home, "skills", "swarm", "stale.md")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := install.SyncSkills(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file survived sync: %v", err)
	}
	// Idempotent: a second sync with nothing stale changes nothing.
	changed, err := install.SyncSkills(home)
	if err != nil || len(changed) != 0 {
		t.Errorf("second sync changed %v, %v; want a no-op", changed, err)
	}
}

// Review round 1, Minor 5 (moved here by review round 2, I1): content-equal
// is not the same as fully in sync -- a synced skill's file mode can drift
// (a tool that doesn't preserve it, a manual edit) even though the bytes
// still match, and only the skills-sync path self-heals that; every other
// WriteIfChanged caller must not (see
// TestWriteIfChangedLeavesAContentEqualFileAtItsOwnModeEvenWhenTheModeArgDiffers
// in files_test.go).
func TestSyncSkillsFixesADriftedFileMode(t *testing.T) {
	home := t.TempDir()
	if _, err := install.SyncSkills(home); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "skills", "swarm", "SKILL.md")
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := install.SyncSkills(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range changed {
		if c == p {
			found = true
		}
	}
	if !found {
		t.Errorf("the mode-only fix was not reported in changed: %v", changed)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

// Review round 1, Major 1: a custom (or relative) --home must not split
// SyncSkills' writes from where WriteSkills/CheckSkills/the claude adapter
// look for them, and a relative home must never produce a relative (and
// therefore cwd-fragile) symlink target.
func TestSkillsHomeIsAbsoluteAndUnderHomeNotUserHome(t *testing.T) {
	userHome := t.TempDir()
	swarmHome := filepath.Join(t.TempDir(), "custom-swarm-home") // unrelated to userHome
	c := install.Config{UserHome: userHome, Home: swarmHome}
	if _, _, err := install.WriteSkills(c, install.KindClaude); err != nil {
		t.Fatal(err)
	}
	// Nothing was written under the (unrelated) user home.
	if _, err := os.Stat(filepath.Join(userHome, ".swarm")); !os.IsNotExist(err) {
		t.Fatalf("WriteSkills wrote under UserHome instead of Home: %v", err)
	}
	link := filepath.Join(c.SkillsDir(install.KindClaude), "swarm")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(swarmHome, "skills", "swarm")
	if target != want {
		t.Errorf("symlink target = %s, want %s", target, want)
	}
	if !filepath.IsAbs(target) {
		t.Errorf("symlink target %q is not absolute", target)
	}

	// A relative Home must still produce an absolute symlink target: a
	// symlink is read by another process (an agent CLI), possibly from a
	// different cwd, so a relative target would resolve to the wrong place.
	relHome, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := install.SkillsHome("relative-swarm-home")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("SkillsHome(%q) = %q, want an absolute path", "relative-swarm-home", got)
	}
	if want := filepath.Join(relHome, "relative-swarm-home", "skills"); got != want {
		t.Errorf("SkillsHome(%q) = %q, want %q", "relative-swarm-home", got, want)
	}
}

// A1: the embedded mirror under internal/install/skills must match the canonical
// skills/ tree byte-for-byte (make skills-sync keeps them in sync).
func TestEmbeddedMirrorMatchesCanonicalTree(t *testing.T) {
	canonicalRoot := filepath.Join("..", "..", "skills")
	var canonical []string
	if err := filepath.WalkDir(canonicalRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(canonicalRoot, p)
		if err != nil {
			return err
		}
		canonical = append(canonical, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var embedded []string
	if err := fs.WalkDir(install.SkillFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		embedded = append(embedded, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	sort.Strings(canonical)
	sort.Strings(embedded)
	if len(canonical) != len(embedded) {
		t.Fatalf("file count differs: canonical %d, embedded %d\ncanonical: %v\nembedded: %v",
			len(canonical), len(embedded), canonical, embedded)
	}
	for i, rel := range canonical {
		if embedded[i] != rel {
			t.Fatalf("file set differs at %d: canonical %q, embedded %q", i, rel, embedded[i])
		}
		want, err := os.ReadFile(filepath.Join(canonicalRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := fs.ReadFile(install.SkillFS(), rel)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s has drifted from the canonical tree; run make skills-sync", rel)
		}
	}
}

func TestSkillBodyPanicsOnAnUnknownName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic: a typo'd skill name must fail at the call site, not install an empty file")
		}
	}()
	install.SkillBody("swarm-nope")
}

// §18: both skills are installed for every agent, at the four listed roots.
func TestWriteSkillsInstallsBothForEveryAgentAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	for _, k := range install.Kinds {
		changed, skipped, err := install.WriteSkills(c, k)
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if len(skipped) != 0 {
			t.Errorf("%s skipped %v on a clean home", k, skipped)
		}
		if len(changed) != len(install.SkillNames()) {
			t.Errorf("%s changed %v, want one entry per registered skill", k, changed)
		}
		for _, name := range []string{"swarm", "swarm-orchestrator"} {
			p := filepath.Join(c.SkillsDir(k), name, "SKILL.md")
			body, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			if !strings.HasPrefix(string(body), "---\n") {
				t.Errorf("%s: %.20q", p, body)
			}
		}
		again, _, err := install.WriteSkills(c, k)
		if err != nil || len(again) != 0 {
			t.Errorf("%s second call changed %v, %v; must be a no-op", k, again, err)
		}
	}
}

func TestWriteSkillsStaysInsideTheGivenHome(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	if _, _, err := install.WriteSkills(c, install.KindClaude); err != nil {
		t.Fatal(err)
	}
	// S-5: nothing may land outside the fake home.
	err := filepath.WalkDir(home, func(p string, _ os.DirEntry, err error) error { return err })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "swarm", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

// A1's symlink fallback: a CLI that is confirmed to follow a symlinked skill
// directory (Claude Code, checked 2026-09-24) gets a real symlink into
// ~/.swarm/skills/<name>, one on-disk copy shared by every kind.
func TestWriteSkillsSymlinksEveryManagedSkill(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	if _, _, err := install.WriteSkills(c, install.KindClaude); err != nil {
		t.Fatal(err)
	}
	skillsHome := filepath.Join(home, ".swarm", "skills")
	for _, name := range install.SkillNames() {
		dst := filepath.Join(c.SkillsDir(install.KindClaude), name)
		fi, err := os.Lstat(dst)
		if err != nil {
			t.Fatalf("%s: %v", dst, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is not a symlink", dst)
		}
		target, err := os.Readlink(dst)
		if err != nil {
			t.Fatal(err)
		}
		if target != filepath.Join(skillsHome, name) {
			t.Errorf("%s -> %s, want %s", dst, target, filepath.Join(skillsHome, name))
		}
	}
}

// Review round 1, Minor 6: a swarm-owned entry whose name is no longer
// registered (a skill that was renamed or retired since this root was last
// installed) must not linger there forever.
func TestLinkSkillsPrunesASwarmOwnedEntryThatIsNoLongerRegistered(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	if _, err := install.SyncSkills(c.Home); err != nil {
		t.Fatal(err)
	}
	skillsHome, err := install.SkillsHome(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	root := c.SkillsDir(install.KindClaude)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A swarm-owned entry for a name the registry no longer has.
	retired := filepath.Join(root, "swarm-retired-skill")
	if err := os.Symlink(filepath.Join(skillsHome, "swarm-retired-skill"), retired); err != nil {
		t.Fatal(err)
	}
	// A user's own same-named leftover must survive regardless.
	usersOwn := filepath.Join(root, "not-a-swarm-skill")
	if err := os.MkdirAll(usersOwn, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := install.LinkSkills(root, skillsHome, install.Symlink); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(retired); !os.IsNotExist(err) {
		t.Errorf("the retired swarm-owned entry survived: %v", err)
	}
	if _, err := os.Stat(usersOwn); err != nil {
		t.Errorf("the user's own unrelated directory was removed: %v", err)
	}
}

// A CLI whose skills root already holds a same-named skill the user made
// themselves (no swarm symlink, no .swarm-managed marker) must be left alone
// and reported, never overwritten.
func TestWriteSkillsSkipsUserOwnedSameName(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	ownDir := filepath.Join(c.SkillsDir(install.KindClaude), "swarm")
	if err := os.MkdirAll(ownDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ownFile := filepath.Join(ownDir, "SKILL.md")
	if err := os.WriteFile(ownFile, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, skipped, err := install.WriteSkills(c, install.KindClaude)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range skipped {
		if s == ownDir {
			found = true
		}
	}
	if !found {
		t.Errorf("skipped = %v, want %s in it", skipped, ownDir)
	}
	for _, ch := range changed {
		if ch == ownDir {
			t.Errorf("changed reports the user-owned dir %s", ownDir)
		}
	}
	body, err := os.ReadFile(ownFile)
	if err != nil || string(body) != "mine\n" {
		t.Errorf("the user's own SKILL.md was touched: %q, %v", body, err)
	}
}

// Review round 1, Minor 9: WriteSkills must tell apart the two kinds of
// dangling (broken) symlink -- one that still resolves inside skillsHome
// (repair it) and one that does not (a foreign link; leave it alone).
func TestWriteSkillsRepairsADanglingSwarmOwnedSymlinkButSkipsADanglingForeignOne(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	skillsHome, err := install.SkillsHome(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	root := c.SkillsDir(install.KindClaude)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	swarmLink := filepath.Join(root, "swarm")
	if err := os.Symlink(filepath.Join(skillsHome, "swarm-was-renamed"), swarmLink); err != nil {
		t.Fatal(err)
	}
	orchLink := filepath.Join(root, "swarm-orchestrator")
	if err := os.Symlink("/nonexistent/somewhere/else", orchLink); err != nil {
		t.Fatal(err)
	}

	changed, skipped, err := install.WriteSkills(c, install.KindClaude)
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.Readlink(swarmLink)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(skillsHome, "swarm"); got != want {
		t.Errorf("dangling swarm-owned link not repaired: %s, want %s", got, want)
	}
	foundChanged := false
	for _, c := range changed {
		if c == swarmLink {
			foundChanged = true
		}
	}
	if !foundChanged {
		t.Errorf("swarm link repair not reported in changed: %v", changed)
	}

	stillDangling, err := os.Readlink(orchLink)
	if err != nil {
		t.Fatal(err)
	}
	if stillDangling != "/nonexistent/somewhere/else" {
		t.Errorf("the foreign dangling link was touched: %s", stillDangling)
	}
	foundSkipped := false
	for _, s := range skipped {
		if s == orchLink {
			foundSkipped = true
		}
	}
	if !foundSkipped {
		t.Errorf("the foreign dangling link was not reported skipped: %v", skipped)
	}
}

// A stale swarm-managed copy (the Copy fallback, or a leftover from before a
// skill's content changed) must be replaced with fresh content, not merged with
// or left alongside the old files.
func TestWriteSkillsReplacesStaleSwarmCopy(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	dst := filepath.Join(c.SkillsDir(install.KindCodex), "swarm")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "leftover.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, install.ManagedMarker), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := install.WriteSkills(c, install.KindCodex); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(install.SkillBody("swarm")) {
		t.Errorf("stale SKILL.md survived: %.20q", body)
	}
	if _, err := os.Stat(filepath.Join(dst, "leftover.md")); !os.IsNotExist(err) {
		t.Errorf("the stale leftover file survived: %v", err)
	}
}

// Review round 2, C1 #3: a marker naming a *different* swarm home is never
// ours, even for an explicit `swarm install` (adopt=true) -- this is the
// direct regression for the mechanism that let a daemon with a dev/test home
// silently recopy a real installation's content, since a content-blind
// marker (round 1's shape) reads as "owned" by any skillsHome that asks.
func TestWriteSkillsLeavesADirAloneWhenItsMarkerNamesADifferentSwarmHome(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	dst := filepath.Join(c.SkillsDir(install.KindCodex), "swarm")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	drifted := []byte("content installed by a different swarm home\n")
	if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), drifted, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, install.ManagedMarker), []byte("/some/other/swarm/skills"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, skipped, err := install.WriteSkills(c, install.KindCodex)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range skipped {
		if s == dst {
			found = true
		}
	}
	if !found {
		t.Errorf("a dir with a foreign marker was not reported skipped: %v", skipped)
	}
	for _, ch := range changed {
		if ch == dst {
			t.Errorf("changed reports the foreign-marker dir %s", dst)
		}
	}
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || string(body) != string(drifted) {
		t.Errorf("the foreign-marker dir's content was touched: %q, %v", body, err)
	}
}

// Review round 2, C1 #3 / I2: an empty (pre-this-fix) marker is owned by an
// explicit `swarm install` (adopt=true) but NOT by the daemon's own automatic
// refresh (adopt=false, RefreshSkillLinks/SyncAndRefreshSkills) -- exactly
// the asymmetry TestWriteSkillsReplacesStaleSwarmCopy above already locks in
// for the adopt=true side; this locks in the adopt=false side too, and
// through the real SyncAndRefreshSkills entry point (not linkSkills
// directly), so the Home-vs-UserHome gate and the marker check are both
// exercised together.
func TestSyncAndRefreshSkillsLeavesAnEmptyMarkerDirAloneButWriteSkillsAdoptsIt(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	dst := filepath.Join(c.SkillsDir(install.KindCodex), "swarm")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	drifted := []byte("stale content, pre-this-fix empty marker\n")
	if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), drifted, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, install.ManagedMarker), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := install.SyncAndRefreshSkills(c); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || string(body) != string(drifted) {
		t.Errorf("SyncAndRefreshSkills touched an empty-marker dir: %q, %v", body, err)
	}

	if _, _, err := install.WriteSkills(c, install.KindCodex); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(install.SkillBody("swarm")) {
		t.Errorf("WriteSkills did not adopt the empty-marker dir: %.20q", body)
	}
}

// Review round 2, item 3: the earlier C1 regression tests all happened to
// pass even without the Home-vs-UserHome gate in SyncAndRefreshSkills,
// because their fixtures' markers never matched the *test's own* skillsHome
// -- the marker-content check alone was enough to protect them. This test
// isolates the gate itself: the seeded entry's marker names exactly the
// skillsHome this call's own (non-canonical) Home would compute, i.e. it
// looks precisely like something *this daemon's own temp home* installed.
// Without the gate, isSwarmOwned's content==skillsHome branch would call it
// owned and RefreshSkillLinks would recopy fresh content over it; the gate
// must stop that regardless, because Home is not the canonical default for
// UserHome.
func TestSyncAndRefreshSkillsGateBlocksRefreshEvenWhenTheMarkerMatchesThisCallsOwnSkillsHome(t *testing.T) {
	userHome := t.TempDir()
	daemonHome := t.TempDir() // deliberately not filepath.Join(userHome, ".swarm")
	skillsHome, err := install.SkillsHome(daemonHome)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(userHome, ".codex", "skills", "swarm")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	drifted := []byte("content that looks exactly like this daemon's own home installed it\n")
	if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), drifted, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, install.ManagedMarker), []byte(skillsHome), 0o644); err != nil {
		t.Fatal(err)
	}

	c := install.Config{UserHome: userHome, Home: daemonHome}
	if _, err := install.SyncAndRefreshSkills(c); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || string(body) != string(drifted) {
		t.Errorf("the gate did not block the refresh: %q, %v", body, err)
	}
}

// readTestdata returns a frozen historical fixture body from testdata/.
// Review round 2, item 1: the pre-A1 adoption tests below used to build their
// fixture from install.SkillBody(name) -- the CURRENT embedded body. That
// only worked because HEAD's own blob happened to be in
// preA1SkillBodyHashes; the next edit to skills/swarm/SKILL.md (P3) would
// have turned these tests red for a reason that has nothing to do with the
// adoption logic they're testing. testdata/pre_a1_*.md are frozen bodies
// (captured from commit 90918bd, "ship the swarm and swarm-orchestrator
// skills for every agent" -- the first commit that shipped them, i.e.
// genuinely pre-A1) whose sha256 is a permanent entry in
// preA1SkillBodyHashes and will never change out from under these tests.
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// Review round 1, Major 2: a pre-A1 install wrote only a bare SKILL.md per
// skill, no .swarm-managed marker (that marker did not exist yet). Without
// recognizing this shape, isSwarmOwned would call it user-owned forever, and
// an operator who installed before A1 would never get the marker, the
// nested-file sync, or any future repair for their own core skills.
func TestWriteSkillsAdoptsAPreA1RealSkillDirectoryInCopyMode(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	dst := filepath.Join(c.SkillsDir(install.KindCodex), "swarm")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), readTestdata(t, "pre_a1_swarm_skill.md"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, skipped, err := install.WriteSkills(c, install.KindCodex)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range skipped {
		if s == dst {
			t.Fatalf("the pre-A1 dir was skipped as user-owned: %v", skipped)
		}
	}
	found := false
	for _, ch := range changed {
		if ch == dst {
			found = true
		}
	}
	if !found {
		t.Errorf("the pre-A1 dir was not adopted: changed = %v", changed)
	}
	if _, err := os.Stat(filepath.Join(dst, install.ManagedMarker)); err != nil {
		t.Errorf("adopted dir is missing its marker: %v", err)
	}
}

// Review round 2, I2: matching frontmatter `name:` was not enough to treat a
// marker-less directory as a pre-A1 swarm install -- a user's own same-named,
// single-file skill (with its own, unrelated body, but a `name: swarm` line)
// must be left alone: not adopted, reported skipped, its content untouched.
// Only a body that byte-matches something swarm actually shipped
// (preA1SkillBodyHashes) is adopted; see
// TestWriteSkillsAdoptsAPreA1RealSkillDirectoryInCopyMode above for that.
func TestWriteSkillsLeavesAMarkerlessSameNameDirAloneWhenItsBodyWasNeverShipped(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	dst := filepath.Join(c.SkillsDir(install.KindCodex), "swarm")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := []byte("---\nname: swarm\ndescription: my own thing\n---\n\nnot swarm's content at all\n")
	if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), mine, 0o644); err != nil {
		t.Fatal(err)
	}

	changed, skipped, err := install.WriteSkills(c, install.KindCodex)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range skipped {
		if s == dst {
			found = true
		}
	}
	if !found {
		t.Errorf("the user's own same-name dir was not reported skipped: %v", skipped)
	}
	for _, ch := range changed {
		if ch == dst {
			t.Errorf("changed reports the user-owned dir %s", dst)
		}
	}
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || string(body) != string(mine) {
		t.Errorf("the user's own SKILL.md was touched: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dst, install.ManagedMarker)); !os.IsNotExist(err) {
		t.Errorf("a marker was written into the user's own dir: %v", err)
	}
}

// Same adoption, Symlink mode (Claude): the pre-A1 real directory is replaced
// with v2's own symlink.
func TestWriteClaudeAdoptsAPreA1RealSkillDirectory(t *testing.T) {
	c := fakeHome(t)
	dst := filepath.Join(c.SkillsDir(install.KindClaude), "swarm-orchestrator")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), readTestdata(t, "pre_a1_swarm_orchestrator_skill.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := install.WriteClaude(context.Background(), c, claudeMCPFake(c.Bin).Runner()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the pre-A1 real directory was not replaced with v2's symlink")
	}
}

// Review round 1, Minor 7: a Copy-mode kind must never follow a leftover
// symlink at dst (this kind used to be Symlink-mode, say) into whatever it
// actually points at -- here, on purpose, a *different* skill's shared
// ~/.swarm/skills directory -- and write through it. dst must become a real
// removed-then-copied directory, and the other skill's shared copy must come
// out untouched.
func TestWriteSkillsRemovesASymlinkBeforeCopyingRatherThanFollowingIt(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	if _, err := install.SyncSkills(c.Home); err != nil {
		t.Fatal(err)
	}
	skillsHome, err := install.SkillsHome(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(c.SkillsDir(install.KindCodex), "swarm")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	wrongTarget := filepath.Join(skillsHome, "swarm-orchestrator")
	if err := os.Symlink(wrongTarget, dst); err != nil {
		t.Fatal(err)
	}

	if _, _, err := install.WriteSkills(c, install.KindCodex); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(wrongTarget, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(install.SkillBody("swarm-orchestrator")) {
		t.Fatal("the shared swarm-orchestrator copy was corrupted through the symlink")
	}
	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("dst is still a symlink instead of a real Copy-mode directory")
	}
	got, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(install.SkillBody("swarm")) {
		t.Errorf("dst content = %q, want the swarm skill", got)
	}
}

// Review round 1, Major 3: the daemon's startup sync must also repair drift
// in an already-installed kind's own skills root -- a broken Symlink, or a
// Copy-mode copy that has drifted -- not just refresh the shared
// ~/.swarm/skills copy underneath it.
func TestSyncAndRefreshSkillsRepairsAnAlreadyInstalledKindsBrokenLink(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	if _, _, err := install.WriteSkills(c, install.KindClaude); err != nil {
		t.Fatal(err)
	}
	skillsHome, err := install.SkillsHome(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(c.SkillsDir(install.KindClaude), "swarm")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	// Dangling but still swarm-owned: it resolves inside skillsHome, just at
	// the wrong (nonexistent) name -- distinct from a foreign dangling link,
	// which must be left alone (see TestWriteSkillsSkipsADanglingForeignSymlink).
	if err := os.Symlink(filepath.Join(skillsHome, "some-old-removed-name"), link); err != nil {
		t.Fatal(err)
	}

	skillErrs, err := install.SyncAndRefreshSkills(c)
	if err != nil {
		t.Fatal(err)
	}
	for k, kerr := range skillErrs {
		if kerr != nil {
			t.Errorf("%s: %v", k, kerr)
		}
	}
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(skillsHome, "swarm"); got != want {
		t.Errorf("link still broken after SyncAndRefreshSkills: %s, want %s", got, want)
	}
	// Codex was never installed: it must not be silently created.
	if _, err := os.Stat(c.SkillsDir(install.KindCodex)); !os.IsNotExist(err) {
		t.Error("SyncAndRefreshSkills must not create a skills root for a never-installed kind")
	}
}

// TestEmbeddedSkillsMatchTheCanonicalFiles was superseded by
// TestEmbeddedMirrorMatchesCanonicalTree (review round 1, nit), which compares
// the whole tree byte-for-byte, not just each skill's SKILL.md.

// vendoredSkillNames is the fixed set from spec A2's source table.
var vendoredSkillNames = []string{
	"web-design-guidelines",
	"building-components",
	"ui-ux-pro-max",
	"expo-native-ui",
	"expo-design-system",
	"vercel-react-native-skills",
	"mobile-ios-design",
	"mobile-android-design",
	"ponytail",
	"ponytail-review",
	"ponytail-debt",
}

// bannedVendorStrings must never appear anywhere under skills/vendor/: each is
// either a leftover plugin-root reference (CLAUDE_PLUGIN_ROOT), a feature we
// deliberately stripped (submit-expo-feedback), a live fetch of upstream
// instead of the vendored copy (raw.githubusercontent.com), or the
// interactive mode-switching text ponytail's swarm note replaces
// ("/ponytail ").
var bannedVendorStrings = []string{
	"CLAUDE_PLUGIN_ROOT",
	"submit-expo-feedback",
	"raw.githubusercontent.com",
	"/ponytail ",
}

// P2 acceptance: every vendored skill has a license file and a VENDORED.md in
// the spec's format, the vendored set is exactly the 11 names in spec A2, and
// none of the banned strings leaked in from upstream or from our own edits.
func TestVendoredSkillsHaveLicenseAndProvenance(t *testing.T) {
	vendorRoot := filepath.Join("..", "..", "skills", "vendor")
	entries, err := os.ReadDir(vendorRoot)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		got[e.Name()] = true
	}
	want := map[string]bool{}
	for _, n := range vendoredSkillNames {
		want[n] = true
	}
	if len(got) != len(want) {
		// Errorf, not Fatalf (review round 1, Minor 3): a wrong dir count must
		// not skip the per-skill checks or the banned-strings walk below.
		t.Errorf("skills/vendor dirs = %v, want exactly %v",
			slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	for n := range want {
		if !got[n] {
			t.Errorf("missing vendored skill dir %s", n)
		}
	}

	for _, name := range vendoredSkillNames {
		dir := filepath.Join(vendorRoot, name)

		hasLicense := false
		for _, lic := range []string{"LICENSE", "LICENSE.md"} {
			if _, err := os.Stat(filepath.Join(dir, lic)); err == nil {
				hasLicense = true
			}
		}
		if !hasLicense {
			t.Errorf("%s: missing LICENSE or LICENSE.md", name)
		}

		body, err := os.ReadFile(filepath.Join(dir, "VENDORED.md"))
		if err != nil {
			t.Errorf("%s: missing VENDORED.md: %v", name, err)
			continue
		}
		text := string(body)
		if !strings.HasPrefix(text, "# Vendored: "+name+"\n") {
			t.Errorf("%s: VENDORED.md must start with '# Vendored: %s'", name, name)
		}
		for _, field := range []string{"\n- Source:", "\n- License:", "\n- Vendored on:", "\n- Changes:"} {
			if !strings.Contains(text, field) {
				t.Errorf("%s: VENDORED.md missing %q field", name, strings.TrimSpace(field))
			}
		}

		skillBody, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			t.Errorf("%s: missing SKILL.md: %v", name, err)
			continue
		}
		fm := parseFrontmatter(t, skillBody)
		if fm["name"] != name {
			t.Errorf("%s: frontmatter name = %q, want %q", name, fm["name"], name)
		}
	}

	err = filepath.WalkDir(vendorRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		text := string(body)
		for _, banned := range bannedVendorStrings {
			if strings.Contains(text, banned) {
				t.Errorf("%s: contains banned string %q", p, banned)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// P3 unit 3.1: one table row per role skill this package writes, each a list
// of substrings that must appear in its SKILL.md. A skill that doesn't exist
// yet fails its own subtest (fs.ReadFile error) without taking the rest of
// the table down with it -- unlike install.SkillBody, which panics on an
// unknown name and would kill the whole test binary before every row got a
// chance to report red.
func TestRoleSkillsReferenceTheirSkills(t *testing.T) {
	cases := []struct {
		skill    string
		requires []string
	}{
		{"swarm", []string{
			"## Workflow",
			"`Workflow`",
			"swarm_workflow",
			"swarm-advisor",
			"don't coordinate the next step yourself",
			"the engine reports the workflow's outcome",
			"`blocked`, `failed`, `handoff`, and `swarm_send` questions still reach",
		}},
		{"swarm-coder", []string{
			"Follow the `swarm` skill first",
			"swarm-batching",
			"\"unit\": <n>",
			"one commit per unit",
			"ponytail",
			"Precedence",
			"superpowers:test-driven-development",
			"superpowers:verification-before-completion",
			"superpowers:receiving-code-review",
			"dirty:false",
			"assignment_update",
			"record it too, in a `progress` checkpoint",
			"new attempt",
		}},
		{"swarm-reviewer", []string{
			"Follow the `swarm` skill first",
			"superpowers:requesting-code-review",
			"ponytail-review",
			"`pass` | `changes_requested` | `blocked`",
			"critical|major|minor|nit",
			"findings",
			"swarm-batching",
			"swarm_read",
			"Never edit",
			"would fail without the change",
			"for each acceptance criterion",
		}},
		{"swarm-ui-reviewer", []string{
			"Follow the `swarm` skill first",
			"swarm-reviewer",
			"`pass` | `changes_requested` | `blocked`",
			"critical|major|minor|nit",
			"web-design-guidelines",
			"building-components",
			"mobile-ios-design",
			"mobile-android-design",
			"expo-native-ui",
			"vercel-react-native-skills",
			"ui-ux-pro-max",
			"44pt",
		}},
		{"swarm-designer", []string{
			"Follow the `swarm` skill first",
			"superpowers:brainstorming",
			"ui-ux-pro-max",
			"building-components",
			"~/.swarm/designs/",
			"artifacts",
			"Goals",
			"Screens",
			"Components",
			"Tokens",
			"Interaction & motion",
			"Accessibility",
			"Mobile specifics",
			"Open questions",
			"Mermaid",
		}},
		{"swarm-batching", []string{
			"| Work in the package | Workflow |",
			"| Units per package |",
		}},
		{"swarm-debugger", []string{
			"Follow the `swarm` skill first",
			"superpowers:systematic-debugging",
			"regression test",
			"root cause",
			"progress",
			"swarm-advisor",
		}},
		{"swarm-mechanical", []string{
			"Follow the `swarm` skill first",
			"ponytail",
			"blocked",
		}},
		{"swarm-researcher", []string{
			"Follow the `swarm` skill first",
			"superpowers:brainstorming",
			"no user dialogue",
			"~/.swarm/research/",
			"### Takeaway",
			"### Cited findings",
			"### Inferences",
			"### Gaps",
			"artifacts",
			"Never invent",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.skill, func(t *testing.T) {
			body, err := fs.ReadFile(install.SkillFS(), path.Join(tc.skill, "SKILL.md"))
			if err != nil {
				t.Fatalf("%s: %v", tc.skill, err)
			}
			text := string(body)
			for _, want := range tc.requires {
				if !strings.Contains(text, want) {
					t.Errorf("%s: missing %q", tc.skill, want)
				}
			}
		})
	}
}

// P4 unit 4.1 acceptance: swarm-mechanical is a light skill, at most 60 lines
// (frontmatter included) -- the mechanical role does the smallest of the work
// packages, so its own skill stays proportionally small.
func TestSwarmMechanicalSkillIsAtMost60Lines(t *testing.T) {
	body, err := fs.ReadFile(install.SkillFS(), path.Join("swarm-mechanical", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "\n"); n > 60 {
		t.Errorf("swarm-mechanical/SKILL.md has %d lines, want <= 60", n)
	}
}

// knownSuperpowersSkills is the fixed set of 15 superpowers v6.4.1 skill
// names (P3 brief). Any `superpowers:<x>` reference in a non-vendored skill
// must name one of these -- a typo'd or invented superpowers skill name would
// otherwise silently tell an agent to "follow" a skill that doesn't exist.
var knownSuperpowersSkills = map[string]bool{
	"brainstorming":                  true,
	"diagnosing-superpowers":         true,
	"dispatching-parallel-agents":    true,
	"executing-plans":                true,
	"finishing-a-development-branch": true,
	"receiving-code-review":          true,
	"requesting-code-review":         true,
	"subagent-driven-development":    true,
	"systematic-debugging":           true,
	"test-driven-development":        true,
	"using-git-worktrees":            true,
	"using-superpowers":              true,
	"verification-before-completion": true,
	"writing-plans":                  true,
	"writing-skills":                 true,
}

var superpowersRefRe = regexp.MustCompile(`superpowers:([a-zA-Z][a-zA-Z0-9-]*)`)

// TestSuperpowersReferencesAreKnown scans every non-vendored skill for
// `superpowers:<name>` references and checks each against the fixed set of
// 15 real superpowers v6.4.1 skill names. Vendored skills are third-party
// content (P2) and are exempt. It also asserts at least one reference was
// found at all: the regex trivially "passes" over a tree with zero
// `superpowers:` references, which would hide a typo'd prefix (e.g. every
// skill silently switching to a different convention) instead of catching it.
func TestSuperpowersReferencesAreKnown(t *testing.T) {
	sk, err := install.Skills()
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, s := range sk {
		if s.Vendored {
			continue
		}
		body, err := fs.ReadFile(install.SkillFS(), path.Join(s.Dir, "SKILL.md"))
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		for _, m := range superpowersRefRe.FindAllStringSubmatch(string(body), -1) {
			total++
			if !knownSuperpowersSkills[m[1]] {
				t.Errorf("%s: unknown superpowers skill %q referenced as superpowers:%s", s.Name, m[1], m[1])
			}
		}
	}
	if total == 0 {
		t.Error("found zero superpowers: references across all non-vendored skills; the regex or the skill tree is broken")
	}
}
