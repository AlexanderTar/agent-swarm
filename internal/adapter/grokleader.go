package adapter

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
)

// The grok leader is a daemon the agent's own TUI starts (--leader
// --leader-socket <path>). Its socket speaks 4-byte big-endian length + JSON
// frames, and carries ACP as {"type":"acp","payload":"<JSON-RPC text>"}. Every
// shape below is the one recorded in the leader probes (TASK-1126 section 4,
// TASK-1129 probe 6); nothing here is guessed.

// ErrUnsupportedLeader is a leader whose protocol or grok version was not probed.
var ErrUnsupportedLeader = errors.New("unsupported grok leader")

// grokLeaderProtocol is the only leader_protocol_version the probes covered.
const grokLeaderProtocol = 1

// grokLeaderVersions is the allowlist of grok builds whose leader was probed. A
// newer build may change the wire format, so it is refused and the wake falls
// back to the tmux paste until the allowlist is widened on purpose.
var grokLeaderVersions = []string{"1.0.60"}

// grokClientVersion is what register reports: the probed client_version.
const grokClientVersion = "1.0.60"

// leaderMaxFrame caps one frame so a corrupt length cannot allocate gigabytes.
const leaderMaxFrame = 16 << 20

// leaderRPCError is a JSON-RPC error the leader answered a call with. The code
// matters: a prompt that hits the quota still reached the session (-32003).
type leaderRPCError struct {
	Code    int
	Message string
}

func (e *leaderRPCError) Error() string {
	return fmt.Sprintf("grok leader: rpc error %d: %s", e.Code, e.Message)
}

// leaderClient is one registered, authenticated connection. Calls are
// sequential: Wake opens a client per prompt, so nothing needs to multiplex.
type leaderClient struct {
	mu   sync.Mutex
	conn net.Conn
	next int
}

// dialLeader connects to the socket, registers, refuses a leader whose
// protocol or grok version was not probed, then initializes and authenticates
// with the cached token.
func dialLeader(ctx context.Context, path string) (*leaderClient, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("grok leader: %w", err)
	}
	c := &leaderClient{conn: conn}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := c.handshake(); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *leaderClient) Close() error { return c.conn.Close() }

func (c *leaderClient) handshake() error {
	if err := writeLeaderFrame(c.conn, map[string]any{"type": "register", "client_type": "grok-shell", "mode": "stdio",
		"capabilities": map[string]any{"yolo_mode": true, "auto_mode": false, "default_model": nil,
			"client_version": grokClientVersion, "code_nav_enabled": false, "terminal": false,
			"fs_read": false, "fs_write": false, "status_line": false, "user_message_echo": true}}); err != nil {
		return err
	}
	raw, err := readLeaderFrame(c.conn)
	if err != nil {
		return err
	}
	var reg struct {
		Type     string `json:"type"`
		Protocol int    `json:"leader_protocol_version"`
		Binary   string `json:"leader_binary_version"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil || reg.Type != "registered" {
		return fmt.Errorf("%w: register was answered with %.80s", ErrUnsupportedLeader, raw)
	}
	if reg.Protocol != grokLeaderProtocol {
		return fmt.Errorf("%w: leader_protocol_version %d is not the probed %d", ErrUnsupportedLeader, reg.Protocol, grokLeaderProtocol)
	}
	if !slices.Contains(grokLeaderVersions, reg.Binary) {
		return fmt.Errorf("%w: grok version %q is not in the probed allowlist %v", ErrUnsupportedLeader, reg.Binary, grokLeaderVersions)
	}
	if _, err := c.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}); err != nil {
		return err
	}
	_, err = c.call("authenticate", map[string]any{"methodId": "cached_token"})
	return err
}

// call sends one JSON-RPC request and returns its result.
func (c *leaderClient) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, err := c.send(method, params)
	if err != nil {
		return nil, err
	}
	return c.await(id)
}

func (c *leaderClient) send(method string, params any) (int, error) {
	c.next++
	id := c.next
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return 0, err
	}
	return id, writeLeaderFrame(c.conn, map[string]any{"type": "acp", "payload": string(body)})
}

// await reads frames until the response to id, skipping every notification and
// any response to another id.
func (c *leaderClient) await(id int) (json.RawMessage, error) {
	for {
		raw, err := readLeaderFrame(c.conn)
		if err != nil {
			return nil, err
		}
		var f struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		}
		if json.Unmarshal(raw, &f) != nil || f.Type != "acp" {
			continue
		}
		var m struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(f.Payload), &m) != nil || m.ID == nil || *m.ID != id {
			continue
		}
		if m.Error != nil {
			return nil, &leaderRPCError{Code: m.Error.Code, Message: m.Error.Message}
		}
		return m.Result, nil
	}
}

// StartPrompt submits text to the session and returns once the request is
// written. Mid-turn it queues behind the running turn (probe 6), and the
// response only comes when that queued turn ends, so wait blocks for that long.
func (c *leaderClient) StartPrompt(sessionID, text string) (wait func() error, err error) {
	c.mu.Lock()
	id, err := c.send("session/prompt", map[string]any{"sessionId": sessionID,
		"prompt": []map[string]string{{"type": "text", "text": text}}})
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	return func() error {
		defer c.mu.Unlock()
		_, err := c.await(id)
		return err
	}, nil
}

// Prompt is StartPrompt and the wait, bounded by ctx.
func (c *leaderClient) Prompt(ctx context.Context, sessionID, text string) error {
	stop := context.AfterFunc(ctx, func() { c.conn.Close() })
	defer stop()
	wait, err := c.StartPrompt(sessionID, text)
	if err != nil {
		return err
	}
	return wait()
}

// Rename sets the session title, as /rename does in the pane.
func (c *leaderClient) Rename(ctx context.Context, sessionID, title string) error {
	stop := context.AfterFunc(ctx, func() { c.conn.Close() })
	defer stop()
	_, err := c.call("_x.ai/session/rename", map[string]any{"sessionId": sessionID, "title": title})
	return err
}

func writeLeaderFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf := make([]byte, 4, 4+len(b))
	binary.BigEndian.PutUint32(buf, uint32(len(b)))
	_, err = w.Write(append(buf, b...))
	return err
}

func readLeaderFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > leaderMaxFrame {
		return nil, errors.New("grok leader: frame too large")
	}
	body := make([]byte, n)
	_, err := io.ReadFull(r, body)
	return body, err
}

// Watch reads the leader's pushes until the connection ends or ctx is done,
// handing each notification's method and params to onNote. It makes no calls,
// so it shares the connection with nothing else.
func (c *leaderClient) Watch(ctx context.Context, onNote func(method string, params json.RawMessage)) error {
	stop := context.AfterFunc(ctx, func() { c.conn.Close() })
	defer stop()
	for {
		raw, err := readLeaderFrame(c.conn)
		if err != nil {
			return err
		}
		var f struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		}
		var m struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &f) != nil || f.Type != "acp" || json.Unmarshal([]byte(f.Payload), &m) != nil || m.Method == "" {
			continue
		}
		onNote(m.Method, m.Params)
	}
}
