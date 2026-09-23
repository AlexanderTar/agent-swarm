package hook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

const testWorktreesDir = "/Users/x/.swarm/worktrees"

// TestBlocksWorktreeMutation is scenarios G1-G6 and G9: blocksWorktreeMutation
// is a pure function, so these run directly against it, no Handle plumbing.
func TestBlocksWorktreeMutation(t *testing.T) {
	cases := []struct {
		name, cmd, dir string
		want           bool
	}{
		{"G1 remove under worktreesDir", `git worktree remove /Users/x/.swarm/worktrees/repo--slug`, testWorktreesDir, true},
		{"G2 add under worktreesDir with -C", `git -C /some/repo worktree add /Users/x/.swarm/worktrees/repo--new main`, testWorktreesDir, true},
		{"G3 remove outside worktreesDir", `git worktree remove /Users/x/GitHub/other-worktree`, testWorktreesDir, false},
		{"G4a list", `git worktree list`, testWorktreesDir, false},
		{"G4b prune", `git worktree prune`, testWorktreesDir, false},
		{"G5 guard disabled (empty WorktreesDir)", `git worktree remove /Users/x/.swarm/worktrees/repo--slug`, "", false},
		{"G6 status only, no mutation verb", `git status`, testWorktreesDir, false},
		{"G9 directory precedes the verb", `git -C /Users/x/.swarm/worktrees/repo--slug commit -m "worktree add fix"`, testWorktreesDir, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := blocksWorktreeMutation(c.cmd, c.dir); got != c.want {
				t.Fatalf("blocksWorktreeMutation(%q, %q) = %v, want %v", c.cmd, c.dir, got, c.want)
			}
		})
	}
}

// TestWorktreeGuardReasonMatchesTheCallersRole is G7/G8: the Handle-level
// wiring picks the reason by ParentAgentID.
func TestWorktreeGuardReasonMatchesTheCallersRole(t *testing.T) {
	cmd := `git worktree remove /Users/x/.swarm/worktrees/repo--slug`

	t.Run("G7 orchestrator (no parent)", func(t *testing.T) {
		h, ses := seed(t, 0, runtime.Running)
		h.WorktreesDir = testWorktreesDir
		input, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": "Bash",
			"tool_input": map[string]string{"command": cmd}})
		out, err := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses, input)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]map[string]string
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v: %s", err, out)
		}
		if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
			t.Fatalf("want deny, got %s", out)
		}
		if got := m["hookSpecificOutput"]["permissionDecisionReason"]; got != worktreeGuardOrchestrator {
			t.Fatalf("reason = %q, want worktreeGuardOrchestrator", got)
		}
	})

	t.Run("G8 worker (has a parent)", func(t *testing.T) {
		h, ses := seed(t, 0, runtime.Running)
		h.WorktreesDir = testWorktreesDir
		// The self-FK trick TestParentedAgentQuestionToolIsBlocked... already
		// uses: the FK is satisfied and the hook only tests for non-empty.
		if _, err := h.DB.ExecContext(context.Background(),
			`UPDATE agents SET parent_agent_id = 'agt_1' WHERE id = 'agt_1'`); err != nil {
			t.Fatal(err)
		}
		input, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": "Bash",
			"tool_input": map[string]string{"command": cmd}})
		out, err := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses, input)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]map[string]string
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v: %s", err, out)
		}
		if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
			t.Fatalf("want deny, got %s", out)
		}
		got := m["hookSpecificOutput"]["permissionDecisionReason"]
		if got != worktreeGuardWorker {
			t.Fatalf("reason = %q, want worktreeGuardWorker", got)
		}
		if strings.Contains(got, "swarm_worktree") {
			t.Fatalf("a worker cannot call swarm_worktree; the reason must not name it: %q", got)
		}
	})
}
