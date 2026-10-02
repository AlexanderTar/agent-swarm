package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// agentSwarmRepoName is the catalog name swarm_report_bug suggests as the
// bug's repository.
const agentSwarmRepoName = "agent-swarm"

// ---------- swarm_report_bug ----------

// reportBugTool lets any bound agent file a bug against agent-swarm itself.
// swarm_items create without a parent can't serve that: it is refused to
// children, and items.CreateTx refuses a chore's orchestrator, so this tool
// creates the Draft root bug as the daemon and sets the origin itself.
func reportBugTool(s *Server) ToolDef {
	return ToolDef{
		Name: "swarm_report_bug",
		Description: "Report a bug in Swarm itself. Creates a Draft top-level bug item that the user triages; " +
			"any bound agent may call it. Never use it for bugs in the code you were assigned.",
		Schema: objSchemaRequired(`"title":{"type":"string"},"what_happened":{"type":"string"},
			"repro":{"type":"string"},"evidence":{"type":"string"},"user_said":{"type":"string"},
			"cause":{"type":"string"},"area":{"type":"string"},"request_id":{"type":"string"}`,
			[]string{"title", "what_happened"}),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Title        string `json:"title"`
				WhatHappened string `json:"what_happened"`
				Repro        string `json:"repro"`
				Evidence     string `json:"evidence"`
				UserSaid     string `json:"user_said"`
				Cause        string `json:"cause"`
				Area         string `json:"area"`
				RequestID    string `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			a, err := callerAgent(ctx, s, c)
			if err != nil {
				return nil, err
			}
			var out items.Item
			if _, err := runtime.IdemTx(ctx, s.RT, c.SessionID, in.RequestID, "swarm_report_bug", &out,
				func(tx *sql.Tx) (err error) {
					var suggested []string
					var repoID string
					switch err := tx.QueryRowContext(ctx, `SELECT id FROM repos WHERE name = ? LIMIT 1`, agentSwarmRepoName).Scan(&repoID); {
					case err == nil:
						suggested = []string{repoID}
					case !errors.Is(err, sql.ErrNoRows):
						return err
					}
					out, err = s.RT.Items.CreateTx(ctx, tx, items.CreateInput{
						Type: items.Bug, Title: in.Title, Brief: bugBrief(in.WhatHappened, in.Repro, in.Evidence, in.UserSaid, in.Cause, in.Area),
						SuggestedRepos: suggested, OriginSpikeID: a.RootItemID,
					}, items.Daemon())
					if err != nil {
						return err
					}
					var originKey string
					if err := tx.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.RootItemID).Scan(&originKey); err != nil {
						return err
					}
					return s.RT.NotifyItemCreated(ctx, tx, originKey, out.Key, out.Title, out.Type)
				}); err != nil {
				return nil, err
			}
			return map[string]any{"key": out.Key, "id": out.ID, "status": out.Status}, nil
		},
	}
}

// bugBrief composes the report's fields as labeled bullets, omitting empty ones.
func bugBrief(whatHappened, repro, evidence, userSaid, cause, area string) string {
	var b strings.Builder
	for _, f := range [][2]string{
		{"What happened", whatHappened}, {"Repro", repro}, {"Evidence", evidence},
		{"User said", userSaid}, {"Cause", cause}, {"Area", area},
	} {
		if v := strings.TrimSpace(f[1]); v != "" {
			b.WriteString("- " + f[0] + ": " + v + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
