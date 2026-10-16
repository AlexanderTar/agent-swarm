// Package grokhome is the one place that knows how a Swarm-run grok process is
// isolated: its own GROK_HOME under the swarm home, with every foreign-agent
// compat surface switched off. Everything that execs grok (launch, catalog,
// usage, advisor, install) builds its environment here.
package grokhome

import "path/filepath"

// Bin is the grok CLI.
const Bin = "grok"

// Dir is the Swarm-owned grok home: <swarmHome>/grok.
func Dir(swarmHome string) string { return filepath.Join(swarmHome, "grok") }

// Env is the environment every grok subprocess runs with: GROK_HOME plus every
// compat switch grok reads for another agent's config (Claude, Cursor, Codex x
// skills, rules, agents, mcps, hooks, sessions) set to 0, so none of the
// operator's other agents' files reach a Swarm session. GROK_ASK_USER_QUESTION
// is deliberately not set: grok's question card stays on and Swarm answers it.
func Env(swarmHome string) map[string]string {
	env := map[string]string{"GROK_HOME": Dir(swarmHome)}
	for _, agent := range []string{"CLAUDE", "CURSOR", "CODEX"} {
		for _, surface := range []string{"SKILLS", "RULES", "AGENTS", "MCPS", "HOOKS", "SESSIONS"} {
			env["GROK_"+agent+"_"+surface+"_ENABLED"] = "0"
		}
	}
	return env
}
