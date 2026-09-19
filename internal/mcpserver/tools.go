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
)

func objSchema(props string) json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"additionalProperties":true}`)
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
		Description: "Acknowledge delivered messages and fetch the agent's inbox: assignments, questions, control notices and advice, newest-priority first.",
		Schema: objSchema(`"ack":{"type":"array","items":{"type":"string"}},
			"limit":{"type":"integer"}`),
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
			return map[string]any{
				"messages":      res.Messages,
				"more":          res.More,
				"session_state": res.SessionState,
			}, nil
		},
	}
}

// ---------- swarm_checkpoint ----------

func checkpointTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_checkpoint",
		Description: "Record progress: accepted, progress, blocked, handoff, completed or failed, with the verification evidence TDD requires.",
		Schema: objSchema(`"kind":{"type":"string"},"item":{"type":"string"},"summary":{"type":"string"},
			"resolution":{"type":"string"},"next":{"type":"array"},"blockers":{"type":"array"},
			"git":{"type":"array"},"verification":{"type":"array"},"artifacts":{"type":"array"},"processed":{"type":"array"},
			"request_id":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Kind         string           `json:"kind"`
				ItemKey      string           `json:"item"`
				Summary      string           `json:"summary"`
				Resolution   string           `json:"resolution"`
				Next         []string         `json:"next"`
				Blockers     []string         `json:"blockers"`
				Git          []runtime.GitRef `json:"git"`
				Verification []runtime.Verify `json:"verification"`
				Artifacts    []string         `json:"artifacts"`
				Processed    []string         `json:"processed"`
				RequestID    string           `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			res, err := s.RT.WriteCheckpoint(ctx, c.SessionID, runtime.CheckpointInput{
				Kind: runtime.CheckpointKind(in.Kind), ItemKey: in.ItemKey, Summary: in.Summary,
				Resolution: in.Resolution, Next: in.Next, Blockers: in.Blockers,
				Git: in.Git, Verification: in.Verification, Artifacts: in.Artifacts, Processed: in.Processed,
				RequestID: in.RequestID,
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"checkpoint_id": res.CheckpointID, "item_status": res.ItemStatus}, nil
		},
	}
}

// ---------- swarm_ask ----------

func askTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_ask",
		Description: "Ask a question, request an approval, propose repos to confirm, or withdraw an earlier ask, and block for the answer.",
		Schema: objSchema(`"kind":{"type":"string"},"prompt":{"type":"string"},"options":{"type":"array"},
			"artifact":{"type":"string"},"section":{"type":"string"},"withdraw":{"type":"string"},
			"repos":{"type":"array"},"expansion":{"type":"array"},"request_id":{"type":"string"}`),
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
				RequestID string                  `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			req, err := s.RT.Ask(ctx, c.SessionID, runtime.AskInput{
				Kind: in.Kind, Prompt: in.Prompt, Options: in.Options, ArtifactID: in.Artifact,
				SectionID: in.Section, Withdraw: in.Withdraw, Repos: in.Repos, Expansion: in.Expansion,
				RequestID: in.RequestID,
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
// second-guessing).
func requestOut(r runtime.Request) map[string]any {
	return map[string]any{"request_id": r.ID, "state": r.State}
}

// ---------- swarm_send ----------

func sendTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_send",
		Description: "Send a short message to another agent in the same top-level item, or to your parent.",
		Schema: objSchema(`"to":{"type":"string"},"kind":{"type":"string","enum":["question","answer","finding"]},
			"body":{"type":"string"},"reply_to":{"type":"string"},"request_id":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				To        string `json:"to"`
				Kind      string `json:"kind"`
				Body      string `json:"body"`
				ReplyTo   string `json:"reply_to"`
				RequestID string `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			kind := in.Kind
			if kind == "" {
				kind = "relay"
			}
			// runtime.Store.Send's last-but-one parameter is named correlationID and
			// is the only thread-tracking hook it exposes (internal/runtime is
			// outside this batch's file ownership); §8.1's reply_to input maps onto
			// it.
			id, err := s.RT.Send(ctx, c.SessionID, in.To, runtime.MessageKind(kind), in.Body, in.ReplyTo, in.RequestID)
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
	} `json:"filter"`
	Repos *struct {
		Q     string `json:"q"`
		Group string `json:"group"`
		Limit int    `json:"limit"`
	} `json:"repos"`
	// a pointer distinguishes "since_seq omitted" (no event scan at all) from
	// an explicit "since_seq":0 (a fresh cursor: scan every event ever issued).
	SinceSeq *int64   `json:"since_seq"`
	Fields   []string `json:"fields"`
}

func agentOut(a runtime.Agent) map[string]any {
	return map[string]any{"name": a.Name, "kind": a.Kind, "model": a.Model, "role": a.Role, "state": a.State}
}

func checkpointOut(c runtime.Checkpoint) map[string]any {
	return map[string]any{"item": c.ItemID, "kind": c.Kind, "summary": c.Summary, "created_at": c.CreatedAt}
}

func artifactOut(a runtime.Artifact) map[string]any {
	sections := make([]artifactSectionWire, len(a.Sections))
	for i, sec := range a.Sections {
		sections[i] = artifactSectionWire{ID: sec.ID, Title: sec.Title, SHA256: sec.SHA256}
	}
	return map[string]any{"artifact_id": a.ID, "kind": a.Kind, "revision": a.Revision, "sections": sections}
}

// readTool is §8.1's catch-all state read, read directly from the real spec
// (fix round 1 — the batch briefs never had the literal shapes). Known gap:
// `fields` (selective field projection) is not implemented; every matched
// item/agent/checkpoint/artifact is returned whole. Nothing in this batch's
// own tests needs it, and no other task in this batch was found depending on
// it either, so it is deferred rather than guessed at.
func readTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_read",
		Description: "Read items, artifacts, agents and checkpoints by ref or filter, search repos, and get changes since a cursor.",
		Schema: objSchema(`"refs":{"type":"array","items":{"type":"string"}},
			"filter":{"type":"object"},
			"repos":{"type":"object","properties":{"q":{"type":"string"},"group":{"type":"string"},"limit":{"type":"integer"}}},
			"since_seq":{"type":"integer"},"fields":{"type":"array","items":{"type":"string"}}`),
		Unbound: true,
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in readInput
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			out := map[string]any{
				"items": []items.Item{}, "artifacts": []any{}, "agents": []any{},
				"checkpoints": []any{}, "repos": []repos.Repo{}, "confirmed_repos": []repos.Repo{},
			}
			itemSeen := map[string]bool{}
			addItem := func(it items.Item) {
				if itemSeen[it.Key] {
					return
				}
				itemSeen[it.Key] = true
				out["items"] = append(out["items"].([]items.Item), it)
			}

			// refs mix item keys, art_ ids and agent names (§8.1); route each by
			// what actually resolves, since there is no single shared id space.
			for _, ref := range in.Refs {
				if strings.HasPrefix(ref, "art_") {
					art, _, err := s.RT.ArtifactMarkdown(ctx, ref, 0, "")
					if err != nil {
						return nil, err
					}
					out["artifacts"] = append(out["artifacts"].([]any), artifactOut(art))
					continue
				}
				if it, err := s.RT.Items.Get(ctx, ref); err == nil {
					addItem(it)
					continue
				}
				a, err := s.RT.Agent(ctx, ref)
				if err != nil {
					return nil, err
				}
				out["agents"] = append(out["agents"].([]any), agentOut(a))
			}
			if in.Filter != nil {
				list, _, err := s.RT.Items.List(ctx, items.ListFilter{
					Type: items.Type(in.Filter.Type), Status: items.Status(in.Filter.Status),
					Q: in.Filter.Q, Root: in.Filter.Root,
				})
				if err != nil {
					return nil, err
				}
				for _, it := range list {
					addItem(it)
				}
			}
			// checkpoints: the latest one per item resolved above.
			for _, it := range out["items"].([]items.Item) {
				cps, err := s.RT.Checkpoints(ctx, it.Key, 1, time.Time{})
				if err != nil {
					return nil, err
				}
				if len(cps) > 0 {
					out["checkpoints"] = append(out["checkpoints"].([]any), checkpointOut(cps[0]))
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
					found, err = s.RT.Repos.Recent(ctx, limit)
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
								if it, err := s.RT.Items.Get(ctx, p.Key); err == nil {
									addItem(it)
								}
							}
						case events.AgentChanged:
							var p struct {
								Name string `json:"name"`
							}
							if json.Unmarshal(e.Payload, &p) == nil && p.Name != "" {
								if a, err := s.RT.Agent(ctx, p.Name); err == nil {
									out["agents"] = append(out["agents"].([]any), agentOut(a))
								}
							}
						case events.CheckpointCreated:
							var p struct {
								Item string `json:"item"`
							}
							if json.Unmarshal(e.Payload, &p) == nil && p.Item != "" {
								if cps, err := s.RT.Checkpoints(ctx, p.Item, 1, time.Time{}); err == nil && len(cps) > 0 {
									out["checkpoints"] = append(out["checkpoints"].([]any), checkpointOut(cps[0]))
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
			out["reset"] = reset
			out["cursor"] = cursor
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
		Schema:      objSchema(kbSearchGetSchema),
		Unbound:     true,
		Handler:     kbHandler(s, false),
	}
}

func kbFullTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_kb",
		Description: "Search, read or write a knowledge base document: a durable decision, spec or note other agents can find later.",
		Schema:      objSchema(kbFullSchema),
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
		Schema: objSchema(`"question":{"type":"string"},"focus":{"type":"array","items":{"type":"string"}},
			"wait_seconds":{"type":"integer"}`),
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
