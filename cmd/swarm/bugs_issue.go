package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/AlexanderTar/swarmrun/internal/execx"
)

// defaultBugRepo is where `swarm bugs --issue` files reports unless --repo says otherwise.
const defaultBugRepo = "AlexanderTar/swarmrun"

// ghRun runs the gh CLI; a package var so tests inject a recorder.
var ghRun execx.Runner = execx.Run

type bugDetail struct {
	bugRow
	WhatHappened string `json:"what_happened"`
	Repro        string `json:"repro"`
	Evidence     string `json:"evidence"`
	UserSaid     string `json:"user_said"`
	Cause        string `json:"cause"`
	Area         string `json:"area"`
}

// secretPatterns match emails, token-like strings and home directories (which name the user). Order matters: the key=value
// form runs last so it doesn't re-wrap what a specific pattern already redacted.
var secretPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "[redacted-email]"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), "[redacted-token]"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`), "[redacted-token]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`), "[redacted-token]"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "[redacted-token]"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[redacted-token]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`), "[redacted-token]"},
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{16,}`), "Bearer [redacted-token]"},
	{regexp.MustCompile(`(?i)([A-Za-z0-9_]*(?:token|secret|password|passwd|api[_-]?key|authorization))(\s*[:=]\s*)[^\s"']{8,}`), "${1}${2}[redacted-token]"},
	{regexp.MustCompile(`(?:/Users|/home)/[^/\s]+`), "~"},
}

// redact masks emails and token-like strings before a report leaves the machine.
// ponytail: pattern list, not entropy detection; an unrecognised secret format passes through, so the maintainer reads --dry-run first.
func redact(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.with)
	}
	return s
}

// issueBody renders the report's non-empty fields as sections, redacted.
func issueBody(b bugDetail) string {
	var sb strings.Builder
	for _, f := range [][2]string{
		{"What happened", b.WhatHappened}, {"Repro", b.Repro}, {"Evidence", b.Evidence},
		{"What the user said", b.UserSaid}, {"Cause", b.Cause}, {"Area", b.Area},
	} {
		if v := strings.TrimSpace(f[1]); v != "" {
			sb.WriteString("### " + f[0] + "\n\n" + redact(v) + "\n\n")
		}
	}
	return sb.String() + "---\nFiled with `swarm bugs --issue " + b.ID + "`."
}

// fileBugIssue files report id on repo: it comments on an open issue with the same
// title, or opens a new one, then stores the issue URL on the report.
func fileBugIssue(c *client, id, repo string, dry bool, stdout, stderr io.Writer) int {
	var b bugDetail
	if err := c.do("GET", "/api/bugs/"+url.PathEscape(id), nil, &b); err != nil {
		return fail(stderr, err)
	}
	if b.IssueURL != "" {
		return fail(stderr, fmt.Errorf("%s is already filed: %s", b.ID, b.IssueURL))
	}
	title, body := redact(strings.TrimSpace(b.Title)), issueBody(b)
	ctx := context.Background()
	found, err := ghRun(ctx, "gh", "issue", "list", "--repo", repo, "--state", "open",
		"--search", `"`+strings.ReplaceAll(title, `"`, "")+`" in:title`, "--json", "number,title,url", "--limit", "30")
	if err != nil {
		return fail(stderr, err)
	}
	var open []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(found, &open); err != nil {
		return fail(stderr, fmt.Errorf("reading gh issue list: %w", err))
	}
	match := -1
	for i, o := range open {
		if strings.EqualFold(strings.TrimSpace(o.Title), title) {
			match = i
			break
		}
	}
	if dry {
		if match >= 0 {
			fmt.Fprintf(stdout, "Would comment on %s\n", open[match].URL)
		} else {
			fmt.Fprintf(stdout, "Would open a new issue on %s (label bug)\n", repo)
		}
		fmt.Fprintf(stdout, "Repo:  %s\nTitle: %s\n\n%s\n", repo, title, body)
		return 0
	}
	var issueURL string
	if match >= 0 {
		issueURL = open[match].URL
		_, err = ghRun(ctx, "gh", "issue", "comment", fmt.Sprint(open[match].Number), "--repo", repo,
			"--body", "Another report of this bug.\n\n"+body)
	} else {
		var out []byte
		out, err = ghRun(ctx, "gh", "issue", "create", "--repo", repo, "--title", title, "--body", body, "--label", "bug")
		lines := strings.Fields(string(out))
		if err == nil && len(lines) > 0 {
			issueURL = lines[len(lines)-1]
		}
	}
	if err != nil {
		return fail(stderr, err)
	}
	if issueURL == "" {
		return fail(stderr, fmt.Errorf("gh gave no issue URL; check %s by hand", repo))
	}
	if err := c.do("POST", "/api/bugs/"+url.PathEscape(b.ID)+"/issue", map[string]string{"issue_url": issueURL}, nil); err != nil {
		return fail(stderr, fmt.Errorf("filed %s but could not store it on %s: %w", issueURL, b.ID, err))
	}
	verb := "Filed"
	if match >= 0 {
		verb = "Commented on"
	}
	fmt.Fprintf(stdout, "%s %s: %s\n", verb, b.ID, issueURL)
	return 0
}
