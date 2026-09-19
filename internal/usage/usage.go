// Package usage polls each agent kind's own usage/quota endpoint (spec §13)
// and stores the latest snapshot per kind.
package usage

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// Meter is one usage bar (a window on one agent's quota).
type Meter struct {
	ID, Label, Window string
	UsedPct           float64
	ResetsAt          *time.Time
}

// Snapshot is both what one source's Fetch returns (Meters/HeadlineID/Source
// populated, everything else zero) and what the poller persists per agent
// kind (Agent/FetchedAt/AttemptedAt/Error/Stale added on top).
type Snapshot struct {
	Agent       runtime.AgentKind
	Meters      []Meter
	HeadlineID  string
	Source      string
	FetchedAt   time.Time
	AttemptedAt time.Time
	Error       string
	Stale       bool
}

// Source is one agent kind's usage fetcher, as the poller calls it.
type Source struct {
	Agent runtime.AgentKind
	Fetch func(ctx context.Context) ([]Meter, string, error)
}

// Error carries an API error code (§7), e.g. "limit_reached" for a manual
// refresh asked for again too soon.
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// RateLimitError is a source-imposed backoff (a 429 with a Retry-After) as
// opposed to a plain fetch failure — the poller must wait it out rather than
// retrying on the normal poll/manual-refresh cadence, or every retry before
// it elapses just extends the real endpoint's penalty further.
type RateLimitError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s, retry in %s", e.Err.Error(), e.RetryAfter.Round(time.Second))
}
func (e *RateLimitError) Unwrap() error { return e.Err }

func httpClientOrDefault(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return http.DefaultClient
}

// DefaultSources builds the four real sources: Claude and Cursor read their
// OAuth token from the login keychain via run (`security
// find-generic-password`); Codex talks to `codex app-server` via start, with
// UserHome's rollout files as a fallback; Agy reads its own OAuth token
// cache from disk and calls Antigravity's real remote quota endpoint
// directly (verified live 2026-09-19 — see agy.go's Agy doc comment for the
// full trace and why it must be the "daily" host, not "prod"). Nothing here
// is called by any test in this package — the only thing exercised is that
// the slice has four entries (S-4).
func DefaultSources(userHome, user string, hc *http.Client, run execx.Runner, start execx.Starter) []Source {
	claudeSrc := &Claude{BaseURL: "https://api.anthropic.com", HTTP: hc, Version: claudeCLIVersion(run),
		ReadToken: claudeKeychainToken(run, "Claude Code-credentials", user), Now: time.Now}
	codexSrc := &Codex{Start: start, UserHome: userHome, Timeout: 10 * time.Second, Now: time.Now}
	agySrc := &Agy{BaseURL: "https://daily-cloudcode-pa.googleapis.com", HTTP: hc,
		ReadToken: agyOAuthToken(userHome), Now: time.Now}
	cursorSrc := &Cursor{BaseURL: "https://api2.cursor.sh", HTTP: hc,
		ReadToken: cursorKeychainToken(run, "cursor-access-token", "cursor-user"), Now: time.Now}
	return []Source{
		{Agent: runtime.Claude, Fetch: func(ctx context.Context) ([]Meter, string, error) {
			snap, err := claudeSrc.Fetch(ctx)
			return snap.Meters, snap.HeadlineID, err
		}},
		{Agent: runtime.Codex, Fetch: func(ctx context.Context) ([]Meter, string, error) {
			snap, err := codexSrc.Fetch(ctx)
			return snap.Meters, snap.HeadlineID, err
		}},
		{Agent: runtime.Agy, Fetch: agySrc.Fetch},
		{Agent: runtime.Cursor, Fetch: func(ctx context.Context) ([]Meter, string, error) {
			snap, err := cursorSrc.Fetch(ctx)
			return snap.Meters, snap.HeadlineID, err
		}},
	}
}

// keychainToken reads an OAuth token through `security find-generic-password
// -s <service> -a <account> -w`, run via the injected Runner, and parses the
// JSON for accessToken/expiresAt. Only DefaultSources calls this, and nothing
// in this package's tests calls DefaultSources (S-4).
// keychainBytes runs `security find-generic-password`, shared by both real
// keychain-based token readers below.
func keychainBytes(run execx.Runner, service, account string) func(context.Context) ([]byte, error) {
	return func(ctx context.Context) ([]byte, error) {
		if run == nil {
			return nil, fmt.Errorf("usage: no runner configured for the %s keychain entry", service)
		}
		return run(ctx, "security", "find-generic-password", "-s", service, "-a", account, "-w")
	}
}

// agyOAuthToken reads agy's own OAuth token cache directly from disk — agy
// has no keychain entry; it (and the Antigravity app) persist
// {token: {access_token, expiry}} to
// UserHome/.gemini/antigravity-cli/antigravity-oauth-token and rewrite it
// whenever they run. No refresh_token exchange is attempted: that needs
// Antigravity's own OAuth client secret, which lives only inside the
// compiled `agy` binary — out of this package's reach. A stale token simply
// surfaces as the ordinary expired-token fetch error.
// ponytail: no refresh; the file is rewritten by agy/Antigravity.app itself.
func agyOAuthToken(userHome string) func(context.Context) (string, time.Time, error) {
	path := filepath.Join(userHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	return func(context.Context) (string, time.Time, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("usage: agy oauth token: %w", err)
		}
		var parsed struct {
			Token struct {
				AccessToken string `json:"access_token"`
				Expiry      string `json:"expiry"`
			} `json:"token"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return "", time.Time{}, fmt.Errorf("usage: agy oauth token: %w", err)
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, parsed.Token.Expiry)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("usage: agy oauth token: expiry: %w", err)
		}
		return parsed.Token.AccessToken, expiresAt, nil
	}
}

// claudeCLIVersion runs the real, installed `claude --version` fresh on
// every call — the same command and the same "first whitespace field" parse
// as internal/adapter/claude.go's Installed() — so the User-Agent this
// package sends always matches the CLI actually on this machine instead of
// a version string that goes stale the next time `claude` updates itself.
func claudeCLIVersion(run execx.Runner) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if run == nil {
			return "", fmt.Errorf("usage: no runner configured to detect the claude CLI version")
		}
		out, err := run(ctx, "claude", "--version")
		if err != nil {
			return "", err
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			return "", fmt.Errorf("usage: claude --version produced no output")
		}
		return fields[0], nil
	}
}

// claudeKeychainToken parses the real "Claude Code-credentials" keychain item,
// which nests everything under "claudeAiOauth" (the same shape
// internal/catalog.ClaudeFetcher.token already unwraps) — a flat
// {accessToken, expiresAt} parse silently zero-values expiresAt, which reads
// as "already expired" forever regardless of the real token's validity.
func claudeKeychainToken(run execx.Runner, service, account string) func(context.Context) (string, time.Time, error) {
	read := keychainBytes(run, service, account)
	return func(ctx context.Context) (string, time.Time, error) {
		out, err := read(ctx)
		if err != nil {
			return "", time.Time{}, err
		}
		var parsed struct {
			ClaudeAiOauth struct {
				AccessToken string `json:"accessToken"`
				ExpiresAt   int64  `json:"expiresAt"`
			} `json:"claudeAiOauth"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil {
			return "", time.Time{}, fmt.Errorf("usage: %s credentials: %w", service, err)
		}
		return parsed.ClaudeAiOauth.AccessToken, time.UnixMilli(parsed.ClaudeAiOauth.ExpiresAt), nil
	}
}

// cursorKeychainToken parses the real "cursor-access-token" keychain item,
// which is a bare JWT string, not JSON — json.Unmarshal-ing it directly fails
// outright. Its expiry comes from the standard "exp" claim (RFC 7519, seconds
// since epoch) in the token's own payload segment.
func cursorKeychainToken(run execx.Runner, service, account string) func(context.Context) (string, time.Time, error) {
	read := keychainBytes(run, service, account)
	return func(ctx context.Context) (string, time.Time, error) {
		out, err := read(ctx)
		if err != nil {
			return "", time.Time{}, err
		}
		token := strings.TrimSpace(string(out))
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			return "", time.Time{}, fmt.Errorf("usage: %s credentials: not a JWT (%d segments)", service, len(parts))
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", time.Time{}, fmt.Errorf("usage: %s credentials: %w", service, err)
		}
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if err := json.Unmarshal(payload, &claims); err != nil {
			return "", time.Time{}, fmt.Errorf("usage: %s credentials: %w", service, err)
		}
		return token, time.Unix(claims.Exp, 0), nil
	}
}

// SourcesFromEnv is the single decision point for whether this daemon talks
// to the real world (S-4). The default is NO sources: a daemon started by a
// test, by scripts/e2e.sh, by `make dev`, or by hand from a worktree polls
// nothing, reads no keychain and makes no request. Production is opt-in —
// `swarm install`'s launchd plist sets SWARM_USAGE=live, and that is the only
// place it is set.
func SourcesFromEnv(osEnv func(string) string, userHome, user string,
	hc *http.Client, run execx.Runner, start execx.Starter) []Source {
	if osEnv("SWARM_USAGE") != "live" {
		return nil
	}
	return DefaultSources(userHome, user, hc, run, start)
}

// Poller fetches every enabled source on a schedule and keeps the latest
// snapshot per agent kind.
type Poller struct {
	DB       *db.DB
	Events   *events.Store
	Settings *settings.Store
	Now      func() time.Time
	Jitter   func(time.Duration) time.Duration
	Sources  []Source
	Log      func(format string, args ...any)

	backoffMu sync.Mutex
	backoff   map[runtime.AgentKind]time.Time

	// fetchMu serializes every fetchAndStore call (pollOnce's loop, plus any
	// concurrent RefreshOne from an HTTP request) so the automatic poll loop
	// and a manual refresh can never both pass the backoff check and fire a
	// real request to the same source at once — confirmed live: exactly that
	// race sent Claude's oauth/usage endpoint two near-simultaneous requests
	// right as a backoff cleared, and its own real Retry-After escalated
	// from ~2m to ~59m in response.
	fetchMu sync.Mutex
}

const minManualRefreshGap = 60 * time.Second

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poller) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}

func (p *Poller) sourceFor(kind runtime.AgentKind) *Source {
	for i := range p.Sources {
		if p.Sources[i].Agent == kind {
			return &p.Sources[i]
		}
	}
	return nil
}

// RefreshOne fetches one agent kind's usage right now, refusing a repeat
// within 60 s of the last attempt (manual refresh, not the poll loop).
func (p *Poller) RefreshOne(ctx context.Context, kind runtime.AgentKind) error {
	var attemptedAt sql.NullInt64
	if err := p.DB.QueryRowContext(ctx, `SELECT attempted_at FROM usage_snapshots
		WHERE agent_kind = ?`, string(kind)).Scan(&attemptedAt); err != nil && err != sql.ErrNoRows {
		return err
	}
	if attemptedAt.Valid && p.now().Sub(db.FromMillis(attemptedAt.Int64)) < minManualRefreshGap {
		return &Error{Code: "limit_reached", Message: "Wait a bit before refreshing again."}
	}
	src := p.sourceFor(kind)
	if src == nil {
		// contracts §4: POST /api/usage/refresh is 202 with only one other
		// branch, 429 limit_reached — "no source configured" (S-4: nothing at
		// all when SWARM_USAGE isn't "live", or a kind not enabled) is not a
		// third status. httpapi.refreshUsage would otherwise have turned this
		// plain error into a 500 (wrapUsageErr only recognises *usage.Error).
		// Recording the attempt (not just returning nil) is what makes the
		// existing 60s rate limit above apply to a second sourceless refresh
		// too, and it's what puts a row in usage_snapshots for GET /api/usage
		// to return at all — before the first refresh, a never-configured
		// kind has no row and so is absent from the list. Found wiring the
		// e2e harness's scenario 17.
		return p.recordAttemptOnly(ctx, kind, p.now())
	}
	return p.fetchAndStore(ctx, *src)
}

// pollOnce fetches every source enabled in Settings once. The daemon calls
// this on a jittered UsagePollSec-ish interval (Loop).
func (p *Poller) pollOnce(ctx context.Context) error {
	cfg, err := p.Settings.Get(ctx)
	if err != nil {
		return err
	}
	for _, src := range p.Sources {
		if !slices.Contains(cfg.EnabledAgents, src.Agent) {
			continue
		}
		if err := p.fetchAndStore(ctx, src); err != nil {
			p.logf("usage: %s: %v", src.Agent, err)
		}
	}
	return nil
}

func (p *Poller) backoffUntil(kind runtime.AgentKind) time.Time {
	p.backoffMu.Lock()
	defer p.backoffMu.Unlock()
	return p.backoff[kind]
}

func (p *Poller) setBackoff(kind runtime.AgentKind, until time.Time) {
	p.backoffMu.Lock()
	defer p.backoffMu.Unlock()
	if p.backoff == nil {
		p.backoff = map[runtime.AgentKind]time.Time{}
	}
	p.backoff[kind] = until
}

func (p *Poller) fetchAndStore(ctx context.Context, src Source) error {
	// Serializes the whole check-fetch-store sequence: without this, the
	// automatic poll loop and a manual refresh (a concurrent HTTP request)
	// can both pass the backoff check for the same source in the same
	// instant and each fire a real network request — the exact race that
	// hit Claude's real endpoint twice and made its own penalty worse.
	p.fetchMu.Lock()
	defer p.fetchMu.Unlock()
	now := p.now()
	// A source's own Retry-After (RateLimitError) must be honored: hitting
	// it again before that elapses only extends its penalty further, so
	// this skips the network call entirely rather than retrying on the
	// normal poll/manual-refresh cadence.
	if until := p.backoffUntil(src.Agent); now.Before(until) {
		return p.storeFailure(ctx, src.Agent, now,
			fmt.Errorf("%s: rate limited, retry in %s", src.Agent, until.Sub(now).Round(time.Second)))
	}
	meters, headline, err := src.Fetch(ctx)
	if err != nil {
		var rl *RateLimitError
		if errors.As(err, &rl) {
			p.setBackoff(src.Agent, now.Add(rl.RetryAfter))
		}
		return p.storeFailure(ctx, src.Agent, now, err)
	}
	p.setBackoff(src.Agent, time.Time{})
	return p.storeSuccess(ctx, src.Agent, now, meters, headline)
}

func (p *Poller) storeSuccess(ctx context.Context, kind runtime.AgentKind, now time.Time, meters []Meter, headline string) error {
	body, err := json.Marshal(meters)
	if err != nil {
		return err
	}
	_, err = p.DB.ExecContext(ctx, `INSERT INTO usage_snapshots
		(agent_kind, meters_json, headline_id, source, error, fetched_at, attempted_at)
		VALUES (?, ?, ?, ?, NULL, ?, ?)
		ON CONFLICT(agent_kind) DO UPDATE SET meters_json = excluded.meters_json,
			headline_id = excluded.headline_id, source = excluded.source, error = NULL,
			fetched_at = excluded.fetched_at, attempted_at = excluded.attempted_at`,
		string(kind), string(body), nullIf(headline), string(kind), db.Millis(now), db.Millis(now))
	if err != nil {
		return err
	}
	_, err = p.Events.Publish(ctx, events.UsageChanged, map[string]any{"agent_kind": kind})
	return err
}

func (p *Poller) storeFailure(ctx context.Context, kind runtime.AgentKind, now time.Time, fetchErr error) error {
	_, err := p.DB.ExecContext(ctx, `INSERT INTO usage_snapshots
		(agent_kind, meters_json, headline_id, source, error, fetched_at, attempted_at)
		VALUES (?, '[]', NULL, ?, ?, 0, ?)
		ON CONFLICT(agent_kind) DO UPDATE SET error = excluded.error, attempted_at = excluded.attempted_at`,
		string(kind), string(kind), fetchErr.Error(), db.Millis(now))
	return err
}

// recordAttemptOnly is storeFailure without an error: a kind with no source
// configured at all isn't a fetch failure, so it leaves the error column
// clear (an empty string in the wire shape, never a message blaming a fetch
// that never happened) while still giving GET /api/usage a row (meters: [],
// stale: true) and the 60s manual-refresh gate something to check.
func (p *Poller) recordAttemptOnly(ctx context.Context, kind runtime.AgentKind, now time.Time) error {
	_, err := p.DB.ExecContext(ctx, `INSERT INTO usage_snapshots
		(agent_kind, meters_json, headline_id, source, error, fetched_at, attempted_at)
		VALUES (?, '[]', NULL, ?, NULL, 0, ?)
		ON CONFLICT(agent_kind) DO UPDATE SET attempted_at = excluded.attempted_at`,
		string(kind), string(kind), db.Millis(now))
	return err
}

func nullIf(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// Snapshots returns the latest stored snapshot for every agent kind that has
// ever been polled, with Stale computed against the current settings.
func (p *Poller) Snapshots(ctx context.Context) ([]Snapshot, error) {
	cfg, err := p.Settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	pollSec := cfg.UsagePollSec
	if pollSec <= 0 {
		pollSec = 300
	}
	staleAfter := 3 * time.Duration(pollSec) * time.Second
	rows, err := p.DB.QueryContext(ctx, `SELECT agent_kind, meters_json, COALESCE(headline_id, ''),
		source, COALESCE(error, ''), fetched_at, attempted_at FROM usage_snapshots`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := p.now()
	var out []Snapshot
	for rows.Next() {
		var s Snapshot
		var kind, metersJSON string
		var fetchedAt, attemptedAt int64
		if err := rows.Scan(&kind, &metersJSON, &s.HeadlineID, &s.Source, &s.Error,
			&fetchedAt, &attemptedAt); err != nil {
			return nil, err
		}
		s.Agent = runtime.AgentKind(kind)
		if err := json.Unmarshal([]byte(metersJSON), &s.Meters); err != nil {
			return nil, err
		}
		s.FetchedAt = db.FromMillis(fetchedAt)
		s.AttemptedAt = db.FromMillis(attemptedAt)
		s.Stale = s.AttemptedAt.After(s.FetchedAt) || now.Sub(s.FetchedAt) > staleAfter
		out = append(out, s)
	}
	return out, rows.Err()
}

// Loop runs pollOnce on a jittered UsagePollSec-ish interval until ctx is
// cancelled. Jitter defaults to the identity function.
func (p *Poller) Loop(ctx context.Context, after func(time.Duration) <-chan time.Time) {
	if after == nil {
		after = time.After
	}
	jitter := p.Jitter
	if jitter == nil {
		jitter = func(d time.Duration) time.Duration { return d }
	}
	for {
		cfg, err := p.Settings.Get(ctx)
		interval := 300 * time.Second
		if err == nil && cfg.UsagePollSec > 0 {
			interval = time.Duration(cfg.UsagePollSec) * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-after(jitter(interval)):
		}
		if err := p.pollOnce(ctx); err != nil {
			p.logf("usage: poll: %v", err)
		}
	}
}
