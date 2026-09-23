package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

var orchestratorRole = []runtime.Role{runtime.RoleOrchestrator}

func orchestratorTools(s *Server) []ToolDef {
	return []ToolDef{itemsTool(s), artifactTool(s), worktreeTool(s), spawnTool(s), controlTool(s), roleOverridesTool(s)}
}

// §17.3 copy owned by this file.
func repoNotConfirmed(name, rootKey string) error {
	return fmt.Errorf(`repo_not_confirmed: %s is not confirmed for %s. Ask with swarm_ask kind "confirm_repos".`, name, rootKey)
}

func dependenciesOpen(keys []string) error {
	return fmt.Errorf("dependencies_open: %s", strings.Join(keys, ", "))
}

// callerAgent resolves the caller's own agents row.
func callerAgent(ctx context.Context, s *Server, c Caller) (runtime.Agent, error) {
	return s.RT.Agent(ctx, c.AgentName)
}

func rootKeyFor(ctx context.Context, s *Server, rootID string) (string, error) {
	var key string
	err := s.RT.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, rootID).Scan(&key)
	return key, err
}

// ---------- swarm_items ----------

// promoteDraft moves a Draft task, and its Draft parent story (even when the
// task is already Ready), to Ready as the caller's orchestrator so the daemon
// may later move them to In progress.
func promoteDraft(ctx context.Context, s *Server, it items.Item, actor items.Actor) error {
	if it.Type != items.Task {
		return nil
	}
	if it.Status == items.Draft {
		if _, err := s.RT.Items.Transition(ctx, it.Key, items.Ready, actor); err != nil {
			return err
		}
	}
	if it.ParentKey == "" {
		return nil
	}
	parent, err := s.RT.Items.Get(ctx, it.ParentKey)
	if err != nil {
		return err
	}
	if parent.Type == items.Story && parent.Status == items.Draft {
		_, err = s.RT.Items.Transition(ctx, parent.Key, items.Ready, actor)
	}
	return err
}

// itemsTool is §8.1, read directly from the real spec (fix round 1): op is
// exactly create|update|link|unlink — reading and listing items is
// swarm_read's job (its refs/filter cover exactly that), not swarm_items'.
func itemsTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_items",
		Description: "Create or update an item, or link/unlink a dependency, inside your own top-level item's tree.",
		Roles:       orchestratorRole,
		Schema: objSchema(`"op":{"type":"string","enum":["create","update","link","unlink"]},
			"key":{"type":"string"},"parent":{"type":"string"},"type":{"type":"string"},
			"title":{"type":"string"},"brief":{"type":"string"},"acceptance":{"type":"array"},
			"priority":{"type":"integer"},"role_hint":{"type":"string"},"tdd_exempt":{"type":"string"},
			"repos":{"type":"array"},"revision":{"type":"integer"},"status":{"type":"string"},
			"blocked_by":{"type":"string"},"request_id":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Op         string   `json:"op"`
				Key        string   `json:"key"`
				Parent     string   `json:"parent"`
				Type       string   `json:"type"`
				Title      string   `json:"title"`
				Brief      string   `json:"brief"`
				Acceptance []string `json:"acceptance"`
				Priority   *int     `json:"priority"`
				RoleHint   string   `json:"role_hint"`
				TddExempt  string   `json:"tdd_exempt"`
				Repos      []string `json:"repos"`
				Revision   int      `json:"revision"`
				Status     string   `json:"status"`
				BlockedBy  string   `json:"blocked_by"`
				RequestID  string   `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			a, err := callerAgent(ctx, s, c)
			if err != nil {
				return nil, err
			}
			actor := items.Orchestrator(a.ID, a.RootItemID)
			// Idempotency (I11) is wired here rather than inside internal/items:
			// that package has no session concept, and runtime already imports it
			// (the reverse import would cycle), so CreateTx/UpdateTx/AddDepTx/
			// RemoveDepTx (each already a transaction the caller owns, the same
			// pattern Task 19's materializer uses) run through runtime.IdemTx
			// directly at this call site, inside one transaction with the
			// idempotency record.
			switch in.Op {
			case "create":
				var out items.Item
				if _, err := runtime.IdemTx(ctx, s.RT, c.SessionID, in.RequestID, "swarm_items", &out,
					func(tx *sql.Tx) (err error) {
						out, err = s.RT.Items.CreateTx(ctx, tx, items.CreateInput{
							Type: items.Type(in.Type), ParentKey: in.Parent, Title: in.Title, Brief: in.Brief,
							Acceptance: in.Acceptance, Priority: in.Priority, RoleHint: in.RoleHint,
							TddExempt: in.TddExempt, Repos: in.Repos, Status: items.Status(in.Status),
						}, actor)
						return err
					}); err != nil {
					return nil, err
				}
				return out, nil
			case "update":
				p := items.Patch{Revision: in.Revision}
				if in.Title != "" {
					p.Title = &in.Title
				}
				if in.Brief != "" {
					p.Brief = &in.Brief
				}
				if in.Acceptance != nil {
					p.Acceptance = &in.Acceptance
				}
				if in.Priority != nil {
					p.Priority = in.Priority
				}
				if in.TddExempt != "" {
					p.TddExempt = &in.TddExempt
				}
				if in.Status != "" {
					st := items.Status(in.Status)
					p.Status = &st
				}
				var out items.Item
				if _, err := runtime.IdemTx(ctx, s.RT, c.SessionID, in.RequestID, "swarm_items", &out,
					func(tx *sql.Tx) (err error) {
						out, err = s.RT.Items.UpdateTx(ctx, tx, in.Key, p, actor)
						return err
					}); err != nil {
					// The StaleRevision message already promises "Showing its latest
					// status" without delivering it (fix round 2: a caller correcting a
					// guessed revision needs the real number, not just confirmation it
					// guessed wrong). Any other conflict (a cycle, a denied hierarchy
					// dep) keeps its own message as-is.
					var ie *items.Error
					if errors.As(err, &ie) && ie.Code == items.CodeConflict && ie.Message == items.StaleRevision {
						if cur, gerr := s.RT.Items.Get(ctx, in.Key); gerr == nil {
							return nil, fmt.Errorf("%s Current revision: %d, status: %s.",
								ie.Message, cur.Revision, cur.Status)
						}
					}
					return nil, err
				}
				// Update's own non-Tx wrapper re-reads (enriched) after commit, too
				// (internal/items/store.go); this mirrors that same convention rather
				// than duplicating enrich here, and runs whether this call replayed
				// or genuinely mutated -- it is a plain read of current state either
				// way.
				return s.RT.Items.Get(ctx, out.Key)
			case "link":
				var ran bool
				if _, err := runtime.IdemTx(ctx, s.RT, c.SessionID, in.RequestID, "swarm_items", &ran,
					func(tx *sql.Tx) error {
						ran = true
						return s.RT.Items.AddDepTx(ctx, tx, in.Key, in.BlockedBy, actor)
					}); err != nil {
					return nil, err
				}
				return s.RT.Items.Get(ctx, in.Key)
			case "unlink":
				var ran bool
				if _, err := runtime.IdemTx(ctx, s.RT, c.SessionID, in.RequestID, "swarm_items", &ran,
					func(tx *sql.Tx) error {
						ran = true
						return s.RT.Items.RemoveDepTx(ctx, tx, in.Key, in.BlockedBy, actor)
					}); err != nil {
					return nil, err
				}
				return s.RT.Items.Get(ctx, in.Key)
			default:
				return nil, fmt.Errorf("op must be create, update, link or unlink, got %q", in.Op)
			}
		},
	}
}

// ---------- swarm_artifact ----------

type artifactSectionWire struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	SHA256 string `json:"sha256"`
}

func artifactTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_artifact",
		Description: "Register a spec, plan or debug report artifact and get back its sections and any requests it made stale.",
		Roles:       orchestratorRole,
		// §8.1: op is "register"|"revise" (fix round 2, item 3: the enum was
		// missing "revise", which schema-blocks it for any real MCP client that
		// validates arguments before sending, even though RegisterArtifact
		// (internal/runtime, outside this batch) ignores `op` and infers
		// register-vs-revise itself from whether a row already exists for the
		// item+path).
		Schema: objSchema(`"op":{"type":"string","enum":["register","revise"]},"item":{"type":"string"},
			"kind":{"type":"string"},"path":{"type":"string"},"request_id":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Op        string `json:"op"`
				Item      string `json:"item"`
				Kind      string `json:"kind"`
				Path      string `json:"path"`
				RequestID string `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			res, err := s.RT.RegisterArtifact(ctx, c.SessionID, in.Op, in.Item, in.Kind, in.Path, in.RequestID)
			if err != nil {
				return nil, err
			}
			sections := make([]artifactSectionWire, len(res.Sections))
			for i, sec := range res.Sections {
				sections[i] = artifactSectionWire{ID: sec.ID, Title: sec.Title, SHA256: sec.SHA256}
			}
			stale := res.StaleRequests
			if stale == nil {
				stale = []string{}
			}
			return map[string]any{
				"artifact_id":    res.ArtifactID,
				"revision":       res.Revision,
				"sections":       sections,
				"stale_requests": stale,
			}, nil
		},
	}
}

// ---------- swarm_worktree ----------

// sendAssignmentUpdate enqueues a daemon-originated assignment_update message,
// the same shape Store.Retry already writes inline for a retry note. No
// exported runtime primitive exists for an arbitrary daemon message (only
// agent-to-agent Send), so this mirrors that pattern directly against the
// schema rather than duplicating it inside internal/runtime, which this batch
// does not own.
func sendAssignmentUpdate(ctx context.Context, s *Server, toAgentID, rootItemID, itemID string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := db.Millis(s.RT.Now())
	err = s.RT.DB.Tx(ctx, func(tx *sql.Tx) error {
		var seq int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM messages`).Scan(&seq); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO messages
			(id, seq, kind, wake_class, priority, origin, to_agent_id, root_item_id, item_id, payload_json, state, created_at)
			VALUES (?, ?, 'assignment_update', 'immediate', 1, 'daemon', ?, ?, ?, ?, 'pending', ?)`,
			ids.New("msg"), seq, toAgentID, rootItemID, itemID, string(body), now)
		return err
	})
	if err == nil {
		s.RT.Events.Notify()
	}
	return err
}

func repoNameAndPath(ctx context.Context, s *Server, repoID string) (name, path string, err error) {
	err = s.RT.DB.QueryRowContext(ctx, `SELECT name, path FROM repos WHERE id = ?`, repoID).Scan(&name, &path)
	return name, path, err
}

func isConfirmed(ctx context.Context, s *Server, rootID, repoID string) (bool, error) {
	confirmed, err := s.RT.ConfirmedRepos(ctx, rootID)
	if err != nil {
		return false, err
	}
	for _, r := range confirmed {
		if r.ID == repoID {
			return true, nil
		}
	}
	return false, nil
}

// worktreeOut is §8.1's one documented result shape for the whole tool -
// {worktree_id,path,branch,base_sha,state} - used by every op that has a
// worktree.Worktree to hand back (fix round 2, item 5).
func worktreeOut(wt worktree.Worktree) map[string]any {
	return map[string]any{"worktree_id": wt.ID, "path": wt.Path, "branch": wt.Branch,
		"base_sha": wt.BaseSHA, "state": wt.State}
}

func worktreeTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_worktree",
		Description: "Create, share, review, release or remove a git worktree for a repo confirmed on your top-level item.",
		Roles:       orchestratorRole,
		Schema: objSchema(`"op":{"type":"string","enum":["create","share","review","release","remove"]},
			"repo":{"type":"string"},"branch":{"type":"string"},"base":{"type":"string"},
			"sha":{"type":"string"},"worktree":{"type":"string"},"agent":{"type":"string"},"mode":{"type":"string"},
			"request_id":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Op        string `json:"op"`
				Repo      string `json:"repo"`
				Branch    string `json:"branch"`
				Base      string `json:"base"`
				SHA       string `json:"sha"`
				Worktree  string `json:"worktree"`
				Agent     string `json:"agent"`
				Mode      string `json:"mode"`
				RequestID string `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			a, err := callerAgent(ctx, s, c)
			if err != nil {
				return nil, err
			}
			rootKey, err := rootKeyFor(ctx, s, a.RootItemID)
			if err != nil {
				return nil, err
			}
			// create/review/share/remove each have a real external side
			// effect (a git subprocess, or -- for share -- a DB write gated
			// by a mutex that cannot safely nest inside a shared SQL
			// transaction; see Share's own doc comment) that cannot live
			// inside the same transaction as its idempotency record. Each
			// uses the same two-phase pattern as swarm_control's resume/
			// cancel/retry: PeekIdempotent before the side effect (a
			// replay skips it entirely), the side effect runs unchanged on
			// a miss, then a trivial IdemTx afterward only to record the
			// result. release is not wired this way: calling it twice is
			// already a genuine no-op (verified by reading its SQL -- the
			// second call's UPDATE matches zero rows and returns nil,
			// exactly as the first call's did if the row was already
			// released), so gating it would add machinery without adding
			// safety.
			switch in.Op {
			case "create", "review":
				name, path, err := repoNameAndPath(ctx, s, in.Repo)
				if err != nil {
					return nil, err
				}
				ok, err := isConfirmed(ctx, s, a.RootItemID, in.Repo)
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, repoNotConfirmed(name, rootKey)
				}
				var wt worktree.Worktree
				if hit, err := runtime.PeekIdempotent(ctx, s.RT, c.SessionID, in.RequestID, &wt); err != nil {
					return nil, err
				} else if hit {
					return worktreeOut(wt), nil
				}
				ci := worktree.CreateInput{RepoID: in.Repo, RepoPath: path, Branch: in.Branch, Base: in.Base,
					OwnerAgentID: a.ID, RootItemID: a.RootItemID}
				if in.Op == "create" {
					wt, err = s.RT.Worktree.Create(ctx, ci)
				} else {
					wt, err = s.RT.Worktree.Review(ctx, ci, in.SHA)
				}
				if err != nil {
					return nil, err
				}
				if _, err := runtime.IdemTx(ctx, s.RT, c.SessionID, in.RequestID, "swarm_worktree", &wt,
					func(tx *sql.Tx) error { return nil }); err != nil {
					return nil, err
				}
				return worktreeOut(wt), nil
			case "share":
				target, err := s.RT.Agent(ctx, in.Agent)
				if err != nil {
					return nil, err
				}
				wt, err := s.RT.Worktree.Get(ctx, in.Worktree)
				if err != nil {
					return nil, err
				}
				var out worktree.Worktree
				if hit, err := runtime.PeekIdempotent(ctx, s.RT, c.SessionID, in.RequestID, &out); err != nil {
					return nil, err
				} else if hit {
					return worktreeOut(out), nil
				}
				if err := s.RT.Worktree.Share(ctx, in.Worktree, target.ID, in.Mode); err != nil {
					return nil, err
				}
				// I3: a shared worktree's path reaches the worker as a message; the
				// worker's own session cwd never moves.
				if err := sendAssignmentUpdate(ctx, s, target.ID, target.RootItemID, target.ItemID,
					map[string]string{"worktree_id": wt.ID, "path": wt.Path, "branch": wt.Branch, "mode": in.Mode}); err != nil {
					return nil, err
				}
				// Share doesn't mutate the worktrees row (only a reservation
				// table), so the pre-share `wt` already reflects the true state.
				out = wt
				if _, err := runtime.IdemTx(ctx, s.RT, c.SessionID, in.RequestID, "swarm_worktree", &out,
					func(tx *sql.Tx) error { return nil }); err != nil {
					return nil, err
				}
				return worktreeOut(out), nil
			case "release":
				target, err := s.RT.Agent(ctx, in.Agent)
				if err != nil {
					return nil, err
				}
				if err := s.RT.Worktree.Release(ctx, in.Worktree, target.ID); err != nil {
					return nil, err
				}
				// Release, like Share, doesn't mutate the worktrees row itself, so
				// a fresh Get after it reflects the true (unchanged) state.
				wt, err := s.RT.Worktree.Get(ctx, in.Worktree)
				if err != nil {
					return nil, err
				}
				return worktreeOut(wt), nil
			case "remove":
				var wt worktree.Worktree
				if hit, err := runtime.PeekIdempotent(ctx, s.RT, c.SessionID, in.RequestID, &wt); err != nil {
					return nil, err
				} else if hit {
					return worktreeOut(wt), nil
				}
				wt, err := s.RT.Worktree.Remove(ctx, in.Worktree, a.ID)
				if err != nil {
					return nil, err
				}
				if _, err := runtime.IdemTx(ctx, s.RT, c.SessionID, in.RequestID, "swarm_worktree", &wt,
					func(tx *sql.Tx) error { return nil }); err != nil {
					return nil, err
				}
				return worktreeOut(wt), nil
			default:
				return nil, fmt.Errorf("op must be create, share, review, release or remove, got %q", in.Op)
			}
		},
	}
}

// ---------- swarm_spawn ----------

// spawnWorktreeRef is one entry of §8.1's worktrees: [{worktree, mode}] —
// fixed in round 1 from an earlier, wrong []string.
type spawnWorktreeRef struct {
	Worktree string `json:"worktree"`
	Mode     string `json:"mode"`
}

func spawnTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_spawn",
		Description: "Spawn a worker agent on an item, filling agent, model, effort and advisor defaults from Settings. Supports explicit agent and model overrides, with automatic model-to-agent resolution.",
		Roles:       orchestratorRole,
		Schema: objSchema(`"item":{"type":"string"},"role":{"type":"string"},"agent":{"type":"string"},
			"model":{"type":"string"},"effort":{"type":"string"},"name":{"type":"string"},
			"advisor":{},"cwd":{"type":"string"},
			"brief":{"type":"object"},
			"worktrees":{"type":"array","items":{"type":"object","properties":{
				"worktree":{"type":"string"},"mode":{"type":"string","enum":["rw","ro"]}}}},
			"request_id":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Item   string `json:"item"`
				Role   string `json:"role"`
				Agent  string `json:"agent"`
				Model  string `json:"model"`
				Effort string `json:"effort"`
				Name   string `json:"name"`
				// Advisor and Cwd are accepted (§8.1) but not yet wired to the spawned
				// agent: runtime.SpawnInput/Spawn (internal/runtime/agents.go, outside
				// this batch's file ownership) has no advisor_* columns in its INSERT
				// and startSession always uses the neutral folder — a pre-existing gap
				// from Batches 1-4, confirmed unchanged, carried forward rather than
				// worked around here.
				Advisor json.RawMessage `json:"advisor"`
				Cwd     string          `json:"cwd"`
				Brief   struct {
					Objective  string   `json:"objective"`
					Acceptance []string `json:"acceptance"`
					ScopeIn    []string `json:"scope_in"`
					ScopeOut   []string `json:"scope_out"`
					Context    []string `json:"context"`
					Verify     []string `json:"verify"`
					StopWhen   []string `json:"stop_when"`
				} `json:"brief"`
				Worktrees []spawnWorktreeRef `json:"worktrees"`
				RequestID string             `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			// I12: refuse while the item has an open dependency.
			it, err := s.RT.Items.Get(ctx, in.Item)
			if err != nil {
				return nil, err
			}
			if len(it.BlockedBy) > 0 {
				return nil, dependenciesOpen(it.BlockedBy)
			}
			a, err := callerAgent(ctx, s, c)
			if err != nil {
				return nil, err
			}
			if err := promoteDraft(ctx, s, it, items.Orchestrator(a.ID, a.RootItemID)); err != nil {
				return nil, err
			}
			agent, queued, err := s.RT.Spawn(ctx, runtime.SpawnInput{
				ItemKey: in.Item, Role: runtime.Role(in.Role), Kind: runtime.AgentKind(in.Agent),
				Model: in.Model, Effort: in.Effort, ParentAgentID: a.ID, Name: in.Name,
				Brief: runtime.BriefInput{
					Objective: in.Brief.Objective, Acceptance: in.Brief.Acceptance,
					ScopeIn: in.Brief.ScopeIn, ScopeOut: in.Brief.ScopeOut,
					Context: in.Brief.Context, Verify: in.Brief.Verify, StopWhen: in.Brief.StopWhen,
				},
				SessionID: c.SessionID, RequestID: in.RequestID,
			})
			if err != nil {
				// ErrBriefTooLong and every Preflight message are already the exact
				// §17.3 copy; wrapping them here would break an exact-match test.
				return nil, err
			}
			// A queued spawn gets no session until the queue later drains it
			// (Task 13's limiter): "session" is "" rather than a fabricated id.
			var sessionID string
			if !queued {
				if ses, err := s.RT.LatestSession(ctx, agent.ID); err == nil {
					sessionID = ses.ID
				}
			}
			return map[string]any{"agent": agent.Name, "session": sessionID, "queued": queued}, nil
		},
	}
}

// ---------- swarm_control ----------

// controlTool is §8.1, read directly from the real spec (fix round 1): the
// action enum is exactly pause|resume|cancel|retry (no "ack" — that is a
// separate, non-MCP UI action, POST /api/agents/{name}/ack, not part of this
// tool) and the result is {"state"}, not a bare ok.
func controlTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_control",
		Description: "Pause, resume, cancel or retry an agent in your own subtree.",
		Roles:       orchestratorRole,
		Schema: objSchema(`"target":{"type":"string"},
			"action":{"type":"string","enum":["pause","resume","cancel","retry"]},
			"scope":{"type":"string"},"note":{"type":"string"},"request_id":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Target    string `json:"target"`
				Action    string `json:"action"`
				Scope     string `json:"scope"`
				Note      string `json:"note"`
				RequestID string `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			caller, err := callerAgent(ctx, s, c)
			if err != nil {
				return nil, err
			}
			target, err := s.RT.Agent(ctx, in.Target)
			if err != nil {
				return nil, err
			}
			// §8.1: an orchestrator may only control its own subtree. The single-
			// orchestrator-per-root invariant (§5) makes "same root item" the
			// subtree boundary in practice.
			if target.RootItemID != caller.RootItemID {
				return nil, fmt.Errorf("bad_request: %s is outside your subtree", in.Target)
			}
			var state string
			switch in.Action {
			case "pause":
				scope := in.Scope
				if scope == "" {
					scope = "session"
				}
				ses, err := s.RT.Pause(ctx, in.Target, scope)
				if err != nil {
					return nil, err
				}
				state = string(ses.State)
			case "resume":
				agent, err := s.RT.Resume(ctx, in.Target, c.SessionID, in.RequestID)
				if err != nil {
					return nil, err
				}
				state = string(agent.State)
			case "cancel":
				agent, err := s.RT.Cancel(ctx, in.Target, c.SessionID, in.RequestID)
				if err != nil {
					return nil, err
				}
				state = string(agent.State)
			case "retry":
				agent, err := s.RT.Retry(ctx, in.Target, in.Note, c.SessionID, in.RequestID)
				if err != nil {
					return nil, err
				}
				state = string(agent.State)
			default:
				return nil, fmt.Errorf("action must be pause, resume, cancel or retry, got %q", in.Action)
			}
			return map[string]any{"state": state}, nil
		},
	}
}

// ---------- swarm_role_overrides ----------

// roleOverridesTool is docs/specs/2026-09-23-orchestrator-role-overrides.md's
// write path onto an orchestrator's own agents.role_overrides (previously
// settable only once, at creation, via the roles/Roles param CLI/HTTP
// creation already had -- see runtime.agents.go:706-755, which resolves a
// spawned child's agent/model/effort from the parent's own RoleOverrides
// before ever falling back to the live global Settings). Deliberately
// self-scoped: unlike swarm_worktree/swarm_control, there is no
// target-agent/agent-id parameter here at all -- name is always resolved
// from the caller's own MCP session, so there is no way to reach another
// agent's row through this tool.
func roleOverridesTool(s *Server) ToolDef {
	return ToolDef{
		Name: "swarm_role_overrides",
		Description: "Set or clear your OWN future role->agent/model/effort default (checked before the live global Settings when you spawn). " +
			"Self only -- there is no target-agent parameter. set requires role, agent and model (effort optional); clear requires role.",
		Roles: orchestratorRole,
		Schema: objSchema(`"op":{"type":"string","enum":["set","clear"]},
			"role":{"type":"string"},"agent":{"type":"string"},"model":{"type":"string"},"effort":{"type":"string"},
			"request_id":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Op        string `json:"op"`
				Role      string `json:"role"`
				Agent     string `json:"agent"`
				Model     string `json:"model"`
				Effort    string `json:"effort"`
				RequestID string `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			a, err := callerAgent(ctx, s, c)
			if err != nil {
				return nil, err
			}
			var rd *settings.RoleDefault
			switch in.Op {
			case "set":
				if in.Agent == "" || in.Model == "" {
					return nil, errors.New("bad_request: agent and model are required to set a role override")
				}
				rd = &settings.RoleDefault{Agent: runtime.AgentKind(in.Agent), Model: in.Model, Effort: in.Effort}
			case "clear":
				rd = nil
			default:
				return nil, fmt.Errorf("op must be set or clear, got %q", in.Op)
			}
			out, err := s.RT.SetRoleOverride(ctx, a.Name, runtime.Role(in.Role), rd, c.SessionID, in.RequestID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"role_overrides": roleOverridesOut(out.RoleOverrides)}, nil
		},
	}
}

// ---------- swarm_materialize ----------

func materializeTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_materialize",
		Description: "Turn an approved spike plan into real items: only available to the spike's own orchestrator.",
		Roles:       orchestratorRole,
		// §8.1: input is {spike, spec?, plan?, report?} - spike is required (no
		// "?" in the spec), not inferred from the caller's own item (fix round
		// 2, item 4). RT.Materialize still independently checks the caller is
		// that spike's orchestrator (a.ItemID != spike.ID), so passing it
		// explicitly adds no privilege the caller didn't already have.
		Schema: objSchema(`"spike":{"type":"string"},"spec":{"type":"string"},"plan":{"type":"string"},"report":{"type":"string"},
			"request_id":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Spike     string `json:"spike"`
				Spec      string `json:"spec"`
				Plan      string `json:"plan"`
				Report    string `json:"report"`
				RequestID string `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			if in.Spike == "" {
				return nil, errors.New("bad_request: spike is required")
			}
			res, err := s.RT.Materialize(ctx, c.SessionID, in.Spike, in.Spec, in.Plan, in.Report, in.RequestID)
			if err != nil {
				return nil, err
			}
			// runtime.MaterializeResult has no json tags (the same gap as
			// Advisor/Agent/Checkpoint/Artifact), so it needs the same manual
			// wire-mapping those already get elsewhere in this file - bare `res`
			// would marshal as {"Root":...,"Created":[...]}, not spec's
			// {"root","created"} (fix round 2 full-pass finding).
			created := res.Created
			if created == nil {
				created = []string{}
			}
			return map[string]any{"root": res.Root, "created": created}, nil
		},
	}
}
