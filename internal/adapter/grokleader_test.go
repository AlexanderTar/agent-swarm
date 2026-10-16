package adapter

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortSock returns a socket path under /tmp: t.TempDir is longer than macOS's
// 104-byte sun_path limit.
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "l.sock")
}

// fakeLeader serves one connection on a fresh socket with script and reports
// whatever script returned through the returned channel.
func fakeLeader(t *testing.T, script func(c *fakeLeaderConn)) (path string, done <-chan struct{}) {
	t.Helper()
	path = shortSock(t)
	return path, fakeLeaderAt(t, path, script)
}

// fakeLeaderAt is fakeLeader on a socket path the caller chose. Like the real
// leader it accepts many connections: one that closes without sending a byte (a
// liveness probe) is dropped, and script runs on the first that sends something.
func fakeLeaderAt(t *testing.T, path string, script func(c *fakeLeaderConn)) <-chan struct{} {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				if _, err := br.Peek(1); err != nil {
					return
				}
				once.Do(func() {
					defer close(ch)
					script(&fakeLeaderConn{t: t, c: c, r: br})
				})
			}()
		}
	}()
	return ch
}

// handshake answers register, initialize and authenticate like a probed 1.0.60 leader.
func (f *fakeLeaderConn) handshake() {
	f.read()
	f.registered(1, "1.0.60")
	for i := 0; i < 2; i++ {
		c := f.acp()
		f.reply(c["id"], map[string]any{})
	}
}

// fakeLeaderConn speaks the probed wire format with its own framing code, so a
// bug shared by client and test cannot hide: a 4-byte big-endian length, then JSON.
type fakeLeaderConn struct {
	t *testing.T
	c net.Conn
	r io.Reader // reads go through the reader that peeked the first byte
}

func (f *fakeLeaderConn) read() map[string]any {
	f.t.Helper()
	var hdr [4]byte
	if _, err := io.ReadFull(f.r, hdr[:]); err != nil {
		f.t.Errorf("fake leader: read header: %v", err)
		return nil
	}
	body := make([]byte, binary.BigEndian.Uint32(hdr[:]))
	if _, err := io.ReadFull(f.r, body); err != nil {
		f.t.Errorf("fake leader: read body: %v", err)
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		f.t.Errorf("fake leader: frame is not JSON: %v: %s", err, body)
	}
	return m
}

func (f *fakeLeaderConn) write(v any) {
	f.t.Helper()
	b, _ := json.Marshal(v)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := f.c.Write(append(hdr[:], b...)); err != nil {
		f.t.Errorf("fake leader: write: %v", err)
	}
}

// acp reads one acp frame and returns its decoded JSON-RPC payload.
func (f *fakeLeaderConn) acp() map[string]any {
	f.t.Helper()
	m := f.read()
	if m == nil || m["type"] != "acp" {
		f.t.Errorf("fake leader: want an acp frame, got %v", m)
		return map[string]any{}
	}
	s, _ := m["payload"].(string) // the payload is JSON-RPC text, not an object
	var p map[string]any
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		f.t.Errorf("fake leader: payload is not JSON text: %v: %v", err, m["payload"])
	}
	return p
}

func (f *fakeLeaderConn) reply(id any, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	f.write(map[string]any{"type": "acp", "payload": string(b)})
}

func (f *fakeLeaderConn) notify(method string, params any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	f.write(map[string]any{"type": "acp", "payload": string(b)})
}

func (f *fakeLeaderConn) registered(version int, binary string) {
	f.write(map[string]any{"type": "registered", "client_id": 3, "ready": true,
		"leader_protocol_version": version, "leader_binary_version": binary})
}

func TestLeaderClientFraming(t *testing.T) {
	path, done := fakeLeader(t, func(f *fakeLeaderConn) {
		reg := f.read()
		want := map[string]any{"type": "register", "client_type": "grok-shell", "mode": "stdio",
			"capabilities": map[string]any{"yolo_mode": true, "auto_mode": false, "default_model": nil,
				"client_version": "1.0.60", "code_nav_enabled": false, "terminal": false,
				"fs_read": false, "fs_write": false, "status_line": false, "user_message_echo": true}}
		if a, b := mustJSON(reg), mustJSON(want); a != b {
			t.Errorf("register frame\n got %s\nwant %s", a, b)
		}
		f.registered(1, "1.0.60")

		init := f.acp()
		if init["method"] != "initialize" || init["jsonrpc"] != "2.0" || init["id"] != float64(1) {
			t.Errorf("first call = %v, want initialize id 1", init)
		}
		if a, b := mustJSON(init["params"]), `{"clientCapabilities":{},"protocolVersion":1}`; a != b {
			t.Errorf("initialize params = %s, want %s", a, b)
		}
		f.reply(init["id"], map[string]any{"protocolVersion": 1})

		auth := f.acp()
		if auth["method"] != "authenticate" || auth["id"] != float64(2) ||
			mustJSON(auth["params"]) != `{"methodId":"cached_token"}` {
			t.Errorf("second call = %v, want authenticate cached_token id 2", auth)
		}
		// Notifications and unrelated responses arrive in any order: they must not
		// be taken for this call's response.
		f.notify("_x.ai/mcp/servers_updated", map[string]any{})
		f.reply(auth["id"], map[string]any{"_meta": map[string]any{}})

		p := f.acp()
		if p["method"] != "session/prompt" || p["id"] != float64(3) ||
			mustJSON(p["params"]) != `{"prompt":[{"text":"wake up","type":"text"}],"sessionId":"s-1"}` {
			t.Errorf("third call = %v, want session/prompt id 3", p)
		}
		f.notify("_x.ai/queue/changed", map[string]any{"sessionId": "s-1"})
		f.reply(float64(99), "not ours")
		f.reply(p["id"], map[string]any{"stopReason": "end_turn"})

		r := f.acp()
		if r["method"] != "_x.ai/session/rename" || r["id"] != float64(4) ||
			mustJSON(r["params"]) != `{"sessionId":"s-1","title":"new title"}` {
			t.Errorf("fourth call = %v, want _x.ai/session/rename id 4", r)
		}
		f.reply(r["id"], map[string]any{"success": true})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialLeader(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Prompt(ctx, "s-1", "wake up"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := c.Rename(ctx, "s-1", "new title"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	<-done
}

func TestLeaderClientRefusesWhatItDidNotProbe(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		binary  string
		want    string
	}{
		{"unknown protocol version", 2, "1.0.60", "leader_protocol_version"},
		{"grok version outside the allowlist", 1, "1.0.61", "1.0.61"},
		{"no grok version", 1, "", "version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, done := fakeLeader(t, func(f *fakeLeaderConn) {
				f.read()
				f.registered(tc.version, tc.binary)
				// Anything past the refusal would be a call made to a leader the
				// client does not understand.
				var hdr [4]byte
				if _, err := io.ReadFull(f.r, hdr[:]); err == nil {
					t.Error("client kept talking after a refusal")
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := dialLeader(ctx, path)
			if err == nil {
				c.Close()
				t.Fatal("dialLeader accepted the leader")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
			<-done
		})
	}
}

func TestLeaderClientRPCErrorKeepsItsCode(t *testing.T) {
	path, _ := fakeLeader(t, func(f *fakeLeaderConn) {
		f.read()
		f.registered(1, "1.0.60")
		for i := 0; i < 2; i++ {
			c := f.acp()
			f.reply(c["id"], map[string]any{})
		}
		p := f.acp()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": p["id"],
			"error": map[string]any{"code": -32003, "message": "Rate limited"}})
		f.write(map[string]any{"type": "acp", "payload": string(b)})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialLeader(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Prompt(ctx, "s", "x")
	rpc, ok := err.(*leaderRPCError)
	if !ok || rpc.Code != -32003 {
		t.Fatalf("Prompt error = %#v, want a *leaderRPCError with code -32003", err)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v) // map keys marshal sorted, so equal documents compare equal
	return string(b)
}
