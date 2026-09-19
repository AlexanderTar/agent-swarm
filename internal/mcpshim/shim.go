// Package mcpshim is `swarm mcp`: the stdio MCP server every agent launches. It
// forwards JSON-RPC to the daemon's /mcp (L16) and, for Claude, turns the
// daemon's wake stream into notifications/claude/channel frames (§11.3).
//
// It is a raw line proxy on purpose: the channel notification is not part of
// MCP, and forwarding bytes keeps the shim from drifting from the daemon's SDK
// version.
package mcpshim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Shim proxies stdio JSON-RPC to the daemon's /mcp and bridges its wake
// stream into notifications/claude/channel frames for Claude.
type Shim struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer

	URL, Token, AgentKind string
	HTTP                  *http.Client

	// Backoff is the first wake-stream retry delay; 0 means 1 s. It exists so
	// a test does not have to sleep through a real reconnect backoff.
	Backoff time.Duration

	Log func(format string, args ...any)

	// outMu guards Out: the forward loop and the wake bridge both write to it.
	outMu sync.Mutex
}

// Unset reports whether an environment value counts as unset. Cursor passes an
// unset variable through as the literal text ${env:NAME} (P0-1, L16).
func Unset(v string) bool { return v == "" || strings.HasPrefix(v, "${env:") }

// TokenFromEnv resolves the daemon URL, bearer token and agent kind from the
// shim's environment. A session token file is preferred; an unbound shim
// (SWARM_SESSION or SWARM_TOKEN_FILE unset or a literal ${env:...} placeholder,
// L16) falls back to the daemon's own token file under its home directory.
func TokenFromEnv(env func(string) string) (url, token, kind string, bound bool) {
	url = env("SWARM_URL")
	kind = env("SWARM_AGENT_KIND")
	session := env("SWARM_SESSION")
	tokenFile := env("SWARM_TOKEN_FILE")
	bound = !Unset(session) && !Unset(tokenFile)
	if bound {
		if b, err := os.ReadFile(tokenFile); err == nil {
			token = strings.TrimSpace(string(b))
		}
		return url, token, kind, bound
	}
	home := env("SWARM_HOME")
	if home == "" {
		if h := env("HOME"); h != "" {
			home = filepath.Join(h, ".swarm")
		}
	}
	if home != "" {
		if b, err := os.ReadFile(filepath.Join(home, "run", "daemon.token")); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	return url, token, kind, bound
}

func (s *Shim) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func (s *Shim) httpClient() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

func (s *Shim) writeLine(body []byte) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.Out.Write(body)
	s.Out.Write([]byte("\n"))
}

func (s *Shim) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if s.AgentKind == "claude" {
		go s.bridgeWake(ctx)
	}
	sc := bufio.NewScanner(s.In)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		s.forward(ctx, append([]byte(nil), line...))
	}
	return sc.Err()
}

// forward posts one JSON-RPC message and writes the response. A request without
// an id is a notification and gets no response line.
func (s *Shim) forward(ctx context.Context, line []byte) {
	var head struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	json.Unmarshal(line, &head)
	body, err := s.post(ctx, line)
	if err != nil {
		if len(head.ID) > 0 {
			s.writeLine(rpcError(head.ID, -32603, "The Swarm daemon is unavailable: "+err.Error()))
		}
		return
	}
	if len(body) == 0 || len(head.ID) == 0 {
		return
	}
	if head.Method == "initialize" && s.AgentKind == "claude" {
		body = addChannelCapability(body)
	}
	s.writeLine(body)
}

// post sends one JSON-RPC message to the daemon's stateless streamable-HTTP
// endpoint (L16) and returns its body.
func (s *Shim) post(ctx context.Context, line []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(s.URL, "/")+"/mcp", bytes.NewReader(line))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

type rpcErrorMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func rpcError(id json.RawMessage, code int, message string) []byte {
	var m rpcErrorMsg
	m.JSONRPC, m.ID = "2.0", id
	m.Error.Code, m.Error.Message = code, message
	b, _ := json.Marshal(m)
	return b
}

// addChannelCapability decodes into a generic map, adds
// result.capabilities.experimental["claude/channel"], and re-encodes. A body
// it cannot decode, or that has no result.capabilities object, is returned
// unchanged.
func addChannelCapability(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	result, ok := m["result"].(map[string]any)
	if !ok {
		return body
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		caps = map[string]any{}
		result["capabilities"] = caps
	}
	exp, ok := caps["experimental"].(map[string]any)
	if !ok {
		exp = map[string]any{}
		caps["experimental"] = exp
	}
	exp["claude/channel"] = map[string]any{}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// bridgeWake subscribes to the daemon's wake stream and turns each event into
// one notifications/claude/channel line, reconnecting with backoff (starting
// at Backoff, default 1 s, doubling to a 30 s ceiling) after the daemon
// restarts or the stream otherwise ends.
func (s *Shim) bridgeWake(ctx context.Context) {
	backoff := s.Backoff
	if backoff <= 0 {
		backoff = time.Second
	}
	const maxBackoff = 30 * time.Second
	for ctx.Err() == nil {
		if err := s.streamWake(ctx); err != nil && ctx.Err() == nil {
			s.logf("mcpshim: wake stream: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (s *Shim) streamWake(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(s.URL, "/")+"/api/sessions/self/wake", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var event string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if event == "wake" {
				s.emitWake(strings.TrimPrefix(line, "data: "))
			}
			event = ""
		}
	}
	return sc.Err()
}

// emitWake turns one wake payload {"content":...,"msg_id":...} into exactly
// one notifications/claude/channel frame.
func (s *Shim) emitWake(payload string) {
	var n struct {
		Content string `json:"content"`
		MsgID   string `json:"msg_id"`
	}
	if err := json.Unmarshal([]byte(payload), &n); err != nil {
		s.logf("mcpshim: bad wake payload: %v", err)
		return
	}
	frame := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/claude/channel",
		"params": map[string]any{
			"content": n.Content,
			"meta":    map[string]string{"swarm_msg": n.MsgID},
		},
	}
	b, err := json.Marshal(frame)
	if err != nil {
		return
	}
	s.writeLine(b)
}
