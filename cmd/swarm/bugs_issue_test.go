package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	issueRepo     = "AlexanderTar/agent-swarm"
	testToken     = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	bugDetailJSON = `{"id":"bug_x","created_at":1790000000000,"reporter":"coder-1","root_item_key":"CHORE-1","title":"Daemon drops events","board_item_key":"","issue_url":"%s","what_happened":"It dropped events for me@example.com","repro":"run swarm sync","evidence":"token %s in env, sha 0404ace7747d0a2d005b6946c4261f9821a77ef6","user_said":"why","cause":"a race","area":"runtime","transcript_path":"/h/t.jsonl"}`
	filedURL      = "https://github.com/AlexanderTar/agent-swarm/issues/3"
	createdURL    = "https://github.com/AlexanderTar/agent-swarm/issues/12"
)

type ghCall []string

// fakeGH swaps ghRun for a recorder: issue list answers listOut, issue create answers createdURL.
func fakeGH(t *testing.T, listOut string) *[]ghCall {
	t.Helper()
	var calls []ghCall
	old := ghRun
	ghRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append(ghCall{name}, args...))
		switch {
		case len(args) >= 2 && args[0] == "issue" && args[1] == "list":
			return []byte(listOut), nil
		case len(args) >= 2 && args[0] == "issue" && args[1] == "create":
			return []byte(createdURL + "\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { ghRun = old })
	return &calls
}

func bugDaemon(t *testing.T, issueURL string) (string, string, *[]capture, func()) {
	t.Helper()
	srv, got, home := stubDaemon(t, map[string]string{
		"GET /api/bugs/bug_x":        fmt.Sprintf(bugDetailJSON, issueURL, testToken),
		"POST /api/bugs/bug_x/issue": `{"id":"bug_x","issue_url":"ok"}`,
	})
	return srv.URL, home, got, srv.Close
}

func joined(c ghCall) string { return strings.Join(c, " ") }

func TestBugsIssueCreatesIssueWithRedactedBody(t *testing.T) {
	url, home, got, closeSrv := bugDaemon(t, "")
	defer closeSrv()
	calls := fakeGH(t, `[]`)
	var out bytes.Buffer
	if code := run([]string{"bugs", "--home", home, "--url", url, "--issue", "bug_x"}, &out, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(*calls) != 2 || !strings.HasPrefix(joined((*calls)[0]), "gh issue list --repo "+issueRepo) {
		t.Fatalf("gh calls = %v", *calls)
	}
	create := (*calls)[1]
	s := joined(create)
	for _, want := range []string{"gh issue create", "--repo " + issueRepo, "--label bug", "--title Daemon drops events"} {
		if !strings.Contains(s, want) {
			t.Errorf("create call missing %q: %s", want, s)
		}
	}
	for _, want := range []string{"What happened", "run swarm sync", "Repro", "Evidence", "What the user said", "Cause", "Area", "bug_x",
		"0404ace7747d0a2d005b6946c4261f9821a77ef6"} {
		if !strings.Contains(s, want) {
			t.Errorf("body missing %q: %s", want, s)
		}
	}
	for _, leak := range []string{"me@example.com", testToken, "/h/t.jsonl"} {
		if strings.Contains(s, leak) {
			t.Errorf("body leaks %q: %s", leak, s)
		}
	}
	post := (*got)[len(*got)-1]
	var body map[string]string
	json.Unmarshal([]byte(post.body), &body)
	if post.method != "POST" || post.path != "/api/bugs/bug_x/issue" || body["issue_url"] != createdURL {
		t.Fatalf("stored = %+v", post)
	}
	if !strings.Contains(out.String(), createdURL) {
		t.Errorf("output lacks url: %s", out.String())
	}
}

func TestBugsIssueCommentsOnMatchingOpenIssue(t *testing.T) {
	url, home, got, closeSrv := bugDaemon(t, "")
	defer closeSrv()
	calls := fakeGH(t, `[{"number":9,"title":"Something else","url":"https://github.com/x/y/issues/9"},
		{"number":3,"title":"daemon drops events ","url":"`+filedURL+`"}]`)
	var out bytes.Buffer
	if code := run([]string{"bugs", "--home", home, "--url", url, "--issue", "bug_x", "--repo", "o/r"}, &out, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(*calls) != 2 || !strings.HasPrefix(joined((*calls)[1]), "gh issue comment 3 --repo o/r --body ") {
		t.Fatalf("gh calls = %v", *calls)
	}
	for _, c := range *calls {
		if strings.Contains(joined(c), "issue create") {
			t.Fatalf("opened a duplicate: %v", c)
		}
	}
	post := (*got)[len(*got)-1]
	if !strings.Contains(post.body, filedURL) {
		t.Errorf("stored = %s, want the matched issue url", post.body)
	}
}

func TestBugsIssueDryRunPostsNothing(t *testing.T) {
	url, home, got, closeSrv := bugDaemon(t, "")
	defer closeSrv()
	calls := fakeGH(t, `[]`)
	var out bytes.Buffer
	if code := run([]string{"bugs", "--home", home, "--url", url, "--issue", "bug_x", "--repo", "o/r", "--dry-run"}, &out, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	for _, c := range *calls {
		if s := joined(c); strings.Contains(s, "issue create") || strings.Contains(s, "issue comment") {
			t.Errorf("dry run wrote: %s", s)
		}
	}
	for _, c := range *got {
		if c.method == "POST" {
			t.Errorf("dry run stored a url: %+v", c)
		}
	}
	for _, want := range []string{"o/r", "Daemon drops events", "run swarm sync", "[redacted"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "me@example.com") {
		t.Errorf("output leaks the email:\n%s", out.String())
	}
}

func TestBugsIssueRefusesWhenAlreadyFiled(t *testing.T) {
	url, home, got, closeSrv := bugDaemon(t, filedURL)
	defer closeSrv()
	calls := fakeGH(t, `[]`)
	var out bytes.Buffer
	if code := run([]string{"bugs", "--home", home, "--url", url, "--issue", "bug_x"}, &out, &out); code == 0 {
		t.Fatalf("exit 0, want refusal: %s", out.String())
	}
	if !strings.Contains(out.String(), filedURL) {
		t.Errorf("refusal lacks the url: %s", out.String())
	}
	if len(*calls) != 0 {
		t.Errorf("gh called: %v", *calls)
	}
	for _, c := range *got {
		if c.method == "POST" {
			t.Errorf("posted: %+v", c)
		}
	}
}

func TestRedactStripsEmailsAndTokens(t *testing.T) {
	for _, in := range []string{
		"mail me@example.com now",
		"gh " + testToken,
		"github_pat_11ABCDEFG0abcdefghijkl_mnopqrstuvwxyz0123456789",
		"key sk-ant-api03-abcdefghijklmnop",
		"Authorization: Bearer abcdefghijklmnopqrstuv",
		"TOKEN=abcd1234efgh5678",
		"slack xoxb-1234567890-abcdefghij",
		"aws AKIAABCDEFGHIJKLMNOP",
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc-DEF_123",
	} {
		got := redact(in)
		if !strings.Contains(got, "[redacted") || got == in {
			t.Errorf("redact(%q) = %q", in, got)
		}
		for _, secret := range []string{"me@example.com", "ghp_", "github_pat_11", "sk-ant", "abcdefghijklmnopqrstuv", "abcd1234efgh5678", "xoxb-", "AKIAABCD", "eyJhbGci"} {
			if strings.Contains(got, secret) {
				t.Errorf("redact(%q) = %q still has %q", in, got, secret)
			}
		}
	}
	keep := "commit 0404ace7747d0a2d005b6946c4261f9821a77ef6 in msg_01M59KVZF9VN1HHJ5HCB8832SE at /a/b/c.go:12"
	if got := redact(keep); got != keep {
		t.Errorf("redact changed benign text: %q", got)
	}
}

func TestBugsListShowsIssueURL(t *testing.T) {
	srv, _, home := stubDaemon(t, map[string]string{
		"GET /api/bugs": `[{"id":"bug_b","created_at":1790000000000,"reporter":"c","root_item_key":"CHORE-1","title":"T","board_item_key":"","issue_url":"` + filedURL + `"},
			{"id":"bug_a","created_at":1780000000000,"reporter":"c","root_item_key":"CHORE-2","title":"U","board_item_key":"","issue_url":""}]`,
	})
	defer srv.Close()
	var out bytes.Buffer
	if code := run([]string{"bugs", "--home", home, "--url", srv.URL}, &out, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "ISSUE") || !strings.Contains(out.String(), filedURL) {
		t.Errorf("list lacks the issue column:\n%s", out.String())
	}
}
