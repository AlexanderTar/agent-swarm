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
	// No default URL (S6, matching internal/hook.Run): `swarm mcp` only ever
	// runs inside a pane the daemon spawned, which always has SWARM_URL.
	// Defaulting to :7777 would mean running this binary by hand posts MCP
	// calls into the user's live production daemon.
	s := &mcpshim.Shim{In: stdin, Out: stdout, Err: stderr, URL: url, Token: token,
		AgentKind: kind, HTTP: &http.Client{Timeout: 60 * time.Second},
		Log: log.New(stderr, "", log.LstdFlags).Printf}
	if err := s.Run(context.Background()); err != nil {
		return fail(stderr, err)
	}
	return 0
}
