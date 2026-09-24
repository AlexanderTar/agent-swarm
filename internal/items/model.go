// Package items stores work items: hierarchy, dependencies, statuses (§5, §10.1).
package items

import (
	"fmt"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

type Type string

const (
	Epic  Type = "epic"
	Story Type = "story"
	Task  Type = "task"
	Bug   Type = "bug"
	Spike Type = "spike"
	Chore Type = "chore"
)

type Status string

const (
	Draft            Status = "draft"
	Ready            Status = "ready"
	InProgress       Status = "in_progress"
	Blocked          Status = "blocked"
	InReview         Status = "in_review"
	AwaitingApproval Status = "awaiting_approval"
	Done             Status = "done"
	Cancelled        Status = "cancelled"
)

var statusLabels = map[Status]string{
	Draft: "Draft", Ready: "Ready", InProgress: "In progress", Blocked: "Blocked",
	InReview: "In review", AwaitingApproval: "Awaiting approval", Done: "Done", Cancelled: "Cancelled",
}

// StatusLabel is the §17.2 label.
func StatusLabel(s Status) string { return statusLabels[s] }

const (
	ActorUser   = "user"
	ActorAgent  = "agent"
	ActorDaemon = "daemon"
)

// Actor says who asks for a change. Role and RootID are set by Phase 2 for agents.
type Actor struct {
	Kind    string `json:"kind"`
	AgentID string `json:"agent_id,omitempty"`
	Role    string `json:"role,omitempty"`
	RootID  string `json:"root_id,omitempty"`
	Via     string `json:"via,omitempty"` // menubar | board | cli
}

func User(via string) Actor { return Actor{Kind: ActorUser, Via: via} }
func Daemon() Actor         { return Actor{Kind: ActorDaemon} }
func Orchestrator(agentID, rootID string) Actor {
	return Actor{Kind: ActorAgent, AgentID: agentID, Role: "orchestrator", RootID: rootID}
}

func (a Actor) isOrchestrator() bool { return a.Kind == ActorAgent && a.Role == "orchestrator" }

type Progress struct {
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Unit  string `json:"unit"` // tasks | stories
}

// Unit is one batched execution unit of a task (spec C1/C4): a titled group
// of steps, gated independently. Mirrors the swarm-tree TreeUnit shape.
type Unit struct {
	Title string   `json:"title"`
	Steps []string `json:"steps"`
}

type Item struct {
	ID                string   `json:"id"`
	Key               string   `json:"key"`
	Type              Type     `json:"type"`
	ParentID          string   `json:"parent_id,omitempty"`
	ParentKey         string   `json:"parent_key,omitempty"`
	RootID            string   `json:"root_id"`
	RootKey           string   `json:"root_key"`
	Title             string   `json:"title"`
	Brief             string   `json:"brief"`
	Acceptance        []string `json:"acceptance"`
	Status            Status   `json:"status"`
	StatusBeforeBlock Status   `json:"status_before_block,omitempty"`
	Priority          int      `json:"priority"`
	RoleHint          string   `json:"role_hint,omitempty"`
	TddExempt         string   `json:"tdd_exempt,omitempty"`
	// Workflow is the resolved workflow (spec B2/B3); nil means a legacy
	// task with no workflow. Steps/Units/Solo/Verify are the task's own
	// execution script, separate from Workflow.Steps (the run/review DSL
	// steps): a task has either Steps or Units, never both (C1/C4).
	Workflow       *workflow.Spec `json:"workflow,omitempty"`
	Steps          []string       `json:"steps"`
	Units          []Unit         `json:"units"`
	Solo           string         `json:"solo,omitempty"`
	Verify         []string       `json:"verify"`
	Repos          []string       `json:"repos"` // top-level: confirmed repo ids; children: repo hints
	ReposVersion   int            `json:"repos_version"`
	SuggestedRepos []string       `json:"suggested_repos"`
	SpikeIntent    string         `json:"spike_intent,omitempty"`
	OriginSpikeID  string         `json:"origin_spike_id,omitempty"`
	LegacyKey      string         `json:"legacy_key,omitempty"`
	SortOrder      int            `json:"sort_order"`
	Revision       int            `json:"revision"`
	ArchivedAt     *time.Time     `json:"archived_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	// computed, not stored
	BlockedBy    []string  `json:"blocked_by"`
	Progress     *Progress `json:"progress,omitempty"`
	ActiveAgents int       `json:"active_agents"`
	OpenRequests int       `json:"open_requests"`
	Context      bool      `json:"context,omitempty"`
}

const (
	CodeBadRequest       = "bad_request"
	CodeNotFound         = "not_found"
	CodeConflict         = "conflict"
	CodeTransitionDenied = "transition_denied"
)

const StaleRevision = "This item changed elsewhere. Showing its latest status."

// Error carries an API error code (§7) and user-facing copy (§17.3).
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// article returns "an epic" / "a story".
func article(word string) string {
	if word != "" && (word[0] == 'e' || word[0] == 'a' || word[0] == 'i' || word[0] == 'o' || word[0] == 'u') {
		return "an " + word
	}
	return "a " + word
}
