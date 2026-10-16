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
	Grok   AgentKind = "grok"
	Fake   AgentKind = "fake"
)

// AgentKinds is the settings order (claude first).
var AgentKinds = []AgentKind{Claude, Codex, Agy, Cursor, Muse, Grok}

// MuseAdvisorEnabled keeps muse without an advisor. Its read-only argv
// (advisor.museAdvisorCommand) and parser are built and unit-tested, but the
// write block was never proved live: muse answered 429 (quota exhausted until
// 2026-10-19) before any tool ran. Enabling it is CHORE-102, once a live write
// test passes. The advisor runner, the settings validation and the catalog's
// advisor_enabled flag all follow this one constant.
const MuseAdvisorEnabled = false

// AdvisorEnabled reports whether the kind can be picked as the advisor.
func (k AgentKind) AdvisorEnabled() bool {
	return k != Muse || MuseAdvisorEnabled
}

func (k AgentKind) Display() string {
	switch k {
	case Claude:
		return "Claude"
	case Codex:
		return "Codex"
	case Agy:
		return "Antigravity"
	case Cursor:
		return "Cursor"
	case Muse:
		return "Muse"
	case Grok:
		return "Grok"
	case Fake:
		return "Fake"
	}
	return "unknown agent"
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
	RoleDesigner     Role = "designer"
)

var SettingsRoles = []Role{RoleOrchestrator, RoleAdvisor, RoleCoder, RoleReviewer, RoleUIReviewer, RoleResearcher, RoleDebugger, RoleMechanical, RoleDesigner}
