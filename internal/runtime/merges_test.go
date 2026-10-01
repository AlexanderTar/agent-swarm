package runtime

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

const ghOpenArmed = `{"state":"OPEN","headRefName":"swarm/chore-1","baseRefName":"main","number":412,
 "url":"https://github.com/o/proj/pull/412","autoMergeRequest":{},"mergeCommit":null,
 "statusCheckRollup":[{"name":"test","status":"COMPLETED","conclusion":"SUCCESS"}]}`
const ghOpenUnarmed = `{"state":"OPEN","headRefName":"swarm/chore-1","baseRefName":"main","number":412,
 "url":"https://github.com/o/proj/pull/412","autoMergeRequest":null,"mergeCommit":null,"statusCheckRollup":[]}`
const ghMerged = `{"state":"MERGED","headRefName":"swarm/chore-1","baseRefName":"main","number":412,
 "url":"https://github.com/o/proj/pull/412","autoMergeRequest":null,"mergeCommit":{"oid":"abc123"},"statusCheckRollup":[]}`

const prURL = "https://github.com/o/proj/pull/412"

// finishFixture: a chore with one integrated repo "proj" (catalog row with remoteURL, default main),
// its orchestrator session, and the accept_fix row approved with $.merge = merge ("" leaves it open).
func finishFixture(t *testing.T, remoteURL, merge string) (s *Store, orch Agent, ses, key, reqID string) {
	t.Helper()
	s, _, _ = newStore(t)
	s.Items.RootDone = s.OnRootDone
	ctx := context.Background()
	mustExec(t, s.DB, `INSERT INTO repos (id, name, path, remote_url, remote_owner, default_branch, source, created_at, updated_at)
		VALUES ('repo_proj', 'proj', '/tmp/proj', ?, 'o', 'main', 'manual', 1, 1)`, nullIf(remoteURL))
	key, orch, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bump deps", Intent: "chore", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses = mustSessionID(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Accepted, Summary: "bumping"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Integrated, Summary: "merged",
		Git:          []GitRef{{Repo: "proj", Branch: "swarm/chore-1", SHA: "3f9c2ab0000"}},
		Verification: []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	it, _ := s.Items.Get(ctx, key)
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE item_id = ? AND kind = 'accept_fix' AND state = 'open'`, it.ID).Scan(&reqID); err != nil {
		t.Fatal(err)
	}
	if merge != "" {
		mustExec(t, s.DB, `UPDATE requests SET state = 'approved', agent_id = ?, responded_at = 2,
			binding_json = json_set(binding_json, '$.merge', ?) WHERE id = ?`, orch.ID, merge, reqID)
	}
	return
}

func fakeGH(s *Store, r map[string]execx.Result) {
	s.Exec = (&execx.Fake{Responses: r}).Runner()
}

func prView(url string) string { return "gh pr view " + url + " --json " + prFields }

func itemStatus(t *testing.T, s *Store, key string) items.Status {
	t.Helper()
	it, err := s.Items.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return it.Status
}

func finishPR(s *Store, ses string) (CheckpointResult, error) {
	return s.WriteCheckpoint(context.Background(), ses, CheckpointInput{Kind: Finishing, Summary: "PR open",
		PRs: []FinishPR{{Repo: "proj", URL: prURL}}})
}

func TestRollupChecks(t *testing.T) {
	type e = struct{ name, status, conclusion, context, state string }
	mk := func(es ...e) ghPR {
		var p ghPR
		for _, x := range es {
			p.StatusCheckRollup = append(p.StatusCheckRollup, struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				Context    string `json:"context"`
				State      string `json:"state"`
			}{x.name, x.status, x.conclusion, x.context, x.state})
		}
		return p
	}
	cases := []struct {
		name    string
		p       ghPR
		want    string
		failing []string
	}{
		{"empty", mk(), "", nil},
		{"one failing check run", mk(e{name: "lint", status: "COMPLETED", conclusion: "SUCCESS"},
			e{name: "test (ubuntu)", status: "COMPLETED", conclusion: "FAILURE"}), "failing", []string{"test (ubuntu)"}},
		{"status context error", mk(e{context: "ci/jenkins", state: "ERROR"}), "failing", []string{"ci/jenkins"}},
		{"in progress", mk(e{name: "test", status: "IN_PROGRESS"}), "pending", nil},
		{"all success", mk(e{name: "test", status: "COMPLETED", conclusion: "SUCCESS"},
			e{context: "ci/x", state: "SUCCESS"}), "passing", nil},
	}
	for _, c := range cases {
		got, failing := rollupChecks(c.p)
		if got != c.want || !slices.Equal(failing, c.failing) {
			t.Errorf("%s: got %q %v, want %q %v", c.name, got, failing, c.want, c.failing)
		}
	}
}

func TestFinishingHappyPROpenRowStaysInReview(t *testing.T) {
	s, _, ses, key, _ := finishFixture(t, "https://github.com/o/proj.git", "auto")
	fakeGH(s, map[string]execx.Result{prView(prURL): {Out: ghOpenArmed}})
	res, err := finishPR(s, ses)
	if err != nil {
		t.Fatal(err)
	}
	if res.CheckpointID == "" || res.ItemStatus != items.InReview {
		t.Fatalf("result = %+v", res)
	}
	ms, err := s.Merges(context.Background(), mustItemID(t, s, key))
	if err != nil || len(ms) != 1 {
		t.Fatalf("merges = %+v, %v", ms, err)
	}
	m := ms[0]
	if m.Kind != "pr" || m.State != "open" || m.Checks != "passing" || !m.AutoMerge || m.Number != 412 ||
		m.Base != "main" || m.Head != "swarm/chore-1" || m.URL != prURL {
		t.Fatalf("row = %+v", m)
	}
	if st := itemStatus(t, s, key); st != items.InReview {
		t.Fatalf("status = %s", st)
	}
}

func TestFinishingPRAlreadyMergedIsDone(t *testing.T) {
	s, _, ses, key, _ := finishFixture(t, "https://github.com/o/proj.git", "auto")
	fakeGH(s, map[string]execx.Result{prView(prURL): {Out: ghMerged}})
	if _, err := finishPR(s, ses); err != nil {
		t.Fatal(err)
	}
	ms, _ := s.Merges(context.Background(), mustItemID(t, s, key))
	if len(ms) != 1 || ms[0].State != "merged" || ms[0].MergedSHA != "abc123" {
		t.Fatalf("merges = %+v", ms)
	}
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status = %s, want done", st)
	}
	if !slices.Contains(s.Notify.(*fakeNotifier).kinds(), "item.merged") {
		t.Fatalf("notifications = %v", s.Notify.(*fakeNotifier).kinds())
	}
}

func TestFinishingLocalMerge(t *testing.T) {
	s, _, ses, key, _ := finishFixture(t, "", "local")
	fakeGH(s, map[string]execx.Result{
		"git -C /tmp/proj merge-base --is-ancestor 3f9c2ab0000 d00d": {},
		"git -C /tmp/proj merge-base --is-ancestor d00d main":        {},
	})
	if _, err := s.WriteCheckpoint(context.Background(), ses, CheckpointInput{Kind: Finishing, Summary: "merged locally",
		Merged: []FinishMerged{{Repo: "proj", SHA: "d00d"}}}); err != nil {
		t.Fatal(err)
	}
	ms, _ := s.Merges(context.Background(), mustItemID(t, s, key))
	if len(ms) != 1 || ms[0].Kind != "local" || ms[0].State != "merged" || ms[0].MergedSHA != "d00d" {
		t.Fatalf("merges = %+v", ms)
	}
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status = %s, want done", st)
	}
}

func TestFinishingRefusals(t *testing.T) {
	gh := "https://github.com/o/proj.git"
	prs := func(url string) CheckpointInput {
		return CheckpointInput{Kind: Finishing, Summary: "s", PRs: []FinishPR{{Repo: "proj", URL: url}}}
	}
	cases := []struct {
		name, remote, merge string
		in                  CheckpointInput
		gh                  map[string]execx.Result
		setup               func(t *testing.T, s *Store, orch Agent, key string) string // returns the session to write from ("" = orch's)
		want                string
	}{
		{name: "worker session", remote: gh, merge: "auto", in: prs(prURL),
			setup: func(t *testing.T, s *Store, orch Agent, key string) string {
				w, _, err := s.Spawn(context.Background(), SpawnInput{ItemKey: key, Role: RoleReviewer, Kind: Fake, Model: "fake-1",
					ParentAgentID: orch.ID, Brief: BriefInput{Objective: "x"}})
				if err != nil {
					t.Fatal(err)
				}
				return mustSessionID(t, s, w.ID)
			},
			want: "Only the top-level orchestrator can write finishing on its own item."},
		{name: "not approved", remote: gh, merge: "", in: prs(prURL),
			want: "Nothing to finish: CHORE-1 has no approved finish request for its latest integration."},
		{name: "missing repo", remote: gh, merge: "auto", in: CheckpointInput{Kind: Finishing, Summary: "s"},
			want: "Missing proj: report a PR or a local merge for every integrated repo."},
		{name: "extra repo", remote: gh, merge: "auto",
			in: CheckpointInput{Kind: Finishing, Summary: "s", PRs: []FinishPR{{Repo: "proj", URL: prURL},
				{Repo: "other", URL: "https://github.com/o/other/pull/1"}}},
			want: "other is not in CHORE-1's integrated checkpoint."},
		{name: "github repo under merged", remote: gh, merge: "auto",
			in:   CheckpointInput{Kind: Finishing, Summary: "s", Merged: []FinishMerged{{Repo: "proj", SHA: "d00d"}}},
			want: "proj has a GitHub remote; report it under prs."},
		{name: "local repo under prs", remote: "", merge: "local", in: prs(prURL),
			want: "proj has no GitHub remote; merge it locally and report it under merged."},
		{name: "bad url", remote: gh, merge: "auto", in: prs("https://github.com/evil/proj/pull/1"),
			want: "https://github.com/evil/proj/pull/1 is not a PR in o's proj repository."},
		{name: "gh missing", remote: gh, merge: "auto", in: prs(prURL),
			gh:   map[string]execx.Result{prView(prURL): {Err: &exec.Error{Name: "gh", Err: exec.ErrNotFound}}},
			want: `gh isn't available to Swarm (exec: "gh": executable file not found in $PATH). Install GitHub CLI and run gh auth login, then send finishing again.`},
		{name: "gh unauthenticated", remote: gh, merge: "auto", in: prs(prURL),
			gh:   map[string]execx.Result{prView(prURL): {Err: errors.New("gh: exit status 1: To get started with GitHub CLI, please run:  gh auth login")}},
			want: "Couldn't read https://github.com/o/proj/pull/412 with gh: To get started with GitHub CLI, please run:  gh auth login. Check gh auth status, then send finishing again."},
		{name: "head mismatch", remote: gh, merge: "auto", in: prs(prURL),
			gh:   map[string]execx.Result{prView(prURL): {Out: `{"state":"OPEN","headRefName":"other","baseRefName":"main","number":412,"autoMergeRequest":{}}`}},
			want: "https://github.com/o/proj/pull/412 merges other, not the integrated branch swarm/chore-1."},
		{name: "base mismatch", remote: gh, merge: "auto", in: prs(prURL),
			gh:   map[string]execx.Result{prView(prURL): {Out: `{"state":"OPEN","headRefName":"swarm/chore-1","baseRefName":"dev","number":412,"autoMergeRequest":{}}`}},
			want: "https://github.com/o/proj/pull/412 targets dev, not proj's default branch main."},
		{name: "closed", remote: gh, merge: "auto", in: prs(prURL),
			gh:   map[string]execx.Result{prView(prURL): {Out: `{"state":"CLOSED","headRefName":"swarm/chore-1","baseRefName":"main","number":412}`}},
			want: "https://github.com/o/proj/pull/412 is closed without merging."},
		{name: "auto not armed", remote: gh, merge: "auto", in: prs(prURL),
			gh: map[string]execx.Result{prView(prURL): {Out: ghOpenUnarmed},
				"gh api repos/o/proj": {Out: `{"allow_squash_merge":false,"allow_merge_commit":true}`}},
			want: "Auto-merge isn't on for https://github.com/o/proj/pull/412. Wait for checks (gh pr checks https://github.com/o/proj/pull/412 --watch), fix failures, merge with gh pr merge https://github.com/o/proj/pull/412 --merge, then send finishing again."},
		{name: "local not merged", remote: "", merge: "local",
			in:   CheckpointInput{Kind: Finishing, Summary: "s", Merged: []FinishMerged{{Repo: "proj", SHA: "d00d"}}},
			gh:   map[string]execx.Result{"git -C /tmp/proj merge-base --is-ancestor 3f9c2ab0000 d00d": {Err: errors.New("git: exit status 1: ")}},
			want: "3f9c2ab is not on proj's main; merge the integrated branch first."},
		{name: "unknown repo", remote: gh, merge: "auto", in: prs(prURL),
			setup: func(t *testing.T, s *Store, orch Agent, key string) string {
				mustExec(t, s.DB, `DELETE FROM repos WHERE id = 'repo_proj'`)
				return ""
			},
			want: "Couldn't find proj in Swarm's repository catalog; register it with swarm_repo_register, then send finishing again."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, orch, ses, key, _ := finishFixture(t, c.remote, c.merge)
			fakeGH(s, c.gh)
			if c.setup != nil {
				if w := c.setup(t, s, orch, key); w != "" {
					ses = w
				}
			}
			before := countRows(t, s, "checkpoints")
			_, err := s.WriteCheckpoint(context.Background(), ses, c.in)
			if err == nil || err.Error() != c.want {
				t.Fatalf("err = %v\nwant  %s", err, c.want)
			}
			if n := countRows(t, s, "item_merges"); n != 0 {
				t.Fatalf("item_merges rows = %d", n)
			}
			if n := countRows(t, s, "checkpoints"); n != before {
				t.Fatalf("checkpoints = %d, want %d", n, before)
			}
		})
	}
}

func TestFinishingRepeatRefused(t *testing.T) {
	s, _, ses, _, _ := finishFixture(t, "https://github.com/o/proj.git", "auto")
	fakeGH(s, map[string]execx.Result{prView(prURL): {Out: ghOpenArmed}})
	if _, err := finishPR(s, ses); err != nil {
		t.Fatal(err)
	}
	_, err := finishPR(s, ses)
	if err == nil || err.Error() != "Finishing for CHORE-1 is already recorded for this integration." {
		t.Fatalf("err = %v", err)
	}
	if n := countRows(t, s, "item_merges"); n != 1 {
		t.Fatalf("item_merges rows = %d", n)
	}
}

func TestPrsMergedRefusedOnOtherKinds(t *testing.T) {
	s, _, ses, _, _ := finishFixture(t, "https://github.com/o/proj.git", "auto")
	_, err := s.WriteCheckpoint(context.Background(), ses, CheckpointInput{Kind: Progress, Summary: "s",
		PRs: []FinishPR{{Repo: "proj", URL: prURL}}})
	if err == nil || err.Error() != "prs and merged are only for a finishing checkpoint." {
		t.Fatalf("err = %v", err)
	}
}

func TestMergesAndMergeProgress(t *testing.T) {
	ctx := context.Background()
	s, _, ses, key, _ := finishFixture(t, "https://github.com/o/proj.git", "auto")
	id := mustItemID(t, s, key)
	if ms, err := s.Merges(ctx, id); err != nil || ms != nil {
		t.Fatalf("merges = %+v, %v", ms, err)
	}
	if p, err := s.MergeProgressFor(ctx, id); err != nil || p == nil || *p != (MergeProgress{0, 1}) {
		t.Fatalf("progress = %+v, %v", p, err)
	}
	fakeGH(s, map[string]execx.Result{prView(prURL): {Out: ghOpenArmed}})
	if _, err := finishPR(s, ses); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.Merges(ctx, id); len(ms) != 1 || ms[0].State != "open" {
		t.Fatalf("merges = %+v", ms)
	}

	s2, _, _, key2, _ := finishFixture(t, "https://github.com/o/proj.git", "")
	if p, err := s2.MergeProgressFor(ctx, mustItemID(t, s2, key2)); err != nil || p != nil {
		t.Fatalf("unapproved progress = %+v, %v", p, err)
	}
}

func mustItemID(t *testing.T, s *Store, key string) string {
	t.Helper()
	it, err := s.Items.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return it.ID
}

// customFinishFixture is finishFixture approved with a custom merge (an agent option), its
// integrated checkpoint spanning proj (GitHub remote) and docs (no remote).
func customFinishFixture(t *testing.T) (s *Store, ses, key string) {
	t.Helper()
	s, orch, ses, key, _ := finishFixture(t, "https://github.com/o/proj.git", "custom")
	mustExec(t, s.DB, `INSERT INTO repos (id, name, path, remote_url, remote_owner, default_branch, source, created_at, updated_at)
		VALUES ('repo_docs', 'docs', '/tmp/docs', NULL, 'o', 'main', 'manual', 1, 1)`)
	git := `[{"repo":"proj","branch":"swarm/chore-1","sha":"3f9c2ab0000"},{"repo":"docs","branch":"swarm/chore-1","sha":"aaa1110000"}]`
	mustExec(t, s.DB, `UPDATE checkpoints SET git_json = ? WHERE agent_id = ? AND kind = 'integrated'`, git, orch.ID)
	mustExec(t, s.DB, `UPDATE requests SET binding_json = json_set(binding_json, '$.git', json(?)) WHERE item_id = ?`, git, mustItemID(t, s, key))
	return s, ses, key
}

func finishCustom(s *Store, ses string, in CheckpointInput) (CheckpointResult, error) {
	in.Kind, in.Summary = Finishing, "finished as chosen"
	return s.WriteCheckpoint(context.Background(), ses, in)
}

func TestFinishingCustomPRPlusKeptWaitsForPRThenDone(t *testing.T) {
	s, ses, key := customFinishFixture(t)
	fakeGH(s, map[string]execx.Result{prView(prURL): {Out: ghOpenUnarmed}}) // no auto-merge needed under custom
	res, err := finishCustom(s, ses, CheckpointInput{PRs: []FinishPR{{Repo: "proj", URL: prURL}},
		Kept: []KeptRepo{{Repo: "docs", Note: "docs stay on the branch"}}})
	if err != nil || res.ItemStatus != items.InReview {
		t.Fatalf("finishing = %+v, %v", res, err)
	}
	ms, _ := s.Merges(context.Background(), mustItemID(t, s, key))
	if len(ms) != 2 || ms[0].AutoMerge || ms[1].AutoMerge {
		t.Fatalf("merges = %+v", ms)
	}
	byRepo := map[string]ItemMerge{ms[0].Repo: ms[0], ms[1].Repo: ms[1]}
	if byRepo["proj"].Kind != "pr" || byRepo["proj"].State != "open" || byRepo["docs"].Kind != "kept" || byRepo["docs"].State != "merged" {
		t.Fatalf("rows = %+v", byRepo)
	}
	tick(t, s, map[string]execx.Result{prView(prURL): {Out: ghPRJSON("MERGED", `[]`)}})
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status = %s, want done", st)
	}
}

func TestFinishingCustomKeptOnlyIsDone(t *testing.T) {
	s, ses, key := customFinishFixture(t)
	if _, err := finishCustom(s, ses, CheckpointInput{Kept: []KeptRepo{
		{Repo: "proj", Note: "branch stays for review"}, {Repo: "docs", Note: "same"}}}); err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status = %s, want done", st)
	}
}

func TestFinishingCustomRecordsLocalMergeWithoutVerifying(t *testing.T) {
	s, ses, key := customFinishFixture(t)
	fakeGH(s, map[string]execx.Result{}) // any git/gh call would fail the fake
	// proj has a GitHub remote, yet "pushed straight to main" is reported under merged
	if _, err := finishCustom(s, ses, CheckpointInput{Merged: []FinishMerged{{Repo: "proj", SHA: "d00d"}},
		Kept: []KeptRepo{{Repo: "docs", Note: "kept"}}}); err != nil {
		t.Fatal(err)
	}
	if st := itemStatus(t, s, key); st != items.Done {
		t.Fatalf("status = %s, want done", st)
	}
}

func TestFinishingCustomRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		in  CheckpointInput
		msg string
	}{
		"missing repo": {CheckpointInput{PRs: []FinishPR{{Repo: "proj", URL: prURL}}},
			"Report every integrated repo once under prs, merged or kept: docs."},
		"repeated repo": {CheckpointInput{Kept: []KeptRepo{{Repo: "proj", Note: "a"}, {Repo: "docs", Note: "b"}},
			Merged: []FinishMerged{{Repo: "docs", SHA: "d00d"}}},
			"Report every integrated repo once under prs, merged or kept: docs."},
		"kept without note": {CheckpointInput{Kept: []KeptRepo{{Repo: "proj"}, {Repo: "docs", Note: "b"}}},
			"kept needs a note of 1–300 characters for each repo."},
	} {
		t.Run(name, func(t *testing.T) {
			s, ses, key := customFinishFixture(t)
			fakeGH(s, map[string]execx.Result{prView(prURL): {Out: ghOpenUnarmed}})
			if _, err := finishCustom(s, ses, c.in); err == nil || err.Error() != c.msg {
				t.Fatalf("err = %v, want %q", err, c.msg)
			}
			if ms, _ := s.Merges(context.Background(), mustItemID(t, s, key)); len(ms) != 0 {
				t.Fatalf("rows written on refusal: %+v", ms)
			}
		})
	}
}

func TestFinishingKeptRefusedWithoutCustomMerge(t *testing.T) {
	s, _, ses, _, _ := finishFixture(t, "https://github.com/o/proj.git", "auto")
	if _, err := finishCustom(s, ses, CheckpointInput{Kept: []KeptRepo{{Repo: "proj", Note: "a"}}}); err == nil ||
		err.Error() != "kept is only valid when the user chose one of your finish options." {
		t.Fatalf("err = %v", err)
	}
}
