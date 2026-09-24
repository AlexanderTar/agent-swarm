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

// Skills walks the embedded tree and returns one entry per directory that holds
// a SKILL.md, sorted by name for a deterministic install order.
func Skills() ([]Skill, error) {
	fsys := SkillFS()
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

// isSwarmOwned reports whether dst is safe for WriteSkills/SyncSkills/Uninstall
// to create, replace or remove: nothing is there yet, it is a symlink whose
// target resolves inside skillsHome, or it is a directory carrying
// ManagedMarker. Anything else (a real file or a plain directory with no
// marker) is the user's own same-named skill.
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
		_, err := os.Stat(filepath.Join(dst, ManagedMarker))
		return err == nil, nil
	}
	return false, nil
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
	// once written by swarm.
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

// LinkSkills exposes every registered skill under root, pointed at
// skillsHome/<name> (A1). An entry that is not swarm-owned (isSwarmOwned) is
// left untouched and reported in skipped rather than overwritten.
func LinkSkills(root, skillsHome string, mode LinkMode) (skipped []string, err error) {
	for _, name := range SkillNames() {
		dst := filepath.Join(root, name)
		src := filepath.Join(skillsHome, name)
		owned, err := isSwarmOwned(dst, skillsHome)
		if err != nil {
			return skipped, err
		}
		if !owned {
			skipped = append(skipped, dst)
			continue
		}
		if _, err := applyLink(dst, src, mode); err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}

// WriteSkills makes every registered skill available under k's own skills
// root, symlinked or copied from ~/.swarm/skills per skillLinkMode (A1). It
// syncs that shared copy first, so a fresh install has real content to link to
// even before the daemon's first SyncSkills. changed lists the skills it
// created or replaced; skipped lists ones left alone because the user already
// owns a same-named skill there.
func WriteSkills(c Config, k Kind) (changed, skipped []string, err error) {
	if _, err := SyncSkills(c.UserHome); err != nil {
		return nil, nil, err
	}
	root := c.SkillsDir(k)
	if root == "" {
		return nil, nil, fmt.Errorf("install: no skills folder for agent %q", k)
	}
	skillsHome := filepath.Join(c.Home, "skills")
	mode := skillLinkMode[k]
	for _, name := range SkillNames() {
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
	return changed, skipped, nil
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

// SyncSkills extracts the embedded skills tree to ~/.swarm/skills/<name>/ (one
// on-disk copy every kind links or copies from, A1). Vendored skills are
// flattened by name: install.Skill.Dir may be nested (vendor/<name>), but the
// extracted copy always lands directly under <name>. Each file is written with
// WriteIfChanged, each skill directory gets ManagedMarker, and any file left
// over from a previous sync that is no longer part of the embed is pruned.
func SyncSkills(home string) ([]string, error) {
	root := filepath.Join(home, ".swarm", "skills")
	sk, err := Skills()
	if err != nil {
		return nil, err
	}
	fsys := SkillFS()
	var changed []string
	for _, s := range sk {
		dst := filepath.Join(root, s.Name)
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
	return changed, nil
}

// CheckSkills is doctor's per-kind skills check (A1, unit 1.4): every
// registered skill must be reachable under k's own skills root, whether that
// is v2's own symlink/copy or a same-named skill the user made themselves.
// The latter is reported (not failed): swarm's own copy simply is not
// installed there.
func CheckSkills(c Config, k Kind) Check {
	name := k.Display() + " skills"
	root := c.SkillsDir(k)
	skillsHome := filepath.Join(c.Home, "skills")
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
// files first so a directory empties before it is itself considered.
func pruneUnkept(dst string, keep map[string]bool) error {
	return filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
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
