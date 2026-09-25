package runtime

import (
	"regexp"
	"slices"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Preservation policy (spec §3): shared by Pause and Handoff (Pause
// disables auto-launch). The predecessor stops new work, finishes or
// interrupts its atomic action, and may use read/edit/shell/wait/commit
// under existing permissions. Denied: delegation, new workflow steps,
// push/deploy, and completed checkpoints. Anything preservation denies
// must fail closed with an explicit error, never a silent skip.

// preservationSaveMCP are the MCP tools a preserving predecessor may use:
// sync/read the state, checkpoint handoff/blocked/failed, ask/withdraw,
// report a blocker, snapshot specs/plans via the artifact registry.
var preservationSaveMCP = []string{
	"swarm_sync", "swarm_read", "swarm_checkpoint", "swarm_ask", "swarm_blocker", "swarm_artifact",
}

// preservationDeniedMCP starts new work: delegation mints agents outside
// the replaced identity, and a workflow step belongs to the engine.
var preservationDeniedMCP = []string{"swarm_spawn", "swarm_workflow"}

// PreservationMCPAllowed gates one MCP tool name in preservation mode.
func PreservationMCPAllowed(tool string) error {
	if slices.Contains(preservationDeniedMCP, tool) {
		return &items.Error{Code: items.CodeConflict,
			Message: "preservation: " + tool + " starts new work; finish saving instead."}
	}
	if !slices.Contains(preservationSaveMCP, tool) {
		return &items.Error{Code: items.CodeConflict,
			Message: "preservation: " + tool + " is not a save tool; finish saving instead."}
	}
	return nil
}

// PreservationCheckpointAllowed gates checkpoint kinds in preservation
// mode: handoff, blocked and failed only (mirrors pauseAllowedKinds --
// the daemon enforces it whatever the caller claims).
func PreservationCheckpointAllowed(kind CheckpointKind) error {
	if slices.Contains(pauseAllowedKinds, kind) {
		return nil
	}
	return &items.Error{Code: items.CodeConflict,
		Message: "preservation: only handoff, blocked and failed checkpoints while preserving."}
}

// preservationDelegationNative mirrors the hook's native fork set plus the
// Workflow tool: every one of these starts work outside this session.
var preservationDelegationNative = []string{
	"Agent", "Task", "Fork", "fork", "invoke_subagent", "subagent",
	"dispatch_agent", "spawn_agent", "Workflow", "workflow",
}

// PreservationNativeAllowed gates one native tool name in preservation
// mode: everything but delegation is a save-path tool (read, edit, shell,
// wait, commit) kept under the session's existing permissions.
func PreservationNativeAllowed(toolName string) error {
	if slices.Contains(preservationDelegationNative, toolName) {
		return &items.Error{Code: items.CodeConflict,
			Message: "preservation: " + toolName + " delegates; finish saving instead."}
	}
	return nil
}

var pushRe = regexp.MustCompile(`(^|[;&|()\s])git\s+push\b`)
var deployRe = regexp.MustCompile(`(?i)(^|[;&|()\s])(deploy\b|kubectl\s+(apply|create)|terraform\s+apply|fly\s+deploy|railway\s+(up|deploy))`)

// PreservationCommandAllowed gates one shell command in preservation mode:
// push/deploy leave the machine, everything else (inspect, stage, commit,
// snapshot, wait on owned commands) stays under existing permissions.
func PreservationCommandAllowed(command string) error {
	if pushRe.MatchString(command) {
		return &items.Error{Code: items.CodeConflict,
			Message: "preservation: git push is denied while preserving; commit locally instead."}
	}
	if deployRe.MatchString(command) {
		return &items.Error{Code: items.CodeConflict,
			Message: "preservation: deploy is denied while preserving; finish saving instead."}
	}
	if strings.TrimSpace(command) == "" {
		return &items.Error{Code: items.CodeBadRequest, Message: "preservation: empty command."}
	}
	return nil
}
