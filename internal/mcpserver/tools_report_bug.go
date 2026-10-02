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

	"github.com/AlexanderTar/agent-swarm/internal/advisor"
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
			// The cached idempotency result carries the transcript too, so a
			// replay returns the same answer.
			var res struct {
				Item       items.Item `json:"item"`
				Transcript string     `json:"transcript"`
			}
			if _, err := runtime.IdemTx(ctx, s.RT, c.SessionID, in.RequestID, "swarm_report_bug", &res,
				func(tx *sql.Tx) (err error) {
					var suggested []string
					var repoID string
					switch err := tx.QueryRowContext(ctx, `SELECT id FROM repos WHERE name = ? LIMIT 1`, agentSwarmRepoName).Scan(&repoID); {
					case err == nil:
						suggested = []string{repoID}
					case !errors.Is(err, sql.ErrNoRows):
						return err
					}
					brief := bugBrief(in.WhatHappened, in.Repro, in.Evidence, in.UserSaid, in.Cause, in.Area)
					res.Item, err = s.RT.Items.CreateTx(ctx, tx, items.CreateInput{
						Type: items.Bug, Title: in.Title, Brief: brief,
						SuggestedRepos: suggested, OriginSpikeID: a.RootItemID,
					}, items.Daemon())
					if err != nil {
						return err
					}
					// The copy's path embeds the key CreateTx just assigned, so
					// the transcript line goes in with a follow-up brief update.
					out := res.Item
					res.Transcript = s.copyTranscript(ctx, tx, c, a, out.Key)
					defer func() {
						if err != nil { // the tx rolls back; don't leave an orphan copy
							_ = os.RemoveAll(filepath.Join(s.RT.Home, "bug-reports", out.Key))
						}
					}()
					brief += "\n- Transcript: " + res.Transcript
					if _, err = s.RT.Items.UpdateTx(ctx, tx, out.Key, items.Patch{Revision: out.Revision, Brief: &brief}, items.Daemon()); err != nil {
						return err
					}
					var originKey string
					if err = tx.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.RootItemID).Scan(&originKey); err != nil {
						return err
					}
					err = s.RT.NotifyItemCreated(ctx, tx, originKey, out.Key, out.Title, out.Type)
					return err
				}); err != nil {
				return nil, err
			}
			return map[string]any{"key": res.Item.Key, "id": res.Item.ID, "status": res.Item.Status, "transcript": res.Transcript}, nil
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

// copyTranscript best-effort copies the reporter's own transcript to
// <home>/bug-reports/<key>/transcript<ext> (the agent's work dir is deleted
// once it finishes). It returns the copied path, or "unavailable (<reason>)";
// a transcript problem never fails the report.
func (s *Server) copyTranscript(ctx context.Context, tx *sql.Tx, c Caller, a runtime.Agent, key string) string {
	var cwd, providerID string
	if err := tx.QueryRowContext(ctx, `SELECT cwd, COALESCE(provider_session_id, '') FROM sessions WHERE id = ?`, c.SessionID).
		Scan(&cwd, &providerID); err != nil {
		return "unavailable (" + err.Error() + ")"
	}
	userHome, _ := os.UserHomeDir()
	if s.Advisor != nil && s.Advisor.UserHome != "" {
		userHome = s.Advisor.UserHome
	}
	// ponytail: derived path only; runtime doesn't persist a hook-reported transcript_path (codex has none derivable), upgrade by storing it on the session.
	src, err := advisor.TranscriptPath(a.Kind, userHome, cwd, providerID)
	if err != nil {
		return "unavailable (" + err.Error() + ")"
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Sprintf("unavailable (%v)", err)
	}
	dir := filepath.Join(s.RT.Home, "bug-reports", key)
	dst := filepath.Join(dir, "transcript"+filepath.Ext(src))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "unavailable (" + err.Error() + ")"
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "unavailable (" + err.Error() + ")"
	}
	return dst
}
