package mcpserver

import (
	"context"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

// TestReadFilterListsWorktrees: filter {kind:"worktree"} lists active and
// retained rows with their reason, root key and owner, omits removed rows,
// narrows by filter.root / filter.status, and pages like other kinds.
func TestReadFilterListsWorktrees(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	root, err := s.RT.Items.Get(ctx, seed.RootKey)
	if err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.RT.Now())
	ins := func(state, reason string, age int64) string {
		id := ids.New("wt")
		var r any
		if reason != "" {
			r = reason
		}
		if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO worktrees
			(id, repo_id, path, branch, base_ref, base_sha, owner_agent_id, root_item_id, state, retained_reason, created_at)
			VALUES (?, ?, ?, ?, 'main', 'abc1234', ?, ?, ?, ?, ?)`,
			id, seed.RepoID, t.TempDir()+"/wt", "b/"+id, worker.ID, root.ID, state, r, now+age); err != nil {
			t.Fatal(err)
		}
		return id
	}
	active := ins("active", "", 3)
	retained := ins("retained", "unmerged", 2)
	removed := ins("removed", "", 1)

	list := func(args string) map[string]any {
		t.Helper()
		out, err := s.call(ctx, seed.Caller, "swarm_read", args)
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		return readOut(t, out)
	}
	m := list(`{"filter":{"kind":"worktree"}}`)
	wts := readArr(m, "worktrees")
	if len(wts) != 2 {
		t.Fatalf("worktrees = %v, want active+retained only", wts)
	}
	if wts[0].(map[string]any)["worktree_id"] != active || wts[1].(map[string]any)["worktree_id"] != retained {
		t.Fatalf("worktrees = %v, want %s then %s", wts, active, retained)
	}
	r := wts[1].(map[string]any)
	if r["retained_reason"] != "unmerged" || r["root"] != seed.RootKey || r["owner"] != worker.Name || r["state"] != "retained" {
		t.Fatalf("retained row = %v, want reason/root/owner", r)
	}
	for _, w := range wts {
		if w.(map[string]any)["worktree_id"] == removed {
			t.Fatal("removed row listed")
		}
	}

	if n := len(readArr(list(`{"filter":{"kind":"worktree","root":"`+seed.RootKey+`"}}`), "worktrees")); n != 2 {
		t.Fatalf("root-narrowed = %d, want 2", n)
	}
	if n := len(readArr(list(`{"filter":{"kind":"worktree","root":"`+seed.StoryKey+`"}}`), "worktrees")); n != 0 {
		t.Fatalf("other-root = %d, want 0", n)
	}
	st := readArr(list(`{"filter":{"kind":"worktree","status":"retained"}}`), "worktrees")
	if len(st) != 1 || st[0].(map[string]any)["worktree_id"] != retained {
		t.Fatalf("status=retained = %v", st)
	}

	p1 := list(`{"filter":{"kind":"worktree"},"limit":1}`)
	if len(readArr(p1, "worktrees")) != 1 || p1["has_more"] != true || p1["page_cursor"] == "" {
		t.Fatalf("page 1 = %v", p1)
	}
	p2 := list(`{"filter":{"kind":"worktree"},"limit":1,"cursor":"` + p1["page_cursor"].(string) + `"}`)
	w2 := readArr(p2, "worktrees")
	if len(w2) != 1 || w2[0].(map[string]any)["worktree_id"] != retained || p2["has_more"] != false {
		t.Fatalf("page 2 = %v", p2)
	}
}
