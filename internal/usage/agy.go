package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Agy reads Antigravity's real remote quota endpoint directly, with the
// account's own OAuth access token — not agy's local per-invocation server.
//
// That local server (documented in an earlier pass over this file) only
// exists while a real `agy` process is alive, binds a random port logged
// per-invocation, and rejects any request without a CSRF token this package
// has no way to obtain. There is no stable local endpoint to poll.
//
// The real mechanism, confirmed live on 2026-09-19 (see docs/specs or the
// investigation that produced this file for the full trace): agy's OAuth
// token, cached at UserHome/.gemini/antigravity-cli/antigravity-oauth-token,
// carries the "https://www.googleapis.com/auth/aicode" scope and works
// directly against Google's Cloud Code backend — the same
// v1internal:retrieveUserQuotaSummary RPC agy's local server forwards to,
// with no project ID required. Two hosts exist: the "prod" host
// (cloudcode-pa.googleapis.com) reported remainingFraction:1 for every
// bucket with a resetTime pinned at exactly now+5h — a known, reported bug
// (this account is metered on the "daily" deployment instead) — while
// daily-cloudcode-pa.googleapis.com returned real, partially-consumed
// fractions matching this account's actual usage. BaseURL must be the daily
// host; see DefaultSources.
type Agy struct {
	BaseURL   string // "https://daily-cloudcode-pa.googleapis.com" in production
	HTTP      *http.Client
	ReadToken func(ctx context.Context) (token string, expiresAt time.Time, err error)
	Now       func() time.Time
	Log       func(string, ...any)
}

func (a *Agy) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Agy) logf(format string, args ...any) {
	if a.Log != nil {
		a.Log(format, args...)
	}
}

// agyBucket and agyGroup mirror the real
// google.internal.cloud.code.v1internal.PredictionService
// RetrieveUserQuotaSummaryResponse shape (confirmed live): a nested
// groups[].buckets[], not the flat buckets[] an earlier pass assumed.
type agyBucket struct {
	Window            string   `json:"window"`
	ResetTime         string   `json:"resetTime"`
	RemainingFraction *float64 `json:"remainingFraction"`
	Disabled          bool     `json:"disabled"`
}

type agyGroup struct {
	DisplayName string      `json:"displayName"`
	Buckets     []agyBucket `json:"buckets"`
}

type agyQuota struct {
	Groups []agyGroup `json:"groups"`
}

var agyGroups = map[string]struct{ id, label string }{
	"Gemini Models": {"gemini", "Gemini"},
}

// agyExtraGroups are the non-Gemini models agy also offers. Their quota is
// not agy's native usage: they are not reported (the agy app shows them),
// and must never drive the headline -- the usage gate reads the headline,
// so a busy extra group would mark agy exhausted while Gemini is free.
// Unknown groups are still reported, in case Gemini's group is renamed.
var agyExtraGroups = map[string]bool{"Claude and GPT models": true}

// ParseAgyQuota is §13's rule: used_pct = (1 - remainingFraction) × 100, a
// missing fraction counts as 0 remaining (fully used), and a disabled bucket
// is skipped. The headline is the Gemini 5h bucket, else the busiest 5h one.
func ParseAgyQuota(body []byte) ([]Meter, string, error) {
	var q agyQuota
	if err := json.Unmarshal(body, &q); err != nil {
		return nil, "", fmt.Errorf("agy: %w", err)
	}
	var meters []Meter
	headline := ""
	var headlineUsed float64 = -1
	for _, g := range q.Groups {
		if agyExtraGroups[g.DisplayName] {
			continue
		}
		group, ok := agyGroups[g.DisplayName]
		if !ok {
			group = struct{ id, label string }{g.DisplayName, g.DisplayName}
		}
		for _, b := range g.Buckets {
			if b.Disabled {
				continue
			}
			remaining := 0.0
			if b.RemainingFraction != nil {
				remaining = *b.RemainingFraction
			}
			usedPct := (1 - remaining) * 100
			id := group.id + "_" + b.Window
			meters = append(meters, Meter{ID: id, Label: group.label + " " + b.Window,
				Window: b.Window, UsedPct: usedPct, ResetsAt: parseResetsAt(b.ResetTime)})
			if b.Window == "5h" && usedPct > headlineUsed {
				headline, headlineUsed = id, usedPct
			}
		}
	}
	for _, m := range meters {
		if m.ID == "gemini_5h" {
			headline = m.ID
		}
	}
	return meters, headline, nil
}

// Fetch calls Antigravity's real remote quota endpoint directly. No agy
// process needs to be running, and no project ID is resolved first
// (confirmed live: the endpoint accepts an empty JSON body and infers the
// project from the token itself).
func (a *Agy) Fetch(ctx context.Context) ([]Meter, string, error) {
	if a.ReadToken == nil {
		return nil, "", fmt.Errorf("agy: no token source configured")
	}
	token, expiresAt, err := a.ReadToken(ctx)
	if err != nil {
		return nil, "", err
	}
	if !expiresAt.After(a.now()) {
		return nil, "", fmt.Errorf("agy: oauth token has expired")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.BaseURL+"/v1internal:retrieveUserQuotaSummary", strings.NewReader("{}"))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity")
	resp, err := httpClientOrDefault(a.HTTP).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, "", fmt.Errorf("agy: oauth token rejected (401)")
	}
	if resp.StatusCode != http.StatusOK {
		a.logf("usage: agy: status %d", resp.StatusCode)
		return nil, "", fmt.Errorf("agy: status %d", resp.StatusCode)
	}
	return ParseAgyQuota(body)
}
