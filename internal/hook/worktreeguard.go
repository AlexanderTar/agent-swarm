package hook

import (
	"regexp"
	"strings"
)

// swarmWorktreeMutation matches an agent trying to add or remove a worktree
// under the daemon's directory itself. It is deliberately narrow: `remove` and
// `add` are the two operations that desynchronize the worktrees table, and
// `remove` under ~/.swarm/worktrees is the measured actor behind ~210 stale
// rows (docs/specs/2026-09-23-worktree-cleanup-enforcement.md §1). `prune` and
// `list` are read-only-ish bookkeeping and are not blocked.
var swarmWorktreeMutation = regexp.MustCompile(`\bgit\b[^|;&]*\bworktree\b[^|;&]*\b(remove|add)\b`)

// worktreeGuardOrchestrator is the reason an orchestrator sees: it can see and
// call the swarm_worktree tool.
const worktreeGuardOrchestrator = "[swarm] ~/.swarm/worktrees belongs to the daemon. " +
	"Use the swarm_worktree tool (op: \"create\" / \"remove\") instead — its remove refuses a dirty " +
	"or unmerged tree and keeps the daemon's records straight, which raw git does not. " +
	"If superpowers:finishing-a-development-branch told you a path under worktrees/ is yours to " +
	"clean up: for a swarm worktree that is wrong. Skip that step."

// worktreeGuardWorker is the reason a parented agent sees. swarm_worktree is
// Roles: orchestratorRole (mcpserver/orchestrator.go:18), so a coder cannot
// see or call it: naming that tool to a worker is a dead end, and workers
// following superpowers Step 6 are the population most likely to hit this
// guard. Same ParentAgentID != "" test nativeQuestionRelay already uses.
const worktreeGuardWorker = "[swarm] ~/.swarm/worktrees belongs to the daemon, not to you — " +
	"your orchestrator and the daemon remove worktrees. " +
	"If superpowers:finishing-a-development-branch told you a path under worktrees/ is yours to " +
	"clean up: for a swarm worktree that is wrong. Skip that step and write your " +
	"`completed` checkpoint instead."

// blocksWorktreeMutation reports whether cmd mutates a worktree under
// worktreesDir. worktreesDir empty disables the check.
//
// The directory must appear *after* the remove/add verb, not merely somewhere
// in the command: `git -C ~/.swarm/worktrees/x commit -m "worktree add fix"`
// contains both the directory and a matching verb, and blocking a legitimate
// commit would be worse than the leak this guard prevents.
func blocksWorktreeMutation(cmd, worktreesDir string) bool {
	if worktreesDir == "" {
		return false
	}
	m := swarmWorktreeMutation.FindStringIndex(cmd)
	return m != nil && strings.Contains(cmd[m[1]:], worktreesDir)
}
