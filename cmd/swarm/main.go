// Command swarm is the Agent Swarm CLI and daemon (one binary, L1).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var version = "dev" // set with -ldflags "-X main.version=…"

const usage = `Usage: swarm <command> [flags]

Commands:
  daemon [--port N]                run the daemon (launchd starts it)
  install [--plugins] [--yes]      set up launchd, agent integrations and the superpowers plugins
  uninstall                        remove the launchd job and agent integrations (keeps data)
  migrate [--dry-run | --resume | --rollback]   import Agent Swarm 1.x data and cut over
  doctor [--json] [--legacy]       check prerequisites, agents, skills, Ollama, signing; --legacy reports 1.x state
  status                           daemon, items, repositories, knowledge base
  items [--type T] [--status S] [-q TEXT]
  repos [--rescan] | repos add PATH
  kb search QUERY | kb status
  mcp                              the stdio MCP server every agent launches
  hook <agent> <event>             the hook client every agent's config calls
  new --name N --intent feature|debug [--repo PATH...] [--agent A --model M --effort E] [--request TEXT]
  start KEY [--agent A --model M --effort E] [--repo PATH...]
  agents [--all]                   list agents as a tree
  attach NAME                      attach to an agent's tmux session here
  pause NAME|--all [--group]       pause an agent, its group, or everything
  resume NAME
  cancel NAME
  retry NAME [--note TEXT]         new attempt for a failed, crashed or interrupted agent
  ack NAME                         move a failed, crashed or interrupted agent to history
  lineage NAME [--repair]          show an agent's canonical identity and generation history
  requests                         list open requests
  answer REQ TEXT
  approve REQ
  confirm-repos REQ PATH... [--comment TEXT]
  request-changes REQ COMMENT
  usage [--refresh]
  dev-seed                          load the contract fixture keys (make dev only)
  version

Every command takes --home DIR (default $SWARM_HOME or ~/.swarm).
Client commands take --url URL (default $SWARM_URL or http://127.0.0.1:7777).
Flags go before arguments: swarm repos --home DIR add PATH.
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is runWithStdin over the process's own stdin.
func run(args []string, stdout, stderr io.Writer) int {
	return runWithStdin(args, os.Stdin, stdout, stderr)
}

// runWithStdin is the dispatcher; a test seam only mcp and hook need, since
// they are the two subcommands that read stdin (§11.2, L16).
func runWithStdin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "daemon":
		return cmdDaemon(args[1:], stdout, stderr)
	case "install":
		return cmdInstall(args[1:], stdout, stderr)
	case "uninstall":
		return cmdUninstall(args[1:], stdout, stderr)
	case "migrate":
		return cmdMigrate(args[1:], stdout, stderr)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "items":
		return cmdItems(args[1:], stdout, stderr)
	case "repos":
		return cmdRepos(args[1:], stdout, stderr)
	case "kb":
		return cmdKB(args[1:], stdout, stderr)
	case "mcp":
		return cmdMCP(args[1:], stdin, stdout, stderr)
	case "hook":
		return cmdHook(args[1:], stdin, stdout, stderr)
	case "new":
		return cmdNew(args[1:], stdout, stderr)
	case "start":
		return cmdStart(args[1:], stdout, stderr)
	case "agents":
		return cmdAgents(args[1:], stdout, stderr)
	case "attach":
		return cmdAttach(args[1:], stdout, stderr)
	case "pause":
		return cmdPause(args[1:], stdout, stderr)
	case "resume":
		return cmdResume(args[1:], stdout, stderr)
	case "cancel":
		return cmdCancel(args[1:], stdout, stderr)
	case "retry":
		return cmdRetry(args[1:], stdout, stderr)
	case "ack":
		return cmdAck(args[1:], stdout, stderr)
	case "lineage":
		return cmdLineage(args[1:], stdout, stderr)
	case "requests":
		return cmdRequests(args[1:], stdout, stderr)
	case "answer":
		return cmdAnswer(args[1:], stdout, stderr)
	case "approve":
		return cmdApprove(args[1:], stdout, stderr)
	case "confirm-repos":
		return cmdConfirmRepos(args[1:], stdout, stderr)
	case "request-changes":
		return cmdRequestChanges(args[1:], stdout, stderr)
	case "usage":
		return cmdUsage(args[1:], stdout, stderr)
	case "dev-seed":
		return cmdDevSeed(args[1:], stdout, stderr)
	case "version", "--version":
		fmt.Fprintln(stdout, "swarm "+version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "swarm: unknown command %q\n\n%s", args[0], usage)
	return 2
}

func defaultHome() string {
	if h := os.Getenv("SWARM_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".swarm")
}

func defaultURL() string {
	if u := os.Getenv("SWARM_URL"); u != "" {
		return u
	}
	return "http://127.0.0.1:7777"
}

// flags returns a flag set with --home (and --url for client commands).
func flags(name string, stderr io.Writer, withURL bool) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet("swarm "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", defaultHome(), "swarm data folder")
	url := new(string)
	if withURL {
		url = fs.String("url", defaultURL(), "daemon address")
	}
	return fs, home, url
}

// parse returns (exit code, done). -h/--help exits 0.
func parse(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, true
		}
		return 2, true
	}
	return 0, false
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "swarm:", err)
	return 1
}
