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
  install [--dry-run]              set up the launchd service
  doctor [--json]                  check prerequisites, agents, Ollama, signing
  status                           daemon, items, repositories, knowledge base
  items [--type T] [--status S] [-q TEXT]
  repos [--rescan] | repos add PATH
  kb search QUERY | kb status
  version

Every command takes --home DIR (default $SWARM_HOME or ~/.swarm).
Client commands take --url URL (default $SWARM_URL or http://127.0.0.1:7777).
Flags go before arguments: swarm repos --home DIR add PATH.
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "daemon":
		return cmdDaemon(args[1:], stdout, stderr)
	case "install":
		return cmdInstall(args[1:], stdout, stderr)
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
