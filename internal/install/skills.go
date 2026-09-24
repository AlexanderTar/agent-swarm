package install

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// The whole skills/ tree ships inside the binary (A1): every skill's SKILL.md
// plus any nested scripts/data it carries, so `swarm install` and the daemon
// work from any directory with no external file dependency. `all:` is required
// so directories that would otherwise be skipped (e.g. a leading-dot or
// leading-underscore entry) are still embedded.
//
//go:embed all:skills
var skillFS embed.FS

// ManagedMarker is written into every skill directory SyncSkills extracts, so a
// later sync (or uninstall) can tell a swarm-managed copy from a user's own
// same-named skill.
const ManagedMarker = ".swarm-managed"

// Skill is one entry in the registry (A1): Name is its SKILL.md frontmatter
// `name:`, Dir is its path inside SkillFS(), and Vendored marks a skill that
// lives under vendor/ (third-party, licensed content).
type Skill struct {
	Name     string
	Dir      string
	Vendored bool
}

// SkillFS returns the embedded tree rooted at the skills/ directory itself, so
// callers address a skill as "swarm/SKILL.md" rather than "skills/swarm/SKILL.md".
func SkillFS() fs.FS {
	sub, err := fs.Sub(skillFS, "skills")
	if err != nil {
		panic(err) // the embed directive is fixed at compile time
	}
	return sub
}

// skillsIn walks fsys and returns one entry per directory that holds a
// SKILL.md, sorted by name for a deterministic install order. It is the
// fs.FS-generic primitive behind Skills() (bound to the real embedded tree)
// and syncSkills' tests (bound to a synthetic fstest.MapFS).
func skillsIn(fsys fs.FS) ([]Skill, error) {
	var out []Skill
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		dir := path.Dir(p)
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		name := frontmatterField(body, "name")
		if name == "" {
			name = path.Base(dir)
		}
		out = append(out, Skill{
			Name:     name,
			Dir:      dir,
			Vendored: dir == "vendor" || strings.HasPrefix(dir, "vendor/"),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// cachedSkills memoizes skillsIn(SkillFS()): the embed is fixed at compile
// time, so re-walking and re-parsing frontmatter on every SkillNames() or
// SkillBody() call (both go through Skills()) is pure waste.
var cachedSkills = sync.OnceValues(func() ([]Skill, error) { return skillsIn(SkillFS()) })

// Skills returns the registry derived from the embedded tree (cached: see
// cachedSkills).
func Skills() ([]Skill, error) { return cachedSkills() }

// frontmatterField reads a top-level "key: value" line from the YAML-ish
// frontmatter between the first pair of "---" fence lines. It is deliberately
// minimal: skills' frontmatter is flat, single-line key/value pairs.
func frontmatterField(body []byte, key string) string {
	lines := strings.Split(string(body), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "---" {
			break
		}
		k, v, ok := strings.Cut(l, ":")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// SkillNames is the registry's names, in Skills() order (alphabetical).
func SkillNames() []string {
	sk, err := Skills()
	if err != nil {
		panic(err) // the embedded tree cannot fail to walk
	}
	names := make([]string, len(sk))
	for i, s := range sk {
		names[i] = s.Name
	}
	return names
}

// SkillBody is the embedded SKILL.md for name. It panics on an unknown name: a
// typo must fail at the call site, not install an empty skill.
func SkillBody(name string) []byte {
	sk, err := Skills()
	if err != nil {
		panic(err)
	}
	for _, s := range sk {
		if s.Name == name {
			body, err := fs.ReadFile(SkillFS(), path.Join(s.Dir, "SKILL.md"))
			if err != nil {
				panic(fmt.Sprintf("install: %s: %v", name, err))
			}
			return body
		}
	}
	panic(fmt.Sprintf("install: no embedded skill %q", name))
}

// LinkMode is how WriteSkills exposes the shared ~/.swarm/skills copy inside one
// kind's own skills root (A1's symlink fallback).
type LinkMode int

const (
	// Symlink points <kind-skills-root>/<name> straight at
	// ~/.swarm/skills/<name>: one on-disk copy, picked up by the daemon's next
	// SyncSkills with no further action.
	Symlink LinkMode = iota
	// Copy is for a CLI that does not follow (or is not yet confirmed to
	// follow) a symlinked skill directory: the tree is copied in instead, and
	// re-copied whenever it drifts from ~/.swarm/skills.
	Copy
)

// skillLinkMode is §A1's per-kind choice, checked empirically against each
// agent CLI on 2026-09-24: only `claude` is installed on the machine this
// check ran on, and it does follow a skills-root entry that is a symlink to
// another directory (it lists and can invoke the skill inside). codex, agy,
// cursor-agent and muse were not installed to check, so each defaults to the
// safe Copy fallback until someone verifies it and flips the entry below.
var skillLinkMode = map[Kind]LinkMode{
	KindClaude: Symlink,
	KindCodex:  Copy,
	KindAgy:    Copy,
	KindCursor: Copy,
	KindMuse:   Copy,
}

// SkillLinkMode reports how WriteSkills exposes skills for k. Exported so a
// per-spawn config writer (adapter/claude.go's writeProjectSwarmConfig) can
// match swarm install's own choice for that kind exactly, rather than
// hardcoding a mode that could drift from skillLinkMode.
func SkillLinkMode(k Kind) LinkMode { return skillLinkMode[k] }

// SkillsHome is the one on-disk copy every kind's WriteSkills links or copies
// from (A1): <home>/skills, where home is the swarm home (Config.Home, e.g.
// ~/.swarm, or a custom --home/SWARM_HOME). It is always made absolute: the
// result also becomes a symlink target (LinkSkills), and a relative one would
// resolve against whatever cwd the *reading* process (an agent CLI, possibly
// launched from a different directory) happens to have, not the writer's.
func SkillsHome(home string) (string, error) {
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	return filepath.Join(abs, "skills"), nil
}

// isSwarmOwned reports whether dst is safe for WriteSkills/SyncSkills/Uninstall
// to create, replace or remove: nothing is there yet, it is a symlink whose
// target resolves inside skillsHome, it is a directory carrying ManagedMarker,
// or it is a pre-A1 real directory (isPreA1CoreSkillDir). Anything else (a
// real file, or a plain directory with no marker that isn't a recognized
// pre-A1 skill dir) is the user's own same-named skill.
func isSwarmOwned(dst, skillsHome string) (bool, error) {
	fi, err := os.Lstat(dst)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(dst)
		if err != nil {
			return false, nil // unreadable link: treat as foreign, never touch it
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(dst), target)
		}
		rel, err := filepath.Rel(skillsHome, target)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
	}
	if fi.IsDir() {
		if _, err := os.Stat(filepath.Join(dst, ManagedMarker)); err == nil {
			return true, nil
		}
		return isPreA1CoreSkillDir(dst), nil
	}
	return false, nil
}

// isPreA1CoreSkillDir reports whether dst is a marker-less directory written
// by the pre-A1 WriteSkills, which wrote only a single SKILL.md per skill, for
// the two skills that existed before this feature (swarm, swarm-orchestrator;
// A1 is the first release with vendored or otherwise-nested skills, so no
// other name could be a pre-A1 install). Recognizing and adopting it (rather
// than treating it as user-owned) means an operator who installed before A1
// does not have their own v2 skills frozen out of every later sync or repair.
func isPreA1CoreSkillDir(dst string) bool {
	name := filepath.Base(dst)
	if name != "swarm" && name != "swarm-orchestrator" {
		return false
	}
	entries, err := os.ReadDir(dst)
	if err != nil || len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		return false
	}
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil {
		return false
	}
	return frontmatterField(body, "name") == name
}

// applyLink makes dst reflect src under mode and reports whether it changed
// anything on disk. The caller must already know dst is swarm-owned (or does
// not exist yet).
func applyLink(dst, src string, mode LinkMode) (bool, error) {
	if mode == Symlink {
		if fi, err := os.Lstat(dst); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				if cur, err := os.Readlink(dst); err == nil && cur == src {
					return false, nil // already correct: no churn
				}
			}
			if err := os.RemoveAll(dst); err != nil {
				return false, err
			}
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return false, err
		}
		return true, os.Symlink(src, dst)
	}
	// Copy: mirror src (the synced ~/.swarm/skills/<name> tree) into dst
	// file-by-file, so a previous copy that has drifted self-heals instead of
	// silently keeping stale content next to a marker that only proves it was
	// once written by swarm. A leftover symlink at dst (e.g. this kind used to
	// be Symlink-mode, or a stale entry pointing at a *different* skill's
	// shared directory) must be removed first: filepath.WalkDir/MkdirAll would
	// otherwise silently follow it, writing this skill's files through the
	// link into whatever it actually points at -- possibly another skill's
	// shared ~/.swarm/skills copy.
	if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(dst); err != nil {
			return false, err
		}
	}
	return copyTreeSynced(src, dst)
}

// copyTreeSynced mirrors src into dst with WriteIfChanged per file (keeping
// each file's own mode, unlike the embed which loses exec bits) and prunes
// anything in dst that is no longer present in src. It reports whether
// anything changed.
func copyTreeSynced(src, dst string) (bool, error) {
	changed := false
	keep := map[string]bool{dst: true}
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		keep[target] = true
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if fi, err := d.Info(); err == nil {
			mode = fi.Mode().Perm()
		}
		wrote, err := WriteIfChanged(target, body, mode)
		if err != nil {
			return err
		}
		if wrote {
			changed = true
		}
		return nil
	})
	if err != nil {
		return changed, err
	}
	if err := pruneUnkept(dst, keep); err != nil {
		return changed, err
	}
	return changed, nil
}

// linkSkills is the shared implementation behind LinkSkills and WriteSkills:
// it exposes every registered skill under root, pointed at skillsHome/<name>
// (A1), and prunes a swarm-owned entry under root whose name is no longer
// registered (a skill that was renamed or retired). An entry that is not
// swarm-owned is left untouched and reported in skipped.
func linkSkills(root, skillsHome string, mode LinkMode) (changed, skipped []string, err error) {
	registered := make(map[string]bool, len(SkillNames()))
	for _, name := range SkillNames() {
		registered[name] = true
		dst := filepath.Join(root, name)
		src := filepath.Join(skillsHome, name)
		owned, err := isSwarmOwned(dst, skillsHome)
		if err != nil {
			return changed, skipped, err
		}
		if !owned {
			skipped = append(skipped, dst)
			continue
		}
		chg, err := applyLink(dst, src, mode)
		if err != nil {
			return changed, skipped, err
		}
		if chg {
			changed = append(changed, dst)
		}
	}
	pruned, err := pruneUnregistered(root, skillsHome, registered)
	if err != nil {
		return changed, skipped, err
	}
	return append(changed, pruned...), skipped, nil
}

// pruneUnregistered removes a swarm-owned entry directly under root whose
// name is not in registered (a skill that was renamed or retired since it was
// last installed there). A user-owned same-named leftover is never touched.
func pruneUnregistered(root, skillsHome string, registered map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		if registered[e.Name()] {
			continue
		}
		dst := filepath.Join(root, e.Name())
		owned, err := isSwarmOwned(dst, skillsHome)
		if err != nil {
			return removed, err
		}
		if !owned {
			continue
		}
		if err := os.RemoveAll(dst); err != nil {
			return removed, err
		}
		removed = append(removed, dst)
	}
	return removed, nil
}

// LinkSkills exposes every registered skill under root, pointed at
// skillsHome/<name> (A1). An entry that is not swarm-owned (isSwarmOwned) is
// left untouched and reported in skipped rather than overwritten.
func LinkSkills(root, skillsHome string, mode LinkMode) (skipped []string, err error) {
	_, skipped, err = linkSkills(root, skillsHome, mode)
	return skipped, err
}

// WriteSkills makes every registered skill available under k's own skills
// root, symlinked or copied from ~/.swarm/skills per skillLinkMode (A1). It
// syncs that shared copy first, so a fresh install has real content to link to
// even before the daemon's first SyncSkills. changed lists the skills it
// created or replaced; skipped lists ones left alone because the user already
// owns a same-named skill there.
func WriteSkills(c Config, k Kind) (changed, skipped []string, err error) {
	if _, err := SyncSkills(c.Home); err != nil {
		return nil, nil, err
	}
	root := c.SkillsDir(k)
	if root == "" {
		return nil, nil, fmt.Errorf("install: no skills folder for agent %q", k)
	}
	skillsHome, err := SkillsHome(c.Home)
	if err != nil {
		return nil, nil, err
	}
	return linkSkills(root, skillsHome, skillLinkMode[k])
}

// RefreshSkillLinks re-links every kind whose skills root already holds at
// least one swarm-owned entry -- i.e. WriteSkills has run for it before --
// against the current shared skills copy. This is what lets the daemon's own
// startup sync (SyncSkills) also repair drift in an already-installed kind's
// Copy-mode copy, or a broken Symlink, without requiring a `swarm install`
// re-run. A kind that was never installed (no swarm-owned entry yet) is left
// alone: it is never silently created here. Per-kind errors are returned in
// the map rather than aborting the rest.
func RefreshSkillLinks(c Config) map[Kind]error {
	errs := map[Kind]error{}
	skillsHome, err := SkillsHome(c.Home)
	if err != nil {
		for _, k := range Kinds {
			errs[k] = err
		}
		return errs
	}
	for _, k := range Kinds {
		root := c.SkillsDir(k)
		if root == "" || !anyAlreadyInstalled(root, skillsHome) {
			continue
		}
		if _, _, err := linkSkills(root, skillsHome, skillLinkMode[k]); err != nil {
			errs[k] = err
		}
	}
	return errs
}

// anyAlreadyInstalled reports whether root already holds at least one
// swarm-owned registered skill entry -- the signal that WriteSkills has run
// for this kind before, distinct from isSwarmOwned's own "nothing there yet"
// case (which must not count as "installed").
func anyAlreadyInstalled(root, skillsHome string) bool {
	for _, name := range SkillNames() {
		dst := filepath.Join(root, name)
		if _, err := os.Lstat(dst); err != nil {
			continue
		}
		if owned, err := isSwarmOwned(dst, skillsHome); err == nil && owned {
			return true
		}
	}
	return false
}

// SyncAndRefreshSkills is the daemon's startup skills step (A1): sync the
// shared skills copy from the embedded tree, then refresh every
// already-installed kind's own skills root against it (RefreshSkillLinks).
// syncErr means the shared copy itself failed and skillErrs is nil; a
// per-kind refresh error is returned in skillErrs instead, since one kind's
// problem must never mask another's or stop the daemon starting.
func SyncAndRefreshSkills(c Config) (skillErrs map[Kind]error, syncErr error) {
	if _, err := SyncSkills(c.Home); err != nil {
		return nil, err
	}
	return RefreshSkillLinks(c), nil
}

// scriptsFileMode returns the mode a synced file should carry: executable
// under any scripts/ directory (embed.FS drops the source's own exec bit), the
// usual 0o644 otherwise.
func scriptsFileMode(rel string) os.FileMode {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "scripts" {
			return 0o755
		}
	}
	return 0o644
}

// syncSkills mirrors every skill in fsys (an already-namespaced skills tree --
// SkillFS() in production, a synthetic fstest.MapFS in tests) into
// dstRoot/<name>, flattening a vendored skill's nested path. Each file is
// written with WriteIfChanged, each skill directory gets ManagedMarker, any
// file left over from a previous sync that is no longer part of fsys is
// pruned, and a whole skill directory under dstRoot that is no longer
// registered at all (renamed or retired) is removed too, as long as it is
// still swarm-managed (carries the marker) -- a real user directory of the
// same name, though unexpected directly under dstRoot, is never touched.
func syncSkills(fsys fs.FS, dstRoot string) ([]string, error) {
	sk, err := skillsIn(fsys)
	if err != nil {
		return nil, err
	}
	var changed []string
	registered := make(map[string]bool, len(sk))
	for _, s := range sk {
		registered[s.Name] = true
		dst := filepath.Join(dstRoot, s.Name)
		keep := map[string]bool{dst: true}
		err := fs.WalkDir(fsys, s.Dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(filepath.FromSlash(s.Dir), filepath.FromSlash(p))
			if err != nil {
				return err
			}
			target := dst
			if rel != "." {
				target = filepath.Join(dst, rel)
			}
			keep[target] = true
			if d.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			body, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			wrote, err := WriteIfChanged(target, body, scriptsFileMode(rel))
			if err != nil {
				return err
			}
			if wrote {
				changed = append(changed, target)
			}
			return nil
		})
		if err != nil {
			return changed, err
		}
		marker := filepath.Join(dst, ManagedMarker)
		wrote, err := WriteIfChanged(marker, []byte{}, 0o644)
		if err != nil {
			return changed, err
		}
		if wrote {
			changed = append(changed, marker)
		}
		keep[marker] = true

		if err := pruneUnkept(dst, keep); err != nil {
			return changed, err
		}
	}

	// A skill that used to be registered (renamed or retired) must not linger
	// under dstRoot forever just because fsys no longer embeds it.
	entries, err := os.ReadDir(dstRoot)
	if err != nil && !os.IsNotExist(err) {
		return changed, err
	}
	for _, e := range entries {
		if registered[e.Name()] {
			continue
		}
		dst := filepath.Join(dstRoot, e.Name())
		if _, err := os.Stat(filepath.Join(dst, ManagedMarker)); err != nil {
			continue // not (or no longer) swarm-managed; never touch it
		}
		if err := os.RemoveAll(dst); err != nil {
			return changed, err
		}
		changed = append(changed, dst)
	}
	return changed, nil
}

// SyncSkills extracts the embedded skills tree to SkillsHome(home)/<name>/
// (one on-disk copy every kind links or copies from, A1). home is the swarm
// home (Config.Home), not the user's home.
func SyncSkills(home string) ([]string, error) {
	root, err := SkillsHome(home)
	if err != nil {
		return nil, err
	}
	return syncSkills(SkillFS(), root)
}

// CheckSkills is doctor's per-kind skills check (A1, unit 1.4): every
// registered skill must be reachable under k's own skills root, whether that
// is v2's own symlink/copy or a same-named skill the user made themselves.
// The latter is reported (not failed): swarm's own copy simply is not
// installed there.
func CheckSkills(c Config, k Kind) Check {
	name := k.Display() + " skills"
	root := c.SkillsDir(k)
	skillsHome, err := SkillsHome(c.Home)
	if err != nil {
		return Check{name, false, err.Error()}
	}
	var userOwned []string
	for _, s := range SkillNames() {
		dst := filepath.Join(root, s)
		if _, err := os.Stat(filepath.Join(dst, "SKILL.md")); err != nil {
			return Check{name, false, "Missing " + dst + ". Run swarm install."}
		}
		owned, err := isSwarmOwned(dst, skillsHome)
		if err != nil {
			return Check{name, false, err.Error()}
		}
		if !owned {
			userOwned = append(userOwned, s)
		}
	}
	if len(userOwned) > 0 {
		var notes []string
		for _, s := range userOwned {
			notes = append(notes, fmt.Sprintf("skill %s for %s is user-owned; swarm's copy is not installed there", s, k.Display()))
		}
		return Check{name, true, strings.Join(notes, " ")}
	}
	return Check{name, true, root}
}

// pruneUnkept removes anything under dst that keep does not list, deepest
// files first so a directory empties before it is itself considered. A
// `*.swarm-tmp` entry is always skipped: it is WriteIfChanged's own in-flight
// temp file (write-then-rename), and a concurrent sync elsewhere (the daemon
// and a `swarm install` can run at the same time) must never delete out from
// under a rename that is mid-flight.
func pruneUnkept(dst string, keep map[string]bool) error {
	return filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if strings.HasSuffix(p, ".swarm-tmp") {
			return nil
		}
		if keep[p] {
			return nil
		}
		if d.IsDir() {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
			return filepath.SkipDir
		}
		return os.Remove(p)
	})
}
