package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
)

// The test seams for the P5 commands. Production values are the real functions.
var (
	installLaunchd     = install.Install
	installAgents      = install.Agents
	installUninstall   = install.Uninstall
	doctorLegacyChecks = func(ctx context.Context, d install.Doctor) []install.Check { return d.LegacyChecks(ctx) }
)

// newConfig is the one place os.UserHomeDir() is consulted for the install
// package's paths (S-5). Every path in internal/install comes from the struct it
// returns.
func newConfig(home string) (install.Config, error) {
	bin, err := os.Executable()
	if err != nil {
		return install.Config{}, err
	}
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		bin = real
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return install.Config{}, err
	}
	return install.Config{
		Bin: bin, Home: home, LaunchAgentsDir: filepath.Join(userHome, "Library", "LaunchAgents"),
		Path: install.LaunchPath(os.Getenv("PATH"), userHome), UID: os.Getuid(),
		User: os.Getenv("USER"), UserHome: userHome,
	}, nil
}

// agentsOpts builds the AgentsOpts for install and uninstall. §12.4 gives the
// plugin commands 120 s, which execx.Run's 30 s cap would cut short.
func agentsOpts(cfg install.Config, yes, pluginsOnly bool, stdin io.Reader, out io.Writer) install.AgentsOpts {
	confirm := func(prompt string) bool {
		if yes {
			return true
		}
		if stdin == nil {
			return false
		}
		fmt.Fprint(out, prompt)
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil {
			return false
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		return answer == "y" || answer == "yes"
	}
	return install.AgentsOpts{
		Cfg: cfg, Run: execx.RunFor(120 * time.Second), HTTP: &http.Client{Timeout: 30 * time.Second},
		MarketplaceURL: install.MarketplaceURL,
		Installed:      install.InstalledKinds(exec.LookPath),
		Confirm:        confirm, PluginsOnly: pluginsOnly, Out: out,
	}
}

// daemonClaudeSessions asks the running daemon, over the authenticated CLI
// client (<home>/run/daemon.token, Bearer), for the Claude sessions D3's
// install prune and D4's doctor check need. Any failure -- no token yet,
// daemon not listening -- is an error the caller turns into its one-line
// "daemon isn't running" note.
func daemonClaudeSessions(home, base string) install.ClaudeSessionsFunc {
	return func(ctx context.Context) (install.ClaudeSessions, error) {
		var out install.ClaudeSessions
		c, err := newClient(home, base)
		if err != nil {
			return out, err
		}
		c.http.Timeout = 5 * time.Second // a hung daemon must not stall install or doctor
		type node struct {
			Name    string `json:"name"`
			Kind    string `json:"kind"`
			State   string `json:"state"`
			Session *struct {
				State string `json:"state"`
				Cwd   string `json:"cwd"`
			} `json:"session"`
			Children []node `json:"children"`
			Finished []node `json:"finished"`
		}
		var tree []node
		if err := c.do("GET", "/api/agents?state=all", nil, &tree); err != nil {
			return out, err
		}
		inUse := map[string]bool{} // cwds a not-finished agent of any kind still uses
		var finished []string
		var walk func([]node)
		walk = func(ns []node) {
			for _, n := range ns {
				if n.Session != nil && n.Session.Cwd != "" {
					done := n.State == "finished" || n.State == "acknowledged"
					switch {
					case !done:
						inUse[n.Session.Cwd] = true
						if n.Kind == "claude" && (n.Session.State == "running" || n.Session.State == "spawning") {
							out.Live = append(out.Live, install.LiveClaudeSession{Agent: n.Name, Cwd: n.Session.Cwd})
						}
					case n.Kind == "claude":
						finished = append(finished, n.Session.Cwd)
					}
				}
				walk(n.Children)
				walk(n.Finished)
			}
		}
		walk(tree)
		for _, cwd := range finished {
			if !inUse[cwd] {
				out.Finished = append(out.Finished, cwd)
			}
		}
		return out, nil
	}
}

// newDoctor sets every field: Doctor.Checks calls each one unguarded.
func newDoctor(home, daemonURL string) install.Doctor {
	cfg, _ := newConfig(home) // a failure leaves the paths empty, and the checks report it
	return install.Doctor{
		Run: execx.Run, Home: home, UserHome: cfg.UserHome,
		LaunchAgentsDir: cfg.LaunchAgentsDir,
		DaemonURL:       strings.TrimRight(daemonURL, "/"),
		OllamaCheck:     kb.NewOllama().Check,
		GhosttyApps:     []string{"/Applications/Ghostty.app", filepath.Join(cfg.UserHome, "Applications", "Ghostty.app")},
		LookPath:        exec.LookPath,
		HTTP:            &http.Client{Timeout: 3 * time.Second},
		Cfg:             cfg,
		Installed:       install.InstalledKinds(exec.LookPath),
		ClaudeSessions:  daemonClaudeSessions(home, daemonURL),
	}
}

// doctorChecks is the test seam; production runs every check.
var doctorChecks = func(ctx context.Context, d install.Doctor) []install.Check { return d.Checks(ctx) }

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs, home, base := flags("doctor", stderr, true)
	asJSON := fs.Bool("json", false, "print JSON")
	legacy := fs.Bool("legacy", false, "report Agent Swarm 1.x state left on this Mac")
	if code, done := parse(fs, args); done {
		return code
	}
	d := newDoctor(*home, *base)
	var checks []install.Check
	if *legacy {
		checks = doctorLegacyChecks(context.Background(), d)
	} else {
		checks = doctorChecks(context.Background(), d)
	}
	code := 0
	for _, c := range checks {
		if !c.OK {
			code = 1
		}
	}
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(checks); err != nil {
			return fail(stderr, err)
		}
		return code
	}
	for _, c := range checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		fmt.Fprintf(stdout, "%s %s: %s\n", mark, c.Name, c.Detail)
	}
	return code
}

func cmdInstall(args []string, stdout, stderr io.Writer) int {
	fs, home, base := flags("install", stderr, true) // --url: D3's prune asks the daemon
	dry := fs.Bool("dry-run", false, "print what would change")
	plugins := fs.Bool("plugins", false, "only install or update the superpowers plugins")
	yes := fs.Bool("yes", false, "answer the Agent Swarm 1.x removal prompt with yes")
	if code, done := parse(fs, args); done {
		return code
	}
	cfg, err := newConfig(*home)
	if err != nil {
		return fail(stderr, err)
	}
	o := agentsOpts(cfg, *yes, *plugins, os.Stdin, stdout)
	o.ClaudeSessions = daemonClaudeSessions(*home, *base)
	if *plugins {
		if err := installAgents(context.Background(), o); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	if err := installLaunchd(context.Background(), cfg, execx.Run, *dry, stdout); err != nil {
		return fail(stderr, err)
	}
	if *dry {
		// A dry run reports the plist and stops before touching agent configuration.
		fmt.Fprintln(stdout, "Would remove the Agent Swarm 1.x integrations and write the agent configuration.")
		return 0
	}
	if err := installAgents(context.Background(), o); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func cmdUninstall(args []string, stdout, stderr io.Writer) int {
	fs, home, _ := flags("uninstall", stderr, false)
	if code, done := parse(fs, args); done {
		return code
	}
	cfg, err := newConfig(*home)
	if err != nil {
		return fail(stderr, err)
	}
	if err := installUninstall(context.Background(), agentsOpts(cfg, true, false, nil, stdout)); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "Removed the launch agent and the Swarm entries in each agent's configuration. Your data in "+cfg.Home+" is untouched.")
	return 0
}

// connect parses a client command's flags (extra adds command flags) and opens a client.
func connect(name string, args []string, stderr io.Writer, extra func(fs *flag.FlagSet)) (*client, []string, int, bool) {
	fs, home, base := flags(name, stderr, true)
	if extra != nil {
		extra(fs)
	}
	if code, done := parse(fs, args); done {
		return nil, nil, code, true
	}
	c, err := newClient(*home, *base)
	if err != nil {
		return nil, nil, fail(stderr, err), true
	}
	return c, fs.Args(), 0, false
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	c, _, code, done := connect("status", args, stderr, nil)
	if done {
		return code
	}
	var health struct {
		Version string `json:"version"`
		Schema  int    `json:"schema"`
	}
	if err := c.do("GET", "/api/health", nil, &health); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Daemon: running at %s (version %s, schema %d)\n", c.base, health.Version, health.Schema)

	var list struct {
		Items []itemRow `json:"items"`
	}
	if err := c.do("GET", "/api/items?view=flat", nil, &list); err != nil {
		return fail(stderr, err)
	}
	counts := map[items.Status]int{}
	for _, it := range list.Items {
		counts[it.Status]++
	}
	var parts []string
	for _, st := range []items.Status{items.Draft, items.Ready, items.InProgress, items.Blocked, items.InReview,
		items.AwaitingApproval, items.Done, items.Cancelled} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", strings.ToLower(items.StatusLabel(st)), counts[st]))
		}
	}
	line := fmt.Sprintf("Items: %d", len(list.Items))
	if len(parts) > 0 {
		line += " (" + strings.Join(parts, ", ") + ")"
	}
	fmt.Fprintln(stdout, line)

	var rp struct {
		All       []repos.Repo `json:"all"`
		ScannedAt int64        `json:"scanned_at"`
		Scanning  bool         `json:"scanning"`
	}
	if err := c.do("GET", "/api/repos", nil, &rp); err != nil {
		return fail(stderr, err)
	}
	scan := "not scanned yet"
	switch {
	case rp.Scanning:
		scan = "Scanning your home folder…"
	case rp.ScannedAt > 0:
		scan = "scanned " + time.UnixMilli(rp.ScannedAt).Format("2006-01-02 15:04")
	}
	fmt.Fprintf(stdout, "Repositories: %d (%s)\n", len(rp.All), scan)

	var st kb.Status
	if err := c.do("GET", "/api/kb/status", nil, &st); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Knowledge base: %d documents, %d chunks, %d embedded\n", st.Docs, st.Chunks, st.Embedded)
	if !st.Available {
		fmt.Fprintln(stdout, kb.ErrUnavailable.Error())
	}
	return 0
}

// itemRow is the part of the wire Item the CLI prints (timestamps are ms on the wire, contracts §1).
type itemRow struct {
	Key    string       `json:"key"`
	Type   items.Type   `json:"type"`
	Status items.Status `json:"status"`
	Title  string       `json:"title"`
}

func cmdItems(args []string, stdout, stderr io.Writer) int {
	var typ, status, q *string
	c, _, code, done := connect("items", args, stderr, func(fs *flag.FlagSet) {
		typ = fs.String("type", "", "epic, story, task, bug or spike")
		status = fs.String("status", "", "draft, ready, in_progress, blocked, in_review, awaiting_approval, done or cancelled")
		q = fs.String("q", "", "search text")
	})
	if done {
		return code
	}
	v := url.Values{"view": {"flat"}}
	for k, val := range map[string]string{"type": *typ, "status": *status, "q": *q} {
		if val != "" {
			v.Set(k, val)
		}
	}
	var list struct {
		Items []itemRow `json:"items"`
	}
	if err := c.do("GET", "/api/items?"+v.Encode(), nil, &list); err != nil {
		return fail(stderr, err)
	}
	if len(list.Items) == 0 {
		if *typ != "" || *status != "" || *q != "" {
			fmt.Fprintln(stdout, "No items match these filters.")
		} else {
			fmt.Fprintln(stdout, "No work items yet.")
		}
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tTYPE\tSTATUS\tTITLE")
	for _, it := range list.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", it.Key, it.Type, items.StatusLabel(it.Status), it.Title)
	}
	tw.Flush()
	return 0
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// absPath resolves a CLI path argument: ~ against userHome, relative paths against the cwd.
// The daemon's route only takes absolute paths.
func absPath(p, userHome string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return repos.ExpandHome(p, userHome), nil
	}
	return filepath.Abs(p)
}

func cmdRepos(args []string, stdout, stderr io.Writer) int {
	var rescan *bool
	c, rest, code, done := connect("repos", args, stderr, func(fs *flag.FlagSet) {
		rescan = fs.Bool("rescan", false, "scan the home folder first")
	})
	if done {
		return code
	}
	if len(rest) > 0 {
		if rest[0] != "add" || len(rest) != 2 {
			fmt.Fprint(stderr, "Usage: swarm repos [--rescan] | repos add PATH\n")
			return 2
		}
		userHome, _ := os.UserHomeDir()
		abs, err := absPath(rest[1], userHome)
		if err != nil {
			return fail(stderr, err)
		}
		var r repos.Repo
		if err := c.do("POST", "/api/repos", map[string]string{"path": abs}, &r); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "Added %s (%s).\n", r.Name, tildePath(r.Path))
		return 0
	}
	if *rescan {
		var stats repos.ScanStats
		if err := c.do("POST", "/api/repos/rescan", nil, &stats); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "Found %d repositories (%d missing).\n", stats.Found, stats.Missing)
	}
	var body struct {
		All []repos.Repo `json:"all"`
	}
	if err := c.do("GET", "/api/repos", nil, &body); err != nil {
		return fail(stderr, err)
	}
	if len(body.All) == 0 {
		fmt.Fprintln(stdout, "No repositories found. Run swarm repos --rescan.")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPATH\tGROUPS")
	for _, r := range body.All {
		name := r.Name
		if r.Missing {
			name += " (missing)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, tildePath(r.Path), strings.Join(r.Groups, ", "))
	}
	tw.Flush()
	return 0
}

func cmdKB(args []string, stdout, stderr io.Writer) int {
	const kbUsage = "Usage: swarm kb search QUERY | kb status\n"
	fs, home, base := flags("kb", stderr, true)
	if code, done := parse(fs, args); done {
		return code
	}
	rest := fs.Args() // check the subcommand before needing a token
	if len(rest) == 0 || (rest[0] != "status" && rest[0] != "search") {
		fmt.Fprint(stderr, kbUsage)
		return 2
	}
	c, err := newClient(*home, *base)
	if err != nil {
		return fail(stderr, err)
	}
	switch {
	case rest[0] == "status" && len(rest) == 1:
		var st kb.Status
		if err := c.do("GET", "/api/kb/status", nil, &st); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%d documents, %d chunks, %d embedded, %d pending\n", st.Docs, st.Chunks, st.Embedded, st.Pending)
		if !st.Available {
			fmt.Fprintln(stdout, kb.ErrUnavailable.Error())
		}
		return 0
	case rest[0] == "search" && len(rest) > 1:
		var hits []kb.Hit
		q := url.Values{"q": {strings.Join(rest[1:], " ")}}
		if err := c.do("GET", "/api/kb/search?"+q.Encode(), nil, &hits); err != nil {
			return fail(stderr, err)
		}
		if len(hits) == 0 {
			fmt.Fprintln(stdout, "No matches.")
			return 0
		}
		for _, h := range hits {
			title := h.Title
			if h.Heading != "" {
				title += " › " + h.Heading
			}
			fmt.Fprintf(stdout, "%s — %s\n  %s\n", h.Slug, title, h.Snippet)
		}
		return 0
	}
	fmt.Fprint(stderr, kbUsage)
	return 2
}
