package runtime

import (
	"testing"
)

// TestPreservationAllowsSaveButNotNewWork pins the preservation-mode
// rules (shared by Pause and Handoff; Pause disables auto-launch): the
// predecessor may use read/edit/shell/wait/commit under existing
// permissions, and is denied delegation, new workflow steps, push/deploy
// and completed checkpoints.
func TestPreservationAllowsSaveButNotNewWork(t *testing.T) {
	for _, tool := range []string{"swarm_sync", "swarm_read", "swarm_checkpoint", "swarm_ask", "swarm_blocker", "swarm_artifact"} {
		if err := PreservationMCPAllowed(tool); err != nil {
			t.Fatalf("PreservationMCPAllowed(%q) = %v, want nil (save path)", tool, err)
		}
	}
	for _, tool := range []string{"swarm_spawn", "swarm_workflow"} {
		if err := PreservationMCPAllowed(tool); err == nil {
			t.Fatalf("PreservationMCPAllowed(%q) = nil, want denial (new work)", tool)
		}
	}
	for _, kind := range []CheckpointKind{Handoff, BlockedCkp, FailedCkp} {
		if err := PreservationCheckpointAllowed(kind); err != nil {
			t.Fatalf("PreservationCheckpointAllowed(%q) = %v, want nil", kind, err)
		}
	}
	for _, kind := range []CheckpointKind{Accepted, Progress, CompletedCkp} {
		if err := PreservationCheckpointAllowed(kind); err == nil {
			t.Fatalf("PreservationCheckpointAllowed(%q) = nil, want denial", kind)
		}
	}
	for _, tool := range []string{"Read", "Edit", "Write", "Bash", "Glob", "Grep"} {
		if err := PreservationNativeAllowed(tool); err != nil {
			t.Fatalf("PreservationNativeAllowed(%q) = %v, want nil (save path)", tool, err)
		}
	}
	for _, tool := range []string{"Agent", "Task", "Fork", "invoke_subagent", "Workflow", "dispatch_agent"} {
		if err := PreservationNativeAllowed(tool); err == nil {
			t.Fatalf("PreservationNativeAllowed(%q) = nil, want denial (delegation)", tool)
		}
	}
	for _, cmd := range []string{"git status", "git commit -m wip", "git diff --stat", "go test ./..."} {
		if err := PreservationCommandAllowed(cmd); err != nil {
			t.Fatalf("PreservationCommandAllowed(%q) = %v, want nil", cmd, err)
		}
	}
	for _, cmd := range []string{"git push origin main", "git push --tags", "npm run deploy", "kubectl apply -f k.yaml"} {
		if err := PreservationCommandAllowed(cmd); err == nil {
			t.Fatalf("PreservationCommandAllowed(%q) = nil, want denial (push/deploy)", cmd)
		}
	}
}
