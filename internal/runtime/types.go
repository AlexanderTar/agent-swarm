package runtime

import "github.com/AlexanderTar/swarmrun/internal/kinds"

// The enums live in internal/kinds so settings and catalog can use them without
// importing runtime (which would be a cycle once Store holds them).
type AgentKind = kinds.AgentKind
type Role = kinds.Role

const (
	Claude = kinds.Claude
	Codex  = kinds.Codex
	Agy    = kinds.Agy
	Cursor = kinds.Cursor
	Muse   = kinds.Muse
	Grok   = kinds.Grok
	Fake   = kinds.Fake
)

const (
	RoleOrchestrator = kinds.RoleOrchestrator
	RoleCoder        = kinds.RoleCoder
	RoleReviewer     = kinds.RoleReviewer
	RoleUIReviewer   = kinds.RoleUIReviewer
	RoleResearcher   = kinds.RoleResearcher
	RoleDebugger     = kinds.RoleDebugger
	RoleMechanical   = kinds.RoleMechanical
	RoleAdvisor      = kinds.RoleAdvisor
	RoleDesigner     = kinds.RoleDesigner
)

var AgentKinds = kinds.AgentKinds
var SettingsRoles = kinds.SettingsRoles
