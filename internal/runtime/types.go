// Package runtime holds agents, sessions and messages (Phase 2). Phase 1 only
// needs the enums, which settings and catalog share.
package runtime

type AgentKind string

const (
	Claude AgentKind = "claude"
	Codex  AgentKind = "codex"
	Agy    AgentKind = "agy"
	Cursor AgentKind = "cursor"
	Fake   AgentKind = "fake"
)

// AgentKinds is the settings order (claude first).
var AgentKinds = []AgentKind{Claude, Codex, Agy, Cursor}

func (k AgentKind) Display() string {
	switch k {
	case Claude:
		return "Claude"
	case Codex:
		return "Codex"
	case Agy:
		return "agy"
	case Cursor:
		return "Cursor"
	}
	return "Fake"
}

type Role string

const (
	RoleOrchestrator Role = "orchestrator"
	RoleCoder        Role = "coder"
	RoleReviewer     Role = "reviewer"
	RoleUIReviewer   Role = "ui_reviewer"
	RoleResearcher   Role = "researcher"
	RoleDebugger     Role = "debugger"
	RoleMechanical   Role = "mechanical"
	RoleAdvisor      Role = "advisor" // Settings only; never an agent row (L28)
)

var SettingsRoles = []Role{RoleOrchestrator, RoleAdvisor, RoleCoder, RoleReviewer, RoleUIReviewer, RoleResearcher, RoleDebugger, RoleMechanical}
