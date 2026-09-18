package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

var orchestratorRole = []runtime.Role{runtime.RoleOrchestrator}

func orchestratorTools(s *Server) []ToolDef {
	return []ToolDef{itemsTool(s), artifactTool(s), worktreeTool(s), spawnTool(s), controlTool(s)}
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

func itemsTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_items",
		Description: "Create, update, read or list items inside your own top-level item's tree.",
		Roles:       orchestratorRole,
		Schema: objSchema(`"op":{"type":"string","enum":["create","update","get","list"]},
			"key":{"type":"string"},"parent":{"type":"string"},"type":{"type":"string"},
			"title":{"type":"string"},"brief":{"type":"string"},"acceptance":{"type":"array"},
			"priority":{"type":"integer"},"role_hint":{"type":"string"},"tdd_exempt":{"type":"string"},
			"repos":{"type":"array"},"revision":{"type":"integer"},"status":{"type":"string"},
			"filter":{"type":"object"}`),
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
				Filter     *struct {
					View, Type, Status, Q, Root string
				} `json:"filter"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			a, err := callerAgent(ctx, s, c)
			if err != nil {
				return nil, err
			}
			actor := items.Orchestrator(a.ID, a.RootItemID)
			switch in.Op {
			case "create":
				return s.RT.Items.Create(ctx, items.CreateInput{
					Type: items.Type(in.Type), ParentKey: in.Parent, Title: in.Title, Brief: in.Brief,
					Acceptance: in.Acceptance, Priority: in.Priority, RoleHint: in.RoleHint,
					TddExempt: in.TddExempt, Repos: in.Repos,
				}, actor)
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
				if in.Status != "" {
					st := items.Status(in.Status)
					p.Status = &st
				}
				return s.RT.Items.Update(ctx, in.Key, p, actor)
			case "get":
				return s.RT.Items.Get(ctx, in.Key)
			case "list":
				f := items.ListFilter{}
				if in.Filter != nil {
					f = items.ListFilter{View: in.Filter.View, Type: items.Type(in.Filter.Type),
						Status: items.Status(in.Filter.Status), Q: in.Filter.Q, Root: in.Filter.Root}
				} else {
					f.Root = a.RootItemID
				}
				list, total, err := s.RT.Items.List(ctx, f)
				if err != nil {
					return nil, err
				}
				if list == nil {
					list = []items.Item{}
				}
				return map[string]any{"items": list, "total": total}, nil
			default:
				return nil, fmt.Errorf("op must be create, update, get or list, got %q", in.Op)
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
		Schema: objSchema(`"op":{"type":"string","enum":["register"]},"item":{"type":"string"},
			"kind":{"type":"string"},"path":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Op   string `json:"op"`
				Item string `json:"item"`
				Kind string `json:"kind"`
				Path string `json:"path"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			res, err := s.RT.RegisterArtifact(ctx, c.SessionID, in.Op, in.Item, in.Kind, in.Path)
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

func worktreeTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_worktree",
		Description: "Create, share, review, release or remove a git worktree for a repo confirmed on your top-level item.",
		Roles:       orchestratorRole,
		Schema: objSchema(`"op":{"type":"string","enum":["create","share","review","release","remove"]},
			"repo":{"type":"string"},"branch":{"type":"string"},"base":{"type":"string"},
			"sha":{"type":"string"},"worktree":{"type":"string"},"agent":{"type":"string"},"mode":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Op       string `json:"op"`
				Repo     string `json:"repo"`
				Branch   string `json:"branch"`
				Base     string `json:"base"`
				SHA      string `json:"sha"`
				Worktree string `json:"worktree"`
				Agent    string `json:"agent"`
				Mode     string `json:"mode"`
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
				ci := worktree.CreateInput{RepoID: in.Repo, RepoPath: path, Branch: in.Branch, Base: in.Base,
					OwnerAgentID: a.ID, RootItemID: a.RootItemID}
				var wt worktree.Worktree
				if in.Op == "create" {
					wt, err = s.RT.Worktree.Create(ctx, ci)
				} else {
					wt, err = s.RT.Worktree.Review(ctx, ci, in.SHA)
				}
				if err != nil {
					return nil, err
				}
				return map[string]any{"worktree_id": wt.ID, "path": wt.Path, "branch": wt.Branch}, nil
			case "share":
				target, err := s.RT.Agent(ctx, in.Agent)
				if err != nil {
					return nil, err
				}
				wt, err := s.RT.Worktree.Get(ctx, in.Worktree)
				if err != nil {
					return nil, err
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
				return map[string]any{"ok": true}, nil
			case "release":
				target, err := s.RT.Agent(ctx, in.Agent)
				if err != nil {
					return nil, err
				}
				if err := s.RT.Worktree.Release(ctx, in.Worktree, target.ID); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true}, nil
			case "remove":
				wt, err := s.RT.Worktree.Remove(ctx, in.Worktree, a.ID)
				if err != nil {
					return nil, err
				}
				return map[string]any{"worktree_id": wt.ID, "state": wt.State}, nil
			default:
				return nil, fmt.Errorf("op must be create, share, review, release or remove, got %q", in.Op)
			}
		},
	}
}

// ---------- swarm_spawn ----------

func spawnTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_spawn",
		Description: "Spawn a worker agent on an item, filling agent, model, effort and advisor defaults from Settings.",
		Roles:       orchestratorRole,
		Schema: objSchema(`"item":{"type":"string"},"role":{"type":"string"},"agent":{"type":"string"},
			"model":{"type":"string"},"effort":{"type":"string"},"name":{"type":"string"},
			"brief":{"type":"object"},"worktrees":{"type":"array"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Item   string `json:"item"`
				Role   string `json:"role"`
				Agent  string `json:"agent"`
				Model  string `json:"model"`
				Effort string `json:"effort"`
				Name   string `json:"name"`
				Brief  struct {
					Objective  string   `json:"objective"`
					Acceptance []string `json:"acceptance"`
					ScopeIn    []string `json:"scope_in"`
					ScopeOut   []string `json:"scope_out"`
					Context    []string `json:"context"`
					Verify     []string `json:"verify"`
					StopWhen   []string `json:"stop_when"`
				} `json:"brief"`
				Worktrees []string `json:"worktrees"`
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
			agent, queued, err := s.RT.Spawn(ctx, runtime.SpawnInput{
				ItemKey: in.Item, Role: runtime.Role(in.Role), Kind: runtime.AgentKind(in.Agent),
				Model: in.Model, Effort: in.Effort, ParentAgentID: a.ID, Name: in.Name,
				Brief: runtime.BriefInput{
					Objective: in.Brief.Objective, Acceptance: in.Brief.Acceptance,
					ScopeIn: in.Brief.ScopeIn, ScopeOut: in.Brief.ScopeOut,
					Context: in.Brief.Context, Verify: in.Brief.Verify, StopWhen: in.Brief.StopWhen,
				},
			})
			if err != nil {
				// ErrBriefTooLong and every Preflight message are already the exact
				// §17.3 copy; wrapping them here would break an exact-match test.
				return nil, err
			}
			return map[string]any{"agent": agent.Name, "queued": queued}, nil
		},
	}
}

// ---------- swarm_control ----------

func controlTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_control",
		Description: "Pause, resume, cancel, retry or acknowledge an agent in your own subtree.",
		Roles:       orchestratorRole,
		Schema: objSchema(`"target":{"type":"string"},
			"action":{"type":"string","enum":["pause","resume","cancel","retry","ack"]},
			"scope":{"type":"string"},"note":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Target string `json:"target"`
				Action string `json:"action"`
				Scope  string `json:"scope"`
				Note   string `json:"note"`
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
			switch in.Action {
			case "pause":
				scope := in.Scope
				if scope == "" {
					scope = "session"
				}
				if _, err := s.RT.Pause(ctx, in.Target, scope); err != nil {
					return nil, err
				}
			case "resume":
				if _, err := s.RT.Resume(ctx, in.Target); err != nil {
					return nil, err
				}
			case "cancel":
				if _, err := s.RT.Cancel(ctx, in.Target); err != nil {
					return nil, err
				}
			case "retry":
				if _, err := s.RT.Retry(ctx, in.Target, in.Note); err != nil {
					return nil, err
				}
			case "ack":
				if err := s.RT.Ack(ctx, in.Target); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("action must be pause, resume, cancel, retry or ack, got %q", in.Action)
			}
			return map[string]any{"ok": true}, nil
		},
	}
}

// ---------- swarm_materialize ----------

func materializeTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_materialize",
		Description: "Turn an approved spike plan into real items: only available to the spike's own orchestrator.",
		Roles:       orchestratorRole,
		Schema:      objSchema(`"spec":{"type":"string"},"plan":{"type":"string"},"report":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Spec   string `json:"spec"`
				Plan   string `json:"plan"`
				Report string `json:"report"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			a, err := callerAgent(ctx, s, c)
			if err != nil {
				return nil, err
			}
			spikeKey, err := rootKeyFor(ctx, s, a.ItemID)
			if err != nil {
				return nil, err
			}
			res, err := s.RT.Materialize(ctx, c.SessionID, spikeKey, in.Spec, in.Plan, in.Report)
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	}
}
