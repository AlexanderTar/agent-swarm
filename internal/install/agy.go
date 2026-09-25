package install

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// The agy hook file is keyed by hook NAME, and the shapes differ per event
// (P0-2, §11.1): the two tool events take a matcher wrapper, the other three are
// flat handlers. A nested PreInvocation fails to parse in agy (§21.4 row 1).
const agyHookName = "swarm"

var (
	agyMatcherEvents = []string{"PreToolUse", "PostToolUse"}
	agyFlatEvents    = []string{"PreInvocation", "PostInvocation", "Stop"}
)

type agyFlatHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// AgyHooks is the swarm block of ~/.gemini/config/hooks.json.
func AgyHooks(c Config) []byte {
	body, err := json.MarshalIndent(map[string]any{agyHookName: agySwarmBlock(c)}, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(body, '\n')
}

func agySwarmBlock(c Config) map[string]any {
	block := map[string]any{}
	for _, ev := range agyMatcherEvents {
		block[ev] = []any{map[string]any{
			"matcher": "*",
			"hooks":   []any{agyHook(c, ev)},
		}}
	}
	for _, ev := range agyFlatEvents {
		block[ev] = []any{agyHook(c, ev)}
	}
	return block
}

func agyHook(c Config, event string) map[string]any {
	return map[string]any{"type": "command", "command": c.Bin + " hook agy " + event, "timeout": agyHookTimeout}
}

const agyHookTimeout = 3

// WriteAgy installs agy's hooks, MCP server and skills.
func WriteAgy(ctx context.Context, c Config, run execx.Runner) ([]string, error) {
	if err := repairAgySkillsRoot(c); err != nil {
		return nil, err
	}
	var changed []string
	p := c.Gemini("config", "hooks.json")
	wrote, err := EditJSON(p, true, func(m map[string]any) error {
		// Replace only our own hook name; the file merges with plugin files.
		block, err := toAny(agySwarmBlock(c))
		if err != nil {
			return err
		}
		m[agyHookName] = block
		return nil
	})
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, p)
	}

	// §11.1: the MCP server is global and registered once by swarm install (M3).
	// agy stores it in mcp_config.json itself, so this is idempotent on agy's side.
	if _, err := run(ctx, "agy", "mcp", "add", "--type", "stdio", "swarm", c.Bin, "mcp"); err != nil {
		return changed, err
	}

	skills, _, err := WriteSkills(c, KindAgy)
	return append(changed, skills...), err
}

// RemoveLegacyAgy removes the v1 agy integration: the GEMINI.md block, the
// mcp_config.json entry and the v1 plugin (a symlink on this machine, so it is
// removed as a link and never followed, §20).
func RemoveLegacyAgy(ctx context.Context, c Config, run execx.Runner) ([]string, error) {
	var changed []string

	md := c.Gemini("GEMINI.md")
	if old, err := os.ReadFile(md); err == nil {
		if next, removed := RemoveMarkedSpan(string(old), "<!-- swarm:start -->", "<!-- swarm:end -->"); removed {
			if _, err := WriteIfChanged(md, []byte(next), 0o644); err != nil {
				return changed, err
			}
			changed = append(changed, md)
		}
	} else if !os.IsNotExist(err) {
		return changed, err
	}

	mcp := c.Gemini("antigravity", "mcp_config.json")
	wrote, err := EditJSON(mcp, false, dropSwarmMCPServer)
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, mcp)
	}

	plugin := c.Gemini("config", "plugins", "swarm")
	removedLink, realDir, err := RemoveLink(plugin)
	if err != nil {
		return changed, err
	}
	switch {
	case removedLink:
		changed = append(changed, plugin)
	case realDir:
		// §20: agy plugin uninstall exists (P0-8), but its argument form is not
		// verified, so a failure falls back to removing the folder directly.
		if _, err := run(ctx, "agy", "plugin", "uninstall", "swarm"); err != nil {
			if err := os.RemoveAll(plugin); err != nil {
				return changed, err
			}
		}
		if _, err := os.Stat(plugin); err == nil {
			if err := os.RemoveAll(plugin); err != nil {
				return changed, err
			}
		}
		changed = append(changed, plugin)
	}
	return changed, nil
}

// dropSwarmMCPServer removes the "swarm" entry from an mcpServers object, keeping
// every other server. Shared with the cursor removal.
func dropSwarmMCPServer(m map[string]any) error {
	servers, _ := m["mcpServers"].(map[string]any)
	if servers == nil {
		return nil
	}
	delete(servers, "swarm")
	return nil
}

// CheckAgy is doctor's agy block (§21.4 row 1, plus A1's skills check).
func CheckAgy(ctx context.Context, c Config, run execx.Runner) []Check {
	return []Check{agyHooksCheck(c), CheckSkills(c, KindAgy), agySkillsRootLegacyCheck(c)}
}

// legacyAgySkillsChain reports whether ~/.gemini/antigravity-cli/skills
// (agy's pre-A7 skills location) is currently a symlink whose FIRST hop
// points somewhere under a swarm session's run/launch folder -- the exact
// shape a spawned agy's own first-run migration used to leave behind (A7,
// package PA; see docs/plans/2026-09-24-skill-symlink-probe.md,
// "Follow-up"). resolved is the chain's fully-resolved target when it
// resolves cleanly, and empty when it doesn't (fix round 1, finding 4):
//
//   - a dangling first hop (a later hop was deleted, e.g. a reaped session):
//     ok is still true (there is still a legacy link to repoint), resolved
//     is empty (nothing to salvage).
//   - a first hop into run/launch whose chain resolves all the way back to
//     the healthy Config.SkillsDir(KindAgy) itself (possible once a spawn's
//     own agy-home/.gemini/config/skills is itself a symlink to the real
//     config/skills, per PA.2's setupEnv): ok is true, resolved is that
//     healthy path -- repairAgySkillsRoot must not try to copy it into
//     itself.
//
// Checking only the first hop (not the fully-resolved target, which the
// pre-fix-round-1 version compared instead) is what catches both: a fully
// dangling or redirected-elsewhere chain still LOOKS like the legacy shape
// at its first hop even when where it ends up isn't inside run/launch (or
// doesn't exist) any more. ok is false for anything else: missing, a real
// directory, or a first hop pointing somewhere other than run/launch
// (including the healthy post-repair case, a symlink straight at
// Config.SkillsDir(KindAgy)).
func legacyAgySkillsChain(c Config) (resolved string, ok bool) {
	oldPath := c.Gemini("antigravity-cli", "skills")
	fi, err := os.Lstat(oldPath)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return "", false
	}
	target, err := os.Readlink(oldPath)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(oldPath), target)
	}
	// Match against both the unresolved and the (if resolvable) fully
	// resolved run/launch root (fix round 2, finding 3): the first hop's
	// stored text could have been written against either form depending on
	// when and how it was created, and a home path with its own symlink
	// (e.g. macOS's /var -> /private/var) makes those two forms genuinely
	// different strings for the identical directory. The resolved form
	// resolves c.Home FIRST and then joins "run/launch" (fix round 3,
	// minor), rather than resolving the already-joined <Home>/run/launch
	// path directly: filepath.EvalSymlinks requires the path it's given to
	// exist, and c.Home (the swarm home itself, e.g. ~/.swarm) is far more
	// likely to already exist than one specific dangling chain's own
	// run/launch subdirectory -- resolving the joined path directly would
	// silently skip this whole fallback (and so miss an aliased dangling
	// chain) whenever run/launch itself happened not to exist yet.
	launchRoot := filepath.Join(c.Home, "run", "launch")
	matched := underDir(target, launchRoot)
	if !matched {
		if resolvedHome, err := filepath.EvalSymlinks(c.Home); err == nil {
			matched = underDir(target, filepath.Join(resolvedHome, "run", "launch"))
		}
	}
	if !matched {
		return "", false
	}
	if r, err := filepath.EvalSymlinks(target); err == nil {
		resolved = r
	}
	return resolved, true
}

// repairAgySkillsRoot is A7 decision 3, run only from WriteAgy (i.e. only an
// explicit `swarm install` or `swarm migrate`'s step 9, which calls the same
// install.Agents -> WriteAgy path -- never the daemon): when
// ~/.gemini/antigravity-cli/skills chains into a swarm session's launch
// folder, salvage whatever is there into Config.SkillsDir(KindAgy) (never
// overwriting an entry already at the new root that is user-owned -- a
// swarm-owned one is rewritten by WriteSkills right after this runs anyway),
// then repoint the old path at the new root, matching the shape agy's own
// post-migration setup leaves. Nothing under run/launch is ever deleted.
//
// resolved from legacyAgySkillsChain can be empty (a dangling chain --
// nothing to salvage) or the same directory as newRoot itself (the chain
// resolved all the way back to the healthy root -- also nothing to salvage,
// and reading newRoot's own entries to copy them into itself would be both
// pointless and unsafe); both skip straight to repointing (fix round 1,
// finding 4). "The same directory" is checked with sameDir (os.SameFile),
// not a string comparison (fix round 2, finding 1): resolved has gone
// through filepath.EvalSymlinks (fully resolving every hop, including any
// symlink in c.Home itself, e.g. macOS's /var -> /private/var), while newRoot
// is built from the unresolved c.Home. On a home path that itself has such a
// symlink, the two strings differ even though they name the identical
// directory -- a plain != treated that as "a distinct directory to copy
// from", which then read newRoot's own entries, deleted each one (RemoveAll
// on the very dst it was about to copy from, since src and dst were the same
// path), and failed the subsequent copyTree on the now-missing source,
// aborting the whole `swarm install` for agy after having already deleted
// real, swarm-owned content.
func repairAgySkillsRoot(c Config) error {
	resolved, ok := legacyAgySkillsChain(c)
	if !ok {
		return nil
	}
	newRoot := c.Gemini("config", "skills")
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		return err
	}
	if resolved != "" {
		same, err := sameDir(resolved, newRoot)
		if err != nil {
			return err
		}
		if !same {
			skillsHome, err := SkillsHome(c.Home)
			if err != nil {
				return err
			}
			entries, err := os.ReadDir(resolved)
			if err != nil {
				return err
			}
			launchRoot := filepath.Join(c.Home, "run", "launch")
			var resolvedLaunchRoot string
			resolvedHome, evalErr := filepath.EvalSymlinks(c.Home)
			if evalErr == nil {
				resolvedLaunchRoot = filepath.Join(resolvedHome, "run", "launch")
			}
			for _, e := range entries {
				dst := filepath.Join(newRoot, e.Name())
				owned, err := isSwarmOwned(dst, skillsHome, true) // explicit `swarm install`: adopt
				if err != nil {
					return err
				}
				if !owned {
					continue // a user-owned entry already at the new root: never overwrite it
				}
				if err := os.RemoveAll(dst); err != nil {
					return err
				}
				src := filepath.Join(resolved, e.Name())
				// Fix round 3, controller ruling: a TOP-LEVEL entry that is
				// itself a symlink whose resolved target lies OUTSIDE
				// run/launch (the user's own skill, linked in from somewhere
				// else entirely -- not part of the migration-chain wreckage
				// this repair exists to clean up) is preserved as a symlink to
				// that absolute resolved target, not deep-copied. Everything
				// else -- a real file or directory, or a symlink whose target
				// is itself still inside run/launch -- goes through copyTree,
				// which dereferences every symlink it finds (including nested
				// ones) into real content, so salvaged content survives the
				// source session later being reaped.
				if e.Type()&os.ModeSymlink != 0 {
					real, err := filepath.EvalSymlinks(src)
					if err == nil {
						outside := !underDir(real, launchRoot) &&
							!(evalErr == nil && underDir(real, resolvedLaunchRoot))
						if outside {
							if err := os.Symlink(real, dst); err != nil {
								return err
							}
							continue
						}
					}
				}
				if err := copyTree(src, dst); err != nil {
					return err
				}
			}
		}
	}
	oldPath := c.Gemini("antigravity-cli", "skills")
	if err := os.Remove(oldPath); err != nil {
		return err
	}
	return os.Symlink(newRoot, oldPath)
}

// sameDir reports whether a and b name the same directory on disk (fix
// round 2, finding 1), regardless of how each path is spelled -- unlike a
// plain string comparison, this is correct even when one side has gone
// through filepath.EvalSymlinks and the other hasn't. Both paths are
// expected to already exist (repairAgySkillsRoot's caller MkdirAlls newRoot
// first, and resolved only reaches here after EvalSymlinks succeeded on it).
func sameDir(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}

// agySkillsRootLegacyCheck is A7 decision 4: warn (never fail) doctor when
// the legacy antigravity-cli/skills path still chains into a swarm session's
// launch folder -- the exact state repairAgySkillsRoot fixes on `swarm
// install` (or `swarm migrate`'s step 9), but doctor must be able to say so
// even before that repair runs. A dangling first hop into run/launch (e.g. a
// reaped intermediate session) gets the same warning: legacyAgySkillsChain
// reports ok=true for it too (fix round 1, finding 4), since there is still
// a legacy link that needs repointing even though nothing is left to
// salvage. Nothing at the old path at all (never installed, or already
// cleaned up some other way) gets a distinct, neutral message -- fix round
// 1, finding 3: that case used to fall into the same "is not inside a
// session folder" text as a healthy symlink pointing somewhere unrelated,
// which reads as reassurance for a case that has nothing to reassure about.
func agySkillsRootLegacyCheck(c Config) Check {
	const name = "agy skills root"
	oldPath := c.Gemini("antigravity-cli", "skills")
	if _, ok := legacyAgySkillsChain(c); ok {
		return Check{name, true, "agy skills live inside a swarm session folder; run swarm install to move them"}
	}
	if _, err := os.Lstat(oldPath); os.IsNotExist(err) {
		return Check{name, true, "nothing at " + oldPath + " yet"}
	}
	return Check{name, true, oldPath + " is not inside a session folder"}
}

func agyHooksCheck(c Config) Check {
	p := c.Gemini("config", "hooks.json")
	body, err := os.ReadFile(p)
	if err != nil {
		return Check{"agy hooks", false, "Not installed. Run swarm install."}
	}
	if strings.Contains(string(body), "${PLUGIN_ROOT}") {
		return Check{"agy hooks", false, p + " still uses ${PLUGIN_ROOT}. Run swarm install."}
	}
	var f map[string]map[string][]map[string]any
	if err := json.Unmarshal(body, &f); err != nil {
		return Check{"agy hooks", false, "Invalid " + p + ". Run swarm install."}
	}
	block, ok := f[agyHookName]
	if !ok {
		return Check{"agy hooks", false, "The swarm hook block is missing from " + p + ". Run swarm install."}
	}
	for _, ev := range agyFlatEvents {
		entries := block[ev]
		if len(entries) == 0 {
			return Check{"agy hooks", false, ev + " is missing from " + p + ". Run swarm install."}
		}
		cmd, _ := entries[0]["command"].(string)
		if cmd == "" {
			return Check{"agy hooks", false,
				ev + " must be a flat { command } handler; a nested hooks array fails to parse in agy. Run swarm install."}
		}
		if cmd != c.Bin+" hook agy "+ev {
			return Check{"agy hooks", false, ev + " points somewhere else: " + cmd + ". Run swarm install."}
		}
	}
	for _, ev := range agyMatcherEvents {
		entries := block[ev]
		if len(entries) == 0 {
			return Check{"agy hooks", false, ev + " is missing from " + p + ". Run swarm install."}
		}
		if m, _ := entries[0]["matcher"].(string); m != "*" {
			return Check{"agy hooks", false, ev + " needs a matcher wrapper. Run swarm install."}
		}
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return Check{"agy hooks", false, "The hook binary is missing: " + c.Bin + ". Run make install."}
	}
	return Check{"agy hooks", true, p}
}
