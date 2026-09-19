package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/mcpshim"
)

// cmdMCP is `swarm mcp`: the stdio MCP server every agent launches (L16).
func cmdMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, home, _ := flags("mcp", stderr, false)
	if code, done := parse(fs, args); done {
		return code
	}
	url, token, kind, bound := mcpshim.TokenFromEnv(os.Getenv)
	if !bound {
		// an unbound shim authenticates with the daemon token and gets the
		// read-only tool set (L16, I6)
		b, err := os.ReadFile(filepath.Join(*home, "run", "daemon.token"))
		if err != nil || strings.TrimSpace(string(b)) == "" {
			return fail(stderr, fmt.Errorf("Can't read a session token or the daemon token (%s). Run swarm install.",
				filepath.Join(*home, "run", "daemon.token")))
		}
		token = strings.TrimSpace(string(b))
	}
	// A bound shim (spawned by the daemon) always has SWARM_URL already. An
	// unbound one is now also the normal case for `swarm install`'s global
	// MCP registration (M3) — an interactive, non-spawned session with no
	// SWARM_URL of its own — so it defaults to the local daemon the same way
	// every other client command does, instead of failing with an empty URL.
	if url == "" {
		url = defaultURL()
	}
	s := &mcpshim.Shim{In: stdin, Out: stdout, Err: stderr, URL: url, Token: token,
		AgentKind: kind, HTTP: &http.Client{Timeout: 60 * time.Second},
		Log: log.New(stderr, "", log.LstdFlags).Printf}
	if err := s.Run(context.Background()); err != nil {
		return fail(stderr, err)
	}
	return 0
}
