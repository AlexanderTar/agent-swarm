// Package mcpserver exposes the Swarm tools over MCP (spec §8, L16). The tool
// list is filtered by the caller's role, and the caller is resolved from the
// bearer token — never from a tool argument.
package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/AlexanderTar/agent-swarm/internal/advisor"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Caller is who is asking: resolved once, from the bearer token, and carried
// through every tool call. A tool argument can never change it (L16).
type Caller struct {
	SessionID, AgentID, AgentName string
	Role                          runtime.Role
	Kind                          runtime.AgentKind
	AdvisorMode                   string
	Unbound                       bool
	SpikeOrchestrator             bool
}

// Server carries the runtime state every tool handler reads and writes.
type Server struct {
	RT      *runtime.Store
	KB      *kb.Index
	Advisor *advisor.Service

	// Version is reported in the MCP initialize response.
	Version string
	Log     func(format string, args ...any)
}

// ToolDef is one MCP tool: a hand-written JSON Schema and a handler taking
// raw arguments, so this package owns its own validation (§8).
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Roles       []runtime.Role
	Unbound     bool
	Handler     func(ctx context.Context, c Caller, args json.RawMessage) (any, error)
}

// Tools returns every tool this server knows, whatever the caller — used to
// validate descriptions and schemas across the whole registry.
func (s *Server) Tools() []ToolDef {
	all := sharedTools(s) // includes the read-only swarm_kb variant
	all = append(all, kbFullTool(s), advisorTool(s))
	all = append(all, orchestratorTools(s)...)
	all = append(all, materializeTool(s))
	return all
}

// ToolsFor is the role filter (§8, I6, L28): the caller's role and mode decide
// which of the tools above it can see. Kept explicit, not clever, because the
// six roles and their exceptions are exactly the ones the spec lists.
func (s *Server) ToolsFor(c Caller) []ToolDef {
	if c.Unbound {
		return []ToolDef{readTool(s), kbReadOnlyTool(s)}
	}
	out := sharedTools(s)
	for i, d := range out {
		if d.Name == "swarm_kb" {
			out[i] = kbToolFor(s) // a bound caller may write; unbound never reaches this branch
		}
	}
	if c.AdvisorMode == "simulated" {
		out = append(out, advisorTool(s))
	}
	if c.Role == runtime.RoleOrchestrator {
		out = append(out, orchestratorTools(s)...)
		if c.SpikeOrchestrator {
			out = append(out, materializeTool(s))
		}
	}
	return out
}

// sharedTools is §8.1's six tools available to every bound caller, minus
// swarm_kb (ToolsFor picks the read-only or full schema separately) which
// this slice still includes as a placeholder so Tools() enumerates it once;
// ToolsFor overwrites it in place above.
func sharedTools(s *Server) []ToolDef {
	return []ToolDef{
		syncTool(s), checkpointTool(s), askTool(s), sendTool(s), readTool(s), kbReadOnlyTool(s),
	}
}

// dispatch applies the pause allow-list (C3) before the handler runs.
func (s *Server) dispatch(ctx context.Context, c Caller, d ToolDef, args json.RawMessage) (any, error) {
	if c.SessionID != "" {
		state, err := s.RT.SessionState(ctx, c.SessionID)
		if err != nil {
			return nil, err
		}
		if err := runtime.PauseAllowed(state, d.Name); err != nil {
			return nil, err
		}
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return d.Handler(ctx, c, args)
}

// MCPServer builds a server carrying only this caller's tools.
func (s *Server) MCPServer(c Caller) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "swarm", Version: s.Version}, nil)
	for _, d := range s.ToolsFor(c) {
		def := d
		srv.AddTool(&mcp.Tool{Name: def.Name, Description: def.Description, InputSchema: json.RawMessage(def.Schema)},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				// v1.8.0's CallToolParamsRaw.Arguments is already json.RawMessage
				// (verified against the module cache; an earlier draft of this file
				// re-marshalled req.Params.Arguments from `any`, which no longer
				// matches this SDK version and is unnecessary).
				out, err := s.dispatch(ctx, c, def, req.Params.Arguments)
				if err != nil {
					// a one-line MCP tool error (§8)
					return &mcp.CallToolResult{IsError: true,
						Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
				}
				body, _ := json.Marshal(out)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil
			})
	}
	return srv
}

type callerKey struct{}

func withCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func callerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":{"code":"unauthorized","message":"Unauthorized."}}`))
}

// Handler serves /mcp in stateless JSON mode (L16). resolve turns the request's
// bearer token into a caller; a token it does not know gives 401.
func (s *Server) Handler(resolve func(*http.Request) (Caller, bool)) http.Handler {
	inner := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		c, _ := callerFrom(r.Context())
		return s.MCPServer(c)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := resolve(r)
		if !ok {
			writeUnauthorized(w)
			return
		}
		inner.ServeHTTP(w, r.WithContext(withCaller(r.Context(), c)))
	})
}
