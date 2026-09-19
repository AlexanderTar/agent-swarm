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

type codexRPCRateLimit struct {
	ID                 string  `json:"id"`
	WindowDurationMins int     `json:"windowDurationMins"`
	UsedPercent        float64 `json:"usedPercent"`
	ResetsAt           int64   `json:"resetsAt"`
}

type codexRPCMessage struct {
	Method string `json:"method"`
	Result *struct {
		RateLimits []codexRPCRateLimit `json:"rateLimits"`
	} `json:"result"`
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

func (c *Codex) fetchViaAppServer(ctx context.Context) (Snapshot, error) {
	if c.Start == nil {
		return Snapshot{}, fmt.Errorf("codex: no app-server starter configured")
	}
	proc, err := c.Start(ctx, "codex", "app-server")
	if err != nil {
		return Snapshot{}, err
	}
	defer proc.Kill()

	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "getRateLimits"})
	if _, err := proc.Stdin.Write(append(req, '\n')); err != nil {
		return Snapshot{}, fmt.Errorf("codex: writing the app-server request: %w", err)
	}

	type result struct {
		snap Snapshot
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		// A json.Decoder, not a line scanner: a pretty-printed JSON-RPC message
		// can span several physical lines, and Decode reads exactly one JSON
		// value at a time regardless of the whitespace inside it.
		dec := json.NewDecoder(proc.Stdout)
		for {
			var msg codexRPCMessage
			if err := dec.Decode(&msg); err != nil {
				ch <- result{err: fmt.Errorf("codex: app-server closed before answering: %w", err)}
				return
			}
			if msg.Method != "" || msg.Result == nil {
				continue // a notification, or a response we're not waiting for
			}
			ch <- result{snap: codexSnapshotFromRPC(msg.Result.RateLimits)}
			return
		}
	}()

	select {
	case r := <-ch:
		return r.snap, r.err
	case <-time.After(c.timeout()):
		return Snapshot{}, fmt.Errorf("codex: app-server timed out after %s", c.timeout())
	}
}

func codexSnapshotFromRPC(limits []codexRPCRateLimit) Snapshot {
	meters := make([]Meter, 0, len(limits))
	headline := ""
	for i, l := range limits {
		label, window := codexWindow(l.WindowDurationMins)
		var resetsAt *time.Time
		if l.ResetsAt > 0 {
			t := time.Unix(l.ResetsAt, 0)
			resetsAt = &t
		}
		meters = append(meters, Meter{ID: l.ID, Label: label, Window: window,
			UsedPct: l.UsedPercent, ResetsAt: resetsAt})
		if i == 0 {
			headline = l.ID
		}
	}
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
