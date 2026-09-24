package install_test

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// parseFrontmatter extracts the "key: value" lines between the first pair of
// "---" fence lines. It is a test-local, from-scratch parse (not the
// production one), so a registry bug in Skills() can't hide behind a shared
// helper.
func parseFrontmatter(t *testing.T, body []byte) map[string]string {
	t.Helper()
	lines := strings.Split(string(body), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		t.Fatalf("no frontmatter fence: %.40q", body)
	}
	out := map[string]string{}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "---" {
			return out
		}
		k, v, ok := strings.Cut(l, ":")
		if ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	t.Fatalf("frontmatter fence never closed: %.40q", body)
	return nil
}

func TestSkillBodyCarriesTheSpecFrontmatterAndLastRule(t *testing.T) {
	agent := string(install.SkillBody("swarm"))
	for _, want := range []string{
		"name: swarm\n",
		"description: Rules for any agent spawned by Agent Swarm (SWARM_SESSION is set).",
		"# Working as a Swarm agent",
		"Never add `Co-Authored-By`, \"Generated with\", session links, emoji signatures or agent names",
		"Reviewers: check that the tests cover each acceptance criterion and would fail without the change.",
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
func TestSyncSkillsWritesNestedFilesAndPrunes(t *testing.T) {
	home := t.TempDir()
	if _, err := install.SyncSkills(home); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, ".swarm", "skills", "swarm", install.ManagedMarker)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(home, ".swarm", "skills", "swarm", "SKILL.md"))
	if err != nil || !strings.HasPrefix(string(body), "---\n") {
		t.Fatalf("SKILL.md: body=%.20q err=%v", body, err)
	}
	stale := filepath.Join(home, ".swarm", "skills", "swarm", "stale.md")
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

func TestEmbeddedSkillsMatchTheCanonicalFiles(t *testing.T) {
	for _, name := range install.SkillNames() {
		want, err := os.ReadFile(filepath.Join("..", "..", "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(install.SkillBody(name)) != string(want) {
			t.Errorf("internal/install/skills/%s/SKILL.md has drifted from skills/%s/SKILL.md; run make skills-sync", name, name)
		}
	}
}
