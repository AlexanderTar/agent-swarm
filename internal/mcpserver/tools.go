package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

// roleOverridesOut normalizes an Agent's RoleOverrides for the wire: a nil
// map (the common case -- most agents have never set one) marshals as an
// empty object, not JSON null, matching every other empty-collection field
// this package returns (e.g. readTool's out["items"] = []items.Item{}).
func roleOverridesOut(m map[runtime.Role]settings.RoleDefault) map[runtime.Role]settings.RoleDefault {
	if m == nil {
		return map[runtime.Role]settings.RoleDefault{}
	}
	return m
}

func objSchema(props string) json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"additionalProperties":true}`)
}

func objSchemaRequired(props string, required []string) json.RawMessage {
	reqJSON, _ := json.Marshal(required)
	return json.RawMessage(fmt.Sprintf(`{"type":"object","required":%s,"properties":{%s},"additionalProperties":true}`, reqJSON, props))
}

func decode(args json.RawMessage, v any) error {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return json.Unmarshal(args, v)
}

// ---------- swarm_sync ----------

func syncTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_sync",
		Description: "Acknowledge handled messages (`ack`) and fetch the agent's inbox: assignments, questions, control notices and advice, newest-priority first. Messages delivered three times without an ack are listed in `unacked` by id and kind only.",
		Schema: objSchema(`"ack":{"type":"array","items":{"type":"string"},"description":"Message ids already handled"},
			"limit":{"type":"integer","description":"Max messages to return"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Ack   []string `json:"ack"`
				Limit int      `json:"limit"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			res, err := s.RT.Sync(ctx, c.SessionID, in.Ack, in.Limit)
			if err != nil {
				return nil, err
			}
			if res.Messages == nil {
				res.Messages = []runtime.Envelope{}
			}
			if res.Unacked == nil {
				res.Unacked = []runtime.UnackedRef{}
			}
			// Batch 2 successor recovery: every generation's first sync
			// carries the durable assignment plus the recovery bundle
			// (operation, manifest path/hash, predecessor session, cursor,
			// workflow binding), independent of acked inbox rows.
			rec, err := s.RT.SyncRecovery(ctx, c.SessionID)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"messages":      res.Messages,
				"unacked":       res.Unacked,
				"more":          res.More,
				"session_state": res.SessionState,
				"assignment":    assignmentOut(rec.Assignment),
				"recovery":      recoveryBundleOut(rec.Recovery),
				"first_sync":    rec.FirstSync,
			}, nil
		},
	}
}

// ---------- swarm_checkpoint ----------

func checkpointTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_checkpoint",
		Description: "Record progress: accepted, progress, blocked, handoff, completed or failed, with the verification evidence TDD requires.",
		Schema: objSchemaRequired(`"kind":{"type":"string"},"item":{"type":"string"},"summary":{"type":"string"},
			"resolution":{"type":"string"},"next":{"type":"array"},"blockers":{"type":"array"},
			"git":{"type":"array","items":{"type":"object","properties":{
				"repo":{"type":"string"},"branch":{"type":"string"},"sha":{"type":"string"},"dirty":{"type":"boolean"}},
				"required":["repo","sha"]}},
			"verification":{"type":"array","items":{"type":"object","properties":{
				"cmd":{"type":"string"},"phase":{"type":"string"},"ok":{"type":"boolean"},"note":{"type":"string"},
				"unit":{"type":"integer"}},
				"required":["cmd","ok"]}},
			"artifacts":{"type":"array"},"processed":{"type":"array"},
			"verdict":{"type":"string","enum":["","pass","changes_requested","blocked"]},
			"findings":{"type":"array","items":{"type":"object","properties":{
				"severity":{"type":"string","enum":["critical","major","minor","nit"]},
				"file":{"type":"string","description":"omit for a package-wide finding"},
				"line":{"type":"integer"},
				"unit":{"type":"integer"},"summary":{"type":"string"}},
				"required":["severity","summary"]}},
			"request_id":{"type":"string"}`, []string{"kind", "summary"}),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Kind         string             `json:"kind"`
				ItemKey      string             `json:"item"`
				Summary      string             `json:"summary"`
				Resolution   string             `json:"resolution"`
				Next         []string           `json:"next"`
				Blockers     []string           `json:"blockers"`
				Git          []runtime.GitRef   `json:"git"`
				Verification []runtime.Verify   `json:"verification"`
				Artifacts    []string           `json:"artifacts"`
				Processed    []string           `json:"processed"`
				Verdict      string             `json:"verdict"`
				Findings     []workflow.Finding `json:"findings"`
				RequestID    string             `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			res, err := s.RT.WriteCheckpoint(ctx, c.SessionID, runtime.CheckpointInput{
				Kind: runtime.CheckpointKind(in.Kind), ItemKey: in.ItemKey, Summary: in.Summary,
				Resolution: in.Resolution, Next: in.Next, Blockers: in.Blockers,
				Git: in.Git, Verification: in.Verification, Artifacts: in.Artifacts, Processed: in.Processed,
				Verdict: in.Verdict, Findings: in.Findings,
				RequestID: in.RequestID,
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"checkpoint_id": res.CheckpointID, "item_status": res.ItemStatus,
				"item_revision": res.ItemRevision}, nil
		},
	}
}

// ---------- swarm_ask ----------

func askTool(s *Server) ToolDef {
	return ToolDef{
		Name: "swarm_ask",
		Description: "Request an approval, propose repos to confirm, forward a native answer, or withdraw an earlier ask. " +
			"Returns at once with the request id; the answer arrives later as a message.",
		Schema: objSchemaRequired(`"kind":{"type":"string","description":"Kind: question, approval, confirm_repos, native_prompt, native_answer, or withdraw. question is refused for claude and agy (they have a native question tool Swarm hooks instead); cursor, muse and codex keep it, since their native question tool is either not hookable or not yet confirmed. native_prompt for_msg gets a child's approval question's native prompt; native_answer ref forwards the user's observed decision."},"prompt":{"type":"string"},"options":{"type":"array"},
			"artifact":{"type":"string","description":"Artifact id for approval kinds"},"section":{"type":"string","description":"Section id for per-section approval"},"withdraw":{"type":"string"},
			"repos":{"type":"array","items":{"type":"object","properties":{
				"repo":{"type":"string","description":"repository id, e.g. from a swarm_read repos search -- not its name or path"},
				"reason":{"type":"string"},"source":{"type":"string","enum":["","dropped"]}},
				"required":["repo","reason"]}},
			"expansion":{"type":"array","items":{"type":"object","properties":{
				"repo":{"type":"string","description":"repository id, e.g. from a swarm_read repos search -- not its name or path"},
				"reason":{"type":"string"}},
				"required":["repo","reason"]}},
			"for_msg":{"type":"string","description":"kind native_prompt: the msg_id of a child's approval question addressed to you"},
			"ref":{"type":"string","description":"kind native_answer: the request_id or msg_id a native_prompt was issued for"},
			"decision":{"type":"string","enum":["approve","request_changes"],"description":"kind native_answer: the user's observed decision"},
			"comment":{"type":"string","description":"kind native_answer: free text for request_changes, or when the adapter reports no answer text"},
			"request_id":{"type":"string"}`,
			[]string{"kind"}),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Kind      string                  `json:"kind"`
				Prompt    string                  `json:"prompt"`
				Options   []string                `json:"options"`
				Artifact  string                  `json:"artifact"`
				Section   string                  `json:"section"`
				Withdraw  string                  `json:"withdraw"`
				Repos     []runtime.ReposProposal `json:"repos"`
				Expansion []runtime.ReposProposal `json:"expansion"`
				ForMsg    string                  `json:"for_msg"`
				Ref       string                  `json:"ref"`
				Decision  string                  `json:"decision"`
				Comment   string                  `json:"comment"`
				RequestID string                  `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			req, err := s.RT.Ask(ctx, c.SessionID, runtime.AskInput{
				Kind: in.Kind, Prompt: in.Prompt, Options: in.Options, ArtifactID: in.Artifact,
				SectionID: in.Section, Withdraw: in.Withdraw, Repos: in.Repos, Expansion: in.Expansion,
				ForMsg: in.ForMsg, Ref: in.Ref, Decision: in.Decision, Comment: in.Comment, RequestID: in.RequestID,
			})
			if err != nil {
				return nil, err
			}
			return requestOut(req), nil
		},
	}
}

// requestOut is §8.1's swarm_ask result, exactly {"request_id","state"} - no
// echoed-back kind/prompt/artifact_id/section_id (fix round 2, item 1: the
// caller already sent those, so echoing them isn't a spec omission worth
// second-guessing). Task 13a adds "native_prompt" for the approval and
// confirm_repos kinds, whose Request carries one; every other kind's
// NativePrompt is nil and the key is omitted.
func requestOut(r runtime.Request) map[string]any {
	out := map[string]any{"request_id": r.ID, "state": r.State}
	if r.NativePrompt != nil {
		out["native_prompt"] = r.NativePrompt
		// 2026-09-26 fix (native-railway-tracing finding): a stale skill can
		// bind the answer and never forward it, since nothing else in this
		// result says there is a next step. Spell it out here too.
		out["next"] = fmt.Sprintf("Print the summary in chat first, not in the question. Then show native_prompt "+
			"with your native question tool now (one question per call, verbatim, no added text). Once the user "+
			"answers, call swarm_ask kind:\"native_answer\", ref:%q, decision:\"approve\"|\"request_changes\" "+
			"forwarding only what the user picked, never a decision they did not make.", r.ID)
	}
	return out
}

// ---------- swarm_blocker ----------

func blockerTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_blocker",
		Description: "Log a genuine blocker that requires user or orchestrator intervention to proceed.",
		Schema: objSchemaRequired(`"reason":{"type":"string"},"options":{"type":"array","items":{"type":"string"}}`,
			[]string{"reason"}),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Reason  string   `json:"reason"`
				Options []string `json:"options"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Reason) == "" {
				return nil, &items.Error{Code: items.CodeBadRequest, Message: "reason is required."}
			}
			req, err := s.RT.AskBlocker(ctx, c.SessionID, in.Reason, in.Options)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"request_id": req.ID,
				"status":     "blocked",
			}, nil
		},
	}
}

// ---------- swarm_send ----------

func sendTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_send",
		Description: "Send a short message to another agent in the same top-level item, or to your parent.",
		Schema: objSchemaRequired(`"to":{"type":"string","description":"Recipient agent name, or 'parent' for your orchestrator"},"kind":{"type":"string","enum":["question","answer","finding"],"description":"Message kind; omitted or relay stores as finding"},
			"body":{"type":"string"},"reply_to":{"type":"string","description":"Required for kind answer: the msg_id of the question it answers"},
			"options":{"type":"array","items":{"type":"string"},"description":"Choices for kind question; at most 10, each at most 200 chars"},
			"approval":{"type":"boolean","description":"kind question to parent only: request explicit Approve/Request changes"},
			"request_id":{"type":"string"}`,
			[]string{"to", "body"}),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				To        string   `json:"to"`
				Kind      string   `json:"kind"`
				Body      string   `json:"body"`
				ReplyTo   string   `json:"reply_to"`
				Options   []string `json:"options"`
				Approval  bool     `json:"approval"`
				RequestID string   `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			kind := in.Kind
			if kind == "" {
				kind = "finding"
			}
			switch kind {
			case "question", "answer", "finding":
			default:
				return nil, fmt.Errorf("kind must be question, answer or finding, got %q", in.Kind)
			}
			if in.Approval {
				if kind != "question" || in.To != "parent" {
					return nil, fmt.Errorf("approval is only for kind question to parent")
				}
				id, err := s.RT.SendApproval(ctx, c.SessionID, in.Body, in.RequestID)
				if err != nil {
					return nil, err
				}
				return map[string]any{"msg_id": id}, nil
			}
			id, err := s.RT.Send(ctx, c.SessionID, in.To, runtime.MessageKind(kind), in.Body, in.ReplyTo, in.RequestID, in.Options...)
			if err != nil {
				return nil, err
			}
			return map[string]any{"msg_id": id}, nil
		},
	}
}

// ---------- swarm_read ----------

// readInput is §8.1's swarm_read input, read directly from
// ~/.superpowers/specs/2026-09-17-agent-swarm-go-orchestrator.md §8.1 (fix
// round 1): {refs?, filter?, repos?: {q?, group?, limit?}, since_seq?, fields?}.
// fields (selective projection) is deliberately not implemented in this fix
// round — see the note on readTool below.
type readInput struct {
	Refs   []string `json:"refs"`
	Filter *struct {
		Root, Type, Status, Q string
		// Kind selects collections (Batch 4/F6): item|agent|checkpoint|
		// artifact|request|worktree, validated by parseReadKind. Empty
		// means every collection, the historical behavior.
		Kind string `json:"kind"`
	} `json:"filter"`
	Repos *struct {
		Q     string `json:"q"`
		Group string `json:"group"`
		Limit int    `json:"limit"`
	} `json:"repos"`
	// a pointer distinguishes "since_seq omitted" (no event scan at all) from
	// an explicit "since_seq":0 (a fresh cursor: scan every event ever issued).
	SinceSeq *int64 `json:"since_seq"`
	// Fields projects allowlisted fields per collection (identity always
	// retained); unknown fields are refused by validateReadFields.
	Fields []string `json:"fields"`
	// Limit/Cursor bound filter reads (Batch 4/F6): default 50, max 200,
	// stable (updated_at, id) keyset cursor. Refs are explicit lookups and
	// since_seq is an event poll; neither is paged.
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor"`
	// Batch 2 successor recovery: agent-scoped paginated checkpoint history
	// with full fields/provenance plus readable requests, worktrees and
	// artifact revisions/hashes. Handled by recoveryOut (recovery.go); the
	// general refs/filter/repos/since_seq paths below are untouched.
	Recovery *recoveryInput `json:"recovery"`
}

func (s *Server) agentOut(ctx context.Context, a runtime.Agent) map[string]any {
	out := map[string]any{"name": a.Name, "kind": a.Kind, "model": a.Model, "role": a.Role, "state": a.State,
		"role_overrides": roleOverridesOut(a.RoleOverrides)}
	if a.ParentAgentID != "" {
		if parent, err := s.RT.AgentByID(ctx, a.ParentAgentID); err == nil {
			out["parent"] = parent.Name
		} else {
			out["parent"] = nil
		}
	} else {
		out["parent"] = nil
	}
	if s.RT != nil {
		if step, ok, err := s.RT.StepForAgent(ctx, a.ID); err == nil && ok {
			out["step"] = step
		}
	}
	return out
}

// checkpointOut takes itemKey rather than resolving c.ItemID itself: every
// other surface returns a KEY in a field named "item" (httpapi's checkpoint
// wire type resolves one explicitly; the checkpoint.created event already
// carries one straight from WriteCheckpoint), and both of this function's
// callers already have the exact key that produced c on hand -- Checkpoints
// filters by that item's row, so c always belongs to it (Fix R-2: this used
// to return c.ItemID, the opaque checkpoints.item_id column, leaving an
// agent unable to correlate its own checkpoint back to the item it was on).
func checkpointOut(itemKey string, c runtime.Checkpoint) map[string]any {
	return map[string]any{"item": itemKey, "kind": c.Kind, "summary": c.Summary, "created_at": c.CreatedAt}
}

func artifactOut(a runtime.Artifact) map[string]any {
	sections := make([]artifactSectionWire, len(a.Sections))
	for i, sec := range a.Sections {
		sections[i] = artifactSectionWire{ID: sec.ID, Title: sec.Title, SHA256: sec.SHA256}
	}
	return map[string]any{"artifact_id": a.ID, "kind": a.Kind, "revision": a.Revision, "sections": sections}
}

// readTool is §8.1's catch-all state read, read directly from the real spec
// (fix round 1 — the batch briefs never had the literal shapes). Batch 4
// (F6) bounds it: validated filter.kind, allowlisted field projection,
// default-50/max-200 filter pagination with a stable cursor, per-ref errors
// that keep the successes, and explicit unsupported-filter errors.
func readTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_read",
		Description: "Read items, artifacts, agents and checkpoints by ref or filter, search repos, and get changes since a cursor. filter.kind selects collections (item, agent, checkpoint, artifact, request, worktree); fields projects allowlisted fields; filter reads page with limit (default 50, max 200) and an opaque cursor; unresolvable refs return per-ref errors without failing the call.",
		Schema: objSchema(`"refs":{"type":"array","items":{"type":"string"},"description":"Item, artifact, agent or checkpoint refs to fetch"},
			"filter":{"type":"object","description":"Filter listing by root, type, status or query","properties":{"kind":{"type":"string"},"root":{"type":"string"},"type":{"type":"string"},"status":{"type":"string"},"q":{"type":"string"}}},
			"repos":{"type":"object","description":"Repo search","properties":{"q":{"type":"string"},"group":{"type":"string"},"limit":{"type":"integer"}}},
			"since_seq":{"type":"integer","description":"Event cursor to list changes since"},"fields":{"type":"array","items":{"type":"string"}},
			"limit":{"type":"integer"},"cursor":{"type":"string"},
			"recovery":{"type":"object","properties":{"agent":{"type":"string"},"limit":{"type":"integer"},"cursor":{"type":"string"}}}`),
		Unbound: true,
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var rawArgs map[string]json.RawMessage
			if len(args) > 0 {
				if err := json.Unmarshal(args, &rawArgs); err != nil {
					return nil, err
				}
			}
			// Unknown filter keys are refused here, before decoding drops
			// them silently.
			if err := checkFilterKeys(rawArgs["filter"]); err != nil {
				return nil, err
			}
			var in readInput
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			kind := ""
			if in.Filter != nil {
				kind = in.Filter.Kind
			}
			var err error
			if kind, err = parseReadKind(kind); err != nil {
				return nil, err
			}
			want := func(name string) bool { return kind == "" || kind == name }
			returned := readKinds
			if kind != "" {
				returned = []string{kind}
			}
			if err := validateReadFields(returned, in.Fields); err != nil {
				return nil, err
			}
			out := map[string]any{
				"items": []any{}, "artifacts": []any{}, "agents": []any{},
				"checkpoints": []any{}, "requests": []any{}, "worktrees": []any{},
				"errors": []any{}, "repos": []repos.Repo{}, "confirmed_repos": []repos.Repo{},
				"page_cursor": "", "has_more": false,
			}
			var collectedItems []items.Item
			itemSeen := map[string]bool{}
			addItem := func(it items.Item) {
				if itemSeen[it.Key] {
					return
				}
				itemSeen[it.Key] = true
				collectedItems = append(collectedItems, it)
			}
			fail := func(ref string, err error) {
				out["errors"] = append(out["errors"].([]any), refError(ref, err))
			}

			// refs mix item keys, art_/req_/wt_ ids and agent names (§8.1);
			// route each by what actually resolves, since there is no
			// single shared id space. A ref that resolves nowhere is a
			// per-ref error entry (F6): the valid results still come back.
			for _, ref := range in.Refs {
				if strings.HasPrefix(ref, "art_") {
					art, _, err := s.RT.ArtifactMarkdown(ctx, ref, 0, "")
					if err != nil {
						fail(ref, err)
						continue
					}
					if want("artifact") {
						projected, err := projectFields("artifact", artifactOut(art), in.Fields)
						if err != nil {
							return nil, err
						}
						out["artifacts"] = append(out["artifacts"].([]any), projected)
					}
					continue
				}
				if strings.HasPrefix(ref, "req_") {
					req, err := s.RT.RequestByID(ctx, ref)
					if err != nil {
						fail(ref, err)
						continue
					}
					if want("request") {
						// The general read names kind alongside identity
						// (the ask result's tighter {request_id,state}
						// shape stays untouched above).
						projected, err := projectFields("request", map[string]any{
							"request_id": req.ID, "kind": string(req.Kind), "state": string(req.State),
						}, in.Fields)
						if err != nil {
							return nil, err
						}
						out["requests"] = append(out["requests"].([]any), projected)
					}
					continue
				}
				if strings.HasPrefix(ref, "wt_") {
					wt, err := s.RT.Worktree.Get(ctx, ref)
					if err != nil {
						fail(ref, err)
						continue
					}
					if want("worktree") {
						projected, err := projectFields("worktree", worktreeOut(wt), in.Fields)
						if err != nil {
							return nil, err
						}
						out["worktrees"] = append(out["worktrees"].([]any), projected)
					}
					continue
				}
				if it, err := s.RT.Items.Get(ctx, ref); err == nil {
					if want("item") {
						addItem(it)
					}
					continue
				}
				a, err := s.RT.Agent(ctx, ref)
				if err != nil {
					if a, err := s.RT.AgentByID(ctx, ref); err != nil {
						fail(ref, err)
						continue
					} else if want("agent") {
						projected, err := projectFields("agent", s.agentOut(ctx, a), in.Fields)
						if err != nil {
							return nil, err
						}
						out["agents"] = append(out["agents"].([]any), projected)
						continue
					}
					continue
				}
				if want("agent") {
					projected, err := projectFields("agent", s.agentOut(ctx, a), in.Fields)
					if err != nil {
						return nil, err
					}
					out["agents"] = append(out["agents"].([]any), projected)
				}
			}
			if in.Filter != nil {
				// root/type/status/q select items; pairing them with a
				// non-item kind is an explicit unsupported-filter error.
				if kind != "" && kind != "item" &&
					(in.Filter.Root != "" || in.Filter.Type != "" || in.Filter.Status != "" || in.Filter.Q != "") {
					return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
						"unsupported filter: root/type/status/q select items, which kind %q excludes; drop the item filter or use kind \"item\".", kind)}
				}
				if want("item") {
					// Flat view: paginated reads return the match set
					// exactly (newest first), never tree-enrichment
					// context rows that would break page accounting.
					list, _, err := s.RT.Items.List(ctx, items.ListFilter{
						View: "flat",
						Type: items.Type(in.Filter.Type), Status: items.Status(in.Filter.Status),
						Q: in.Filter.Q, Root: in.Filter.Root,
					})
					if err != nil {
						return nil, err
					}
					var fresh []items.Item
					for _, it := range list {
						if itemSeen[it.Key] {
							continue
						}
						itemSeen[it.Key] = true
						fresh = append(fresh, it)
					}
					sortFilterItems(fresh)
					page, next, err := pageFilterItems(fresh, parseReadLimit(in.Limit), in.Cursor)
					if err != nil {
						return nil, err
					}
					collectedItems = append(collectedItems, page...)
					out["page_cursor"] = next
					out["has_more"] = next != ""
				}
			}
			// checkpoints: the latest one per item resolved above.
			if want("checkpoint") {
				for _, it := range collectedItems {
					cps, err := s.RT.Checkpoints(ctx, it.Key, 1, time.Time{})
					if err != nil {
						return nil, err
					}
					if len(cps) > 0 {
						projected, err := projectFields("checkpoint", checkpointOut(it.Key, cps[0]), in.Fields)
						if err != nil {
							return nil, err
						}
						out["checkpoints"] = append(out["checkpoints"].([]any), projected)
					}
				}
			}

			if in.Repos != nil {
				limit := in.Repos.Limit
				if limit <= 0 {
					limit = 30
				}
				var found []repos.Repo
				var err error
				switch {
				case in.Repos.Q != "":
					found, err = s.RT.Repos.Search(ctx, in.Repos.Q, limit)
				case in.Repos.Group != "":
					var views []repos.GroupView
					if views, err = s.RT.Repos.GroupViews(ctx); err == nil {
						for _, v := range views {
							if v.Name == in.Repos.Group {
								found = v.Repos
							}
						}
					}
				default:
					found, err = s.RT.Repos.Search(ctx, "", limit)
				}
				if err != nil {
					return nil, err
				}
				if found == nil {
					found = []repos.Repo{}
				}
				out["repos"] = found
			}

			if !c.Unbound {
				rootID, err := callerRootID(ctx, s, c)
				if err != nil {
					return nil, err
				}
				confirmed, err := s.RT.ConfirmedRepos(ctx, rootID)
				if err != nil {
					return nil, err
				}
				if confirmed == nil {
					confirmed = []repos.Repo{}
				}
				out["confirmed_repos"] = confirmed
			}

			// since_seq: only items/agents/checkpoints changed after that events.seq
			// (agent.changed is never actually published anywhere in the codebase —
			// a pre-existing gap outside this batch's file ownership — so an agent
			// changing never surfaces here; item.changed and checkpoint.created are
			// real and do). since_seq is a *int64 so an omitted field (no scan, just
			// hand back the current head as a cursor to start polling from) is
			// distinguishable from an explicit 0 (scan every event ever issued).
			latest, err := s.RT.Events.Latest(ctx)
			if err != nil {
				return nil, err
			}
			cursor := latest
			reset := false
			if in.SinceSeq != nil {
				since := *in.SinceSeq
				expired, err := s.RT.Events.Expired(ctx, since)
				if err != nil {
					return nil, err
				}
				reset = expired
				if !expired {
					evs, err := s.RT.Events.After(ctx, since, 1000)
					if err != nil {
						return nil, err
					}
					for _, e := range evs {
						switch e.Type {
						case events.ItemChanged:
							var p struct {
								Key string `json:"key"`
							}
							if json.Unmarshal(e.Payload, &p) == nil && p.Key != "" {
								if it, err := s.RT.Items.Get(ctx, p.Key); err == nil && want("item") {
									addItem(it)
								}
							}
						case events.AgentChanged:
							var p struct {
								Name string `json:"name"`
							}
							if json.Unmarshal(e.Payload, &p) == nil && p.Name != "" {
								if a, err := s.RT.Agent(ctx, p.Name); err == nil && want("agent") {
									projected, perr := projectFields("agent", s.agentOut(ctx, a), in.Fields)
									if perr != nil {
										return nil, perr
									}
									out["agents"] = append(out["agents"].([]any), projected)
								}
							}
						case events.CheckpointCreated:
							var p struct {
								Item string `json:"item"`
							}
							if json.Unmarshal(e.Payload, &p) == nil && p.Item != "" {
								if cps, err := s.RT.Checkpoints(ctx, p.Item, 1, time.Time{}); err == nil && len(cps) > 0 && want("checkpoint") {
									projected, perr := projectFields("checkpoint", checkpointOut(p.Item, cps[0]), in.Fields)
									if perr != nil {
										return nil, perr
									}
									out["checkpoints"] = append(out["checkpoints"].([]any), projected)
								}
							}
						}
					}
					// ponytail: a page stops short of `latest` at the 1000-event cap, so
					// the cursor must not skip past what was actually scanned; below the
					// cap, `latest` is already exactly where the scan left off.
					if len(evs) == 1000 {
						cursor = evs[len(evs)-1].Seq
					}
				} else {
					cursor = latest
				}
			}
			itemsOut := make([]any, 0, len(collectedItems))
			for _, it := range collectedItems {
				data, err := json.Marshal(it)
				if err != nil {
					return nil, err
				}
				var itemMap map[string]any
				if err := json.Unmarshal(data, &itemMap); err != nil {
					return nil, err
				}
				ws, hasWf, err := s.RT.WorkflowFor(ctx, it.Key)
				if err != nil {
					return nil, err
				}
				if hasWf {
					runs := ws.Runs
					if runs == nil {
						runs = []runtime.WorkflowRunView{}
					}
					itemMap["workflow_state"] = map[string]any{
						"state":      ws.State,
						"round":      ws.Round,
						"escalation": ws.Escalation,
						"runs":       runs,
					}
					crew := []map[string]any{}
					for _, r := range ws.Runs {
						if r.AgentName != "" {
							crew = append(crew, map[string]any{
								"agent": r.AgentName,
								"role":  r.Role,
								"step":  r.StepID,
								"state": r.State,
							})
						}
					}
					itemMap["crew"] = crew
				}
				projected, err := projectFields("item", itemMap, in.Fields)
				if err != nil {
					return nil, err
				}
				itemsOut = append(itemsOut, projected)
			}
			out["items"] = itemsOut
			out["reset"] = reset
			out["cursor"] = cursor
			if in.Recovery != nil {
				rec, err := s.recoveryOut(ctx, c, in.Recovery)
				if err != nil {
					return nil, err
				}
				out["recovery"] = rec
			}
			return out, nil
		},
	}
}

// callerRootID resolves the caller's top-level item id from its agent row.
func callerRootID(ctx context.Context, s *Server, c Caller) (string, error) {
	a, err := s.RT.Agent(ctx, c.AgentName)
	if err != nil {
		return "", err
	}
	return a.RootItemID, nil
}

// ---------- swarm_kb ----------

const kbSearchGetSchema = `"op":{"type":"string","enum":["search","get"]},"q":{"type":"string"},
	"slug":{"type":"string"},"limit":{"type":"integer"}`

// kbFullSchema is the bound-caller schema: its own op enum, not
// kbSearchGetSchema's, because §8.1 gives bound callers "write" too - reusing
// the unbound enum here schema-blocked write for any real MCP client that
// validates arguments before sending (fix round 2 full-pass finding, same
// class as item 3's swarm_artifact enum).
const kbFullSchema = `"op":{"type":"string","enum":["search","get","write"]},"q":{"type":"string"},
	"slug":{"type":"string"},"limit":{"type":"integer"},
	"subdir":{"type":"string","enum":["specs","plans","decisions","notes"]},"filename":{"type":"string"},"title":{"type":"string"},"body":{"type":"string"},
	"request_id":{"type":"string"}`

func kbReadOnlyTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_kb",
		Description: "Search or read the Swarm knowledge base.",
		Schema:      objSchemaRequired(kbSearchGetSchema, []string{"op"}),
		Unbound:     true,
		Handler:     kbHandler(s, false),
	}
}

func kbFullTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_kb",
		Description: "Search, read or write a knowledge base document: a durable decision, spec or note other agents can find later.",
		Schema:      objSchemaRequired(kbFullSchema, []string{"op"}),
		Handler:     kbHandler(s, true),
	}
}

// kbToolFor is only ever called for a bound caller (ToolsFor's unbound branch
// returns kbReadOnlyTool directly), so it need not re-check c.Unbound.
func kbToolFor(s *Server) ToolDef { return kbFullTool(s) }

func kbHandler(s *Server, canWrite bool) func(context.Context, Caller, json.RawMessage) (any, error) {
	return func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
		var in struct {
			Op        string `json:"op"`
			Q         string `json:"q"`
			Slug      string `json:"slug"`
			Limit     int    `json:"limit"`
			Subdir    string `json:"subdir"`
			Filename  string `json:"filename"`
			Title     string `json:"title"`
			Body      string `json:"body"`
			RequestID string `json:"request_id"`
		}
		if err := decode(args, &in); err != nil {
			return nil, err
		}
		switch in.Op {
		case "search":
			hits, err := s.KB.Search(ctx, in.Q, in.Limit)
			if err != nil {
				return nil, err
			}
			// §8.1: search's result is the bare array, not {"hits": [...]}.
			if hits == nil {
				hits = []kb.Hit{}
			}
			return hits, nil
		case "get":
			doc, err := s.KB.Get(ctx, in.Slug)
			if err != nil {
				return nil, err
			}
			// §8.1: get's result is exactly {"markdown"} - not the full DocView
			// (fix round 2, item 2).
			return map[string]any{"markdown": doc.Markdown}, nil
		case "write":
			if !canWrite {
				return nil, errors.New("read-only: an unbound caller cannot write to the knowledge base")
			}
			return kbWrite(ctx, s, c.SessionID, in.RequestID, in.Subdir, in.Filename, in.Title, in.Body)
		default:
			return nil, fmt.Errorf("op must be search, get or write, got %q", in.Op)
		}
	}
}

// kbWrite writes a new markdown document under KB.Dir and re-syncs the index.
// subdir/filename come from an agent, so ".." is refused before it reaches a
// path.Join. The schema's subdir enum is advisory only for a non-validating
// client (decode is plain json.Unmarshal, no schema check), so it is
// re-enforced here the same way op's enum is enforced by the switch above.
//
// The idempotency guard (I11) wraps only the file write, not KB.Sync: Sync
// issues several of its own un-batched queries/execs against the same shared
// *sql.DB (internal/kb's Index.Sync), and SQLite allows only one writer at a
// time, so calling it from inside the transaction runtime.IdemTx already has
// open would self-block waiting on a lock the outer transaction itself holds.
// Sync runs after that transaction commits instead, unconditionally (cache
// hit or miss) -- it only reconciles the search index against whatever is on
// disk, so re-running it on a replay is always safe, the same way a plain
// post-commit read is elsewhere in this codebase (e.g. items.Store.Update's
// own final Get).
func kbWrite(ctx context.Context, s *Server, sessionID, requestID, subdir, filename, title, body string) (any, error) {
	switch subdir {
	case "specs", "plans", "decisions", "notes":
	default:
		return nil, fmt.Errorf("bad_request: subdir must be specs, plans, decisions or notes, got %q", subdir)
	}
	if strings.Contains(filename, "..") {
		return nil, errors.New("bad_request: filename cannot contain \"..\"")
	}
	if filename == "" {
		return nil, errors.New("bad_request: filename is required")
	}
	if !strings.HasSuffix(filename, ".md") {
		filename += ".md"
	}
	dir := filepath.Join(s.KB.Dir, filepath.Clean(subdir))
	path := filepath.Join(dir, filepath.Clean(filename))
	rel, err := filepath.Rel(s.KB.Dir, path)
	if err != nil {
		return nil, err
	}
	slug := strings.TrimSuffix(filepath.ToSlash(rel), ".md")

	var out map[string]string
	if _, err := runtime.IdemTx(ctx, s.RT, sessionID, requestID, "swarm_kb", &out, func(tx *sql.Tx) error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		content := fmt.Sprintf("---\ntitle: %q\n---\n\n%s\n", title, body)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		out = map[string]string{"slug": slug}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.KB.Sync(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------- swarm_advise ----------

const minWait, maxWait, defaultWait = 0, 50, 45

func advisorTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_advise",
		Description: "Ask the simulated advisor a question and wait briefly for its answer, or move on and receive it as a later message.",
		Schema: objSchemaRequired(`"question":{"type":"string"},"focus":{"type":"array","items":{"type":"string"}},
			"wait_seconds":{"type":"integer"}`,
			[]string{"question"}),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			if s.Advisor == nil {
				return nil, errors.New("no advisor is configured for this agent")
			}
			var in struct {
				Question    string   `json:"question"`
				Focus       []string `json:"focus"`
				WaitSeconds *int     `json:"wait_seconds"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			wait := defaultWait
			if in.WaitSeconds != nil {
				wait = *in.WaitSeconds
			}
			if wait < minWait {
				wait = minWait
			}
			if wait > maxWait {
				wait = maxWait
			}
			adv, err := s.Advisor.Ask(ctx, c.SessionID, in.Question, in.Focus, time.Duration(wait)*time.Second)
			if err != nil {
				return nil, err
			}
			// §8.1: result is {"advice_id","state","answer"?,"error"?} - "error"
			// added 2026-09-18 so a failed run's diagnostic (adv.Error, e.g. "Fake
			// can't run as a read-only advisor") isn't discarded; state:"failed"
			// alone doesn't tell the caller why.
			out := map[string]any{"advice_id": adv.ID, "state": adv.State, "answer": adv.Answer}
			if adv.Error != "" {
				out["error"] = adv.Error
			}
			return out, nil
		},
	}
}

// ---------- swarm_instructions ----------

func instructionsTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_instructions",
		Description: "Read or update durable Swarm instructions injected into agents.",
		Schema: objSchemaRequired(`"op":{"type":"string","enum":["get","set"]},"instructions":{"type":"string"}`,
			[]string{"op"}),
		Unbound: true,
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Op           string `json:"op"`
				Instructions string `json:"instructions"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			st := s.Settings
			if st == nil && s.RT != nil {
				st = s.RT.Settings
			}
			if st == nil {
				return nil, errors.New("settings store not available")
			}
			switch in.Op {
			case "get":
				cfg, err := st.Get(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{"instructions": cfg.Instructions}, nil
			case "set":
				if c.Unbound {
					return nil, errors.New("read-only: an unbound caller cannot update swarm instructions")
				}
				cfg, err := st.Get(ctx)
				if err != nil {
					return nil, err
				}
				cfg.Instructions = in.Instructions
				if _, err := st.Put(ctx, cfg); err != nil {
					return nil, err
				}
				return map[string]any{"status": "ok"}, nil
			default:
				return nil, fmt.Errorf("op must be get or set, got %q", in.Op)
			}
		},
	}
}
