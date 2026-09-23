package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// MuseUsage is summed per-turn token counts for one muse session, read from a
// redacted `muse export`. The CLI emits no quota endpoint and no token stream
// on stdout, so the export file is the only usage source (probed 2026-09-23).
type MuseUsage struct {
	Turns                             int
	InputTokens, OutputTokens         int
	ReasoningTokens                   int
	CacheReadTokens, CacheWriteTokens int
}

// museUsageEvent is the only object summed: events[i].envelope.payload.event
// .usage. record/quantity objects duplicate the same turns and are ignored.
type museUsageEvent struct {
	Usage *struct {
		InputTokens      int `json:"input_tokens"`
		OutputTokens     int `json:"output_tokens"`
		ReasoningTokens  int `json:"reasoning_tokens"`
		CacheReadTokens  int `json:"cache_read_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"usage"`
}

type museExportEnvelope struct {
	Payload *struct {
		Event *museUsageEvent `json:"event"`
	} `json:"payload"`
}

type museExportDoc struct {
	Events []struct {
		Envelope *museExportEnvelope `json:"envelope"`
	} `json:"events"`
}

// ParseMuseExport sums the per-turn usage objects of one export document.
// Missing events is an error, never an empty success: callers must not mistake
// "nothing exported" for "nothing spent".
func ParseMuseExport(raw []byte) (MuseUsage, error) {
	var doc museExportDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return MuseUsage{}, err
	}
	if doc.Events == nil {
		return MuseUsage{}, fmt.Errorf("muse export has no events array")
	}
	var out MuseUsage
	for _, e := range doc.Events {
		if e.Envelope == nil || e.Envelope.Payload == nil || e.Envelope.Payload.Event == nil {
			continue
		}
		u := e.Envelope.Payload.Event.Usage
		if u == nil {
			continue
		}
		out.Turns++
		out.InputTokens += u.InputTokens
		out.OutputTokens += u.OutputTokens
		out.ReasoningTokens += u.ReasoningTokens
		out.CacheReadTokens += u.CacheReadTokens
		out.CacheWriteTokens += u.CacheWriteTokens
	}
	return out, nil
}

// MuseSessionUsage exports one muse session (redacted) and sums its token
// usage. The export lands in dir and is removed afterwards; dir must exist.
func MuseSessionUsage(ctx context.Context, run execx.Runner, dir, sessionID string) (MuseUsage, error) {
	out := filepath.Join(dir, "muse-export-"+sessionID+".json")
	// --out <file>: confirmed live 2026-09-23 via `muse export --help` (v1.3.0).
	if _, err := run(ctx, "muse", "export", "--session", sessionID, "--redacted", "--out", out); err != nil {
		return MuseUsage{}, err
	}
	defer os.Remove(out)
	raw, err := os.ReadFile(out)
	if err != nil {
		return MuseUsage{}, err
	}
	return ParseMuseExport(raw)
}

// ---------------------------------------------------------------------------
// Muse: the real subscription-quota source, read over MSP.
// ---------------------------------------------------------------------------

// Muse reads Muse Code's subscription quota from a `muse serve` MSP host's
// `usage/read`, live-verified against Muse Code 1.3.0 on 2026-09-23 — see
// docs/specs/2026-09-23-muse-usage-probe.md for the full trace. The payload is the same shape codex app-server returns: an integer
// usedPercent per window, the window's length in minutes, and an epoch-ms
// reset stamp — verbatim from the provider, not derived from token counts.
//
// THIS PROBE IS NOT FREE. A freshly spawned host has observed nothing and
// answers `{}`; the numbers only arrive with a provider response frame, so a
// fresh observation costs one minimal-effort muse turn (prompt "ok",
// reasoningEffort "minimal", shell and writes disabled). ProbeGap caps that
// at one turn per 15 minutes and serves the last observation in between —
// which is exactly what MSP's own usage/read does, so the cached answer is
// the same truth, only older. The poller's fetched_at then means "last
// confirmed", and the data is at most ProbeGap + one poll old.
type Muse struct {
	Start    execx.Starter
	Dir      string        // workspaceRoot for the throwaway probe session
	Timeout  time.Duration // default 60s
	ProbeGap time.Duration // default 15m
	Now      func() time.Time

	mu       sync.Mutex
	cached   []Meter
	headline string
	at       time.Time
}

func (m *Muse) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Muse) timeout() time.Duration {
	if m.Timeout > 0 {
		return m.Timeout
	}
	return 60 * time.Second
}

// probeGap defaults to 15 minutes: three times the poller's default
// UsagePollSec, which is exactly its own staleAfter, so a cached answer
// never reads as stale.
func (m *Muse) probeGap() time.Duration {
	if m.ProbeGap > 0 {
		return m.ProbeGap
	}
	return 15 * time.Minute
}

// museUsageWindow is one block of the SubscriptionUsage payload. The weekly
// block carries no windowDurationMins; the field simply stays zero there.
type museUsageWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAtMs         int64   `json:"resetsAtMs"`
}

type museSubscriptionUsage struct {
	Window museUsageWindow `json:"window"`
	Weekly museUsageWindow `json:"weekly"`
	Tier   string          `json:"tier"`
}

// museRPCEnvelope covers everything that can arrive on the host's stdout: a
// response (ID set, Result or Error) or a notification (Method set, no ID).
type museRPCEnvelope struct {
	ID     *int               `json:"id"`
	Method string             `json:"method"`
	Result json.RawMessage    `json:"result"`
	Params json.RawMessage    `json:"params"`
	Error  *museRPCErrorField `json:"error"`
}

type museRPCErrorField struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Fetch returns the 5h and weekly quota meters. Inside ProbeGap it replays
// the last observation without starting a host; a failed probe leaves the
// cache alone so the next poll retries instead of serving a stale answer for
// the whole gap.
func (m *Muse) Fetch(ctx context.Context) ([]Meter, string, error) {
	m.mu.Lock()
	if len(m.cached) > 0 && m.now().Sub(m.at) < m.probeGap() {
		meters, headline := m.cached, m.headline
		m.mu.Unlock()
		return meters, headline, nil
	}
	m.mu.Unlock()

	usage, err := m.probe(ctx)
	if err != nil {
		return nil, "", err
	}
	meters, headline := museMeters(usage)
	m.mu.Lock()
	m.cached, m.headline, m.at = meters, headline, m.now()
	m.mu.Unlock()
	return meters, headline, nil
}

// probe spawns one MSP host, spends one minimal turn and returns the
// subscription usage the host observed while that turn ran.
func (m *Muse) probe(ctx context.Context) (museSubscriptionUsage, error) {
	var zero museSubscriptionUsage
	if m.Start == nil {
		return zero, fmt.Errorf("muse: no MSP host starter configured")
	}
	proc, err := m.Start(ctx, "muse", "serve",
		"--no-session-log", "--disable-shell", "--disable-write")
	if err != nil {
		return zero, err
	}
	defer proc.Kill()

	deadline := time.Now().Add(m.timeout())
	msgs := make(chan museRPCEnvelope)
	readErrs := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		dec := json.NewDecoder(proc.Stdout)
		for {
			var env museRPCEnvelope
			if err := dec.Decode(&env); err != nil {
				select {
				case readErrs <- err:
				case <-done:
				}
				return
			}
			select {
			case msgs <- env:
			case <-done:
				return
			}
		}
	}()

	if _, err := m.call(proc, msgs, readErrs, deadline, 1, "initialize", map[string]any{
		// MSP requires ^[a-z0-9_]+$ here. A hyphen does NOT fail loudly: the
		// initialize response still arrives, the initialized notification is
		// dropped (FR-008), and every later call answers notInitialized.
		"clientInfo": map[string]any{"name": "swarm", "title": "agent-swarm", "version": "0.0.0"},
	}); err != nil {
		return zero, fmt.Errorf("muse: initialize: %w", err)
	}
	// The handshake only closes once this lands, and it must carry object
	// params or the host drops it as FR-005.
	if err := m.notify(proc, "initialized"); err != nil {
		return zero, fmt.Errorf("muse: initialized: %w", err)
	}

	sessionID, err := uuid.NewV7()
	if err != nil {
		return zero, err
	}
	startID, err := uuid.NewV7()
	if err != nil {
		return zero, err
	}
	if _, err := m.call(proc, msgs, readErrs, deadline, 2, "session/start", map[string]any{
		"commandId": startID.String(), "sessionId": sessionID.String(), "workspaceRoot": m.Dir,
	}); err != nil {
		return zero, fmt.Errorf("muse: session/start: %w", err)
	}

	turnID, err := uuid.NewV7()
	if err != nil {
		return zero, err
	}
	if _, err := m.call(proc, msgs, readErrs, deadline, 3, "turn/start", map[string]any{
		"commandId": turnID.String(), "sessionId": sessionID.String(),
		"reasoningEffort": "minimal",
		"input":           []any{map[string]any{"type": "text", "text": "ok"}},
	}); err != nil {
		return zero, fmt.Errorf("muse: turn/start: %w", err)
	}
	return m.pollUsage(proc, msgs, readErrs, deadline)
}

// pollUsage asks usage/read until the host answers with a usage member.
// usage/read itself makes no model call; it replays whatever the host has
// observed, so this is just waiting for the turn above to produce the
// provider frame. It deliberately does NOT wait on the usage/changed
// notification: verified live 2026-09-23 that a host reached this way
// answers usage/read correctly while never delivering usage/changed on the
// same connection, so a notification wait hangs until the deadline.
//
// A deadline with the usage member still absent is an error, never an empty
// success: that is the PAYG/META_API_KEY case, where the provider sends no
// subscription frames at all.
func (m *Muse) pollUsage(proc *execx.Proc, msgs <-chan museRPCEnvelope, readErrs <-chan error,
	deadline time.Time) (museSubscriptionUsage, error) {
	var zero museSubscriptionUsage
	for id := 10; ; id++ {
		raw, err := m.call(proc, msgs, readErrs, deadline, id, "usage/read", nil)
		if err != nil {
			return zero, fmt.Errorf("muse: usage/read: %w", err)
		}
		var result struct {
			Usage *museSubscriptionUsage `json:"usage"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return zero, fmt.Errorf("muse: decoding usage/read: %w", err)
		}
		if result.Usage != nil {
			return *result.Usage, nil
		}
		if !time.Now().Add(museUsagePollGap).Before(deadline) {
			return zero, fmt.Errorf("muse: no subscription usage observed within %s "+
				"(the probe turn may have failed; check `muse exec ok`)", m.timeout())
		}
		time.Sleep(museUsagePollGap)
	}
}

// museUsagePollGap paces the usage/read retries while the probe turn runs.
const museUsagePollGap = 2 * time.Second

// call writes one JSON-RPC request and waits for the response with a
// matching id, skipping notifications and other ids on the way.
func (m *Muse) call(proc *execx.Proc, msgs <-chan museRPCEnvelope, readErrs <-chan error,
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
				continue
			}
			if env.Error != nil {
				return nil, fmt.Errorf("host error %d: %s", env.Error.Code, env.Error.Message)
			}
			return env.Result, nil
		case err := <-readErrs:
			return nil, fmt.Errorf("the host closed before answering: %w", err)
		case <-time.After(time.Until(deadline)):
			return nil, fmt.Errorf("the host timed out after %s", m.timeout())
		}
	}
}

func (m *Muse) notify(proc *execx.Proc, method string) error {
	data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": map[string]any{}})
	if err != nil {
		return err
	}
	_, err = proc.Stdin.Write(append(data, '\n'))
	return err
}

// museMeters maps one SubscriptionUsage payload onto this package's meters.
// The 5h block reuses codexWindow (windowDurationMins 300 -> "5h"); the
// weekly block has no duration of its own, so its labels are fixed.
func museMeters(u museSubscriptionUsage) ([]Meter, string) {
	resetsAt := func(ms int64) *time.Time {
		if ms <= 0 {
			return nil
		}
		t := time.UnixMilli(ms)
		return &t
	}
	label, window := codexWindow(u.Window.WindowDurationMins)
	meters := []Meter{
		{ID: window, Label: label, Window: window,
			UsedPct: u.Window.UsedPercent, ResetsAt: resetsAt(u.Window.ResetsAtMs)},
		{ID: "weekly", Label: "Weekly", Window: "weekly",
			UsedPct: u.Weekly.UsedPercent, ResetsAt: resetsAt(u.Weekly.ResetsAtMs)},
	}
	return meters, meters[0].ID
}
