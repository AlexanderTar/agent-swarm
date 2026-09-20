// Package notifyrules holds the §17.5 notification table as plain data,
// leaf-only (it imports nothing), so internal/runtime's own test suite can
// validate the Args it builds against the real template a kind renders
// with — internal/notify imports internal/runtime (for NotifyInput), so
// runtime's tests can never import notify itself without a cycle. Moving
// only the data here, not the render logic, mirrors internal/kinds' own
// reason for existing (see its doc comment).
package notifyrules

import "regexp"

// Rule is one §17.5 row: a notification level, title, body template and the
// menubar category it groups under.
type Rule struct {
	Level, Title, Body, Category string
}

// Rules is the literal §17.5 table. item.created.bug is an internal lookup
// key only (a different title for a bug root); Raise writes "item.created"
// into the kind column and the SSE payload either way, because §17.5 and the
// menubar's category map know one kind.
var Rules = map[string]Rule{
	"agent.accepted":          {"info", "Task accepted", "{name} started {KEY}: {title}.", "swarm.info"},
	"item.completed":          {"info", "Task completed", "{KEY}: {title} is complete.", "swarm.info"},
	"item.created":            {"info", "Epic ready", "{SPIKE-KEY} produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready.", "swarm.item"},
	"item.created.bug":        {"info", "Bug ready", "{SPIKE-KEY} produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready.", "swarm.item"},
	"item.created.chore":      {"info", "Chore ready", "{SPIKE-KEY} produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready.", "swarm.item"},
	"agent.queued":            {"info", "Agent queued", "{name} starts when an agent slot becomes available.", "swarm.info"},
	"agent.paused":            {"attention", "Agent paused", "{name} is paused on {KEY}.", "swarm.agent"},
	"agent.interrupted":       {"attention", "Agent stopped", "{name} stopped before {KEY} finished.", "swarm.agent"},
	"agent.retried":           {"info", "Agent retrying", "{name} started attempt {N} on {KEY}.", "swarm.info"},
	"agent.failed":            {"attention", "Agent failed", "{name} couldn't finish {KEY}. Review the error.", "swarm.agent"},
	"agent.crashed":           {"attention", "Agent crashed", "{name} exited unexpectedly on {KEY}.", "swarm.agent"},
	"agent.stale":             {"attention", "No recent activity", "{name} has been quiet for 30 minutes on {KEY}.", "swarm.agent"},
	"agent.no_ack":            {"attention", "No response from agent", "{name} hasn't checkpointed in 2 minutes on {KEY}.", "swarm.agent"},
	"agent.no_recipient":      {"attention", "Message undelivered", "{name} has no live session on {KEY}; a message to it was never delivered.", "swarm.agent"},
	"agent.undeliverable":     {"attention", "Couldn't deliver messages", "{name} hasn't picked up {N} message(s).", "swarm.agent"},
	"agent.preflight_failed":  {"attention", "Couldn't start agent", "{reason}", "swarm.info"},
	"agent.fallback_used":     {"info", "Fallback agent used", "{name} switched to {agent} because {from} is out of usage.", "swarm.info"},
	"worktree.retained":       {"attention", "Worktree kept", "The worktree for {ROOT-KEY} has {detail} and was kept.", "swarm.info"},
	"tmux.unknown":            {"attention", "Unknown tmux session", "{name} is running but Swarm has no record of it.", "swarm.info"},
	"request.confirm_repos":   {"action", "Confirm repositories", "{KEY}: {name} proposes {N} repositories{expansion}.", "swarm.approval"},
	"request.close_spike":     {"action", "Close spike?", "{KEY}: {name} found nothing to build ({resolution}).", "swarm.approval"},
	"request.question":        {"action", "Answer needed", "{KEY}: {prompt}", "swarm.question"},
	"request.prompt":          {"action", "Approval needed", "{KEY}: {name} is waiting on approval: {prompt}", "swarm.approval"},
	"request.blocker":         {"action", "Blocker reported", "{KEY}: {name} is blocked: {prompt}", "swarm.agent"},
	"request.approve_section": {"action", "Section approval needed", `{KEY}: Review "{section}".`, "swarm.approval"},
	"request.approve_plan":    {"action", "Plan approval needed", "{KEY}: Review the proposed implementation plan.", "swarm.approval"},
	"request.approve_report":  {"action", "Report approval needed", "{KEY}: Review the root cause and fix plan.", "swarm.approval"},
	"request.accept_epic":     {"action", "Epic acceptance needed", "{KEY}: Review completed work and accept the epic.", "swarm.approval"},
	"request.accept_fix":      {"action", "Fix acceptance needed", "{KEY}: Review the fix and accept it.", "swarm.approval"},
}

// placeholderRe finds every {name} in a template.
var placeholderRe = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9 _-]*)\}`)

// Placeholders lists the distinct {name} tokens in tmpl, in first-seen order.
func Placeholders(tmpl string) []string {
	ms := placeholderRe.FindAllStringSubmatch(tmpl, -1)
	out := make([]string, 0, len(ms))
	seen := map[string]bool{}
	for _, m := range ms {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}
