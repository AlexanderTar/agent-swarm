// Package workflow is the declarative workflow DSL that drives self-contained
// tasks: types, templates, validation, resolution, the "## Workflow" brief
// section renderer, and the pure Next planner. It is a leaf package: no DB,
// no imports from other internal packages. Other packages consume it, so its
// exported API is a contract.
package workflow

// Gate names a completion requirement checked against an agent's checkpoint
// evidence.
type Gate string

const (
	GateTDD            Gate = "tdd"             // red (ok:false) before green (ok:true), same attempt
	GateCommit         Gate = "commit"          // git present, clean, sha == worktree HEAD
	GateVerify         Gate = "verify"          // every declared verify cmd recorded ok:true
	GateArtifactDesign Gate = "artifact:design" // a design artifact path under ~/.swarm/designs
	GateArtifactNotes  Gate = "artifact:notes"  // research notes under ~/.swarm/research
)

// Loop describes the fix-and-retry cycle attached to a review step.
type Loop struct {
	Fix         string `json:"fix"`                    // run-step id retried with findings
	MaxRounds   int    `json:"max_rounds,omitempty"`   // default 3, allowed 1..5
	OnExhausted string `json:"on_exhausted,omitempty"` // "escalate" (only value)
}

// Step is either a run step (Run set) or a review step (Review set).
type Step struct {
	ID     string   `json:"id"`
	Run    string   `json:"run,omitempty"` // role doing the work
	Gates  []Gate   `json:"gates,omitempty"`
	Review []string `json:"review,omitempty"` // reviewer roles, run in parallel
	Of     string   `json:"of,omitempty"`     // run step reviewed; default: nearest preceding run step
	Loop   *Loop    `json:"loop,omitempty"`   // review steps only
}

// Integration describes how a root's tasks are merged and verified.
type Integration struct {
	MergeOrder  []string `json:"merge_order,omitempty"` // task refs/keys
	Verify      []string `json:"verify,omitempty"`
	FinalReview []string `json:"final_review,omitempty"` // roles reviewing the integrated sha
}

// Spec is the "workflow" object on a swarm-tree node / item. Which fields are
// legal depends on the level: task -> Template/Steps/MaxRounds/Retries;
// story -> AfterTasks; root -> Integration.
type Spec struct {
	// Template names one of Templates, for Resolve to expand into Steps.
	// Resolve clears it once it has (a resolved Spec never carries both):
	// a caller that wants to display which template a task was resolved
	// from (e.g. "Workflow · tdd-reviewed · Running") must store the name
	// separately alongside the resolved Spec, not read it back off here.
	Template    string       `json:"template,omitempty"`
	Steps       []Step       `json:"steps,omitempty"`
	MaxRounds   int          `json:"max_rounds,omitempty"`  // overrides every loop's max_rounds
	Retries     *int         `json:"retries,omitempty"`     // auto-retries for crashed/failed step agents; default 1, 0..2
	AfterTasks  *Step        `json:"after_tasks,omitempty"` // story: one review step over the story's merged work
	Integration *Integration `json:"integration,omitempty"`
}

// Level is the swarm-tree/item level a Spec belongs to; it decides which
// fields are legal.
type Level string

const (
	LevelTask  Level = "task"
	LevelStory Level = "story"
	LevelRoot  Level = "root" // epics and bugs
)
