package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// watchmanRace stands in for a running watchman: watch-del is recorded, and the
// first `git worktree remove` fails the way git does when a cookie file lands
// in the folder mid-delete.
type watchmanRace struct {
	mu        sync.Mutex
	watchDels []string
	removes   int
}

func (w *watchmanRace) runner(real func(context.Context, string, ...string) ([]byte, error), path string) func(context.Context, string, ...string) ([]byte, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if name == "watchman" {
			w.watchDels = append(w.watchDels, strings.Join(args, " "))
			return nil, nil
		}
		if slices.Contains(args, "worktree") && slices.Contains(args, "remove") {
			w.removes++
			if w.removes == 1 {
				_ = os.WriteFile(filepath.Join(path, ".watchman-cookie-host-4242-7"), nil, 0o644)
				return []byte("error: failed to delete '" + path + "': Directory not empty"),
					errors.New("exit status 255")
			}
		}
		return real(ctx, name, args...)
	}
}

func TestRemoveSurvivesWatchmanCookiesAppearingMidDelete(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/cookies",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	// A cookie from before the remove must not read as dirt.
	if err := os.WriteFile(filepath.Join(wt.Path, ".watchman-cookie-host-4242-1"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w := &watchmanRace{}
	s.Run = w.runner(s.Run, wt.Path)

	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed", out)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
	if !slices.Contains(w.watchDels, "watch-del "+wt.Path) {
		t.Fatalf("watchman watch-del never ran for the path: %v", w.watchDels)
	}
	if w.removes != 2 {
		t.Fatalf("git worktree remove ran %d times, want 2 (one retry)", w.removes)
	}
}
