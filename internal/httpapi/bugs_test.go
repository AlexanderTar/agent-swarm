package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBugReportsRouteListsNewestFirst(t *testing.T) {
	e, _ := newRuntimeServer(t)
	for _, r := range []struct {
		id, title string
		at        int
		board     any
	}{{"bug_a", "Older bug", 1000, nil}, {"bug_b", "Newer bug", 2000, "BUG-4"}} {
		if _, err := e.s.DB.ExecContext(bg, `INSERT INTO bug_reports
			(id, created_at, reporter_agent_id, reporter_agent_name, session_id, root_item_key, title, what_happened, board_item_key)
			VALUES (?, ?, 'agt_1', 'coder-1', 'ses_1', 'CHORE-1', ?, 'x', ?)`, r.id, r.at, r.title, r.board); err != nil {
			t.Fatal(err)
		}
	}
	rec := e.get(t, "/api/bugs")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 || list[0]["id"] != "bug_b" || list[1]["id"] != "bug_a" {
		t.Fatalf("list = %v", list)
	}
	for k, want := range map[string]any{"title": "Newer bug", "reporter": "coder-1", "root_item_key": "CHORE-1", "board_item_key": "BUG-4"} {
		if list[0][k] != want {
			t.Errorf("%s = %v, want %v", k, list[0][k], want)
		}
	}
	if list[1]["board_item_key"] != "" {
		t.Errorf("unset board_item_key = %v, want empty", list[1]["board_item_key"])
	}
}

func seedFullBug(t *testing.T, e *runtimeEnv) {
	t.Helper()
	if _, err := e.s.DB.ExecContext(bg, `INSERT INTO bug_reports
		(id, created_at, reporter_agent_id, reporter_agent_name, session_id, root_item_key, title,
		 what_happened, repro, evidence, user_said, cause, area, transcript_path, board_item_key)
		VALUES ('bug_full', 3000, 'agt_1', 'coder-1', 'ses_1', 'CHORE-1', 'Full bug',
		 'it broke', 'run x', 'log y', 'user z', 'cause c', 'mcp', '/t/transcript.jsonl', 'BUG-9')`); err != nil {
		t.Fatal(err)
	}
}

func TestBugReportRouteReturnsFullReport(t *testing.T) {
	e, _ := newRuntimeServer(t)
	seedFullBug(t, e)
	rec := e.get(t, "/api/bugs/bug_full")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	for k, want := range map[string]any{"id": "bug_full", "title": "Full bug", "reporter": "coder-1",
		"root_item_key": "CHORE-1", "board_item_key": "BUG-9", "what_happened": "it broke", "repro": "run x",
		"evidence": "log y", "user_said": "user z", "cause": "cause c", "area": "mcp",
		"transcript_path": "/t/transcript.jsonl", "issue_url": ""} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if rec := e.get(t, "/api/bugs/bug_nope"); rec.Code != 404 {
		t.Errorf("unknown id status = %d, want 404", rec.Code)
	}
}

func TestBugIssueURLRoundTripsAndRefusesSecondSet(t *testing.T) {
	e, _ := newRuntimeServer(t)
	seedFullBug(t, e)
	const url = "https://github.com/AlexanderTar/agent-swarm/issues/7"
	if rec := e.post(t, "/api/bugs/bug_full/issue", `{"issue_url":"`+url+`"}`); rec.Code != 200 {
		t.Fatalf("set status = %d: %s", rec.Code, rec.Body.String())
	}
	var one, list = map[string]any{}, []map[string]any{}
	json.Unmarshal(e.get(t, "/api/bugs/bug_full").Body.Bytes(), &one)
	json.Unmarshal(e.get(t, "/api/bugs").Body.Bytes(), &list)
	if one["issue_url"] != url || len(list) != 1 || list[0]["issue_url"] != url {
		t.Fatalf("issue_url not stored: one=%v list=%v", one["issue_url"], list)
	}
	rec := e.post(t, "/api/bugs/bug_full/issue", `{"issue_url":"https://github.com/x/y/issues/8"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), url) {
		t.Fatalf("second set = %d %s, want 409 naming the stored url", rec.Code, rec.Body.String())
	}
	if rec := e.post(t, "/api/bugs/bug_nope/issue", `{"issue_url":"u"}`); rec.Code != 404 {
		t.Errorf("unknown id status = %d, want 404", rec.Code)
	}
	if rec := e.post(t, "/api/bugs/bug_full/issue", `{"issue_url":""}`); rec.Code != 400 {
		t.Errorf("empty url status = %d, want 400", rec.Code)
	}
}
