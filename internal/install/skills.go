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

// WriteSkills installs every registered skill's SKILL.md for k and returns the
// paths it changed. (A per-kind symlink/copy of the whole tree lands in a later
// unit; this still writes just the top-level file.)
func WriteSkills(c Config, k Kind) ([]string, error) {
	root := c.SkillsDir(k)
	if root == "" {
		return nil, fmt.Errorf("install: no skills folder for agent %q", k)
	}
	var changed []string
	for _, name := range SkillNames() {
		p := filepath.Join(root, name, "SKILL.md")
		wrote, err := WriteIfChanged(p, SkillBody(name), 0o644)
		if err != nil {
			return changed, err
		}
		if wrote {
			changed = append(changed, p)
		}
	}
	return changed, nil
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
