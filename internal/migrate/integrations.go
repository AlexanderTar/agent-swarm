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
func (r *Runner) integrations(ctx context.Context, s *Step) error {
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

// install is §20 step 9.
func (r *Runner) install(ctx context.Context, s *Step) error {
	if r.DoInstall == nil {
		return fmt.Errorf("migrate: no install function was provided")
	}
	plist := install.PlistPath(r.Cfg)
	_, statErr := os.Stat(plist)
	existedBefore := statErr == nil
	if err := r.DoInstall(ctx); err != nil {
		return err
	}
	if !existedBefore {
		// There was no plist before, so a rollback removes the one install wrote.
		s.Add(Action{Kind: "remove", To: plist})
	}
	return nil
}
