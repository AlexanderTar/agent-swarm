// Package runtime owns every row a Swarm agent produces: agents, sessions,
// messages, checkpoints, requests and artifacts (spec §5, §6, §10).
package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

type AgentState string
type SessionState string
type CheckpointKind string
type MessageKind string
type RequestKind string
type RequestState string
type WakeClass string

const (
	AgentQueued       AgentState = "queued"
	AgentActive       AgentState = "active"
	AgentFinished     AgentState = "finished"
	AgentAcknowledged AgentState = "acknowledged"
)

const (
	Spawning       SessionState = "spawning"
	Running        SessionState = "running"
	PauseRequested SessionState = "pause_requested"
	Quiescing      SessionState = "quiescing"
	Stopping       SessionState = "stopping"
	Paused         SessionState = "paused"
	Interrupted    SessionState = "interrupted"
	Completed      SessionState = "completed"
	Failed         SessionState = "failed"
	Crashed        SessionState = "crashed"
	Cancelled      SessionState = "cancelled"
)

var LiveStates = []SessionState{Spawning, Running, PauseRequested, Quiescing, Stopping}
var PausingStates = []SessionState{PauseRequested, Quiescing, Stopping}

func (s SessionState) Live() bool    { return slices.Contains(LiveStates, s) }
func (s SessionState) Pausing() bool { return slices.Contains(PausingStates, s) }

var sessionLabels = map[SessionState]string{
	Spawning: "Starting", Running: "Running", PauseRequested: "Pause requested",
	Quiescing: "Finishing current step", Stopping: "Finishing current step",
	Paused: "Paused", Interrupted: "Interrupted", Crashed: "Crashed",
	Failed: "Failed", Completed: "Completed", Cancelled: "Cancelled",
}

// Label is the §17.2 label.
func (s SessionState) Label() string { return sessionLabels[s] }

const (
	Accepted     CheckpointKind = "accepted"
	Progress     CheckpointKind = "progress"
	BlockedCkp   CheckpointKind = "blocked"
	Handoff      CheckpointKind = "handoff"
	CompletedCkp CheckpointKind = "completed"
	FailedCkp    CheckpointKind = "failed"
	Integrated   CheckpointKind = "integrated"
)

type Agent struct {
	ID, Name                          string
	Kind                              AgentKind
	Model, Effort                     string
	Role                              Role
	ItemID, RootItemID, ParentAgentID string
	AdvisorKind                       string
	AdvisorModel                      string
	AdvisorEffort                     string
	AdvisorMode                       string
	Brief                             string
	State                             AgentState
	PreflightError                    string // set only when the agent never spawned (contracts §3.2)
	CreatedAt                         time.Time
	FinishedAt                        *time.Time
}

type Session struct {
	ID, AgentID                            string
	Attempt, Generation                    int
	ProviderSessionID, TokenHash, TmuxName string
	Cwd, CwdKind                           string
	State                                  SessionState
	Waiting                                bool
	PauseScope                             string
	PauseDeadlineAt                        *time.Time
	StopBlocks                             int
	NeedsCompactionNotice                  bool
	LastSeenAt, LastWakeAt                 *time.Time
	ExitCode                               *int
	StartedAt                              time.Time
	EndedAt                                *time.Time
}

type GitRef struct {
	Repo, Branch, SHA string
	Dirty             bool
}

type Verify struct {
	Cmd, Phase string
	OK         bool
	Note       string
}

type Checkpoint struct {
	ID, SessionID, AgentID, ItemID string
	Kind                           CheckpointKind
	Attempt                        int
	Resolution, Summary            string
	Next, Blockers                 []string
	Git                            []GitRef
	Verification                   []Verify
	Artifacts, Processed           []string
	DaemonWritten                  bool
	CreatedAt                      time.Time
}

// Party is one message endpoint (spec §6.2).
type Party struct {
	Agent   string `json:"agent"`
	Name    string `json:"name"`
	Session string `json:"session"`
}

// Envelope is what swarm_sync returns (§6.2).
type Envelope struct {
	V           int             `json:"v"`
	MsgID       string          `json:"msg_id"`
	Seq         int64           `json:"seq"`
	Kind        MessageKind     `json:"kind"`
	Origin      string          `json:"origin"`
	From        *Party          `json:"from,omitempty"`
	To          Party           `json:"to"`
	RootItem    string          `json:"root_item"`
	Item        string          `json:"item,omitempty"`
	Correlation string          `json:"correlation_id,omitempty"`
	ReplyTo     string          `json:"reply_to,omitempty"`
	RequestID   string          `json:"request_id,omitempty"`
	Payload     json.RawMessage `json:"payload"`
}

type Message struct {
	ID                                 string
	Seq                                int64
	Kind                               MessageKind
	WakeClass                          WakeClass
	Priority                           int
	Origin, FromAgentID, FromSessionID string
	ToAgentID, RootItemID, ItemID      string
	CorrelationID, ReplyTo, RequestID  string
	Payload                            json.RawMessage
	State                              string
	DeliveryCount                      int
	CreatedAt                          time.Time
}

// ArtifactSection is one heading-delimited slice of an artifact revision. The
// JSON tags are the sections_json wire shape (Task 18 persists it; Task 16
// reads it back to resolve a section's title for the Request wire form).
type ArtifactSection struct {
	ID     string `json:"id"`
	Title  string `json:"heading"`
	SHA256 string `json:"sha256"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

type Artifact struct {
	ID, ItemID             string
	Kind, Path             string
	HeadRevision, Revision int
	Sections               []ArtifactSection
	CreatedBy              string
	CreatedAt              time.Time
}

type Request struct {
	ID                               string
	Kind                             RequestKind
	AgentID, SessionID, ItemID       string
	ArtifactID                       string
	SectionID, SectionSHA256, Prompt string
	Options                          json.RawMessage
	State                            RequestState
	Confirmed                        []string
	ArtifactRevision                 int
	Binding                          json.RawMessage
	ResponseText, RespondedVia       string
	RespondedAt                      *time.Time
	CreatedAt                        time.Time
}

type Advice struct {
	ID, SessionID, ItemID                       string
	AdvisorKind, AdvisorModel, AdvisorEffort    string
	Question, ContextPath                       string
	ContextChars                                int
	State, Answer, Error, Mode, SourceRequestID string
	DurationMs                                  int
	InputTokens, OutputTokens                   int
	CacheReadTokens, CacheWriteTokens           int
	CostUSD                                     *float64
	CreatedAt                                   time.Time
	FinishedAt                                  *time.Time
}

// Pane is a tmux pane snapshot.
type Pane struct {
	Session    string
	Dead       bool
	DeadStatus int
	Attached   bool
	Command    string
}

// Tmux is the spawner seam; *spawn.Spawner implements it (Task 4).
type Tmux interface {
	Start(ctx context.Context, name, cwd string, env map[string]string, argv []string) error
	Panes(ctx context.Context) ([]Pane, error)
	Capture(ctx context.Context, name string, lines int) (string, error)
	PasteLine(ctx context.Context, name, line string) error
	Keys(ctx context.Context, name string, keys ...string) error
	Env(ctx context.Context, name, key string) (string, error)
	Kill(ctx context.Context, name string) error
}

// Notifier raises a §17.5 notification. internal/notify implements it (Task 23).
type Notifier interface {
	Raise(ctx context.Context, tx *sql.Tx, n NotifyInput) error
}

type NotifyInput struct {
	Kind, AgentName, ItemKey, RequestID string
	Args                                map[string]string
}

// Advisor answers an agent's swarm_advise call. internal/advisor implements it
// (Tasks 25-27).
type Advisor interface {
	Mode(kind AgentKind, advisorKind AgentKind, advisorModel string, advisorCapable bool) string
	Ask(ctx context.Context, sessionID, question string, focus []string, wait time.Duration) (Advice, error)
}

// Store is the single P2 service. Fields are wired once in cmd/swarm/daemon.go.
// Task 12 adds Tmux/Adapters/Worktree/Notify/Bin/DaemonURL/OSEnv/BaseEnv/After;
// Task 26 adds Advisor.
type Store struct {
	DB        *db.DB
	Events    *events.Store
	Items     *items.Store
	Repos     *repos.Service
	Settings  *settings.Store
	Catalog   *catalog.Service
	Home      string // SWARM_HOME
	Now       func() time.Time
	Log       func(format string, args ...any)
	Tmux      Tmux
	Adapters  map[AgentKind]adapter.Adapter
	Worktree  *worktree.Service
	Notify    Notifier
	Advisor   Advisor                                     // internal/advisor.Service (Tasks 25-27); nil means no swarm_advise wiring
	Exec      execx.Runner                                // runner for prerun commands; nil means execx.Run
	Bin       string                                      // absolute path to the swarm binary
	DaemonURL string                                      // http://127.0.0.1:<cfg.Port>; never :7777 in a fixture
	OSEnv     func(string) string                         // nil means os.Getenv
	BaseEnv   func(func(string) string) map[string]string // always supplied by cmd/swarm; never defaulted here
	After     func(time.Duration) <-chan time.Time        // nil means time.After
	Go        func(func())                                // nil means `go f()`; tests run it inline

	// TmuxPath and TmuxSocketName are the two strings `swarm attach` and
	// httpapi's Ghostty fallback need, so neither ever writes "-L swarm" as a
	// literal (safety invariant S-1, R11). cmd/swarm sets both from the same
	// Spawner the daemon spawns through, so they can never disagree.
	TmuxPath       string
	TmuxSocketName string

	// bookkeeping is Task 20/22's in-memory state: how long ago each pausing
	// session was sent interrupt keys, how many idle-paste attempts a wake has
	// made, and the live SSE-side wake subscriptions. None of it has a schema
	// column, and none needs one (D57): a restart mid-pause simply resends the
	// interrupt keys or restarts the paste backoff, and the reconciler's
	// un-acked-message rule is the durable guarantee the user hears about a
	// stuck delivery either way.
	bookkeepingMu sync.Mutex
	interruptedAt map[string]time.Time
	pasteAttempts map[string]int
	wakeSubs      map[string][]chan string
}

// TmuxBin and TmuxSocket are the two readers httpapi's Ghostty fallback uses
// (R11, S-1): the socket always comes from here, never from a literal.
func (s *Store) TmuxBin() string    { return s.TmuxPath }
func (s *Store) TmuxSocket() string { return s.TmuxSocketName }

func (s *Store) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

// tx runs fn in one immediate transaction and wakes SSE subscribers after commit.
func (s *Store) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if err := s.DB.Tx(ctx, fn); err != nil {
		return err
	}
	s.Events.Notify()
	return nil
}
