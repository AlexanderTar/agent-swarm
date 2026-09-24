package migrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// integrations is §20 step 8. It calls the same removal code swarm install runs, so
// there is one implementation of it (§20: "swarm install runs the same removal").
func (r *Runner) integrations(ctx context.Context, j *Journal, s *Step) error {
	// The user already chose to migrate, so the v1 release folders go.
	yes := func(string) bool { return true }
	steps := []struct {
		name string
		run  func() ([]string, error)
	}{
		{"shared", func() ([]string, error) { return install.RemoveLegacyShared(ctx, r.Cfg, r.Run, yes) }},
		{"claude", func() ([]string, error) { return install.RemoveLegacyClaude(r.Cfg) }},
		{"codex", func() ([]string, error) { return install.RemoveLegacyCodex(r.Cfg) }},
		{"cursor", func() ([]string, error) { return install.RemoveLegacyCursor(r.Cfg) }},
		{"agy", func() ([]string, error) { return install.RemoveLegacyAgy(ctx, r.Cfg, r.Run) }},
	}
	backups := r.backupIndex()
	for _, st := range steps {
		// Each RemoveLegacy* call performs its own removal before returning what
		// changed, so — unlike a rename or a copy this package controls end to end —
		// there is no way to journal its undo action before the mutation happens.
		// What we can still do is save immediately after each call returns, so a
		// crash between two RemoveLegacy* calls only ever loses an action that has
		// not happened yet, never one that already has (C1).
		changed, err := st.run()
		if err != nil {
			return fmt.Errorf("removing the Agent Swarm 1.x %s integration: %w", st.name, err)
		}
		for _, p := range changed {
			// A file with a step-3 backup can be restored. A removed symlink or folder
			// cannot, deliberately: re-creating a link into a deleted release folder
			// would be worse than leaving it gone. Rollback says so in its output.
			if src, ok := backups[p]; ok {
				s.Add(Action{Kind: "restore", From: src, To: p})
			}
			r.logf("    removed %s", p)
		}
		if err := j.Save(r.home()); err != nil {
			return err
		}
	}
	return nil
}

// backupIndex maps an edited path to its step-3 backup copy, using the newest
// config-* folder.
func (r *Runner) backupIndex() map[string]string {
	dirs, err := filepath.Glob(filepath.Join(r.home(), "backups", "config-*"))
	if err != nil || len(dirs) == 0 {
		return map[string]string{}
	}
	newest := dirs[len(dirs)-1] // Glob sorts, and the names are timestamped
	out := map[string]string{}
	for _, f := range r.filesToBackUp() {
		candidate := filepath.Join(newest, f.rel)
		if _, err := os.Stat(candidate); err == nil {
			out[f.src] = candidate
		}
	}
	return out
}

// newArtifactPaths lists every path step 9 (DoInstall) may create for the first
// time: the daemon plist, every agent's two v2 skill folders (install.WriteSkills
// runs for all of install.Kinds — claude, codex, cursor, agy — each writing
// install.SkillNames; these are fresh directory trees whenever there was no v1
// symlink at that same path to overwrite, which filesToBackUp's restore-from-copy
// mechanism never covers), and the ~/.local/bin/swarm link. If DoInstall creates
// one of these where nothing was there before, rollback just removes it: there is
// nothing to restore it to (Important 3, widened per O2 to cover all four agents'
// skill folders, not only Claude's).
func (r *Runner) newArtifactPaths() []string {
	c := r.Cfg
	paths := []string{install.PlistPath(c), c.LocalBin()}
	for _, k := range install.Kinds {
		for _, name := range install.SkillNames() {
			paths = append(paths, filepath.Join(c.SkillsDir(k), name))
		}
	}
	return paths
}

// install is §20 step 9. Both of its own undo actions are journaled — and saved —
// BEFORE r.DoInstall runs, not after (O1): newArtifactPaths' existed-before check
// only needs an Lstat, which is knowable up front, and the bootout below is a fixed
// action independent of what DoInstall actually does. Unlike step 8's RemoveLegacy*
// calls, where the mutation itself is the only way to learn what changed, there is
// nothing here that needs to wait for DoInstall to return — so a crash or Ctrl-C
// partway through DoInstall's real, multi-minute work (writing the plist,
// bootstrapping, syncing every agent's configuration) still leaves an accurate
// journal, exactly like every other step since C1(b).
func (r *Runner) install(ctx context.Context, j *Journal, s *Step) error {
	if r.DoInstall == nil {
		return fmt.Errorf("migrate: no install function was provided")
	}
	for _, p := range r.newArtifactPaths() {
		// Lstat, not Stat: a pre-existing symlink (a v1 skills link, say) must count
		// as "existed" even though DoInstall's own RemoveLegacy* call removes it and
		// WriteSkills then creates a real directory in its place — that removal is
		// already reported separately (the "unrestorable" note in Rollback's output).
		if _, err := os.Lstat(p); err != nil {
			s.Add(Action{Kind: "remove", To: p})
		}
	}
	// v2 registers under the same launchd label v1 used (dev.swarm.daemon), so
	// --rollback must stop v2 before it bootstraps the restored v1 plist over that
	// same label (C2) — otherwise both would fight over one label, or v2 keeps
	// running against the just-restored v1 schema and crash-loops under KeepAlive.
	// Added last so it undoes first: Undo() replays a step's own actions
	// newest-added-first, and step 9 as a whole runs before step 2 in Undo()'s
	// overall (newest-step-first) order, so this bootout always precedes step 2's
	// bootstrap of the v1 plist.
	s.Add(Action{Kind: "launchctl", Args: []string{"bootout",
		fmt.Sprintf("gui/%d/%s", r.Cfg.UID, install.Label)}})
	if err := j.Save(r.home()); err != nil {
		return err
	}
	return r.DoInstall(ctx)
}
