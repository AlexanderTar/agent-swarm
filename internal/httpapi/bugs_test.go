package httpapi

import (
	"encoding/json"
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
