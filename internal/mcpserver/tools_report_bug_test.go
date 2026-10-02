package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/advisor"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// reportBugSeed is a coder worker under a chore root, plus the agent-swarm
// catalog repo row the tool resolves by name.
type reportBugSeed struct {
	Caller    Caller
	ChoreKey  string
	SessionID string
	RepoID    string
}

func newReportBugServer(t *testing.T) (*Server, reportBugSeed) {
	t.Helper()
	s := newTestServer(t)
	ctx := context.Background()
	now := db.Millis(s.RT.Now())
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO items
		(id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_chore', 'CHORE-77', 'chore', 'itm_chore', 'A chore', 'in_progress', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	agentID, sessionID, _ := seedAgentAndSession(t, s, runtime.RoleCoder, "itm_chore", "itm_chore")
	var name string
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, agentID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO repos
		(id, path, name, default_branch, source, created_at, updated_at)
		VALUES ('rep_swarm', ?, 'agent-swarm', 'main', 'manual', ?, ?)`, gitRepoWithCommit(t), now, now); err != nil {
		t.Fatal(err)
	}
	return s, reportBugSeed{
		Caller:   Caller{SessionID: sessionID, AgentID: agentID, AgentName: name, Role: runtime.RoleCoder},
		ChoreKey: "CHORE-77", SessionID: sessionID, RepoID: "rep_swarm",
	}
}

type reportBugOut struct {
	Key        string `json:"key"`
	ID         string `json:"id"`
	Status     string `json:"status"`
	Transcript string `json:"transcript"`
}

func TestReportBugCreatesDraftBugWithComposedBrief(t *testing.T) {
	s, seed := newReportBugServer(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_report_bug",
		`{"title":"Sync drops acks","what_happened":"ack ignored","repro":"call sync twice","area":"mcpserver"}`)
	if err != nil {
		t.Fatal(err)
	}
	var got reportBugOut
	json.Unmarshal(mustJSON(out), &got)
	if got.Key == "" || got.ID == "" || got.Status != "draft" {
		t.Fatalf("out = %+v", got)
	}
	it, err := s.RT.Items.Get(ctx, got.Key)
	if err != nil {
		t.Fatal(err)
	}
	if string(it.Type) != "bug" || string(it.Status) != "draft" || it.ParentKey != "" {
		t.Fatalf("item = %s/%s parent %q", it.Type, it.Status, it.ParentKey)
	}
	for _, want := range []string{"- What happened: ack ignored", "- Repro: call sync twice", "- Area: mcpserver"} {
		if !strings.Contains(it.Brief, want) {
			t.Fatalf("brief missing %q:\n%s", want, it.Brief)
		}
	}
	for _, unwanted := range []string{"- Evidence:", "- User said:", "- Cause:"} {
		if strings.Contains(it.Brief, unwanted) {
			t.Fatalf("brief has empty field %q:\n%s", unwanted, it.Brief)
		}
	}
	if len(it.SuggestedRepos) != 1 || it.SuggestedRepos[0] != seed.RepoID || len(it.Repos) != 0 {
		t.Fatalf("repos = %v suggested = %v", it.Repos, it.SuggestedRepos)
	}
	if it.OriginSpikeID != "itm_chore" {
		t.Fatalf("origin = %q, want the reporter's root", it.OriginSpikeID)
	}
	f := s.RT.Notify.(*fakeNotifier)
	f.mu.Lock()
	defer f.mu.Unlock()
	var found bool
	for _, n := range f.raised {
		if n.Kind == "item.created.bug" && n.Args["SPIKE-KEY"] == seed.ChoreKey && n.Args["ROOT-KEY"] == got.Key {
			found = true
		}
	}
	if !found {
		t.Fatalf("no item.created.bug notification from %s: %+v", seed.ChoreKey, f.raised)
	}
}

func TestReportBugCopiesTheReportersTranscript(t *testing.T) {
	s, seed := newReportBugServer(t)
	ctx := context.Background()
	// A claude session derives its transcript path from cwd + provider id.
	if _, err := s.RT.DB.ExecContext(ctx, `UPDATE agents SET kind = 'claude' WHERE id = ?`, seed.Caller.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.DB.ExecContext(ctx, `UPDATE sessions SET provider_session_id = 'prov-1' WHERE id = ?`, seed.SessionID); err != nil {
		t.Fatal(err)
	}
	var cwd string
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT cwd FROM sessions WHERE id = ?`, seed.SessionID).Scan(&cwd); err != nil {
		t.Fatal(err)
	}
	src, err := advisor.TranscriptPath(runtime.Claude, s.Advisor.UserHome, cwd, "prov-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
		t.Fatal(err)
	}
	const body = `{"type":"user","message":"hi"}` + "\n"
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := s.call(ctx, seed.Caller, "swarm_report_bug", `{"title":"Transcript bug","what_happened":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	var got reportBugOut
	json.Unmarshal(mustJSON(out), &got)
	want := filepath.Join(s.RT.Home, "bug-reports", got.Key, "transcript.jsonl")
	if got.Transcript != want {
		t.Fatalf("transcript = %q, want %q", got.Transcript, want)
	}
	data, err := os.ReadFile(want)
	if err != nil || string(data) != body {
		t.Fatalf("copy = %q, %v", data, err)
	}
	if fi, _ := os.Stat(want); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	it, _ := s.RT.Items.Get(ctx, got.Key)
	if !strings.Contains(it.Brief, "- Transcript: "+want) {
		t.Fatalf("brief missing transcript line:\n%s", it.Brief)
	}
}

func TestReportBugMissingTranscriptStillCreatesTheItem(t *testing.T) {
	s, seed := newReportBugServer(t)
	ctx := context.Background()
	// The fake agent kind has no derivable transcript path.
	out, err := s.call(ctx, seed.Caller, "swarm_report_bug", `{"title":"No transcript","what_happened":"x"}`)
	if err != nil {
		t.Fatalf("a missing transcript must not fail the report: %v", err)
	}
	var got reportBugOut
	json.Unmarshal(mustJSON(out), &got)
	if !strings.HasPrefix(got.Transcript, "unavailable (") {
		t.Fatalf("transcript = %q, want unavailable (...)", got.Transcript)
	}
	it, _ := s.RT.Items.Get(ctx, got.Key)
	if !strings.Contains(it.Brief, "- Transcript: unavailable (") {
		t.Fatalf("brief missing unavailable line:\n%s", it.Brief)
	}
	if _, err := os.Stat(filepath.Join(s.RT.Home, "bug-reports", got.Key)); !os.IsNotExist(err) {
		t.Fatalf("no bug-reports dir expected without a transcript: %v", err)
	}
}

func TestReportBugRefusesInvalidInput(t *testing.T) {
	s, seed := newReportBugServer(t)
	ctx := context.Background()
	long := strings.Repeat("x", 121)
	for _, tc := range []struct{ name, args, want string }{
		{"missing title", `{"what_happened":"x"}`, "title is required"},
		{"short title", `{"title":"ab","what_happened":"x"}`, "title must be 3-120 characters"},
		{"long title", `{"title":"` + long + `","what_happened":"x"}`, "title must be 3-120 characters"},
		{"missing what_happened", `{"title":"A fine title"}`, "what_happened is required"},
		{"blank what_happened", `{"title":"A fine title","what_happened":"  "}`, "what_happened is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.call(ctx, seed.Caller, "swarm_report_bug", tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	var n int
	s.RT.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE type = 'bug'`).Scan(&n)
	if n != 0 {
		t.Fatalf("a refused report created %d bug items", n)
	}
}

func TestReportBugRefusesUnboundCallers(t *testing.T) {
	s, _ := newReportBugServer(t)
	_, err := s.call(context.Background(), Caller{Unbound: true}, "swarm_report_bug",
		`{"title":"A fine title","what_happened":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want refusal", err)
	}
}
