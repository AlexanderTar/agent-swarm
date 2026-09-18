package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
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
			"git":{"type":"array"},"verify":{"type":"array"},"artifacts":{"type":"array"},"processed":{"type":"array"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Kind       string           `json:"kind"`
				ItemKey    string           `json:"item"`
				Summary    string           `json:"summary"`
				Resolution string           `json:"resolution"`
				Next       []string         `json:"next"`
				Blockers   []string         `json:"blockers"`
				Git        []runtime.GitRef `json:"git"`
				Verify     []runtime.Verify `json:"verify"`
				Artifacts  []string         `json:"artifacts"`
				Processed  []string         `json:"processed"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			res, err := s.RT.WriteCheckpoint(ctx, c.SessionID, runtime.CheckpointInput{
				Kind: runtime.CheckpointKind(in.Kind), ItemKey: in.ItemKey, Summary: in.Summary,
				Resolution: in.Resolution, Next: in.Next, Blockers: in.Blockers,
				Git: in.Git, Verification: in.Verify, Artifacts: in.Artifacts, Processed: in.Processed,
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
			"repos":{"type":"array"},"expansion":{"type":"array"}`),
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
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			req, err := s.RT.Ask(ctx, c.SessionID, runtime.AskInput{
				Kind: in.Kind, Prompt: in.Prompt, Options: in.Options, ArtifactID: in.Artifact,
				SectionID: in.Section, Withdraw: in.Withdraw, Repos: in.Repos, Expansion: in.Expansion,
			})
			if err != nil {
				return nil, err
			}
			return requestOut(req), nil
		},
	}
}

func requestOut(r runtime.Request) map[string]any {
	return map[string]any{
		"request_id": r.ID, "kind": r.Kind, "state": r.State, "prompt": r.Prompt,
		"artifact_id": r.ArtifactID, "section_id": r.SectionID,
	}
}

// ---------- swarm_send ----------

func sendTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_send",
		Description: "Send a short message to another agent in the same top-level item, or to your parent.",
		Schema: objSchema(`"to":{"type":"string"},"kind":{"type":"string"},"body":{"type":"string"},
			"correlation_id":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				To            string `json:"to"`
				Kind          string `json:"kind"`
				Body          string `json:"body"`
				CorrelationID string `json:"correlation_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			kind := in.Kind
			if kind == "" {
				kind = "relay"
			}
			id, err := s.RT.Send(ctx, c.SessionID, in.To, runtime.MessageKind(kind), in.Body, in.CorrelationID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"message_id": id}, nil
		},
	}
}

// ---------- swarm_read ----------

// readTool is §8.1's catch-all state read: items by ref or filter, the
// caller's confirmed repos, and the event feed since a cursor (I19's
// since_seq/reset rule, mirrored from internal/httpapi's SSE handler: a
// cursor older than the retention window comes back with reset:true and the
// caller starts over from the latest cursor). This wire shape is invented for
// this batch — §8.1's literal text was not available; the coordinator should
// check it against the spec.
func readTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_read",
		Description: "Read items by key or filter, your top-level item's confirmed repos, and events since a cursor.",
		Schema: objSchema(`"refs":{"type":"array","items":{"type":"string"}},
			"filter":{"type":"object"},"repos":{"type":"boolean"},"since_seq":{"type":"integer"}`),
		Unbound: true,
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Refs   []string `json:"refs"`
				Filter *struct {
					View   string `json:"view"`
					Type   string `json:"type"`
					Status string `json:"status"`
					Q      string `json:"q"`
					Root   string `json:"root"`
				} `json:"filter"`
				Repos    bool  `json:"repos"`
				SinceSeq int64 `json:"since_seq"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			out := map[string]any{"items": []items.Item{}, "confirmed_repos": []any{}}

			for _, ref := range in.Refs {
				it, err := s.RT.Items.Get(ctx, ref)
				if err != nil {
					return nil, err
				}
				out["items"] = append(out["items"].([]items.Item), it)
			}
			if in.Filter != nil {
				list, _, err := s.RT.Items.List(ctx, items.ListFilter{
					View: in.Filter.View, Type: items.Type(in.Filter.Type),
					Status: items.Status(in.Filter.Status), Q: in.Filter.Q, Root: in.Filter.Root,
				})
				if err != nil {
					return nil, err
				}
				out["items"] = append(out["items"].([]items.Item), list...)
			}

			if in.Repos && !c.Unbound {
				rootID, err := callerRootID(ctx, s, c)
				if err != nil {
					return nil, err
				}
				repos, err := s.RT.ConfirmedRepos(ctx, rootID)
				if err != nil {
					return nil, err
				}
				out["confirmed_repos"] = repos
			}

			expired, err := s.RT.Events.Expired(ctx, in.SinceSeq)
			if err != nil {
				return nil, err
			}
			after := in.SinceSeq
			if expired {
				out["reset"] = true
				if after, err = s.RT.Events.Latest(ctx); err != nil {
					return nil, err
				}
			} else {
				out["reset"] = false
			}
			evs, err := s.RT.Events.After(ctx, after, 500)
			if err != nil {
				return nil, err
			}
			out["events"] = evs
			if len(evs) > 0 {
				after = evs[len(evs)-1].Seq
			}
			out["cursor"] = after
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

const kbFullSchema = kbSearchGetSchema + `,
	"subdir":{"type":"string"},"filename":{"type":"string"},"title":{"type":"string"},"body":{"type":"string"}`

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
			Op       string `json:"op"`
			Q        string `json:"q"`
			Slug     string `json:"slug"`
			Limit    int    `json:"limit"`
			Subdir   string `json:"subdir"`
			Filename string `json:"filename"`
			Title    string `json:"title"`
			Body     string `json:"body"`
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
			if hits == nil {
				hits = []kb.Hit{}
			}
			return map[string]any{"hits": hits}, nil
		case "get":
			doc, err := s.KB.Get(ctx, in.Slug)
			if err != nil {
				return nil, err
			}
			return doc, nil
		case "write":
			if !canWrite {
				return nil, errors.New("read-only: an unbound caller cannot write to the knowledge base")
			}
			return kbWrite(ctx, s, in.Subdir, in.Filename, in.Title, in.Body)
		default:
			return nil, fmt.Errorf("op must be search, get or write, got %q", in.Op)
		}
	}
}

// kbWrite writes a new markdown document under KB.Dir and re-syncs the index.
// subdir/filename come from an agent, so ".." is refused before it reaches a
// path.Join.
func kbWrite(ctx context.Context, s *Server, subdir, filename, title, body string) (any, error) {
	if strings.Contains(subdir, "..") || strings.Contains(filename, "..") {
		return nil, errors.New("bad_request: subdir and filename cannot contain \"..\"")
	}
	if filename == "" {
		return nil, errors.New("bad_request: filename is required")
	}
	if !strings.HasSuffix(filename, ".md") {
		filename += ".md"
	}
	dir := filepath.Join(s.KB.Dir, filepath.Clean(subdir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, filepath.Clean(filename))
	content := fmt.Sprintf("---\ntitle: %q\n---\n\n%s\n", title, body)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, err
	}
	if err := s.KB.Sync(ctx); err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(s.KB.Dir, path)
	if err != nil {
		return nil, err
	}
	slug := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
	return map[string]any{"slug": slug}, nil
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
			return map[string]any{"advice_id": adv.ID, "state": adv.State, "answer": adv.Answer,
				"error": adv.Error}, nil
		},
	}
}
