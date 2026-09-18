// Package httpapi: P2's runtime routes and wire types (contracts §3.2-3.4, §4).
// Every response type this batch produces is declared here, in one file, so
// Task 40's contract test has one home for them (P34).
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/notify"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/usage"
)

// ---- response wire types (contracts §3.2-3.4) ----

type sessionInfoWire struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Attempt    int    `json:"attempt"`
	Generation int    `json:"generation"`
	Waiting    bool   `json:"waiting"`
	Stale      bool   `json:"stale"`
	TmuxAlive  bool   `json:"tmux_alive"`
	StartedAt  int64  `json:"started_at"`
	EndedAt    *int64 `json:"ended_at"`
}

type advisorInfoWire struct {
	Kind   runtime.AgentKind `json:"kind"`
	Model  string            `json:"model"`
	Effort *string           `json:"effort"`
	Mode   string            `json:"mode"`
}

// agentNodeWire is contracts §3.2 AgentNode. Children/Finished are always []
// (W4), never null, even for a leaf.
type agentNodeWire struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Kind           runtime.AgentKind  `json:"kind"`
	Model          string             `json:"model"`
	Effort         *string            `json:"effort"`
	Role           runtime.Role       `json:"role"`
	ItemKey        string             `json:"item_key"`
	ItemTitle      string             `json:"item_title"`
	RootKey        string             `json:"root_key"`
	ParentName     *string            `json:"parent_name"`
	Advisor        *advisorInfoWire   `json:"advisor"`
	State          runtime.AgentState `json:"state"`
	Session        *sessionInfoWire   `json:"session"`
	PreflightError *string            `json:"preflight_error"`
	CreatedAt      int64              `json:"created_at"`
	FinishedAt     *int64             `json:"finished_at"`
	Children       []agentNodeWire    `json:"children"`
	Finished       []agentNodeWire    `json:"finished"`
}

type gitRefWire struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
	Dirty  bool   `json:"dirty,omitempty"`
}

func gitRefsOut(in []runtime.GitRef) []gitRefWire {
	out := make([]gitRefWire, 0, len(in))
	for _, g := range in {
		out = append(out, gitRefWire{Repo: g.Repo, Branch: g.Branch, SHA: g.SHA, Dirty: g.Dirty})
	}
	return out
}

type verifyWire struct {
	Cmd   string `json:"cmd"`
	Phase string `json:"phase"`
	OK    bool   `json:"ok"`
	Note  string `json:"note,omitempty"`
}

func verifiesOut(in []runtime.Verify) []verifyWire {
	out := make([]verifyWire, 0, len(in))
	for _, v := range in {
		out = append(out, verifyWire{Cmd: v.Cmd, Phase: v.Phase, OK: v.OK, Note: v.Note})
	}
	return out
}

// checkpointWire is contracts §3.3 Checkpoint.
type checkpointWire struct {
	ID            string       `json:"id"`
	ItemKey       string       `json:"item_key"`
	AgentName     *string      `json:"agent_name"`
	Kind          string       `json:"kind"`
	Attempt       int          `json:"attempt"`
	Resolution    *string      `json:"resolution"`
	Summary       string       `json:"summary"`
	Next          []string     `json:"next"`
	Blockers      []string     `json:"blockers"`
	Git           []gitRefWire `json:"git"`
	Verification  []verifyWire `json:"verification"`
	Artifacts     []string     `json:"artifacts"`
	DaemonWritten bool         `json:"daemon_written"`
	CreatedAt     int64        `json:"created_at"`
}

// notificationWire is contracts §3.4 Notification.
type notificationWire struct {
	ID        string  `json:"id"`
	Level     string  `json:"level"`
	Kind      string  `json:"kind"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	AgentName *string `json:"agent_name"`
	ItemKey   *string `json:"item_key"`
	RequestID *string `json:"request_id"`
	ReadAt    *int64  `json:"read_at"`
	CreatedAt int64   `json:"created_at"`
}

type meterWire struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	Window   string  `json:"window"`
	UsedPct  float64 `json:"used_pct"`
	ResetsAt *int64  `json:"resets_at"`
}

// usageWire is contracts §3.4 UsageSnapshot.
type usageWire struct {
	Agent       runtime.AgentKind `json:"agent"`
	Meters      []meterWire       `json:"meters"`
	HeadlineID  *string           `json:"headline_id"`
	Source      string            `json:"source"`
	Error       *string           `json:"error"`
	FetchedAt   int64             `json:"fetched_at"`
	AttemptedAt int64             `json:"attempted_at"`
	Stale       bool              `json:"stale"`
}

// adviceWire is contracts §3.2 Advice.
type adviceWire struct {
	ID              string  `json:"id"`
	SessionID       string  `json:"session_id"`
	ItemKey         string  `json:"item_key"`
	AdvisorKind     string  `json:"advisor_kind"`
	AdvisorModel    string  `json:"advisor_model"`
	AdvisorEffort   *string `json:"advisor_effort"`
	Question        string  `json:"question"`
	Answer          *string `json:"answer"`
	Error           *string `json:"error"`
	State           string  `json:"state"`
	Mode            string  `json:"mode"`
	DurationMs      *int    `json:"duration_ms"`
	InputTokens     *int    `json:"input_tokens"`
	OutputTokens    *int    `json:"output_tokens"`
	CacheReadTokens *int    `json:"cache_read_tokens"`
	CacheWriteTokens *int   `json:"cache_write_tokens"`
	CostUSD         *float64 `json:"cost_usd"`
	CreatedAt       int64   `json:"created_at"`
	FinishedAt      *int64  `json:"finished_at"`
}

type artifactSectionWire struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	SHA256 string `json:"sha256"`
}

// artifactWire is contracts §3.3 Artifact.
type artifactWire struct {
	ID           string                `json:"id"`
	ItemKey      string                `json:"item_key"`
	Kind         string                `json:"kind"`
	Path         string                `json:"path"`
	HeadRevision int                   `json:"head_revision"`
	Revision     int                   `json:"revision"`
	Sections     []artifactSectionWire `json:"sections"`
	CreatedAt    int64                 `json:"created_at"`
}

func (s *Server) artifactOut(ctx context.Context, a runtime.Artifact) (artifactWire, error) {
	key, err := s.itemKeyByID(ctx, a.ItemID)
	if err != nil {
		return artifactWire{}, err
	}
	secs := make([]artifactSectionWire, 0, len(a.Sections))
	for _, sec := range a.Sections {
		secs = append(secs, artifactSectionWire{ID: sec.ID, Title: sec.Title, SHA256: sec.SHA256})
	}
	return artifactWire{ID: a.ID, ItemKey: key, Kind: a.Kind, Path: a.Path, HeadRevision: a.HeadRevision,
		Revision: a.Revision, Sections: secs, CreatedAt: db.Millis(a.CreatedAt)}, nil
}

func (s *Server) artifactsOut(ctx context.Context, list []runtime.Artifact) ([]artifactWire, error) {
	out := make([]artifactWire, 0, len(list))
	for _, a := range list {
		w, err := s.artifactOut(ctx, a)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// requestWire is re-exported from runtime (contracts §3.3 Request); the wire
// shape is built once, in runtime.RequestWire, so httpapi never keeps a
// second copy of it.
type requestWire = runtime.RequestWire

// stateWire is contracts §4 GET /api/state: the menubar snapshot.
type stateWire struct {
	Agents        []agentNodeWire    `json:"agents"`
	Requests      []requestWire      `json:"requests"`
	Notifications notificationsWire  `json:"notifications"`
	Usage         []usageWire        `json:"usage"`
	ActiveCount   int                `json:"active_count"`
	Settings      any                `json:"settings"`
}

type notificationsWire struct {
	Unread int                `json:"unread"`
	Items  []notificationWire `json:"items"`
}

// spikeResponseWire is contracts §4 POST /api/spikes.
type spikeResponseWire struct {
	Item   itemWire      `json:"item"`
	Agent  agentNodeWire `json:"agent"`
	Queued bool          `json:"queued"`
}

// terminalWire is contracts §4 POST /api/agents/{name}/terminal.
type terminalWire struct {
	Tmux     string `json:"tmux"`
	OpenedBy string `json:"opened_by"`
}

// pauseAllWire is contracts §4 POST /api/pause-all.
type pauseAllWire struct {
	Requested int `json:"requested"`
}

// readAllWire is contracts §4 POST /api/notifications/read-all.
type readAllWire struct {
	Read int `json:"read"`
}

// ---- request-body types (§4 routes), each the exact readJSON target of its
// route; Tasks 32 and 33 fill the handlers but do not re-declare these. ----

type advisorChoiceBody struct {
	None   bool   `json:"none"`
	Agent  string `json:"agent"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// UnmarshalJSON accepts both the object form and the literal "none" (§7).
func (b *advisorChoiceBody) UnmarshalJSON(data []byte) error {
	if string(data) == `"none"` {
		b.None = true
		return nil
	}
	type alias advisorChoiceBody
	return json.Unmarshal(data, (*alias)(b))
}

// advisorChoiceFromBody maps the wire form to runtime.AdvisorChoice; nil means
// "use Settings" (the field was absent).
func advisorChoiceFromBody(b *advisorChoiceBody) *runtime.AdvisorChoice {
	if b == nil {
		return nil
	}
	if b.None {
		return &runtime.AdvisorChoice{None: true}
	}
	return &runtime.AdvisorChoice{Kind: runtime.AgentKind(b.Agent), Model: b.Model, Effort: b.Effort}
}

type spikeRequestBody struct {
	RequestID string             `json:"request_id"`
	Name      string             `json:"name"`
	Intent    string             `json:"intent"`
	Repos     []string           `json:"repos"`
	Agent     string             `json:"agent"`
	Model     string             `json:"model"`
	Effort    string             `json:"effort"`
	Advisor   *advisorChoiceBody `json:"advisor"`
	Request   string             `json:"request"`
}

type orchestratorRequestBody struct {
	RequestID    string             `json:"request_id"`
	Agent        string             `json:"agent"`
	Model        string             `json:"model"`
	Effort       string             `json:"effort"`
	Advisor      *advisorChoiceBody `json:"advisor"`
	Repos        []string           `json:"repos"`
	ReposVersion int                `json:"repos_version"`
	Name         string             `json:"name"`
}

type answerBody struct {
	Text string `json:"text"`
	Via  string `json:"via"`
}

type approveBody struct {
	SectionSHA256    string `json:"section_sha256"`
	ArtifactRevision int    `json:"artifact_revision"`
	Binding          any    `json:"binding"`
	Via              string `json:"via"`
}

type changesBody struct {
	Comment string `json:"comment"`
	Via     string `json:"via"`
}

type confirmReposBody struct {
	Repos        []string `json:"repos"`
	Comment      string   `json:"comment"`
	ReposVersion int      `json:"repos_version"`
	Via          string   `json:"via"`
}

type pauseBody struct {
	Scope string `json:"scope"`
}

type terminalBody struct{}

type usageRefreshBody struct {
	Agent string `json:"agent"`
}

// ---- helpers ----

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optMs(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	ms := db.Millis(*t)
	return &ms
}

func optInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// itemKeyByID and agentNameByID are one-line joins httpapi runs itself,
// matching the pattern items.go's itemOut already uses for origin_spike_key.
func (s *Server) itemKeyByID(ctx context.Context, id string) (string, error) {
	var key string
	err := s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, id).Scan(&key)
	return key, err
}

func (s *Server) agentNameByID(ctx context.Context, id string) (string, error) {
	var name string
	err := s.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, id).Scan(&name)
	return name, err
}

// staleAfter30Min is §3.2 SessionInfo.stale: no hook call or sync for 30 min
// while not waiting.
const staleAfter = 30 * time.Minute

func (s *Server) sessionInfoOut(ctx context.Context, ses runtime.Session, live map[string]bool) sessionInfoWire {
	w := sessionInfoWire{ID: ses.ID, State: string(ses.State), Attempt: ses.Attempt, Generation: ses.Generation,
		Waiting: ses.Waiting, TmuxAlive: live[ses.TmuxName], StartedAt: db.Millis(ses.StartedAt), EndedAt: optMs(ses.EndedAt)}
	if !ses.Waiting {
		last := ses.StartedAt
		if ses.LastSeenAt != nil {
			last = *ses.LastSeenAt
		}
		w.Stale = time.Since(last) > staleAfter
	}
	return w
}

// livePanes maps tmux session name to "alive" for every pane the spawner
// currently reports, one call per request rather than one per node.
func (s *Server) livePanes(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if s.RT == nil || s.RT.Tmux == nil {
		return out
	}
	panes, err := s.RT.Tmux.Panes(ctx)
	if err != nil {
		return out
	}
	for _, p := range panes {
		if !p.Dead {
			out[p.Session] = true
		}
	}
	return out
}

func (s *Server) advisorInfoOut(a runtime.Agent) *advisorInfoWire {
	if a.AdvisorMode == "" {
		return nil
	}
	return &advisorInfoWire{Kind: runtime.AgentKind(a.AdvisorKind), Model: a.AdvisorModel, Effort: optStr(a.AdvisorEffort), Mode: a.AdvisorMode}
}

// agentNodeOut builds one node (no children/finished yet: the caller assembles
// the tree once every node in a root is fetched).
func (s *Server) agentNodeOut(ctx context.Context, a runtime.Agent, live map[string]bool) (agentNodeWire, error) {
	itemKey, err := s.itemKeyByID(ctx, a.ItemID)
	if err != nil {
		return agentNodeWire{}, err
	}
	rootKey, err := s.itemKeyByID(ctx, a.RootItemID)
	if err != nil {
		return agentNodeWire{}, err
	}
	it, err := s.Items.Get(ctx, itemKey)
	if err != nil {
		return agentNodeWire{}, err
	}
	w := agentNodeWire{ID: a.ID, Name: a.Name, Kind: a.Kind, Model: a.Model, Effort: optStr(a.Effort), Role: a.Role,
		ItemKey: itemKey, ItemTitle: it.Title, RootKey: rootKey, Advisor: s.advisorInfoOut(a), State: a.State,
		PreflightError: optStr(a.PreflightError), CreatedAt: db.Millis(a.CreatedAt), FinishedAt: optMs(a.FinishedAt),
		Children: []agentNodeWire{}, Finished: []agentNodeWire{}}
	if a.ParentAgentID != "" {
		if name, err := s.agentNameByID(ctx, a.ParentAgentID); err == nil {
			w.ParentName = &name
		}
	}
	if a.PreflightError == "" {
		ses, err := s.RT.LatestSession(ctx, a.ID)
		if err == nil {
			si := s.sessionInfoOut(ctx, ses, live)
			w.Session = &si
		}
	}
	return w, nil
}

// isFinishedChild reports whether a child belongs under "finished" rather
// than "children" (contracts §3.2: completed, cancelled and acknowledged).
func isFinishedChild(a runtime.Agent, sessionState string) bool {
	if a.State == runtime.AgentAcknowledged {
		return true
	}
	return sessionState == string(runtime.Completed) || sessionState == string(runtime.Cancelled)
}

// buildAgentTree turns a flat agent list (one root) into the §3.2 tree: every
// node's live children live under "children", finished ones under "finished".
func (s *Server) buildAgentTree(ctx context.Context, flat []runtime.Agent, live map[string]bool) ([]agentNodeWire, error) {
	var roots []runtime.Agent
	for _, a := range flat {
		if a.ParentAgentID == "" {
			roots = append(roots, a)
		}
	}
	return s.agentNodesFor(ctx, roots, flat, live)
}

// agentNodesFor builds one node per entry in roots, each carrying its own
// children/finished resolved from flat (which need not itself start at a
// parentless agent — GET /api/items/{key} calls this with roots scoped to one
// item, but flat still holds the whole tree so descendants resolve).
func (s *Server) agentNodesFor(ctx context.Context, roots, flat []runtime.Agent, live map[string]bool) ([]agentNodeWire, error) {
	byParent := map[string][]runtime.Agent{}
	for _, a := range flat {
		if a.ParentAgentID != "" {
			byParent[a.ParentAgentID] = append(byParent[a.ParentAgentID], a)
		}
	}
	var build func(a runtime.Agent) (agentNodeWire, error)
	build = func(a runtime.Agent) (agentNodeWire, error) {
		node, err := s.agentNodeOut(ctx, a, live)
		if err != nil {
			return node, err
		}
		for _, child := range byParent[a.ID] {
			cn, err := build(child)
			if err != nil {
				return node, err
			}
			sessionState := ""
			if cn.Session != nil {
				sessionState = cn.Session.State
			}
			if isFinishedChild(child, sessionState) {
				node.Finished = append(node.Finished, cn)
			} else {
				node.Children = append(node.Children, cn)
			}
		}
		return node, nil
	}
	out := make([]agentNodeWire, 0, len(roots))
	for _, r := range roots {
		n, err := build(r)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// allAgents lists every agent, optionally scoped to one root key.
func (s *Server) allAgents(ctx context.Context, rootKey string) ([]runtime.Agent, error) {
	if rootKey != "" {
		return s.RT.AgentTree(ctx, rootKey)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT key FROM items WHERE id = root_id`)
	if err != nil {
		return nil, err
	}
	var roots []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		roots = append(roots, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []runtime.Agent
	for _, k := range roots {
		agents, err := s.RT.AgentTree(ctx, k)
		if err != nil {
			return nil, err
		}
		out = append(out, agents...)
	}
	return out, nil
}

func (s *Server) checkpointOut(ctx context.Context, c runtime.Checkpoint) (checkpointWire, error) {
	key, err := s.itemKeyByID(ctx, c.ItemID)
	if err != nil {
		return checkpointWire{}, err
	}
	w := checkpointWire{ID: c.ID, ItemKey: key, Kind: string(c.Kind), Attempt: c.Attempt,
		Resolution: optStr(c.Resolution), Summary: c.Summary, Next: orEmpty(c.Next), Blockers: orEmpty(c.Blockers),
		Git: gitRefsOut(c.Git), Verification: verifiesOut(c.Verification), Artifacts: orEmpty(c.Artifacts),
		DaemonWritten: c.DaemonWritten, CreatedAt: db.Millis(c.CreatedAt)}
	if c.AgentID != "" {
		if name, err := s.agentNameByID(ctx, c.AgentID); err == nil {
			w.AgentName = &name
		}
	}
	return w, nil
}

func notificationOut(n notify.Notification) notificationWire {
	w := notificationWire{ID: n.ID, Level: n.Level, Kind: n.Kind, Title: n.Title, Body: n.Body,
		AgentName: optStr(n.AgentName), ItemKey: optStr(n.ItemKey), RequestID: optStr(n.RequestID),
		CreatedAt: db.Millis(n.CreatedAt)}
	w.ReadAt = optMs(n.ReadAt)
	return w
}

func usageOut(snap usage.Snapshot) usageWire {
	meters := make([]meterWire, 0, len(snap.Meters))
	for _, m := range snap.Meters {
		meters = append(meters, meterWire{ID: m.ID, Label: m.Label, Window: m.Window, UsedPct: m.UsedPct, ResetsAt: optMs(m.ResetsAt)})
	}
	return usageWire{Agent: snap.Agent, Meters: meters, HeadlineID: optStr(snap.HeadlineID), Source: snap.Source,
		Error: optStr(snap.Error), FetchedAt: db.Millis(snap.FetchedAt), AttemptedAt: db.Millis(snap.AttemptedAt), Stale: snap.Stale}
}

func adviceOut(a runtime.Advice, itemKey string) adviceWire {
	w := adviceWire{ID: a.ID, SessionID: a.SessionID, ItemKey: itemKey, AdvisorKind: a.AdvisorKind,
		AdvisorModel: a.AdvisorModel, AdvisorEffort: optStr(a.AdvisorEffort), Question: a.Question,
		Answer: optStr(a.Answer), Error: optStr(a.Error), State: a.State, Mode: a.Mode,
		CreatedAt: db.Millis(a.CreatedAt), FinishedAt: optMs(a.FinishedAt)}
	w.DurationMs = optInt(a.DurationMs)
	w.InputTokens = optInt(a.InputTokens)
	w.OutputTokens = optInt(a.OutputTokens)
	w.CacheReadTokens = optInt(a.CacheReadTokens)
	w.CacheWriteTokens = optInt(a.CacheWriteTokens)
	w.CostUSD = a.CostUSD
	return w
}

// openRequestsWire lists open requests (runtime.Store.Requests's own filter),
// optionally scoped to one item and/or one kind, in the §3.3 wire shape.
func (s *Server) openRequestsWire(ctx context.Context, itemKey, kind string) ([]requestWire, error) {
	list, err := s.RT.Requests(ctx, itemKey)
	if err != nil {
		return nil, err
	}
	out := make([]requestWire, 0, len(list))
	for _, r := range list {
		if kind != "" && string(r.Kind) != kind {
			continue
		}
		w, err := s.RT.RequestWireByID(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// ---- routes ----

func (s *Server) runtimeRoutes() []route {
	return []route{
		{"GET", "/api/state", authDaemon, s.state},
		{"GET", "/api/agents", authDaemon, s.agents},
		{"GET", "/api/items/{key}/checkpoints", authDaemon, s.checkpoints},
	}
}

// agentIORoutes is filled by Task 34, in agentio.go. spawnRoutes and
// requestRoutes are filled by Tasks 32 and 33, in spawn.go and requests.go.

// rtNotWired is the guard the three read routes below share: a plain P1
// harness (no P2 services wired) hits it instead of a nil-pointer panic. Every
// real daemon (cmd/swarm, Task 35) always wires RT/Notify/Usage, so this only
// ever fires in a test that deliberately builds a Server without them.
func (s *Server) rtNotWired(w http.ResponseWriter) bool {
	if s.RT == nil || s.Notify == nil || s.Usage == nil {
		s.writeErr(w, apiErr(http.StatusInternalServerError, "internal", "The daemon isn't fully wired yet."))
		return true
	}
	return false
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	if s.rtNotWired(w) {
		return
	}
	ctx := r.Context()
	flat, err := s.allAgents(ctx, "")
	if err != nil {
		s.writeErr(w, err)
		return
	}
	live := s.livePanes(ctx)
	agents, err := s.buildAgentTree(ctx, flat, live)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	activeCount := 0
	for _, a := range flat {
		if a.State == runtime.AgentQueued || a.State == runtime.AgentActive {
			activeCount++
		}
	}
	reqs, err := s.openRequestsWire(ctx, "", "")
	if err != nil {
		s.writeErr(w, err)
		return
	}
	unread, err := s.Notify.Unread(ctx)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	list, err := s.Notify.List(ctx, false, 20)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	notifs := make([]notificationWire, 0, len(list))
	for _, n := range list {
		notifs = append(notifs, notificationOut(n))
	}
	snaps, err := s.Usage.Snapshots(ctx)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	usages := make([]usageWire, 0, len(snaps))
	for _, sn := range snaps {
		usages = append(usages, usageOut(sn))
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stateWire{Agents: agents, Requests: reqs,
		Notifications: notificationsWire{Unread: unread, Items: notifs}, Usage: usages,
		ActiveCount: activeCount, Settings: cfg})
}

func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	if s.rtNotWired(w) {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	flat, err := s.allAgents(ctx, q.Get("root"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if q.Get("state") != "all" {
		filtered := flat[:0:0]
		for _, a := range flat {
			if a.State == runtime.AgentQueued || a.State == runtime.AgentActive {
				filtered = append(filtered, a)
			}
		}
		flat = filtered
	}
	live := s.livePanes(ctx)
	nodes, err := s.buildAgentTree(ctx, flat, live)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

func (s *Server) checkpoints(w http.ResponseWriter, r *http.Request) {
	if s.RT == nil {
		s.writeErr(w, apiErr(http.StatusInternalServerError, "internal", "The daemon isn't fully wired yet."))
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	limit := 50
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	var before time.Time
	if b := q.Get("before"); b != "" {
		if ms, err := strconv.ParseInt(b, 10, 64); err == nil {
			before = db.FromMillis(ms)
		}
	}
	list, err := s.RT.Checkpoints(ctx, r.PathValue("key"), limit, before)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out := make([]checkpointWire, 0, len(list))
	for _, c := range list {
		cw, err := s.checkpointOut(ctx, c)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		out = append(out, cw)
	}
	writeJSON(w, http.StatusOK, out)
}
