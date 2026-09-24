package runtime

import "hash/fnv"

// roleEmoji is a quick visual tag for an agent's role in a crowded Ghostty
// tab list. RoleAdvisor is omitted deliberately: it never backs a live
// session (kinds.go), so sessionTitle's "🤖" fallback below is dead for it
// in practice and only there for a role string this map hasn't seen yet.
var roleEmoji = map[Role]string{
	RoleOrchestrator: "🧠",
	RoleCoder:        "👷",
	RoleReviewer:     "🔍",
	RoleUIReviewer:   "🎨",
	RoleResearcher:   "🔬",
	RoleDebugger:     "🐛",
	RoleMechanical:   "⚙️",
	RoleDesigner:     "🎨",
}

// groupPalette is what a root item's tree hashes into: a colour circle that
// stays the same for every agent under that tree for as long as the tree
// lives, without a schema column to store it in.
var groupPalette = []string{"🔴", "🟠", "🟡", "🟢", "🔵", "🟣", "🟤", "⚫", "⚪"}

// groupCircle picks groupPalette deterministically from rootItemID, so every
// agent in the same tree gets the same circle and different trees usually
// don't, with no coordination beyond the id itself.
func groupCircle(rootItemID string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(rootItemID))
	return groupPalette[h.Sum32()%uint32(len(groupPalette))]
}

// sessionTitle is the Ghostty tab title the reconciler pushes into tmux via
// RenameWindow: status, then role, then tree, then the session's own name
// last. The name stays last and behind exactly one space on purpose --
// Terminals.swift's AppleScript lookup matches a tab title that "ends with"
// a single space plus the exact tmux/agent name, which is the same
// name-can't-contain-a-space boundary the old literal "swarm:" prefix used
// to provide.
func sessionTitle(waiting bool, role Role, rootItemID, name string) string {
	status := "▶️"
	if waiting {
		status = "⏸️"
	}
	emoji := roleEmoji[role]
	if emoji == "" {
		emoji = "🤖"
	}
	return status + " " + emoji + " " + groupCircle(rootItemID) + " " + name
}
