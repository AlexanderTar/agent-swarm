package runtime

import "strings"

// Request-key prefixes for operations the daemon starts to keep within
// max_concurrent_agents (spec 2026-09-27-single-agent-limit-live). The key,
// never the note, is the marker: a note is pasted into the successor's
// prompt.
const (
	capacityKeyPrefix = "capacity:"
	resumeKeyPrefix   = "resume:"
)

// OperationReason names why the daemon started an operation: "capacity"
// (paused to fit the agent limit), "resume" (a manual resume waiting for a
// slot) or "" (anything else).
func OperationReason(requestKey string) string {
	switch {
	case strings.HasPrefix(requestKey, capacityKeyPrefix):
		return "capacity"
	case strings.HasPrefix(requestKey, resumeKeyPrefix):
		return "resume"
	}
	return ""
}
