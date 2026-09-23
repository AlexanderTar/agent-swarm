// Package kinds holds the enums every other package needs: the agent kinds and
// the roles. It imports nothing, so runtime can depend on settings and catalog
// without a cycle.
package kinds

type AgentKind string

const (
	Claude AgentKind = "claude"
	Codex  AgentKind = "codex"
	Agy    AgentKind = "agy"
	Cursor AgentKind = "cursor"
	Muse   AgentKind = "muse"
	Fake   AgentKind = "fake"
)

// AgentKinds is the settings order (claude first).
var AgentKinds = []AgentKind{Claude, Codex, Agy, Cursor, Muse}

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
	case Muse:
		return "Muse"
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
