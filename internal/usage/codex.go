package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// Codex reads usage from `codex app-server`'s JSON-RPC stream, falling back
// to today's newest rollout file when the app-server can't be reached (§13).
type Codex struct {
	Start    execx.Starter
	UserHome string
	Timeout  time.Duration
	Now      func() time.Time
	Log      func(string, ...any)
}

func (c *Codex) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Codex) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

func (c *Codex) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Second
}

// codexWindow maps a rate-limit window to the §13 label/window pair used
// throughout this package; anything else falls back to a generic label.
func codexWindow(minutes int) (label, window string) {
	switch minutes {
	case 300:
		return "5h", "5h"
	case 10080:
		return "Weekly", "weekly"
	default:
		return fmt.Sprintf("%dm", minutes), "custom"
	}
}

// codexRPCRateLimitWindow is one window ("primary" or "secondary") of
// account/rateLimits/read's response, live-verified against codex-cli
// 0.154.0.
type codexRPCRateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

type codexRPCRateLimits struct {
	Primary   *codexRPCRateLimitWindow `json:"primary"`
	Secondary *codexRPCRateLimitWindow `json:"secondary"`
}

// codexRPCEnvelope covers every shape that can arrive on app-server's
// stdout: a response (ID set, Result or Error), or a fire-and-forget
// notification (Method set, no ID).
type codexRPCEnvelope struct {
	ID     *int                `json:"id"`
	Method string              `json:"method"`
	Result json.RawMessage     `json:"result"`
	Error  *codexRPCErrorField `json:"error"`
}

type codexRPCErrorField struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Fetch tries the live app-server first, then the rollout fallback.
func (c *Codex) Fetch(ctx context.Context) (Snapshot, error) {
	snap, err := c.fetchViaAppServer(ctx)
	if err == nil {
		return snap, nil
	}
	c.logf("usage: codex app-server: %v; falling back to today's rollout", err)
	return c.fetchViaRollout(ctx)
}

// fetchViaAppServer speaks app-server's JSON-RPC protocol: an `initialize`
// handshake is required before it answers anything else (live-verified
// against codex-cli 0.154.0 — a bare `getRateLimits` request without it is
// rejected outright), then `account/rateLimits/read` returns the real
// rate-limit snapshot.
func (c *Codex) fetchViaAppServer(ctx context.Context) (Snapshot, error) {
	if c.Start == nil {
		return Snapshot{}, fmt.Errorf("codex: no app-server starter configured")
	}
	proc, err := c.Start(ctx, "codex", "app-server")
	if err != nil {
		return Snapshot{}, err
	}
	defer proc.Kill()

	// Real wall-clock time, not c.now(): tests pin c.now() to a fixed instant
	// for the rollout's "which day is today" logic, but time.After/time.Until
	// below always compare against the real clock.
	deadline := time.Now().Add(c.timeout())
	msgs := make(chan codexRPCEnvelope)
	readErrs := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		// A json.Decoder, not a line scanner: a pretty-printed JSON-RPC message
		// can span several physical lines, and Decode reads exactly one JSON
		// value at a time regardless of the whitespace inside it.
		dec := json.NewDecoder(proc.Stdout)
		for {
			var env codexRPCEnvelope
			if err := dec.Decode(&env); err != nil {
				select {
				case readErrs <- err:
				case <-done:
				}
				return
			}
			// done fires on timeout/error return from fetchViaAppServer; without
			// this select, a decoded message nobody's waiting for anymore (e.g. a
			// notification racing the final response) would leak this goroutine
			// blocked forever on an unbuffered send.
			select {
			case msgs <- env:
			case <-done:
				return
			}
		}
	}()

	if _, err := c.callAppServer(proc, msgs, readErrs, deadline, 1, "initialize",
		map[string]any{"clientInfo": map[string]any{"name": "agent-swarm", "title": "agent-swarm", "version": "0.0.0"}},
	); err != nil {
		return Snapshot{}, fmt.Errorf("codex: initialize: %w", err)
	}

	result, err := c.callAppServer(proc, msgs, readErrs, deadline, 2, "account/rateLimits/read", nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("codex: account/rateLimits/read: %w", err)
	}

	var payload struct {
		RateLimits codexRPCRateLimits `json:"rateLimits"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return Snapshot{}, fmt.Errorf("codex: decoding rate limits: %w", err)
	}
	return codexSnapshotFromRPC(payload.RateLimits), nil
}

// callAppServer writes one JSON-RPC request and waits for the response with
// a matching id, skipping stray notifications and responses to other
// in-flight ids along the way. A JSON-RPC error response is surfaced
// immediately instead of being silently ignored until the deadline.
func (c *Codex) callAppServer(proc *execx.Proc, msgs <-chan codexRPCEnvelope, readErrs <-chan error,
	deadline time.Time, id int, method string, params any) (json.RawMessage, error) {
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := proc.Stdin.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("writing the request: %w", err)
	}
	for {
		select {
		case env := <-msgs:
			if env.ID == nil || *env.ID != id {
				continue // a notification, or a response to a different call
			}
			if env.Error != nil {
				return nil, fmt.Errorf("app-server error %d: %s", env.Error.Code, env.Error.Message)
			}
			return env.Result, nil
		case err := <-readErrs:
			return nil, fmt.Errorf("app-server closed before answering: %w", err)
		case <-time.After(time.Until(deadline)):
			return nil, fmt.Errorf("app-server timed out after %s", c.timeout())
		}
	}
}

func codexSnapshotFromRPC(rl codexRPCRateLimits) Snapshot {
	var meters []Meter
	headline := ""
	add := func(id string, w *codexRPCRateLimitWindow) {
		if w == nil {
			return
		}
		label, window := codexWindow(w.WindowDurationMins)
		var resetsAt *time.Time
		if w.ResetsAt > 0 {
			t := time.Unix(w.ResetsAt, 0)
			resetsAt = &t
		}
		meters = append(meters, Meter{ID: id, Label: label, Window: window,
			UsedPct: w.UsedPercent, ResetsAt: resetsAt})
		if headline == "" {
			headline = id
		}
	}
	add("primary", rl.Primary)
	add("secondary", rl.Secondary)
	return Snapshot{Meters: meters, HeadlineID: headline, Source: "app-server"}
}

type codexRolloutRateLimit struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
}

type codexRolloutPayload struct {
	Type       string `json:"type"`
	RateLimits struct {
		Primary   *codexRolloutRateLimit `json:"primary"`
		Secondary *codexRolloutRateLimit `json:"secondary"`
	} `json:"rate_limits"`
}

type codexRolloutLine struct {
	Type    string              `json:"type"`
	Payload codexRolloutPayload `json:"payload"`
}

// fetchViaRollout is §13's fallback: the newest token_count event from
// today's rollout files under ~/.codex/sessions/YYYY/MM/DD/.
func (c *Codex) fetchViaRollout(ctx context.Context) (Snapshot, error) {
	now := c.now()
	dir := filepath.Join(c.UserHome, ".codex", "sessions",
		fmt.Sprintf("%04d", now.Year()), fmt.Sprintf("%02d", now.Month()), fmt.Sprintf("%02d", now.Day()))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Snapshot{}, fmt.Errorf("codex: no rollouts for today: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var latest *codexRolloutPayload
	for _, name := range names {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var line codexRolloutLine
			if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
				continue
			}
			if line.Type != "event_msg" || line.Payload.Type != "token_count" {
				continue
			}
			p := line.Payload
			latest = &p
		}
		f.Close()
	}
	if latest == nil {
		return Snapshot{}, fmt.Errorf("codex: no usage recorded in today's rollouts")
	}
	var meters []Meter
	headline := ""
	if p := latest.RateLimits.Primary; p != nil {
		label, window := codexWindow(p.WindowMinutes)
		meters = append(meters, Meter{ID: "primary", Label: label, Window: window, UsedPct: p.UsedPercent})
		headline = "primary"
	}
	if s := latest.RateLimits.Secondary; s != nil {
		label, window := codexWindow(s.WindowMinutes)
		meters = append(meters, Meter{ID: "secondary", Label: label, Window: window, UsedPct: s.UsedPercent})
		if headline == "" {
			headline = "secondary"
		}
	}
	if len(meters) == 0 {
		return Snapshot{}, fmt.Errorf("codex: today's rollouts have no rate limits")
	}
	return Snapshot{Meters: meters, HeadlineID: headline, Source: "rollout"}, nil
}
