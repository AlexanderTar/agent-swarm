// The P2 CLI commands (§19): new, start, agents, attach, pause, resume,
// cancel, retry, ack, requests, answer, approve, confirm-repos,
// request-changes, usage.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// execCommand is a package var so tests can inject a runner instead of really
// launching tmux (S-1: the real tmux server is never allowed as a fallback).
var execCommand = exec.Command

// multiFlag collects a repeatable string flag (--repo PATH...).
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// pullValue removes the first "name VALUE" pair from args, wherever it
// appears, and returns the value. Several §19 commands put their flags after
// a leading positional argument (e.g. "confirm-repos REQ PATH... --comment
// TEXT"), which Go's flag.FlagSet cannot parse in one pass since it stops
// scanning for flags at the first non-flag token.
func pullValue(args []string, name string) (rest []string, val string, found bool) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			rest = append(append([]string{}, args[:i]...), args[i+2:]...)
			return rest, args[i+1], true
		}
	}
	return args, "", false
}

// pullValues is pullValue for a repeatable flag: every occurrence is removed
// and collected.
func pullValues(args []string, name string) (rest []string, vals []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			vals = append(vals, args[i+1])
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return rest, vals
}

// repoRow is the part of repos.Repo the CLI needs to resolve a path to an id.
type repoRow struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// resolveRepoIDs turns CLI-typed paths into the repo ids the wire wants,
// through GET /api/repos (contracts §4). An unmatched path is a hard error:
// silently dropping it would confirm a different repo set than the one typed.
func resolveRepoIDs(c *client, paths []string, userHome string) ([]string, error) {
	if len(paths) == 0 {
		return []string{}, nil
	}
	var body struct {
		All []repoRow `json:"all"`
	}
	if err := c.do("GET", "/api/repos", nil, &body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := absPath(p, userHome)
		if err != nil {
			return nil, err
		}
		id := ""
		for _, r := range body.All {
			if r.Path == abs {
				id = r.ID
				break
			}
		}
		if id == "" {
			return nil, fmt.Errorf("%s isn't a known repository. Run swarm repos --rescan.", abs)
		}
		out = append(out, id)
	}
	return out, nil
}

func cmdNew(args []string, stdout, stderr io.Writer) int {
	var name, intent, agent, model, effort, request *string
	var repoFlag multiFlag
	c, _, code, done := connect("new", args, stderr, func(fs *flag.FlagSet) {
		name = fs.String("name", "", "spike name")
		intent = fs.String("intent", "", "feature or debug")
		agent = fs.String("agent", "", "agent kind")
		model = fs.String("model", "", "model")
		effort = fs.String("effort", "", "reasoning effort")
		request = fs.String("request", "", "the request text")
		fs.Var(&repoFlag, "repo", "repository path (repeatable)")
	})
	if done {
		return code
	}
	if *name == "" || (*intent != "feature" && *intent != "debug") {
		fmt.Fprintln(stderr, "swarm: --name is required and --intent must be feature or debug")
		return 2
	}
	userHome, _ := os.UserHomeDir()
	repoIDs, err := resolveRepoIDs(c, repoFlag, userHome)
	if err != nil {
		return fail(stderr, err)
	}
	body := map[string]any{"request_id": ids.New("req"), "name": *name, "intent": *intent,
		"agent": *agent, "model": *model, "effort": *effort, "request": *request, "repos": repoIDs}
	var resp struct {
		Item struct {
			Key   string `json:"key"`
			Title string `json:"title"`
		} `json:"item"`
		Agent struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"agent"`
		Queued bool `json:"queued"`
	}
	if err := c.do("POST", "/api/spikes", body, &resp); err != nil {
		return fail(stderr, err)
	}
	suffix := ""
	if resp.Queued {
		suffix = " (queued)"
	}
	fmt.Fprintf(stdout, "%s created. Agent %s is %s%s.\n", resp.Item.Key, resp.Agent.Name, resp.Agent.State, suffix)
	return 0
}

func cmdStart(args []string, stdout, stderr io.Writer) int {
	c, rest, code, done := connect("start", args, stderr, nil)
	if done {
		return code
	}
	if len(rest) == 0 {
		fmt.Fprint(stderr, "Usage: swarm start KEY [--agent A --model M --effort E] [--repo PATH...]\n")
		return 2
	}
	key, rest := rest[0], rest[1:]
	rest, agent, _ := pullValue(rest, "--agent")
	rest, model, _ := pullValue(rest, "--model")
	rest, effort, _ := pullValue(rest, "--effort")
	_, repoPaths := pullValues(rest, "--repo")

	userHome, _ := os.UserHomeDir()
	repoIDs, err := resolveRepoIDs(c, repoPaths, userHome)
	if err != nil {
		return fail(stderr, err)
	}
	body := map[string]any{"request_id": ids.New("req"), "agent": agent, "model": model,
		"effort": effort, "repos": repoIDs}
	if len(repoIDs) > 0 {
		// L25: a non-empty repos confirms the item's set, so the version it was
		// last shown has to travel with it (I13).
		var item struct {
			ReposVersion int `json:"repos_version"`
		}
		if err := c.do("GET", "/api/items/"+key, nil, &item); err != nil {
			return fail(stderr, err)
		}
		body["repos_version"] = item.ReposVersion
	}
	var node struct {
		Name string `json:"name"`
	}
	if err := c.do("POST", "/api/items/"+key+"/orchestrator", body, &node); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Started %s on %s.\n", node.Name, key)
	return 0
}

// sessionInfo is the part of §3.2 AgentNode.session the tree printer reads.
type sessionInfo struct {
	State   string `json:"state"`
	Waiting bool   `json:"waiting"`
	Stale   bool   `json:"stale"`
}

// agentNode is §3.2 AgentNode, trimmed to what `swarm agents` prints.
type agentNode struct {
	Name     string       `json:"name"`
	Role     string       `json:"role"`
	State    string       `json:"state"`
	ItemKey  string       `json:"item_key"`
	Session  *sessionInfo `json:"session"`
	Children []agentNode  `json:"children"`
	Finished []agentNode  `json:"finished"`
}

// agentLabel is §17.2's session-state label, overlaid with §17.1's Waiting,
// Stopping and Queued where they apply.
func agentLabel(n agentNode) string {
	if n.Session == nil {
		if n.State == string(runtime.AgentQueued) {
			return "Queued"
		}
		return n.State
	}
	if n.Session.Stale {
		return "No activity for 30 min"
	}
	if n.Session.Waiting {
		return "Waiting"
	}
	return runtime.SessionState(n.Session.State).Label()
}

func printAgentTree(w io.Writer, nodes []agentNode, depth int) {
	for _, n := range nodes {
		indent := strings.Repeat("  ", depth)
		marker := ""
		if depth > 0 {
			marker = "└─ "
		}
		fmt.Fprintf(w, "%s%s%s (%s, %s) — %s\n", indent, marker, n.Name, n.Role, n.ItemKey, agentLabel(n))
		printAgentTree(w, n.Children, depth+1)
		for _, f := range n.Finished {
			fmt.Fprintf(w, "%s  %s (%s, %s) — %s [finished]\n", indent, f.Name, f.Role, f.ItemKey, agentLabel(f))
		}
	}
}

func cmdAgents(args []string, stdout, stderr io.Writer) int {
	var all *bool
	c, _, code, done := connect("agents", args, stderr, func(fs *flag.FlagSet) {
		all = fs.Bool("all", false, "include finished and acknowledged agents")
	})
	if done {
		return code
	}
	path := "/api/agents?state=active"
	if *all {
		path = "/api/agents?state=all"
	}
	var nodes []agentNode
	if err := c.do("GET", path, nil, &nodes); err != nil {
		return fail(stderr, err)
	}
	if len(nodes) == 0 {
		fmt.Fprintln(stdout, "No agents.")
		return 0
	}
	printAgentTree(stdout, nodes, 0)
	return 0
}

func cmdAttach(args []string, stdout, stderr io.Writer) int {
	c, rest, code, done := connect("attach", args, stderr, nil)
	if done {
		return code
	}
	if len(rest) != 1 {
		fmt.Fprint(stderr, "Usage: swarm attach NAME\n")
		return 2
	}
	var out struct {
		Tmux       string `json:"tmux"`
		TmuxSocket string `json:"tmux_socket"`
		OpenedBy   string `json:"opened_by"`
	}
	if err := c.do("POST", "/api/agents/"+rest[0]+"/terminal", map[string]any{}, &out); err != nil {
		return fail(stderr, err)
	}
	if out.TmuxSocket == "" {
		return fail(stderr, errors.New("The daemon didn't say which tmux server to attach to. Update it with swarm install."))
	}
	cmd := execCommand("tmux", "-L", out.TmuxSocket, "attach", "-t", out.Tmux)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func cmdPause(args []string, stdout, stderr io.Writer) int {
	var all, group *bool
	c, rest, code, done := connect("pause", args, stderr, func(fs *flag.FlagSet) {
		all = fs.Bool("all", false, "pause every agent")
		group = fs.Bool("group", false, "pause the agent's whole subtree")
	})
	if done {
		return code
	}
	if *all {
		var out struct {
			Requested int `json:"requested"`
		}
		if err := c.do("POST", "/api/pause-all", map[string]any{}, &out); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "Pausing %d agent(s).\n", out.Requested)
		return 0
	}
	if len(rest) != 1 {
		fmt.Fprint(stderr, "Usage: swarm pause NAME|--all [--group]\n")
		return 2
	}
	scope := "session"
	if *group {
		scope = "subtree"
	}
	var node map[string]any
	if err := c.do("POST", "/api/agents/"+rest[0]+"/pause", map[string]string{"scope": scope}, &node); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Pausing %s.\n", rest[0])
	return 0
}

// agentAction is the shared shape of resume/cancel: POST with no body,
// AgentNode back, one confirmation line.
func agentAction(name string, args []string, stdout, stderr io.Writer) int {
	c, rest, code, done := connect(name, args, stderr, nil)
	if done {
		return code
	}
	if len(rest) != 1 {
		fmt.Fprintf(stderr, "Usage: swarm %s NAME\n", name)
		return 2
	}
	var node map[string]any
	if err := c.do("POST", "/api/agents/"+rest[0]+"/"+name, map[string]any{}, &node); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "%s: %s.\n", strings.ToUpper(name[:1])+name[1:], rest[0])
	return 0
}

func cmdResume(args []string, stdout, stderr io.Writer) int {
	return agentAction("resume", args, stdout, stderr)
}
func cmdCancel(args []string, stdout, stderr io.Writer) int {
	return agentAction("cancel", args, stdout, stderr)
}

func cmdRetry(args []string, stdout, stderr io.Writer) int {
	c, rest, code, done := connect("retry", args, stderr, nil)
	if done {
		return code
	}
	rest, note, _ := pullValue(rest, "--note")
	if len(rest) != 1 {
		fmt.Fprint(stderr, "Usage: swarm retry NAME [--note TEXT]\n")
		return 2
	}
	var node map[string]any
	if err := c.do("POST", "/api/agents/"+rest[0]+"/retry", map[string]string{"note": note}, &node); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Retrying %s.\n", rest[0])
	return 0
}

func cmdAck(args []string, stdout, stderr io.Writer) int {
	c, rest, code, done := connect("ack", args, stderr, nil)
	if done {
		return code
	}
	if len(rest) != 1 {
		fmt.Fprint(stderr, "Usage: swarm ack NAME\n")
		return 2
	}
	if err := c.do("POST", "/api/agents/"+rest[0]+"/ack", map[string]any{}, nil); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Acknowledged %s.\n", rest[0])
	return 0
}

// reqRow is the part of §3.3 Request the CLI reads back.
type reqRow struct {
	ID               string          `json:"id"`
	Kind             string          `json:"kind"`
	ItemKey          string          `json:"item_key"`
	Prompt           string          `json:"prompt"`
	State            string          `json:"state"`
	SectionSHA256    *string         `json:"section_sha256"`
	ArtifactRevision *int            `json:"artifact_revision"`
	Binding          json.RawMessage `json:"binding"`
}

func cmdRequests(args []string, stdout, stderr io.Writer) int {
	c, _, code, done := connect("requests", args, stderr, nil)
	if done {
		return code
	}
	var list []reqRow
	if err := c.do("GET", "/api/requests", nil, &list); err != nil {
		return fail(stderr, err)
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "No open requests.")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tITEM\tPROMPT")
	for _, r := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, r.Kind, r.ItemKey, r.Prompt)
	}
	tw.Flush()
	return 0
}

// requestByID finds one open request by id; GET /api/requests has no
// single-resource counterpart, so the CLI filters the list itself.
func requestByID(c *client, id string) (reqRow, error) {
	var list []reqRow
	if err := c.do("GET", "/api/requests", nil, &list); err != nil {
		return reqRow{}, err
	}
	for _, r := range list {
		if r.ID == id {
			return r, nil
		}
	}
	return reqRow{}, fmt.Errorf("no open request %s", id)
}

func cmdAnswer(args []string, stdout, stderr io.Writer) int {
	c, rest, code, done := connect("answer", args, stderr, nil)
	if done {
		return code
	}
	if len(rest) < 2 {
		fmt.Fprint(stderr, "Usage: swarm answer REQ TEXT\n")
		return 2
	}
	text := strings.Join(rest[1:], " ")
	var wire map[string]any
	if err := c.do("POST", "/api/requests/"+rest[0]+"/answer",
		map[string]string{"text": text, "via": "cli"}, &wire); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "Answered.")
	return 0
}

// cmdApprove fetches the request first and echoes back whatever of
// section_sha256/artifact_revision/binding it carries: Approve's server-side
// check (§7.2) is unconditional on each field the request actually has (a
// stale caller must be refused, not waved through), so a body that omits a
// field the request set doesn't skip that check, it fails it — every
// accept_epic/accept_fix approval, and every approve_section/approve_plan
// whose section has a hash, 409ed unconditionally before this fix.
func cmdApprove(args []string, stdout, stderr io.Writer) int {
	c, rest, code, done := connect("approve", args, stderr, nil)
	if done {
		return code
	}
	if len(rest) != 1 {
		fmt.Fprint(stderr, "Usage: swarm approve REQ\n")
		return 2
	}
	req, err := requestByID(c, rest[0])
	if err != nil {
		return fail(stderr, err)
	}
	body := map[string]any{"via": "cli"}
	if req.SectionSHA256 != nil {
		body["section_sha256"] = *req.SectionSHA256
	}
	if req.ArtifactRevision != nil {
		body["artifact_revision"] = *req.ArtifactRevision
	}
	if len(req.Binding) > 0 && string(req.Binding) != "null" {
		body["binding"] = req.Binding
	}
	var wire map[string]any
	if err := c.do("POST", "/api/requests/"+rest[0]+"/approve", body, &wire); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "Approved.")
	return 0
}

func cmdRequestChanges(args []string, stdout, stderr io.Writer) int {
	c, rest, code, done := connect("request-changes", args, stderr, nil)
	if done {
		return code
	}
	if len(rest) < 2 {
		fmt.Fprint(stderr, "Usage: swarm request-changes REQ COMMENT\n")
		return 2
	}
	comment := strings.Join(rest[1:], " ")
	var wire map[string]any
	if err := c.do("POST", "/api/requests/"+rest[0]+"/request-changes",
		map[string]string{"comment": comment, "via": "cli"}, &wire); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "Requested changes.")
	return 0
}

func cmdConfirmRepos(args []string, stdout, stderr io.Writer) int {
	c, rest, code, done := connect("confirm-repos", args, stderr, nil)
	if done {
		return code
	}
	rest, comment, _ := pullValue(rest, "--comment")
	if len(rest) < 2 {
		fmt.Fprint(stderr, "Usage: swarm confirm-repos REQ PATH... [--comment TEXT]\n")
		return 2
	}
	reqID, paths := rest[0], rest[1:]
	userHome, _ := os.UserHomeDir()
	repoIDs, err := resolveRepoIDs(c, paths, userHome)
	if err != nil {
		return fail(stderr, err)
	}
	req, err := requestByID(c, reqID)
	if err != nil {
		return fail(stderr, err)
	}
	version := 0
	if len(req.Binding) > 0 {
		var b struct {
			ReposVersion int `json:"repos_version"`
		}
		json.Unmarshal(req.Binding, &b) // best-effort: absent binding just means version 0
		version = b.ReposVersion
	}
	body := map[string]any{"repos": repoIDs, "comment": comment, "repos_version": version, "via": "cli"}
	var wire map[string]any
	if err := c.do("POST", "/api/requests/"+reqID+"/confirm-repos", body, &wire); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "Repositories confirmed.")
	return 0
}

// meterRow is §3.4 UsageMeter.
type meterRow struct {
	Label   string  `json:"label"`
	Window  string  `json:"window"`
	UsedPct float64 `json:"used_pct"`
}

// usageRow is §3.4 UsageSnapshot, trimmed to what `swarm usage` prints.
type usageRow struct {
	Agent  string     `json:"agent"`
	Meters []meterRow `json:"meters"`
	Error  *string    `json:"error"`
	Stale  bool       `json:"stale"`
}

func cmdUsage(args []string, stdout, stderr io.Writer) int {
	var refresh *bool
	c, _, code, done := connect("usage", args, stderr, func(fs *flag.FlagSet) {
		refresh = fs.Bool("refresh", false, "refresh every agent's usage before printing")
	})
	if done {
		return code
	}
	var list []usageRow
	if err := c.do("GET", "/api/usage", nil, &list); err != nil {
		return fail(stderr, err)
	}
	if *refresh {
		for _, u := range list {
			// Best-effort: a 429 limit_reached (refreshed too recently) is not a
			// reason to fail the whole command, only to keep showing what's there.
			var ignore any
			c.do("POST", "/api/usage/refresh", map[string]string{"agent": u.Agent}, &ignore)
		}
		if err := c.do("GET", "/api/usage", nil, &list); err != nil {
			return fail(stderr, err)
		}
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tWINDOW\tUSED\tNOTE")
	for _, u := range list {
		note := ""
		if u.Error != nil {
			note = *u.Error
		} else if u.Stale {
			note = "stale"
		}
		if len(u.Meters) == 0 {
			fmt.Fprintf(tw, "%s\t\t\t%s\n", u.Agent, note)
			continue
		}
		for _, m := range u.Meters {
			fmt.Fprintf(tw, "%s\t%s\t%.1f%%\t%s\n", u.Agent, m.Window, m.UsedPct, note)
		}
	}
	tw.Flush()
	return 0
}
