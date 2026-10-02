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
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/advisor"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
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
		Description: "Report a bug in Swarm itself. Always records the report in Swarm's local bug store (read with `swarm bugs`); " +
			"any bound agent may call it. Never for bugs in your assigned code. " +
			"Only pass board:true, which also creates a Draft top-level bug item on the board, when the user's Swarm instructions ask for it.",
		Schema: objSchemaRequired(`"title":{"type":"string"},"what_happened":{"type":"string"},
			"repro":{"type":"string"},"evidence":{"type":"string"},"user_said":{"type":"string"},
			"cause":{"type":"string"},"area":{"type":"string"},"board":{"type":"boolean"},"request_id":{"type":"string"}`,
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
				Board        bool   `json:"board"`
				RequestID    string `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			in.Title = strings.TrimSpace(in.Title)
			switch n := utf8.RuneCountInString(in.Title); {
			case n == 0:
				return nil, errors.New("title is required (3-120 characters)")
			case n < 3 || n > 120:
				return nil, fmt.Errorf("title must be 3-120 characters, got %d", n)
			}
			if strings.TrimSpace(in.WhatHappened) == "" {
				return nil, errors.New("what_happened is required: say what went wrong in Swarm")
			}
			a, err := callerAgent(ctx, s, c)
			if err != nil {
				return nil, err
			}
			// The cached idempotency result carries the transcript too, so a
			// replay returns the same answer.
			var res struct {
				ID         string `json:"id"`
				Transcript string `json:"transcript"`
				ItemKey    string `json:"item_key,omitempty"`
			}
			if _, err := runtime.IdemTx(ctx, s.RT, c.SessionID, in.RequestID, "swarm_report_bug", &res,
				func(tx *sql.Tx) (err error) {
					res.ID = ids.New("bug")
					res.Transcript = s.copyTranscript(ctx, tx, c, a, res.ID)
					id := res.ID
					defer func() {
						if err != nil { // the tx rolls back; don't leave an orphan copy
							_ = os.RemoveAll(filepath.Join(s.RT.Home, "bug-reports", id))
						}
					}()
					var rootKey string
					if err = tx.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.RootItemID).Scan(&rootKey); err != nil {
						return err
					}
					_, err = tx.ExecContext(ctx, `INSERT INTO bug_reports
						(id, created_at, reporter_agent_id, reporter_agent_name, session_id, root_item_key, title,
						 what_happened, repro, evidence, user_said, cause, area, transcript_path)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
						id, db.Millis(s.RT.Now()), a.ID, a.Name, c.SessionID, rootKey, in.Title,
						in.WhatHappened, in.Repro, in.Evidence, in.UserSaid, in.Cause, in.Area, res.Transcript)
					if err != nil || !in.Board {
						return err
					}
					res.ItemKey, err = s.createBoardBug(ctx, tx, a, rootKey, in.Title,
						bugBrief(in.WhatHappened, in.Repro, in.Evidence, in.UserSaid, in.Cause, in.Area)+"\n- Transcript: "+res.Transcript)
					if err != nil {
						return err
					}
					_, err = tx.ExecContext(ctx, `UPDATE bug_reports SET board_item_key = ? WHERE id = ?`, res.ItemKey, id)
					return err
				}); err != nil {
				return nil, err
			}
			out := map[string]any{"id": res.ID, "transcript": res.Transcript}
			if res.ItemKey != "" {
				out["item_key"] = res.ItemKey
			}
			return out, nil
		},
	}
}

// createBoardBug creates the Draft top-level bug item for a board:true report
// as the daemon and notifies the reporter's root's orchestrator.
func (s *Server) createBoardBug(ctx context.Context, tx *sql.Tx, a runtime.Agent, rootKey, title, brief string) (string, error) {
	var suggested []string
	var repoID string
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM repos WHERE name = ? LIMIT 1`, agentSwarmRepoName).Scan(&repoID); {
	case err == nil:
		suggested = []string{repoID}
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	it, err := s.RT.Items.CreateTx(ctx, tx, items.CreateInput{
		Type: items.Bug, Title: title, Brief: brief,
		SuggestedRepos: suggested, OriginSpikeID: a.RootItemID,
	}, items.Daemon())
	if err != nil {
		return "", err
	}
	return it.Key, s.RT.NotifyItemCreated(ctx, tx, rootKey, it.Key, it.Title, it.Type)
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
// <home>/bug-reports/<id>/transcript<ext> (the agent's work dir is deleted
// once it finishes). It returns the copied path, or "unavailable (<reason>)";
// a transcript problem never fails the report.
func (s *Server) copyTranscript(ctx context.Context, tx *sql.Tx, c Caller, a runtime.Agent, id string) string {
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
	dir := filepath.Join(s.RT.Home, "bug-reports", id)
	dst := filepath.Join(dir, "transcript"+filepath.Ext(src))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "unavailable (" + err.Error() + ")"
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "unavailable (" + err.Error() + ")"
	}
	return dst
}
