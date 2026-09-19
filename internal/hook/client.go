// Package hook is the `swarm hook <agent> <event>` client and the daemon-side
// decision handler (spec §11.2).
package hook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	maxStdin = 1 << 20
	timeout  = 1500 * time.Millisecond
)

// Run is the hook client. It never blocks or fails the agent: any problem is a
// silent exit 0 (§11.2). A hook in a session the user started by hand sees no
// SWARM_SESSION and returns in well under 50 ms (§23.2 scenario 15).
func Run(args []string, stdin io.Reader, stdout io.Writer, env func(string) string, client *http.Client) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: swarm hook <claude|codex|agy|cursor|fake> <event>")
		return 2
	}
	agent, event := args[0], args[1]
	session, tokenPath := env("SWARM_SESSION"), env("SWARM_TOKEN_FILE")
	if session == "" || tokenPath == "" {
		return 0
	}
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		return 0
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return 0
	}
	body, _ := io.ReadAll(io.LimitReader(stdin, maxStdin))
	// No default URL (S6). `swarm hook` only ever runs inside a pane the daemon
	// spawned, and that pane always has SWARM_URL. Defaulting to :7777 would mean
	// that running this binary by hand from a worktree posts a hook decision into
	// the user's live v1 daemon; exit 0 (allow, unchanged) is the fail-safe.
	base := env("SWARM_URL")
	if base == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	url := strings.TrimRight(base, "/") + "/hook/" + agent + "/" + event
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxStdin))
	if err != nil {
		return 0
	}
	stdout.Write(out)
	return 0
}
