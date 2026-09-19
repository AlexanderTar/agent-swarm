// Package runtime owns every row a Swarm agent produces: agents, sessions,
// messages, checkpoints, requests and artifacts (spec §5, §6, §10).
package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	PauseRoot                              bool // this session is the target of its own subtree pause
	PauseDeadlineAt                        *time.Time
	StopBlocks                             int
	NeedsCompactionNotice                  bool
	LastSeenAt, LastWakeAt                 *time.Time
	ExitCode                               *int
	StartedAt                              time.Time
	EndedAt                                *time.Time
	FailureText                            *string // full pane text failSession captured; nil unless this session failed
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

// UsageReader reports whether an agent kind is confirmed to be out of usage
// right now (docs/specs/2026-09-19-usage-fallback-agent.md). internal/usage
// already imports this package (for AgentKind), so this package can never
// import internal/usage back; internal/usagegate adapts a live
// *usage.Poller into this interface. A nil Store.Usage disables the
// usage-triggered fallback feature entirely -- matching every existing test
// and every environment where usage polling is off (SWARM_USAGE unset).
type UsageReader interface {
	Exhausted(ctx context.Context, kind AgentKind) bool
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
	Usage     UsageReader                                 // internal/usagegate.Gate; nil means the usage-fallback feature never triggers
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
	// lastAliveAt is P0-crash-3 (2026-09-19): the last reconcile tick that saw
	// each live session's pane present and correctly owned (§10.6's resolveDead
	// doc comment has the incident). Same D57 reasoning as the rest of this
	// block: a daemon restart just resets a session's grace window back to
	// counting from its own StartedAt, exactly like a session on its first tick.
	lastAliveAt map[string]time.Time
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

// IdemTx runs fn (a mutating Store method's existing tx body) inside one
// transaction, guarded by Idempotent (I11) on (sessionID, requestID). It
// exists so each mutating method's own tx-body needs only one extra wrapping
// line, not a hand-rolled marshal/unmarshal at every call site: on a cache
// miss, fn runs and *out is left exactly as fn set it (no round trip); on a
// cache hit (a genuine replay), fn never runs and *out is unmarshaled from
// the stored result instead, so the caller gets the identical typed value
// either way. ran reports whether fn actually executed this call, which
// matters wherever the caller has its own post-commit side effect (e.g.
// starting a tmux session) that must not repeat on replay -- see Spawn,
// Resume and Retry.
func IdemTx[T any](ctx context.Context, s *Store, sessionID, requestID, tool string, out *T, fn func(tx *sql.Tx) error) (ran bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		raw, err := s.Idempotent(ctx, tx, sessionID, requestID, tool, func() (any, error) {
			ran = true
			if err := fn(tx); err != nil {
				return nil, err
			}
			return *out, nil
		})
		if err != nil {
			return err
		}
		if ran {
			return nil
		}
		return json.Unmarshal(raw, out)
	})
	return ran, err
}

// PeekIdempotent reports whether (sessionID, requestID) already has a stored
// I11 result, without running or recording anything. It exists for a
// mutation whose real effect is an external side effect that cannot live
// inside a SQL transaction at all (starting or killing a tmux session, e.g.
// Resume/Cancel/Retry's own startSession/Tmux calls) and whose existing
// ordering -- perform the side effect, then commit the DB bookkeeping --
// must not change (committing the bookkeeping first and gating the side
// effect on the transaction's own "ran" flag, the way Spawn does, would flip
// a deliberate existing failure-safety property: today, if the side effect
// fails, the DB row is never touched). Called before that side effect: a hit
// means a genuine replay, so the side effect and the DB write both skip, and
// the cached typed result decodes straight into out.
func PeekIdempotent[T any](ctx context.Context, s *Store, sessionID, requestID string, out *T) (hit bool, err error) {
	if requestID == "" {
		return false, nil
	}
	// Must fail closed here, before any caller's external side effect: a
	// PeekIdempotent caller (Spawn/Resume/Cancel/Retry) runs its side effect
	// (start/kill a tmux session, flip agent state) *after* this returns, so
	// if the length check lived only in Idempotent -- reached afterward, at
	// the final DB write -- an over-long request_id would let the side
	// effect run, then fail with 400 once IdemTx got to it: a client error
	// response with the mutation already applied, and no idempotency record
	// to make the next (corrected) retry a no-op either.
	if len(requestID) > 64 {
		return false, &items.Error{Code: items.CodeBadRequest,
			Message: "request_id must be at most 64 characters."}
	}
	var stored string
	err = s.DB.QueryRowContext(ctx, `SELECT result_json FROM idempotency WHERE caller = ? AND request_id = ?`,
		sessionID, requestID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(stored), out)
}
