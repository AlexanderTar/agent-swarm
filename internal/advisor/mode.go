// Package advisor picks the advisor mode for a session, builds the context an
// advisor reads, and runs the simulated advisor (spec §11.6, §17.3).
package advisor

import "github.com/AlexanderTar/agent-swarm/internal/runtime"

// Mode is §11.6. "" means no advisor: roles.advisor.model is "none". A Claude
// session with a Claude advisor whose model can advise (native tool support)
// gets "native"; every other combination that has an advisor at all gets
// "simulated".
func Mode(sessionKind, advisorKind runtime.AgentKind, advisorModel string, capable bool) string {
	if advisorModel == "" || advisorModel == "none" {
		return ""
	}
	if sessionKind == runtime.Claude && advisorKind == runtime.Claude && capable {
		return "native"
	}
	return "simulated"
}
