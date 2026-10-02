package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
