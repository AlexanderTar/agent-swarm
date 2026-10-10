package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func ghPRJSON(state, rollup string) string {
	return `{"state":"` + state + `","headRefName":"swarm/chore-1","baseRefName":"main","number":412,
 "url":"` + prURL + `","autoMergeRequest":{},"mergeCommit":` +
		map[bool]string{true: `{"oid":"def456"}`, false: `null`}[state == "MERGED"] +
		`,"statusCheckRollup":` + rollup + `}`
}

const (
	rollupFailing = `[{"name":"test (ubuntu)","status":"COMPLETED","conclusion":"FAILURE"},{"name":"lint","status":"COMPLETED","conclusion":"FAILURE"}]`
	rollupPassing = `[{"name":"test","status":"COMPLETED","conclusion":"SUCCESS"}]`
)

// watchFixture: finishFixture with a successful finishing on an armed, passing PR.
func watchFixture(t *testing.T) (s *Store, orch Agent, key string) {
	t.Helper()
	s, orch, ses, key, _ := finishFixture(t, "https://github.com/o/proj.git", "auto")
	fakeGH(s, map[string]execx.Result{prView(prURL): {Out: ghOpenArmed}})
	if _, err := finishPR(s, ses); err != nil {
		t.Fatal(err)
	}
	return s, orch, key
}

// tick runs one WatchMerges pass against a fresh fake and returns it.
func tick(t *testing.T, s *Store, r map[string]execx.Result) *execx.Fake {
	t.Helper()
	f := &execx.Fake{Responses: r}
	s.Exec = f.Runner()
	if err := s.WatchMerges(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func relays(t *testing.T, s *Store, to, event string) []string {
	t.Helper()
	rows, err := s.DB.QueryContext(context.Background(), `SELECT payload_json FROM messages
		WHERE kind = 'relay' AND to_agent_id = ? AND json_extract(payload_json, '$.event') = ? ORDER BY seq`, to, event)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func mergeRow(t *testing.T, s *Store, repo string) (state, checks, sha string) {
	t.Helper()
	if err := s.DB.QueryRowContext(context.Background(), `SELECT state, checks, COALESCE(merged_sha,'')
		FROM item_merges WHERE repo = ?`, repo).Scan(&state, &checks, &sha); err != nil {
		t.Fatal(err)
	}
	return
}

func TestWatchMergesChecksFailedRelaysOncePerTransition(t *testing.T) {
	s, orch, key := watchFixture(t)
	failing := map[string]execx.Result{prView(prURL): {Out: ghPRJSON("OPEN", rollupFailing)}}
	tick(t, s, failing)
	got := relays(t, s, orch.ID, "pr_checks_failed")
	want := fmt.Sprintf(`{"event":"pr_checks_failed","item":%q,"repo":"proj","url":%q,"number":412,"failing":["test (ubuntu)","lint"]}`, key, prURL)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("relays = %v, want [%s]", got, want)
	}
	n := notified(t, s, "pr.checks_failed")
	if n.Args["KEY"] != key || n.Args["repo"] != "proj" || n.Args["N"] != "412" || n.Args["checks"] != "test (ubuntu), lint" {
		t.Fatalf("args = %v", n.Args)
	}
	tick(t, s, failing)
	if got := relays(t, s, orch.ID, "pr_checks_failed"); len(got) != 1 {
		t.Fatalf("second failing tick relays = %d", len(got))
	}
	tick(t, s, map[string]execx.Result{prView(prURL): {Out: ghPRJSON("OPEN", rollupPassing)}})
	if _, checks, _ := mergeRow(t, s, "proj"); checks != "passing" {
		t.Fatalf("checks = %q", checks)
	}
	tick(t, s, failing)
	if got := relays(t, s, orch.ID, "pr_checks_failed"); len(got) != 2 {
		t.Fatalf("failing again relays = %d", len(got))
	}
	if c := notifiedCount(s, "pr.checks_failed"); c != 2 {
		t.Fatalf("notifications = %d", c)
	}
}

func TestWatchMergesMergedMovesRootToDone(t *testing.T) {
	s, _, key := watchFixture(t)
	tick(t, s, map[string]execx.Result{prView(prURL): {Out: ghPRJSON("MERGED", `[]`)}})
	if state, _, sha := mergeRow(t, s, "proj"); state != "merged" || sha != "def456" {
		t.Fatalf("row = %s %s", state, sha)
	}
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status = %s", st)
	}
	if n := notified(t, s, "item.merged"); n.Args["summary"] != "PR open" {
		t.Fatalf("item.merged args = %v, want the finishing summary", n.Args)
	}
}

func TestWatchMergesMergedOneOfTwoStaysInReview(t *testing.T) {
	s, orch, ses, key, _ := finishFixture(t, "https://github.com/o/proj.git", "auto")
	ctx := context.Background()
	mustExec(t, s.DB, `INSERT INTO repos (id, name, path, remote_url, remote_owner, default_branch, source, created_at, updated_at)
		VALUES ('repo_endurio', 'endurio', '/tmp/endurio', 'https://github.com/o/endurio.git', 'o', 'main', 'manual', 1, 1)`)
	mustExec(t, s.DB, `UPDATE checkpoints SET git_json = ? WHERE agent_id = ? AND kind = 'integrated'`,
		`[{"repo":"proj","branch":"swarm/chore-1","sha":"3f9c2ab0000"},{"repo":"endurio","branch":"swarm/chore-1","sha":"aaa1110000"}]`, orch.ID)
	endURL := "https://github.com/o/endurio/pull/7"
	endOpen := strings.ReplaceAll(strings.ReplaceAll(ghOpenArmed, prURL, endURL), `"number":412`, `"number":7`)
	fakeGH(s, map[string]execx.Result{prView(prURL): {Out: ghOpenArmed}, prView(endURL): {Out: endOpen}})
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Finishing, Summary: "PRs open",
		PRs: []FinishPR{{Repo: "proj", URL: prURL}, {Repo: "endurio", URL: endURL}}}); err != nil {
		t.Fatal(err)
	}
	tick(t, s, map[string]execx.Result{prView(prURL): {Out: ghPRJSON("MERGED", `[]`)}, prView(endURL): {Out: endOpen}})
	if st := itemStatus(t, s, key); st != items.InReview {
		t.Fatalf("status = %s", st)
	}
	mp, err := s.MergeProgressFor(ctx, mustItemID(t, s, key))
	if err != nil || mp == nil || *mp != (MergeProgress{Merged: 1, Total: 2}) {
		t.Fatalf("progress = %+v, %v", mp, err)
	}
}

func TestWatchMergesClosedRelaysAndReopensWork(t *testing.T) {
	s, orch, key := watchFixture(t)
	ctx := context.Background()
	tick(t, s, map[string]execx.Result{prView(prURL): {Out: ghPRJSON("CLOSED", `[]`)}})
	if state, _, _ := mergeRow(t, s, "proj"); state != "closed" {
		t.Fatalf("state = %s", state)
	}
	got := relays(t, s, orch.ID, "pr_closed")
	want := fmt.Sprintf(`{"event":"pr_closed","item":%q,"repo":"proj","url":%q,"number":412}`, key, prURL)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("relays = %v, want [%s]", got, want)
	}
	if st := itemStatus(t, s, key); st != items.InProgress {
		t.Fatalf("status = %s", st)
	}
	id := mustItemID(t, s, key)
	openAccept := func() int {
		var n int
		s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE item_id = ? AND kind = 'accept_fix' AND state = 'open'`, id).Scan(&n)
		return n
	}
	if n := openAccept(); n != 0 {
		t.Fatalf("open accept_fix = %d", n)
	}
	ses := mustSessionID(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Integrated, Summary: "again",
		Git:          []GitRef{{Repo: "proj", Branch: "swarm/chore-1", SHA: "4a0000000000"}},
		Verification: []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	if n := openAccept(); n != 1 {
		t.Fatalf("open accept_fix after re-integrate = %d", n)
	}
}

func TestWatchMergesGHErrorLeavesRowAndLogs(t *testing.T) {
	s, _, _ := watchFixture(t)
	var logged []string
	s.Log = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	tick(t, s, map[string]execx.Result{prView(prURL): {Err: errors.New("gh: exit status 1: boom")}})
	if state, checks, _ := mergeRow(t, s, "proj"); state != "open" || checks != "passing" {
		t.Fatalf("row = %s %s", state, checks)
	}
	prefix := "merges: gh pr view " + prURL + ": "
	if len(logged) != 1 || !strings.HasPrefix(logged[0], prefix) {
		t.Fatalf("logged = %v", logged)
	}
}

func TestWatchMergesSkipsRootNotInReview(t *testing.T) {
	s, _, key := watchFixture(t)
	mustExec(t, s.DB, `UPDATE items SET status = 'in_progress' WHERE key = ?`, key)
	if f := tick(t, s, nil); len(f.Calls()) != 0 {
		t.Fatalf("calls = %v", f.Calls())
	}
}
